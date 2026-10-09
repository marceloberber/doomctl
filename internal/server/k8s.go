package server

import (
	"context"
	"encoding/json"
	"errors"
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

func (s *Server) k8sTemplates(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, gen.K8sTemplates())
}

func (s *Server) k8sRender(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Template string     `json:"template"`
		Values   gen.Values `json:"values"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Values == nil {
		in.Values = gen.Values{}
	}
	files, err := gen.RenderK8s(in.Template, in.Values)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

func (s *Server) k8sBundle(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Name  string            `json:"name"`
		Files map[string]string `json:"files"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	name := in.Name
	if !gen.NameRe.MatchString(name) {
		name = "doomctl-k8s"
	}
	files := map[string]string{}
	for n, c := range in.Files {
		n = filepath.ToSlash(filepath.Clean(n))
		if strings.HasPrefix(n, "..") || strings.HasPrefix(n, "/") {
			continue
		}
		files[name+"/"+n] = c
	}
	writeTarGz(w, name+".tar.gz", files)
}

type Manifest struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Server) listManifests(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, name, kind, updated_at FROM k8s_manifests ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []Manifest{}
	for rows.Next() {
		var m Manifest
		rows.Scan(&m.ID, &m.Name, &m.Kind, &m.UpdatedAt)
		out = append(out, m)
	}
	writeJSON(w, 200, out)
}

func (s *Server) loadManifest(ctx context.Context, id int64) (Manifest, error) {
	var m Manifest
	err := s.db.QueryRowContext(ctx, `SELECT id, name, kind, content, updated_at FROM k8s_manifests WHERE id=$1`, id).
		Scan(&m.ID, &m.Name, &m.Kind, &m.Content, &m.UpdatedAt)
	return m, err
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	m, err := s.loadManifest(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, m)
}

var kindRe = regexp.MustCompile(`(?m)^kind:\s*([A-Za-z]+)`)

