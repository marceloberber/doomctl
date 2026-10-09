package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doomctl/doomctl/internal/gen"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

var (
	tofuFileRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,60}\.(tf|tfvars|tf\.json|tfvars\.json|tftpl)$`)
	tofuEnvRe  = regexp.MustCompile(`^(AWS_[A-Z_]{2,40}|OCI_[A-Z_]{2,40}|TF_VAR_[A-Za-z0-9_]{1,60})$`)
	tofuBusy   sync.Map // project id → true durante uma ação
)

type TofuProject struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`
	Description string            `json:"description"`
	Files       map[string]string `json:"files,omitempty"`
	CredKeys    []string          `json:"credential_keys"`
	PlanKind    string            `json:"plan_kind"`
	PlanAt      *time.Time        `json:"plan_at"`
	PlanValid   bool              `json:"plan_valid"`
	HasState    bool              `json:"has_state"`
	UpdatedAt   time.Time         `json:"updated_at"`
	planHash    string
	credsEnc    []byte
}

func filesHash(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%s\x00", n, files[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) tofuDir(id int64) string { return s.cfg.Path("tofu", strconv.FormatInt(id, 10)) }

func (s *Server) loadTofu(ctx context.Context, id int64) (*TofuProject, error) {
	p := &TofuProject{}
	var files []byte
	err := s.db.QueryRowContext(ctx, `SELECT id, name, provider, description, files, credentials_enc, plan_hash, plan_kind, plan_at, updated_at
		FROM tofu_projects WHERE id=$1`, id).Scan(&p.ID, &p.Name, &p.Provider, &p.Description, &files, &p.credsEnc,
		&p.planHash, &p.PlanKind, &p.PlanAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(files, &p.Files)
	if p.Files == nil {
		p.Files = map[string]string{}
	}
	p.PlanValid = p.PlanKind != "" && p.planHash == filesHash(p.Files) && p.PlanAt != nil && time.Since(*p.PlanAt) < 24*time.Hour
	creds, _ := s.tofuCreds(p)
	p.CredKeys = []string{}
	for k := range creds {
		p.CredKeys = append(p.CredKeys, k)
	}
	sort.Strings(p.CredKeys)
	if st, err := os.Stat(filepath.Join(s.tofuDir(id), "terraform.tfstate")); err == nil && st.Size() > 0 {
		p.HasState = true
	}
	return p, nil
}

func (s *Server) tofuCreds(p *TofuProject) (map[string]string, error) {
	m := map[string]string{}
	if len(p.credsEnc) == 0 {
		return m, nil
	}
	b, err := s.box.Open(p.credsEnc)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(b, &m)
	return m, nil
}

func (s *Server) generateTofu(w http.ResponseWriter, r *http.Request, _ *User) {
	var spec gen.TofuSpec
	if err := decode(r, &spec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	files, err := gen.Tofu(spec)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

func (s *Server) listTofu(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id FROM tofu_projects ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []*TofuProject{}
	for _, id := range ids {
		if p, err := s.loadTofu(r.Context(), id); err == nil {
			p.Files = nil
			out = append(out, p)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) getTofu(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	p, err := s.loadTofu(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	_, busy := tofuBusy.Load(id)
	writeJSON(w, 200, map[string]any{"project": p, "busy": busy})
}

func (s *Server) saveTofu(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Name        string            `json:"name"`
		Provider    string            `json:"provider"`
		Description string            `json:"description"`
		Files       map[string]string `json:"files"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !vaultNameRe.MatchString(in.Name) {
		fail(w, 400, "nome inválido (letras, números, . _ -)")
		return
	}
	if in.Provider != "aws" && in.Provider != "oci" {
		fail(w, 400, "provider deve ser aws ou oci")
		return
	}
	if len(in.Files) == 0 || len(in.Files) > 50 {
		fail(w, 400, "o projeto precisa de 1 a 50 arquivos")
		return
	}
	total := 0
	for n, c := range in.Files {
		if !tofuFileRe.MatchString(n) {
			fail(w, 400, "nome de arquivo inválido: "+n+" (use .tf, .tfvars, .tf.json, .tftpl)")
			return
		}
		total += len(c)
	}
	if total > 2<<20 {
		fail(w, 400, "arquivos excedem 2 MiB")
		return
	}
	files, _ := json.Marshal(in.Files)
	var id int64
	var err error
	if r.Method == http.MethodPut {
		if id, err = pathID(r); err == nil {
			_, err = s.db.Exec(`UPDATE tofu_projects SET name=$2, provider=$3, description=$4, files=$5, updated_at=now() WHERE id=$1`,
				id, in.Name, in.Provider, truncate(in.Description, 300), string(files))
		}
	} else {
		err = s.db.QueryRow(`INSERT INTO tofu_projects(name, provider, description, files) VALUES ($1,$2,$3,$4) RETURNING id`,
			in.Name, in.Provider, truncate(in.Description, 300), string(files)).Scan(&id)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("opentofu", "project_save", in.Name, u, "info", fmt.Sprintf("%d arquivos (%s)", len(in.Files), in.Provider), nil)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) deleteTofu(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Confirm string }
	decode(r, &in)
	p, err := s.loadTofu(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	if p.HasState && in.Confirm != p.Name {
		fail(w, 409, "o projeto possui state local (recursos podem existir na nuvem). Rode destroy antes ou confirme digitando o nome do projeto")
		return
	}
	if _, busy := tofuBusy.Load(id); busy {
		fail(w, 409, "há uma ação em andamento neste projeto")
		return
	}
	s.db.Exec(`DELETE FROM tofu_projects WHERE id=$1`, id)
	os.RemoveAll(s.tofuDir(id))
	s.audit("opentofu", "project_delete", p.Name, u, "info", "projeto excluído (state local removido)", nil)
	ok(w)
}

func (s *Server) setTofuCreds(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		Env   map[string]string `json:"env"`
		Merge bool              `json:"merge"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	p, err := s.loadTofu(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	m := map[string]string{}
	if in.Merge {
		if m, err = s.tofuCreds(p); err != nil {
			failErr(w, err)
			return
		}
	}
	for k, v := range in.Env {
		k = strings.TrimSpace(k)
		if !tofuEnvRe.MatchString(k) {
			fail(w, 400, "variável não permitida: "+k+" (use AWS_*, OCI_* ou TF_VAR_*)")
			return
		}
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	var enc []byte
	if len(m) > 0 {
		b, _ := json.Marshal(m)
		if enc, err = s.box.Seal(b); err != nil {
			failErr(w, err)
			return
		}
	}
	s.db.Exec(`UPDATE tofu_projects SET credentials_enc=$2, updated_at=now() WHERE id=$1`, id, enc)
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s.audit("opentofu", "credentials", p.Name, u, "info", "credenciais atualizadas: "+strings.Join(keys, ", "), nil)
	writeJSON(w, 200, map[string]any{"credential_keys": keys})
}

// syncTofuFiles grava os arquivos do projeto no diretório de trabalho (o state permanece).
func (s *Server) syncTofuFiles(p *TofuProject) (string, error) {
	dir := s.tofuDir(p.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && tofuFileRe.MatchString(e.Name()) {
			if _, keep := p.Files[e.Name()]; !keep {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	for n, c := range p.Files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (s *Server) tofuAction(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	action := r.PathValue("action")
	var in struct{ Confirm string }
	decode(r, &in)
	if !sysinfo.Has(r.Context(), "tofu") {
		fail(w, 400, "OpenTofu (tofu) não está instalado no servidor")
		return
	}
	p, err := s.loadTofu(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	creds, err := s.tofuCreds(p)
	if err != nil {
		failErr(w, err)
		return
	}
	init := jobs.Step{Name: "tofu init", Args: []string{"tofu", "init", "-input=false", "-no-color"}}
	var steps []jobs.Step
	timeout := 30 * time.Minute
	hash := filesHash(p.Files)
	var finish func(context.Context, *jobs.Result)
	switch action {
	case "init":
		steps = []jobs.Step{{Name: "tofu init -upgrade", Args: []string{"tofu", "init", "-input=false", "-no-color", "-upgrade"}}}
	case "validate":
		steps = []jobs.Step{init, {Name: "tofu validate", Args: []string{"tofu", "validate", "-no-color"}}}
	case "fmt":
		steps = []jobs.Step{{Name: "tofu fmt -check -diff", Args: []string{"tofu", "fmt", "-check", "-diff", "-no-color"}}}
	case "output":
		steps = []jobs.Step{{Name: "tofu output", Args: []string{"tofu", "output", "-no-color"}}}
	case "plan", "plan-destroy":
		args := []string{"tofu", "plan", "-input=false", "-no-color", "-out=tfplan"}
		kind := "apply"
		if action == "plan-destroy" {
			args = append(args, "-destroy")
			kind = "destroy"
		}
		steps = []jobs.Step{init, {Name: strings.Join(args[:2], " ") + map[string]string{"apply": "", "destroy": " -destroy"}[kind], Args: args}}
		finish = func(ctx context.Context, res *jobs.Result) {
			if res.Status == "success" {
				s.db.Exec(`UPDATE tofu_projects SET plan_hash=$2, plan_kind=$3, plan_at=now() WHERE id=$1`, id, hash, kind)
			} else {
				s.db.Exec(`UPDATE tofu_projects SET plan_hash='', plan_kind='', plan_at=NULL WHERE id=$1`, id)
			}
		}
	case "apply", "destroy":
		want := "apply"
		if action == "destroy" {
			want = "destroy"
			if in.Confirm != p.Name {
				fail(w, 400, "🛑 Destrutivo: digite o nome do projeto para confirmar o destroy")
				return
			}
		}
		if !p.PlanValid || p.PlanKind != want {
			fail(w, 409, fmt.Sprintf("execute '%s' antes: o %s exige um plano salvo, válido (< 24h) e feito sobre os arquivos atuais",
				map[string]string{"apply": "plan", "destroy": "plan-destroy"}[want], action))
			return
		}
		steps = []jobs.Step{{Name: "tofu apply (plano salvo)", Args: []string{"tofu", "apply", "-input=false", "-no-color", "tfplan"}}}
		timeout = 2 * time.Hour
		finish = func(ctx context.Context, res *jobs.Result) {
			s.db.Exec(`UPDATE tofu_projects SET plan_hash='', plan_kind='', plan_at=NULL WHERE id=$1`, id)
		}
	default:
		fail(w, 400, "ação inválida")
		return
	}
	if _, busy := tofuBusy.LoadOrStore(id, true); busy {
		fail(w, 409, "já existe uma ação em andamento neste projeto")
		return
	}
	dir, err := s.syncTofuFiles(p)
	if err != nil {
		tofuBusy.Delete(id)
		failErr(w, err)
		return
	}
	env := []string{}
	mask := []string{}
	for k, v := range creds {
		env = append(env, k+"="+v)
		if strings.Contains(k, "SECRET") || strings.Contains(k, "TOKEN") || strings.Contains(strings.ToLower(k), "private_key") ||
			strings.Contains(strings.ToLower(k), "password") {
			mask = append(mask, v)
		}
	}
	s.startJob(w, u, jobs.Spec{Module: "opentofu", Action: action, Target: p.Name,
		Input: map[string]any{"project_id": id, "provider": p.Provider, "files_hash": hash[:12]},
		Steps: steps, Dir: dir, Env: env, Mask: mask, Timeout: timeout, Finish: finish,
		Cleanup: func() { tofuBusy.Delete(id) }})
}

func (s *Server) downloadTofu(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	p, err := s.loadTofu(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	files := map[string]string{}
	for n, c := range p.Files {
		files[p.Name+"/"+n] = c
	}
	files[p.Name+"/.gitignore"] = ".terraform/\n*.tfstate\n*.tfstate.*\ntfplan\n*.tfvars\ncrash.log\n"
	writeTarGz(w, p.Name+"-opentofu.tar.gz", files)
}
