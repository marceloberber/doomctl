package gen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ---------- Dockerfile ----------

type DockerfileSpec struct {
	Preset       string `json:"preset"`
	MultiStage   bool   `json:"multi_stage"`
	BuilderImage string `json:"builder_image"`
	BuilderSteps string `json:"builder_steps"` // linhas RUN do estágio de build
	ArtifactFrom string `json:"artifact_from"` // caminho no builder
	ArtifactTo   string `json:"artifact_to"`   // caminho na imagem final
	BaseImage    string `json:"base_image"`
	Packages     string `json:"packages"` // separados por espaço/vírgula
	Workdir      string `json:"workdir"`
	Copy         string `json:"copy"` // linhas "origem destino"
	Env          string `json:"env"`  // linhas CHAVE=valor
	Args         string `json:"args"` // linhas NOME=default
	Run          string `json:"run"`  // linhas RUN extras (imagem final)
	Expose       string `json:"expose"`
	NonRoot      bool   `json:"non_root"`
	User         string `json:"user"`
	Healthcheck  string `json:"healthcheck"`
	Entrypoint   string `json:"entrypoint"`
	Cmd          string `json:"cmd"`
	Labels       string `json:"labels"`
}

var imageRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._/:-]*[a-z0-9])?(:[A-Za-z0-9._-]{1,128})?(@sha256:[a-f0-9]{64})?$`)

func ValidImage(s string) bool { return imageRe.MatchString(s) }

func pkgManager(image string) string {
	i := strings.ToLower(image)
	switch {
	case strings.Contains(i, "alpine"):
		return "apk"
	case strings.Contains(i, "distroless") || strings.Contains(i, "scratch") || strings.Contains(i, "chainguard"):
		return ""
	case strings.Contains(i, "fedora") || strings.Contains(i, "rocky") || strings.Contains(i, "alma") ||
		strings.Contains(i, "ubi") || strings.Contains(i, "centos") || strings.Contains(i, "oraclelinux"):
		return "dnf"
	default:
		return "apt"
	}
}

func execForm(s string) (string, error) {
	args, err := SplitArgs(s)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(args)
	return string(b), nil
}

func Dockerfile(s DockerfileSpec) (string, string, error) {
	if s.BaseImage == "" {
		return "", "", fmt.Errorf("informe a imagem base")
	}
	if !ValidImage(s.BaseImage) || (s.MultiStage && !ValidImage(s.BuilderImage)) {
		return "", "", fmt.Errorf("referência de imagem inválida")
	}
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("# syntax=docker/dockerfile:1")
	w("# Gerado pelo doomctl. Prefira tag fixa ou digest (@sha256:...) em produção.")
	for _, kv := range KVLines(s.Args) {
		w("ARG %s=%s", kv[0], kv[1])
	}
	if s.MultiStage {
		w("")
		w("# ---------- build ----------")
		w("FROM %s AS build", s.BuilderImage)
		w("WORKDIR /src")
		for _, l := range Lines(s.BuilderSteps) {
			if strings.HasPrefix(strings.ToUpper(l), "COPY ") || strings.HasPrefix(strings.ToUpper(l), "RUN ") ||
				strings.HasPrefix(strings.ToUpper(l), "ENV ") || strings.HasPrefix(strings.ToUpper(l), "ARG ") {
				w("%s", l)
			} else {
				w("RUN %s", l)
			}
		}
		w("")
		w("# ---------- runtime ----------")
	} else {
		w("")
	}
	w("FROM %s", s.BaseImage)
	for _, kv := range KVLines(s.Labels) {
		w("LABEL %s=%s", kv[0], DQ(kv[1]))
	}
	pkgs := strings.Fields(strings.ReplaceAll(s.Packages, ",", " "))
	if len(pkgs) > 0 {
		switch pkgManager(s.BaseImage) {
		case "apk":
			w("RUN apk add --no-cache %s", strings.Join(pkgs, " "))
		case "dnf":
			w("RUN dnf install -y %s && dnf clean all", strings.Join(pkgs, " "))
		case "apt":
			w("RUN apt-get update \\\n    && apt-get install -y --no-install-recommends %s \\\n    && rm -rf /var/lib/apt/lists/*", strings.Join(pkgs, " "))
		default:
			w("# ATENÇÃO: imagem sem gerenciador de pacotes; instale %s no estágio de build", strings.Join(pkgs, " "))
		}
	}
	for _, kv := range KVLines(s.Env) {
		w("ENV %s=%s", kv[0], DQ(kv[1]))
	}
	if s.Workdir != "" {
		w("WORKDIR %s", s.Workdir)
	}
	if s.MultiStage && s.ArtifactFrom != "" {
		to := s.ArtifactTo
		if to == "" {
			to = "."
		}
		if s.NonRoot {
			w("COPY --from=build --chown=10001:10001 %s %s", s.ArtifactFrom, to)
		} else {
			w("COPY --from=build %s %s", s.ArtifactFrom, to)
		}
	}
	for _, l := range Lines(s.Copy) {
		f := strings.Fields(l)
		if len(f) < 2 {
			return "", "", fmt.Errorf("COPY inválido: %q (use 'origem destino')", l)
		}
		if s.NonRoot {
			w("COPY --chown=10001:10001 %s", strings.Join(f, " "))
		} else {
			w("COPY %s", strings.Join(f, " "))
		}
	}
	for _, l := range Lines(s.Run) {
		w("RUN %s", l)
	}
	user := s.User
	if s.NonRoot && user == "" {
		switch pkgManager(s.BaseImage) {
		case "apk":
			w("RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app")
		case "apt", "dnf":
			w("RUN groupadd -r -g 10001 app && useradd -r -u 10001 -g app -s /usr/sbin/nologin -M app")
		}
		user = "10001:10001"
	}
	for _, p := range strings.Fields(strings.ReplaceAll(s.Expose, ",", " ")) {
		w("EXPOSE %s", p)
	}
	if s.Healthcheck != "" {
		w("HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \\\n    CMD %s", s.Healthcheck)
	}
	if user != "" {
		w("USER %s", user)
	}
	if s.Entrypoint != "" {
		ef, err := execForm(s.Entrypoint)
		if err != nil {
			return "", "", fmt.Errorf("ENTRYPOINT: %w", err)
		}
		w("ENTRYPOINT %s", ef)
	}
	if s.Cmd != "" {
		ef, err := execForm(s.Cmd)
		if err != nil {
			return "", "", fmt.Errorf("CMD: %w", err)
		}
		w("CMD %s", ef)
	}
	ignore := ".git\n.gitignore\n.env\n*.env\n**/node_modules\n**/__pycache__\n*.log\nDockerfile*\ndocker-compose*.y*ml\n.vscode\n.idea\n"
	return b.String(), ignore, nil
}

// DockerfilePresets: pontos de partida (o usuário pode editar tudo).
func DockerfilePresets() map[string]DockerfileSpec {
	return map[string]DockerfileSpec{
		"go": {Preset: "go", MultiStage: true, BuilderImage: "golang:1.27-trixie",
			BuilderSteps: "COPY go.mod go.sum ./\ngo mod download\nCOPY . .\nCGO_ENABLED=0 go build -trimpath -ldflags=\"-s -w\" -o /out/app ./cmd/app",
			ArtifactFrom: "/out/app", ArtifactTo: "/app", BaseImage: "gcr.io/distroless/static-debian13:nonroot",
			Expose: "8080", User: "nonroot:nonroot", Entrypoint: "/app"},
		"node": {Preset: "node", MultiStage: true, BuilderImage: "node:24-trixie-slim",
			BuilderSteps: "COPY package*.json ./\nnpm ci\nCOPY . .\nnpm run build\nnpm prune --omit=dev",
			ArtifactFrom: "/src", ArtifactTo: "/app", BaseImage: "node:24-trixie-slim", Workdir: "/app",
			Env: "NODE_ENV=production", Expose: "3000", NonRoot: true, Cmd: "node dist/index.js",
			Healthcheck: "node -e \"fetch('http://127.0.0.1:3000/health').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))\""},
		"python": {Preset: "python", BaseImage: "python:3.13-slim-trixie", Workdir: "/app",
			Env: "PYTHONDONTWRITEBYTECODE=1\nPYTHONUNBUFFERED=1", Copy: "requirements.txt .\n. .",
			Run: "pip install --no-cache-dir -r requirements.txt", Expose: "8000", NonRoot: true,
			Cmd: "python -m gunicorn -b 0.0.0.0:8000 app:app"},
		"nginx": {Preset: "nginx", BaseImage: "nginxinc/nginx-unprivileged:stable-alpine",
			Copy: "./dist /usr/share/nginx/html", Expose: "8080",
			Healthcheck: "wget -qO- http://127.0.0.1:8080/ >/dev/null || exit 1"},
		"debian": {Preset: "debian", BaseImage: "debian:trixie-slim", Packages: "ca-certificates curl",
			Workdir: "/app", NonRoot: true, Cmd: "bash"},
	}
}

// ---------- docker-compose ----------

type ComposeHealth struct {
	Test     string `json:"test"`
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
	Retries  int    `json:"retries"`
}

type ComposeService struct {
	Name          string        `json:"name"`
	Image         string        `json:"image"`
	Registry      string        `json:"registry"` // prefixo, ex.: registry.local:5000
	Build         string        `json:"build"`    // contexto
	Dockerfile    string        `json:"dockerfile"`
	ContainerName string        `json:"container_name"`
	Restart       string        `json:"restart"`
	Ports         string        `json:"ports"`
	Environment   string        `json:"environment"`
	EnvFile       string        `json:"env_file"`
	Volumes       string        `json:"volumes"`
	Networks      []string      `json:"networks"`
	DependsOn     []string      `json:"depends_on"`
	Command       string        `json:"command"`
	User          string        `json:"user"`
	ReadOnly      bool          `json:"read_only"`
	NoNewPrivs    bool          `json:"no_new_privileges"`
	CapDropAll    bool          `json:"cap_drop_all"`
	CapAdd        string        `json:"cap_add"`
	MemLimit      string        `json:"mem_limit"`
	CPUs          string        `json:"cpus"`
	Healthcheck   ComposeHealth `json:"healthcheck"`
	Labels        string        `json:"labels"`
}

type ComposeNetwork struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"` // bridge | macvlan | ipvlan | overlay
	Subnet   string `json:"subnet"`
	Gateway  string `json:"gateway"`
	Parent   string `json:"parent"` // interface pai (macvlan/ipvlan)
	Internal bool   `json:"internal"`
	External bool   `json:"external"`
}