func (s *Server) saveManifest(w http.ResponseWriter, r *http.Request, u *User) {
	var m Manifest
	if err := decode(r, &m); err != nil {
		fail(w, 400, err.Error())
		return
	}
	m.Name = strings.TrimSpace(m.Name)
	if !docNameRe.MatchString(m.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if strings.TrimSpace(m.Content) == "" || len(m.Content) > 1<<20 {
		fail(w, 400, "conteúdo vazio ou maior que 1 MiB")
		return
	}
	kinds := []string{}
	for _, k := range kindRe.FindAllStringSubmatch(m.Content, -1) {
		kinds = append(kinds, k[1])
	}
	m.Kind = truncate(strings.Join(kinds, ", "), 120)
	var err error
	if r.Method == http.MethodPut {
		if m.ID, err = pathID(r); err == nil {
			_, err = s.db.Exec(`UPDATE k8s_manifests SET name=$2, kind=$3, content=$4, updated_at=now() WHERE id=$1`, m.ID, m.Name, m.Kind, m.Content)
		}
	} else {
		err = s.db.QueryRow(`INSERT INTO k8s_manifests(name, kind, content) VALUES ($1,$2,$3) RETURNING id`, m.Name, m.Kind, m.Content).Scan(&m.ID)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("kubernetes", "manifest_save", m.Name, u, "info", "manifest salvo ("+m.Kind+")", nil)
	writeJSON(w, 200, map[string]any{"id": m.ID})
}

func (s *Server) deleteManifest(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM k8s_manifests WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("kubernetes", "manifest_delete", name, u, "info", "manifest excluído", nil)
	ok(w)
}

// validateYAML usa o PyYAML (instalado com o Ansible) para validar a sintaxe e
// conferir os campos obrigatórios de cada documento Kubernetes.
const yamlCheckPy = `
import sys, json, yaml
out = {"ok": True, "documents": [], "errors": []}
try:
    for i, d in enumerate(yaml.safe_load_all(sys.stdin)):
        if d is None:
            continue
        if not isinstance(d, dict):
            out["errors"].append("documento %d não é um objeto" % (i+1)); continue
        miss = [k for k in ("apiVersion", "kind", "metadata") if k not in d]
        name = (d.get("metadata") or {}).get("name") if isinstance(d.get("metadata"), dict) else None
        if miss:
            out["errors"].append("documento %d: faltando %s" % (i+1, ", ".join(miss)))
        elif not name:
            out["errors"].append("documento %d (%s): metadata.name ausente" % (i+1, d.get("kind")))
        out["documents"].append({"kind": d.get("kind"), "apiVersion": d.get("apiVersion"), "name": name,
                                 "namespace": (d.get("metadata") or {}).get("namespace") if isinstance(d.get("metadata"), dict) else None})
except yaml.YAMLError as e:
    out["errors"].append(str(e))
out["ok"] = not out["errors"]
print(json.dumps(out))
`

func (s *Server) validateManifest(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Content string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	so, se, code, err := sysinfo.Run(r.Context(), 20*time.Second, []byte(in.Content), nil, "python3", "-c", yamlCheckPy)
	if err != nil || code != 0 {
		fail(w, 500, "validador indisponível (python3 + PyYAML): "+lastLines(se, 2))
		return
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(so), &res); err != nil {
		fail(w, 500, "resposta inválida do validador")
		return
	}
	writeJSON(w, 200, res)
}

// ---------- clusters (kubeconfig) ----------

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, name, context, created_at FROM k8s_clusters ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, ctxName string
		var c time.Time
		rows.Scan(&id, &name, &ctxName, &c)
		out = append(out, map[string]any{"id": id, "name": name, "context": ctxName, "created_at": c})
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveCluster(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Name       string `json:"name"`
		Context    string `json:"context"`
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !vaultNameRe.MatchString(in.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if !strings.Contains(in.Kubeconfig, "clusters:") || len(in.Kubeconfig) > 256<<10 {
		fail(w, 400, "kubeconfig inválido")
		return
	}
	if in.Context != "" && !regexp.MustCompile(`^[A-Za-z0-9@:._/-]{1,200}$`).MatchString(in.Context) {
		fail(w, 400, "contexto inválido")
		return
	}
	for _, bad := range []string{"exec:", "auth-provider:"} {
		if strings.Contains(in.Kubeconfig, bad) {
			fail(w, 400, "kubeconfig com '"+strings.TrimSuffix(bad, ":")+"' não é suportado (executaria binários no servidor). Use token ou certificado de uma ServiceAccount")
			return
		}
	}
	enc, err := s.box.Seal([]byte(in.Kubeconfig))
	if err != nil {
		failErr(w, err)
		return
	}
	var id int64
	if err := s.db.QueryRow(`INSERT INTO k8s_clusters(name, context, kubeconfig_enc) VALUES ($1,$2,$3)
		ON CONFLICT (name) DO UPDATE SET context=EXCLUDED.context, kubeconfig_enc=EXCLUDED.kubeconfig_enc RETURNING id`,
		in.Name, in.Context, enc).Scan(&id); err != nil {
		failErr(w, err)
		return
	}
	s.audit("kubernetes", "cluster_save", in.Name, u, "info", "kubeconfig salvo (criptografado)", nil)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM k8s_clusters WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("kubernetes", "cluster_delete", name, u, "info", "cluster removido", nil)
	ok(w)
}

// kubeRun prepara um diretório com o kubeconfig descriptografado (0600).
func (s *Server) kubeRun(ctx context.Context, id int64) (dir string, cleanup func(), name string, base []string, err error) {
	var enc []byte
	var ctxName string
	if err = s.db.QueryRowContext(ctx, `SELECT name, context, kubeconfig_enc FROM k8s_clusters WHERE id=$1`, id).Scan(&name, &ctxName, &enc); err != nil {
		return
	}
	kc, err := s.box.Open(enc)
	if err != nil {
		return
	}
	dir, cleanup, err = s.runDir()
	if err != nil {
		return
	}
	if err = os.WriteFile(filepath.Join(dir, "kubeconfig"), kc, 0o600); err != nil {
		cleanup()
		return
	}
	base = []string{"--kubeconfig", "kubeconfig"}
	if ctxName != "" {
		base = append(base, "--context", ctxName)
	}
	return
}

var kubectlVerbs = map[string]bool{"get": true, "describe": true, "logs": true, "top": true, "events": true, "explain": true,
	"api-resources": true, "api-versions": true, "version": true, "cluster-info": true, "auth": true, "rollout": true, "scale": true}

var kubectlBlockedFlags = []string{"--kubeconfig", "--context", "--token", "--server", "-s", "--as", "--as-group", "--as-uid",
	"--user", "--cluster", "--certificate-authority", "--client-key", "--client-certificate", "-f", "--filename", "-k", "--kustomize"}

func parseKubectl(line string) ([]string, error) {
	args, err := gen.SplitArgs(line)
	if err != nil {
		return nil, err
	}
	if len(args) > 0 && args[0] == "kubectl" {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil, errors.New("informe um comando kubectl (ex.: get pods -A)")
	}
	if !kubectlVerbs[args[0]] {
		return nil, fmt.Errorf("verbo %q não permitido no terminal. Permitidos: get, describe, logs, top, events, explain, api-resources, version, cluster-info, auth, rollout, scale", args[0])
	}
	if args[0] == "rollout" && len(args) > 1 && !map[string]bool{"status": true, "history": true, "undo": true, "restart": true, "pause": true, "resume": true}[args[1]] {
		return nil, errors.New("rollout: use status, history, undo, restart, pause ou resume")
	}
	if args[0] == "auth" && (len(args) < 2 || args[1] != "can-i") {
		return nil, errors.New("auth: somente 'auth can-i'")
	}
	for _, a := range args {
		f, _, _ := strings.Cut(a, "=")
		for _, b := range kubectlBlockedFlags {
			if f == b {
				return nil, fmt.Errorf("opção %s não permitida", b)
			}
		}
	}
	return args, nil
}

func (s *Server) kubectl(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Command string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	args, err := parseKubectl(strings.TrimSpace(in.Command))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !sysinfo.Has(r.Context(), "kubectl") {
		fail(w, 400, "kubectl não está instalado no servidor")
		return
	}
	dir, cleanup, name, base, err := s.kubeRun(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	argv := append([]string{"kubectl"}, base...)
	argv = append(argv, args...)
	s.startJob(w, u, jobs.Spec{Module: "kubernetes", Action: "kubectl " + args[0], Target: name + ": " + truncate(in.Command, 150),
		Input: map[string]any{"cluster_id": id, "command": in.Command}, Steps: []jobs.Step{{Args: argv}},
		Dir: dir, Cleanup: cleanup, Timeout: 10 * time.Minute})
}

// k8sApply: diff, dry-run (server) ou apply de um manifest salvo.
func (s *Server) k8sApply(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		ManifestID int64  `json:"manifest_id"`
		Mode       string `json:"mode"` // diff | dry-run | apply
		Namespace  string `json:"namespace"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Namespace != "" && !gen.NameRe.MatchString(in.Namespace) {
		fail(w, 400, "namespace inválido")
		return
	}
	m, err := s.loadManifest(r.Context(), in.ManifestID)
	if err != nil {
		failErr(w, err)
		return
	}
	if !sysinfo.Has(r.Context(), "kubectl") {
		fail(w, 400, "kubectl não está instalado no servidor")
		return
	}
	dir, cleanup, name, base, err := s.kubeRun(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(m.Content), 0o600)
	k := append([]string{"kubectl"}, base...)
	if in.Namespace != "" {
		k = append(k, "-n", in.Namespace)
	}
	with := func(extra ...string) []string { return append(append([]string{}, k...), extra...) }
	var steps []jobs.Step
	switch in.Mode {
	case "diff":
		// exit 1 = há diferenças (não é erro)
		steps = []jobs.Step{{Name: "kubectl diff", Args: with("diff", "-f", "manifest.yaml"), AllowFail: true}}
	case "dry-run":
		steps = []jobs.Step{{Name: "kubectl apply --dry-run=server", Args: with("apply", "--dry-run=server", "-f", "manifest.yaml")}}
	case "apply":
		steps = []jobs.Step{
			{Name: "kubectl apply --dry-run=server", Args: with("apply", "--dry-run=server", "-f", "manifest.yaml")},
			{Name: "kubectl apply", Args: with("apply", "-f", "manifest.yaml")},
		}
	default:
		cleanup()
		fail(w, 400, "modo inválido (diff, dry-run, apply)")
		return
	}
	s.startJob(w, u, jobs.Spec{Module: "kubernetes", Action: "manifest_" + in.Mode, Target: name + ": " + m.Name,
		Input: map[string]any{"cluster_id": id, "manifest_id": m.ID, "namespace": in.Namespace}, Steps: steps,
		Dir: dir, Cleanup: cleanup, Timeout: 10 * time.Minute})
}

// k8sScan: misconfigurações e vulnerabilidades do cluster com `trivy k8s`.
func (s *Server) k8sScan(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !sysinfo.Has(r.Context(), "trivy") {
		fail(w, 400, "trivy não está instalado no servidor")
		return
	}
	dir, cleanup, name, base, err := s.kubeRun(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	args := []string{"trivy", "k8s", "--kubeconfig", "kubeconfig", "--report", "summary", "--disable-node-collector", "--no-progress"}
	for i := 0; i+1 < len(base); i++ {
		if base[i] == "--context" {
			args = append(args, base[i+1]) // contexto é posicional: trivy k8s [flags] [CONTEXT]
		}
	}
	s.startJob(w, u, jobs.Spec{Module: "kubernetes", Action: "trivy_k8s", Target: name,
		Input: map[string]any{"cluster_id": id}, Steps: []jobs.Step{{Args: args}}, Dir: dir, Cleanup: cleanup, Timeout: time.Hour})
}
