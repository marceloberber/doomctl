package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doomctl/doomctl/internal/gen"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/secure"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

// trivyImages lista as imagens locais (via socket do Docker) para seleção na UI.
func (s *Server) trivyImages(w http.ResponseWriter, r *http.Request, _ *User) {
	out := []map[string]string{}
	if !sysinfo.Has(r.Context(), "docker") {
		writeJSON(w, 200, map[string]any{"images": out, "warning": "docker CLI não instalado"})
		return
	}
	so, se, code, err := sysinfo.Run(r.Context(), 15*time.Second, nil, s.toolEnv(), "docker", "image", "ls", "--format", "{{json .}}")
	if err != nil || code != 0 {
		writeJSON(w, 200, map[string]any{"images": out, "warning": "não foi possível listar as imagens locais: " + lastLines(se, 1)})
		return
	}
	sc := bufio.NewScanner(strings.NewReader(so))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		repo, _ := m["Repository"].(string)
		tag, _ := m["Tag"].(string)
		if repo == "" || repo == "<none>" {
			continue
		}
		ref := repo
		if tag != "" && tag != "<none>" {
			ref += ":" + tag
		}
		size, _ := m["Size"].(string)
		created, _ := m["CreatedSince"].(string)
		out = append(out, map[string]string{"ref": ref, "size": size, "created": created})
	}
	writeJSON(w, 200, map[string]any{"images": out})
}

type trivyVuln struct {
	Target    string `json:"target"`
	ID        string `json:"id"`
	Pkg       string `json:"pkg"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	URL       string `json:"url"`
}

type trivyMisconf struct {
	Target     string `json:"target"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	Resolution string `json:"resolution"`
	URL        string `json:"url"`
}