type ComposeVolume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	DriverOpts string `json:"driver_opts"` // linhas chave=valor (ex.: type=nfs)
	External   bool   `json:"external"`
}

type ComposeSpec struct {
	Name     string           `json:"name"`
	Services []ComposeService `json:"services"`
	Networks []ComposeNetwork `json:"networks"`
	Volumes  []ComposeVolume  `json:"volumes"`
}

var svcNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

func Compose(s ComposeSpec) (string, error) {
	if len(s.Services) == 0 {
		return "", fmt.Errorf("adicione ao menos um serviço")
	}
	root := Map{}
	if s.Name != "" {
		if !svcNameRe.MatchString(s.Name) {
			return "", fmt.Errorf("nome do projeto inválido (minúsculas, números, - _ .)")
		}
		root = root.Set("name", s.Name)
	}
	services := Map{}
	for _, sv := range s.Services {
		if !svcNameRe.MatchString(sv.Name) {
			return "", fmt.Errorf("nome de serviço inválido: %q", sv.Name)
		}
		m := Map{}
		if sv.Image != "" {
			img := sv.Image
			if sv.Registry != "" {
				img = strings.TrimSuffix(sv.Registry, "/") + "/" + img
			}
			m = m.Set("image", img)
		}
		if sv.Build != "" {
			bm := Map{}.Set("context", sv.Build).SetIf("dockerfile", sv.Dockerfile)
			m = m.Set("build", bm)
		}
		if sv.Image == "" && sv.Build == "" {
			return "", fmt.Errorf("serviço %q: informe image ou build", sv.Name)
		}
		m = m.SetIf("container_name", sv.ContainerName).SetIf("restart", sv.Restart).SetIf("user", sv.User)
		if sv.Command != "" {
			args, err := SplitArgs(sv.Command)
			if err != nil {
				return "", fmt.Errorf("serviço %q command: %w", sv.Name, err)
			}
			l := List{}
			for _, a := range args {
				l = append(l, a)
			}
			m = m.Set("command", l)
		}
		ports := List{}
		for _, p := range Lines(sv.Ports) {
			ports = append(ports, DQRaw(p))
		}
		m = m.SetIf("ports", ports)
		env := Map{}
		for _, kv := range KVLines(sv.Environment) {
			env = env.Set(kv[0], kv[1])
		}
		m = m.SetIf("environment", env)
		if sv.EnvFile != "" {
			m = m.Set("env_file", List{sv.EnvFile})
		}
		vols := List{}
		for _, v := range Lines(sv.Volumes) {
			vols = append(vols, v)
		}
		m = m.SetIf("volumes", vols)
		if len(sv.Networks) > 0 {
			l := List{}
			for _, n := range sv.Networks {
				l = append(l, n)
			}
			m = m.Set("networks", l)
		}
		if len(sv.DependsOn) > 0 {
			dm := Map{}
			for _, d := range sv.DependsOn {
				cond := "service_started"
				for _, o := range s.Services {
					if o.Name == d && o.Healthcheck.Test != "" {
						cond = "service_healthy"
					}
				}
				dm = dm.Set(d, Map{}.Set("condition", cond))
			}
			m = m.Set("depends_on", dm)
		}
		if sv.Healthcheck.Test != "" {
			h := Map{}.Set("test", List{"CMD-SHELL", sv.Healthcheck.Test})
			h = h.Set("interval", def(sv.Healthcheck.Interval, "30s")).Set("timeout", def(sv.Healthcheck.Timeout, "5s"))
			r := sv.Healthcheck.Retries
			if r <= 0 {
				r = 3
			}
			h = h.Set("retries", r)
			m = m.Set("healthcheck", h)
		}
		if sv.ReadOnly {
			m = m.Set("read_only", true)
		}
		if sv.NoNewPrivs {
			m = m.Set("security_opt", List{"no-new-privileges:true"})
		}
		if sv.CapDropAll {
			m = m.Set("cap_drop", List{"ALL"})
		}
		if caps := strings.Fields(strings.ReplaceAll(sv.CapAdd, ",", " ")); len(caps) > 0 {
			l := List{}
			for _, c := range caps {
				l = append(l, strings.ToUpper(c))
			}
			m = m.Set("cap_add", l)
		}
		if sv.MemLimit != "" || sv.CPUs != "" {
			lim := Map{}.SetIf("cpus", sv.CPUs).SetIf("memory", sv.MemLimit)
			m = m.Set("deploy", Map{}.Set("resources", Map{}.Set("limits", lim)))
		}
		lb := Map{}
		for _, kv := range KVLines(sv.Labels) {
			lb = lb.Set(kv[0], kv[1])
		}
		m = m.SetIf("labels", lb)
		services = services.Set(sv.Name, m)
	}
	root = root.Set("services", services)

	if len(s.Networks) > 0 {
		nets := Map{}
		for _, n := range s.Networks {
			if !svcNameRe.MatchString(n.Name) {
				return "", fmt.Errorf("nome de rede inválido: %q", n.Name)
			}
			nm := Map{}
			if n.External {
				nm = nm.Set("external", true)
				nets = nets.Set(n.Name, nm)
				continue
			}
			nm = nm.SetIf("driver", n.Driver)
			if n.Parent != "" && (n.Driver == "macvlan" || n.Driver == "ipvlan") {
				nm = nm.Set("driver_opts", Map{}.Set("parent", n.Parent))
			}
			if n.Internal {
				nm = nm.Set("internal", true)
			}
			if n.Subnet != "" {
				cfg := Map{}.Set("subnet", n.Subnet).SetIf("gateway", n.Gateway)
				nm = nm.Set("ipam", Map{}.Set("config", List{cfg}))
			}
			nets = nets.Set(n.Name, nm)
		}
		root = root.Set("networks", nets)
	}
	if len(s.Volumes) > 0 {
		vols := Map{}
		for _, v := range s.Volumes {
			if !svcNameRe.MatchString(v.Name) {
				return "", fmt.Errorf("nome de volume inválido: %q", v.Name)
			}
			vm := Map{}
			if v.External {
				vm = vm.Set("external", true)
			} else {
				vm = vm.SetIf("driver", v.Driver)
				opts := Map{}
				for _, kv := range KVLines(v.DriverOpts) {
					opts = opts.Set(kv[0], kv[1])
				}
				vm = vm.SetIf("driver_opts", opts)
			}
			vols = vols.Set(v.Name, vm)
		}
		root = root.Set("volumes", vols)
	}
	return "# Gerado pelo doomctl — valide com: docker compose config -q\n" + YAML(root), nil
}

// DQRaw força aspas (portas "8080:80" devem ser string para evitar base 60 do YAML 1.1).
func DQRaw(s string) Raw { return Raw(DQ(s)) }

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
