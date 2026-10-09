package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/doomctl/doomctl/internal/gen"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

func (s *Server) dockerPresets(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, gen.DockerfilePresets())
}

func (s *Server) generateDockerfile(w http.ResponseWriter, r *http.Request, _ *User) {
	var spec gen.DockerfileSpec
	if err := decode(r, &spec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	df, ign, err := gen.Dockerfile(spec)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"content": df, "dockerignore": ign})
}

func (s *Server) generateCompose(w http.ResponseWriter, r *http.Request, _ *User) {
	var spec gen.ComposeSpec
	if err := decode(r, &spec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	y, err := gen.Compose(spec)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"content": y})
}

type DockerFile struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Content   string    `json:"content,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Server) listDockerFiles(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, kind, name, updated_at FROM docker_files ORDER BY kind, name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []DockerFile{}
	for rows.Next() {
		var f DockerFile
		rows.Scan(&f.ID, &f.Kind, &f.Name, &f.UpdatedAt)
		out = append(out, f)
	}
	writeJSON(w, 200, out)
}

func (s *Server) loadDockerFile(ctx context.Context, id int64) (DockerFile, error) {
	var f DockerFile
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, name, content, updated_at FROM docker_files WHERE id=$1`, id).
		Scan(&f.ID, &f.Kind, &f.Name, &f.Content, &f.UpdatedAt)
	return f, err
}

func (s *Server) getDockerFile(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	f, err := s.loadDockerFile(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, f)
}

func (s *Server) saveDockerFile(w http.ResponseWriter, r *http.Request, u *User) {
	var f DockerFile
	if err := decode(r, &f); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if f.Kind != "dockerfile" && f.Kind != "compose" {
		fail(w, 400, "tipo inválido (dockerfile ou compose)")
		return
	}
	f.Name = strings.TrimSpace(f.Name)
	if !docNameRe.MatchString(f.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if strings.TrimSpace(f.Content) == "" || len(f.Content) > 1<<20 {
		fail(w, 400, "conteúdo vazio ou maior que 1 MiB")
		return
	}
	var err error
	if r.Method == http.MethodPut {
		f.ID, err = pathID(r)
		if err == nil {
			_, err = s.db.Exec(`UPDATE docker_files SET kind=$2, name=$3, content=$4, updated_at=now() WHERE id=$1`, f.ID, f.Kind, f.Name, f.Content)
		}
	} else {
		err = s.db.QueryRow(`INSERT INTO docker_files(kind, name, content) VALUES ($1,$2,$3) RETURNING id`, f.Kind, f.Name, f.Content).Scan(&f.ID)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("docker", f.Kind+"_save", f.Name, u, "info", "arquivo salvo", nil)
	writeJSON(w, 200, map[string]any{"id": f.ID})
}

func (s *Server) deleteDockerFile(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name, kind string
	if err := s.db.QueryRow(`DELETE FROM docker_files WHERE id=$1 RETURNING name, kind`, id).Scan(&name, &kind); err != nil {
		failErr(w, err)
		return
	}
	s.audit("docker", kind+"_delete", name, u, "info", "arquivo excluído", nil)
	ok(w)
}

// validateDockerFile: compose → `docker compose config -q`; Dockerfile → `trivy config` (misconfigurações).
func (s *Server) validateDockerFile(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	f, err := s.loadDockerFile(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	dir, cleanup, err := s.runDir()
	if err != nil {
		failErr(w, err)
		return
	}
	var steps []jobs.Step
	if f.Kind == "compose" {
		if !sysinfo.Has(r.Context(), "docker-compose") {
			cleanup()
			fail(w, 400, "docker compose (plugin) não está instalado no servidor")
			return
		}
		os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(f.Content), 0o600)
		// variáveis ${VAR} sem valor geram aviso, não erro
		steps = []jobs.Step{{Name: "docker compose config", Args: []string{"docker", "compose", "-f", "compose.yaml", "config", "--no-interpolate", "-q"}},
			{Name: "docker compose config (renderizado)", Args: []string{"docker", "compose", "-f", "compose.yaml", "config", "--no-interpolate"}}}
	} else {
		if !sysinfo.Has(r.Context(), "trivy") {
			cleanup()
			fail(w, 400, "trivy não está instalado no servidor")
			return
		}
		os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(f.Content), 0o600)
		steps = []jobs.Step{{Name: "trivy config (Dockerfile)", Args: []string{"trivy", "config", "--exit-code", "0", "."}}}
	}
	s.startJob(w, u, jobs.Spec{Module: "docker", Action: "validate_" + f.Kind, Target: f.Name,
		Input: map[string]any{"file_id": id}, Steps: steps, Dir: dir, Cleanup: cleanup, Timeout: 5 * time.Minute})
}

// ---------- registries ----------

type Registry struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Username  string     `json:"username"`
	HasPass   bool       `json:"has_password"`
	LastLogin *time.Time `json:"last_login_at"`
	LoggedIn  bool       `json:"logged_in"`
}

var registryURLRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9._/-]*)?$`)

// dockerConfigAuths lê o config.json do DOCKER_CONFIG do doomctl.
func (s *Server) dockerConfigAuths() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(s.cfg.Path("docker", "config.json"))
	if err != nil {
		return out
	}
	var c struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	json.Unmarshal(b, &c)
	for k := range c.Auths {
		out[k] = true
	}
	return out
}

func registryKey(url string) string {
	if url == "" || url == "docker.io" || url == "index.docker.io" {
		return "https://index.docker.io/v1/"
	}
	return url
}

func (s *Server) listRegistries(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, name, url, username, password_enc IS NOT NULL, last_login_at FROM docker_registries ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	auths := s.dockerConfigAuths()
	out := []Registry{}
	for rows.Next() {
		var g Registry
		rows.Scan(&g.ID, &g.Name, &g.URL, &g.Username, &g.HasPass, &g.LastLogin)
		g.LoggedIn = auths[registryKey(g.URL)]
		out = append(out, g)
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveRegistry(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Name, URL, Username, Password string
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Name, in.URL = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.URL))
	in.URL = strings.TrimPrefix(strings.TrimPrefix(in.URL, "https://"), "http://")
	if !vaultNameRe.MatchString(in.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if in.URL != "" && !registryURLRe.MatchString(in.URL) {
		fail(w, 400, "URL do registry inválida (ex.: registry.exemplo.local:5000, ghcr.io)")
		return
	}
	var enc []byte
	if in.Password != "" {
		var err error
		if enc, err = s.box.Seal([]byte(in.Password)); err != nil {
			failErr(w, err)
			return
		}
	}
	var id int64
	err := s.db.QueryRow(`INSERT INTO docker_registries(name, url, username, password_enc) VALUES ($1,$2,$3,$4)
		ON CONFLICT (name) DO UPDATE SET url=EXCLUDED.url, username=EXCLUDED.username,
		password_enc=COALESCE(EXCLUDED.password_enc, docker_registries.password_enc) RETURNING id`,
		in.Name, in.URL, strings.TrimSpace(in.Username), enc).Scan(&id)
	if err != nil {
		failErr(w, err)
		return
	}
	target := in.URL
	if target == "" {
		target = "Docker Hub"
	}
	s.audit("docker", "registry_save", in.Name, u, "info", "registry "+target+" salvo", nil)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) loadRegistry(ctx context.Context, id int64) (name, url, user, pass string, err error) {
	var enc []byte
	err = s.db.QueryRowContext(ctx, `SELECT name, url, username, password_enc FROM docker_registries WHERE id=$1`, id).Scan(&name, &url, &user, &enc)
	if err != nil {
		return
	}
	if len(enc) > 0 {
		var p []byte
		if p, err = s.box.Open(enc); err != nil {
			return
		}
		pass = string(p)
	}
	return
}

func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM docker_registries WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("docker", "registry_delete", name, u, "info", "registry removido", nil)
	ok(w)
}

func (s *Server) registryLogin(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Password string }
	decode(r, &in)
	name, url, user, pass, err := s.loadRegistry(r.Context(), id)
	if err == sql.ErrNoRows {
		fail(w, 404, "registry não encontrado")
		return
	} else if err != nil {
		failErr(w, err)
		return
	}
	if in.Password != "" {
		pass = in.Password
	}
	if user == "" || pass == "" {
		fail(w, 400, "informe usuário e senha/token do registry")
		return
	}
	if !sysinfo.Has(r.Context(), "docker") {
		fail(w, 400, "docker CLI não está instalado no servidor")
		return
	}
	args := []string{"docker", "login", "--username", user, "--password-stdin"}
	if url != "" {
		args = append(args, url)
	}
	target := url
	if target == "" {
		target = "Docker Hub"
	}
	s.startJob(w, u, jobs.Spec{Module: "docker", Action: "registry_login", Target: fmt.Sprintf("%s (%s)", name, target),
		Input: map[string]any{"registry_id": id}, Steps: []jobs.Step{{Args: args, Stdin: []byte(pass + "\n")}},
		Mask: []string{pass}, Timeout: 2 * time.Minute,
		Finish: func(ctx context.Context, res *jobs.Result) {
			if res.Status == "success" {
				s.db.Exec(`UPDATE docker_registries SET last_login_at=now() WHERE id=$1`, id)
			}
		}})
}

func (s *Server) registryLogout(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	name, url, _, _, err := s.loadRegistry(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	args := []string{"docker", "logout"}
	if url != "" {
		args = append(args, url)
	}
	s.startJob(w, u, jobs.Spec{Module: "docker", Action: "registry_logout", Target: name,
		Steps: []jobs.Step{{Args: args}}, Timeout: time.Minute})
}