type trivySecret struct {
	Target   string `json:"target"`
	RuleID   string `json:"rule_id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Line     int    `json:"line"`
}

type trivySummary struct {
	Kind            string         `json:"kind"`
	Target          string         `json:"target"`
	Summary         map[string]int `json:"summary"`
	Vulnerabilities []trivyVuln    `json:"vulnerabilities"`
	Misconfigs      []trivyMisconf `json:"misconfigurations"`
	Secrets         []trivySecret  `json:"secrets"`
	Truncated       bool           `json:"truncated"`
}

var sevOrder = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}

// parseTrivyJSON resume o relatório JSON do Trivy.
func parseTrivyJSON(b []byte, kind, target string) (*trivySummary, error) {
	var rep struct {
		Results []struct {
			Target          string
			Vulnerabilities []struct {
				VulnerabilityID, PkgName, InstalledVersion, FixedVersion, Severity, Title, PrimaryURL string
			}
			Misconfigurations []struct {
				ID, AVDID, Title, Severity, Message, Resolution, PrimaryURL, Status string
			}
			Secrets []struct {
				RuleID, Title, Severity string
				StartLine               int
			}
		}
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, err
	}
	sum := &trivySummary{Kind: kind, Target: target, Summary: map[string]int{"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0, "UNKNOWN": 0},
		Vulnerabilities: []trivyVuln{}, Misconfigs: []trivyMisconf{}, Secrets: []trivySecret{}}
	const max = 2000
	for _, res := range rep.Results {
		for _, v := range res.Vulnerabilities {
			sum.Summary[v.Severity]++
			if len(sum.Vulnerabilities) < max {
				sum.Vulnerabilities = append(sum.Vulnerabilities, trivyVuln{res.Target, v.VulnerabilityID, v.PkgName,
					v.InstalledVersion, v.FixedVersion, v.Severity, v.Title, v.PrimaryURL})
			} else {
				sum.Truncated = true
			}
		}
		for _, m := range res.Misconfigurations {
			if m.Status != "" && m.Status != "FAIL" {
				continue
			}
			sum.Summary[m.Severity]++
			id := m.ID
			if id == "" {
				id = m.AVDID
			}
			sum.Misconfigs = append(sum.Misconfigs, trivyMisconf{res.Target, id, m.Title, m.Severity, m.Message, m.Resolution, m.PrimaryURL})
		}
		for _, sc := range res.Secrets {
			sum.Summary[sc.Severity]++
			sum.Secrets = append(sum.Secrets, trivySecret{res.Target, sc.RuleID, sc.Title, sc.Severity, sc.StartLine})
		}
	}
	sort.SliceStable(sum.Vulnerabilities, func(i, j int) bool {
		return sevOrder[sum.Vulnerabilities[i].Severity] < sevOrder[sum.Vulnerabilities[j].Severity]
	})
	sort.SliceStable(sum.Misconfigs, func(i, j int) bool {
		return sevOrder[sum.Misconfigs[i].Severity] < sevOrder[sum.Misconfigs[j].Severity]
	})
	return sum, nil
}

func (t *trivySummary) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n━━ Resumo (%s: %s)\n", t.Kind, t.Target)
	fmt.Fprintf(&b, "CRITICAL: %d  HIGH: %d  MEDIUM: %d  LOW: %d  UNKNOWN: %d\n",
		t.Summary["CRITICAL"], t.Summary["HIGH"], t.Summary["MEDIUM"], t.Summary["LOW"], t.Summary["UNKNOWN"])
	n := 0
	for _, v := range t.Vulnerabilities {
		if n >= 50 {
			fmt.Fprintf(&b, "... (%d no total — veja a tabela)\n", len(t.Vulnerabilities))
			break
		}
		fixed := v.Fixed
		if fixed == "" {
			fixed = "sem correção"
		}
		fmt.Fprintf(&b, "%-9s %-18s %-28s %s → %s\n", v.Severity, v.ID, v.Pkg, v.Installed, fixed)
		n++
	}
	for _, m := range t.Misconfigs {
		fmt.Fprintf(&b, "%-9s %-14s %s — %s\n", m.Severity, m.ID, m.Title, m.Resolution)
	}
	for _, sc := range t.Secrets {
		fmt.Fprintf(&b, "%-9s SECRET %s (%s:%d)\n", sc.Severity, sc.Title, sc.Target, sc.Line)
	}
	return b.String()
}

var validSeverities = map[string]bool{"CRITICAL": true, "HIGH": true, "MEDIUM": true, "LOW": true, "UNKNOWN": true}

func (s *Server) trivyScan(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Kind          string   `json:"kind"` // image | sbom | config
		Image         string   `json:"image"`
		Severities    []string `json:"severities"`
		IgnoreUnfixed bool     `json:"ignore_unfixed"`
		Secrets       bool     `json:"secrets"`
		SkipDBUpdate  bool     `json:"skip_db_update"`
		ImageSrc      string   `json:"image_src"` // auto | docker | remote
		SBOMFormat    string   `json:"sbom_format"`
		Source        string   `json:"source"` // dockerfile:<id> | tofu:<id> | k8s:<id> | content
		Filename      string   `json:"filename"`
		Content       string   `json:"content"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !sysinfo.Has(r.Context(), "trivy") {
		fail(w, 400, "trivy não está instalado no servidor")
		return
	}
	sev := []string{}
	for _, sv := range in.Severities {
		if validSeverities[strings.ToUpper(sv)] {
			sev = append(sev, strings.ToUpper(sv))
		}
	}
	if len(sev) == 0 {
		sev = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}
	}
	dir, cleanup, err := s.runDir()
	if err != nil {
		failErr(w, err)
		return
	}
	common := []string{"--no-progress", "--timeout", "15m"}
	if in.SkipDBUpdate {
		common = append(common, "--skip-db-update")
	}
	reportName := fmt.Sprintf("trivy-%s-%s", time.Now().Format("20060102-150405"), secure.RandomToken(4))
	var args []string
	var target string
	switch in.Kind {
	case "image", "sbom":
		in.Image = strings.TrimSpace(in.Image)
		if !gen.ValidImage(in.Image) {
			cleanup()
			fail(w, 400, "referência de imagem inválida (ex.: nginx:1.29-alpine, registry.local:5000/app:1.0)")
			return
		}
		target = in.Image
		src := []string{}
		switch in.ImageSrc {
		case "docker":
			src = []string{"--image-src", "docker"}
		case "remote":
			src = []string{"--image-src", "remote"}
		}
		if in.Kind == "sbom" {
			format := "cyclonedx"
			ext := "cdx.json"
			if in.SBOMFormat == "spdx-json" {
				format, ext = "spdx-json", "spdx.json"
			}
			art := s.cfg.Path("reports", reportName+"-sbom."+ext)
			args = append([]string{"trivy", "image", "--format", format, "--output", art}, common...)
			args = append(append(args, src...), in.Image)
			s.startJob(w, u, jobs.Spec{Module: "trivy", Action: "sbom", Target: target,
				Input: map[string]any{"image": in.Image, "format": format}, Steps: []jobs.Step{{Args: args}},
				Dir: dir, Cleanup: cleanup, Timeout: 30 * time.Minute,
				Finish: func(ctx context.Context, res *jobs.Result) {
					if res.Status != "success" {
						return
					}
					if st, err := os.Stat(art); err == nil {
						res.Artifact = art
						res.Extra = fmt.Sprintf("\n━━ SBOM %s gerado (%d bytes). Baixe pelo botão de artefato.\n", format, st.Size())
					}
				}})
			return
		}
		scanners := "vuln"
		if in.Secrets {
			scanners += ",secret"
		}
		args = append([]string{"trivy", "image", "--format", "json", "--output", "result.json", "--scanners", scanners,
			"--severity", strings.Join(sev, ",")}, common...)
		if in.IgnoreUnfixed {
			args = append(args, "--ignore-unfixed")
		}
		args = append(append(args, src...), in.Image)
	case "config":
		files, label, err := s.trivyConfigFiles(r.Context(), in.Source, in.Filename, in.Content)
		if err != nil {
			cleanup()
			fail(w, 400, err.Error())
			return
		}
		for n, c := range files {
			p := filepath.Join(dir, "src", n)
			os.MkdirAll(filepath.Dir(p), 0o700)
			os.WriteFile(p, []byte(c), 0o600)
		}
		target = label
		args = []string{"trivy", "config", "--format", "json", "--output", "result.json", "--severity", strings.Join(sev, ","), "src"}
	default:
		cleanup()
		fail(w, 400, "tipo inválido (image, sbom, config)")
		return
	}
	kind := in.Kind
	s.startJob(w, u, jobs.Spec{Module: "trivy", Action: kind, Target: target,
		Input: map[string]any{"kind": kind, "target": target, "severities": sev, "ignore_unfixed": in.IgnoreUnfixed, "secrets": in.Secrets},
		Steps: []jobs.Step{{Args: args}}, Dir: dir, Cleanup: cleanup, Timeout: 30 * time.Minute,
		Finish: func(ctx context.Context, res *jobs.Result) {
			b, err := os.ReadFile(filepath.Join(dir, "result.json"))
			if err != nil {
				return
			}
			art := s.cfg.Path("reports", reportName+".json")
			if os.WriteFile(art, b, 0o600) == nil {
				res.Artifact = art
			}
			sum, err := parseTrivyJSON(b, kind, target)
			if err != nil {
				res.Extra = "\n[doomctl] não foi possível interpretar o relatório JSON: " + err.Error() + "\n"
				return
			}
			res.Data = sum
			res.Extra = sum.text()
		}})
}

// trivyConfigFiles obtém os arquivos para `trivy config` a partir de itens salvos no doomctl.
func (s *Server) trivyConfigFiles(ctx context.Context, source, filename, content string) (map[string]string, string, error) {
	kind, idStr, _ := strings.Cut(source, ":")
	var id int64
	fmt.Sscan(idStr, &id)
	switch kind {
	case "dockerfile":
		f, err := s.loadDockerFile(ctx, id)
		if err != nil || f.Kind != "dockerfile" {
			return nil, "", fmt.Errorf("Dockerfile não encontrado")
		}
		return map[string]string{"Dockerfile": f.Content}, "Dockerfile: " + f.Name, nil
	case "tofu":
		p, err := s.loadTofu(ctx, id)
		if err != nil {
			return nil, "", fmt.Errorf("projeto OpenTofu não encontrado")
		}
		return p.Files, "OpenTofu: " + p.Name, nil
	case "k8s":
		m, err := s.loadManifest(ctx, id)
		if err != nil {
			return nil, "", fmt.Errorf("manifest não encontrado")
		}
		return map[string]string{"manifest.yaml": m.Content}, "Kubernetes: " + m.Name, nil
	case "content":
		if strings.TrimSpace(content) == "" || len(content) > 1<<20 {
			return nil, "", fmt.Errorf("conteúdo vazio ou maior que 1 MiB")
		}
		if !gen.FileNameRe.MatchString(filename) {
			return nil, "", fmt.Errorf("informe um nome de arquivo válido (ex.: Dockerfile, main.tf, deploy.yaml)")
		}
		return map[string]string{filename: content}, "Arquivo: " + filename, nil
	}
	return nil, "", fmt.Errorf("origem inválida")
}

func (s *Server) trivyDBUpdate(w http.ResponseWriter, r *http.Request, u *User) {
	if !sysinfo.Has(r.Context(), "trivy") {
		fail(w, 400, "trivy não está instalado no servidor")
		return
	}
	s.startJob(w, u, jobs.Spec{Module: "trivy", Action: "db_update", Target: "trivy-db + java-db",
		Steps: []jobs.Step{
			{Name: "trivy-db", Args: []string{"trivy", "image", "--download-db-only", "--no-progress"}},
			{Name: "trivy-java-db", Args: []string{"trivy", "image", "--download-java-db-only", "--no-progress"}, AllowFail: true},
		}, Timeout: 30 * time.Minute})
}
