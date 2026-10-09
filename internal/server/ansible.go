package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doomctl/doomctl/internal/gen"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

var (
	hostNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	addressRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
	groupRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	patternRe   = regexp.MustCompile(`^[A-Za-z0-9_.:*!&,\[\]~-]{1,200}$`)
	tagsRe      = regexp.MustCompile(`^[A-Za-z0-9_,-]{1,200}$`)
	allowedConn = map[string]bool{"ssh": true, "paramiko": true, "winrm": true, "psrp": true,
		"network_cli": true, "netconf": true, "httpapi": true}
)

// ---------- inventário ----------

type Host struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Address string            `json:"address"`
	Port    int               `json:"port"`
	Groups  []string          `json:"groups"`
	Vars    map[string]string `json:"vars"`
	Updated time.Time         `json:"updated_at"`
}

func validateHost(h *Host) error {
	h.Name, h.Address = strings.TrimSpace(h.Name), strings.TrimSpace(h.Address)
	if !hostNameRe.MatchString(h.Name) {
		return fmt.Errorf("nome inválido %q (letras, números, . _ -)", h.Name)
	}
	if !addressRe.MatchString(h.Address) {
		return fmt.Errorf("endereço inválido %q", h.Address)
	}
	if h.Port == 0 {
		h.Port = 22
	}
	if h.Port < 1 || h.Port > 65535 {
		return fmt.Errorf("porta inválida")
	}
	seen := map[string]bool{}
	groups := []string{}
	for _, g := range h.Groups {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		if !groupRe.MatchString(g) || g == "all" || g == "ungrouped" {
			return fmt.Errorf("grupo inválido %q (use letras, números e _; sem hífen)", g)
		}
		seen[g] = true
		groups = append(groups, g)
	}
	sort.Strings(groups)
	h.Groups = groups
	if h.Vars == nil {
		h.Vars = map[string]string{}
	}
	for k, v := range h.Vars {
		if !gen.IdentRe.MatchString(k) {
			return fmt.Errorf("variável inválida %q", k)
		}
		if err := checkConnVar(k, v); err != nil {
			return err
		}
		if k == "ansible_host" || k == "ansible_port" {
			delete(h.Vars, k) // definidos pelas colunas próprias
		}
	}
	return nil
}

// checkConnVar impede que o inventário/vault faça o Ansible executar no próprio servidor.
func checkConnVar(k, v string) error {
	if k == "ansible_connection" && !allowedConn[strings.TrimSpace(v)] {
		return fmt.Errorf("ansible_connection=%q não é permitido (use ssh, paramiko, winrm...)", v)
	}
	return nil
}

func (s *Server) loadHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, address, port, groups, vars, updated_at FROM ansible_hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Host{}
	for rows.Next() {
		var h Host
		var groups string
		var vars []byte
		if err := rows.Scan(&h.ID, &h.Name, &h.Address, &h.Port, &groups, &vars, &h.Updated); err != nil {
			return nil, err
		}
		h.Groups = parsePGArray(groups)
		json.Unmarshal(vars, &h.Vars)
		if h.Vars == nil {
			h.Vars = map[string]string{}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// parsePGArray lê um text[] simples ({a,b,c}); nomes de grupo já são validados.
func parsePGArray(s string) []string {
	s = strings.Trim(s, "{}")
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.Trim(parts[i], `"`)
	}
	return parts
}

func pgArray(a []string) string { return "{" + strings.Join(a, ",") + "}" }

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request, _ *User) {
	hs, err := s.loadHosts(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	groups := map[string]int{}
	for _, h := range hs {
		for _, g := range h.Groups {
			groups[g]++
		}
	}
	writeJSON(w, 200, map[string]any{"hosts": hs, "groups": groups})
}

func (s *Server) saveHost(w http.ResponseWriter, r *http.Request, u *User) {
	var h Host
	if err := decode(r, &h); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := validateHost(&h); err != nil {
		fail(w, 400, err.Error())
		return
	}
	vars, _ := json.Marshal(h.Vars)
	var err error
	if r.Method == http.MethodPut {
		id, e := pathID(r)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		_, err = s.db.Exec(`UPDATE ansible_hosts SET name=$2, address=$3, port=$4, groups=$5, vars=$6, updated_at=now() WHERE id=$1`,
			id, h.Name, h.Address, h.Port, pgArray(h.Groups), string(vars))
		h.ID = id
	} else {
		err = s.db.QueryRow(`INSERT INTO ansible_hosts(name, address, port, groups, vars) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			h.Name, h.Address, h.Port, pgArray(h.Groups), string(vars)).Scan(&h.ID)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "inventory_save", h.Name, u, "info", "host "+h.Name+" ("+h.Address+") salvo", nil)
	writeJSON(w, 200, h)
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM ansible_hosts WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "inventory_delete", name, u, "info", "host removido do inventário", nil)
	ok(w)
}

// importHosts: CSV com cabeçalho name,address,port,groups,vars
// groups separados por ";" e vars no formato chave=valor;chave2=valor2.
func (s *Server) importHosts(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		CSV     string `json:"csv"`
		Replace bool   `json:"replace"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	rd := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.CSV, "\ufeff")))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	if strings.Count(strings.SplitN(in.CSV, "\n", 2)[0], ";") > strings.Count(strings.SplitN(in.CSV, "\n", 2)[0], ",") {
		rd.Comma = ';' // planilhas pt-BR exportam com ";"
	}
	recs, err := rd.ReadAll()
	if err != nil {
		fail(w, 400, "CSV inválido: "+err.Error())
		return
	}
	if len(recs) < 2 {
		fail(w, 400, "CSV vazio (cabeçalho: name,address,port,groups,vars)")
		return
	}
	if len(recs) > 5001 {
		fail(w, 400, "máximo de 5000 hosts por importação")
		return
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(rec []string, names ...string) string {
		for _, n := range names {
			if i, ok := col[n]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
		}
		return ""
	}
	if _, ok := col["name"]; !ok {
		if _, ok2 := col["hostname"]; !ok2 {
			fail(w, 400, "cabeçalho precisa da coluna name (ou hostname)")
			return
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		failErr(w, err)
		return
	}
	defer tx.Rollback()
	if in.Replace {
		tx.Exec(`DELETE FROM ansible_hosts`)
	}
	var imported int
	var errs []string
	for i, rec := range recs[1:] {
		line := i + 2
		h := Host{Name: get(rec, "name", "hostname"), Address: get(rec, "address", "ip", "ansible_host"), Vars: map[string]string{}}
		if h.Address == "" {
			h.Address = h.Name
		}
		if p := get(rec, "port", "ansible_port"); p != "" {
			h.Port, _ = strconv.Atoi(p)
		}
		for _, g := range strings.FieldsFunc(get(rec, "groups", "group"), func(r rune) bool { return r == ';' || r == '|' || r == ' ' }) {
			h.Groups = append(h.Groups, g)
		}
		for _, kv := range strings.Split(get(rec, "vars"), ";") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				h.Vars[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if user := get(rec, "user", "ansible_user"); user != "" {
			h.Vars["ansible_user"] = user
		}
		if err := validateHost(&h); err != nil {
			errs = append(errs, fmt.Sprintf("linha %d: %v", line, err))
			continue
		}
		vars, _ := json.Marshal(h.Vars)
		_, err := tx.Exec(`INSERT INTO ansible_hosts(name, address, port, groups, vars) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (name) DO UPDATE SET address=EXCLUDED.address, port=EXCLUDED.port, groups=EXCLUDED.groups,
			vars=EXCLUDED.vars, updated_at=now()`, h.Name, h.Address, h.Port, pgArray(h.Groups), string(vars))
		if err != nil {
			errs = append(errs, fmt.Sprintf("linha %d: %v", line, err))
			continue
		}
		imported++
	}
	if err := tx.Commit(); err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "inventory_import", "csv", u, "info",
		fmt.Sprintf("%d hosts importados, %d erros\n%s", imported, len(errs), strings.Join(errs, "\n")), nil)
	writeJSON(w, 200, map[string]any{"imported": imported, "errors": errs})
}

// inventoryJSON gera o inventário no formato YAML/JSON do Ansible.
func (s *Server) inventoryJSON(ctx context.Context) ([]byte, int, error) {
	hs, err := s.loadHosts(ctx)
	if err != nil {
		return nil, 0, err
	}
	hosts := map[string]map[string]any{}
	children := map[string]map[string]map[string]any{}
	for _, h := range hs {
		hv := map[string]any{"ansible_host": h.Address, "ansible_port": h.Port}
		for k, v := range h.Vars {
			hv[k] = v
		}
		hosts[h.Name] = hv
		for _, g := range h.Groups {
			if children[g] == nil {
				children[g] = map[string]map[string]any{"hosts": {}}
			}
			children[g]["hosts"][h.Name] = map[string]any{}
		}
	}
	all := map[string]any{"hosts": hosts}
	if len(children) > 0 {
		all["children"] = children
	}
	b, err := json.MarshalIndent(map[string]any{"all": all}, "", "  ")
	return b, len(hs), err
}

func iniQuote(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t'\"#;=\\") {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

func (s *Server) exportInventory(w http.ResponseWriter, r *http.Request, _ *User) {
	if r.URL.Query().Get("format") == "json" {
		b, _, err := s.inventoryJSON(r.Context())
		if err != nil {
			failErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="inventory.json"`)
		w.Write(b)
		return
	}
	hs, err := s.loadHosts(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	var b strings.Builder
	b.WriteString("# Inventário exportado pelo doomctl em " + time.Now().Format(time.RFC3339) + "\n[all]\n")
	groups := map[string][]string{}
	for _, h := range hs {
		line := fmt.Sprintf("%s ansible_host=%s ansible_port=%d", h.Name, h.Address, h.Port)
		keys := make([]string, 0, len(h.Vars))
		for k := range h.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			line += " " + k + "=" + iniQuote(h.Vars[k])
		}
		b.WriteString(line + "\n")
		for _, g := range h.Groups {
			groups[g] = append(groups[g], h.Name)
		}
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		b.WriteString("\n[" + g + "]\n" + strings.Join(groups[g], "\n") + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="inventory.ini"`)
	io.WriteString(w, b.String())
}

// ---------- ansible-vault ----------

type VaultKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type VaultData struct {
	AnsibleUser     string    `json:"ansible_user"`
	AnsiblePassword string    `json:"ansible_password"`
	BecomePassword  string    `json:"ansible_become_password"`
	SSHPrivateKey   string    `json:"ssh_private_key"`
	Extra           []VaultKV `json:"extra"`
}

const sshKeyVar = "doomctl_ssh_private_key"

func (d VaultData) toMap() (map[string]string, error) {
	m := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("ansible_user", strings.TrimSpace(d.AnsibleUser))
	set("ansible_password", d.AnsiblePassword)
	set("ansible_become_password", d.BecomePassword)
	if k := strings.TrimSpace(d.SSHPrivateKey); k != "" {
		if !strings.Contains(k, "PRIVATE KEY-----") {
			return nil, errors.New("chave SSH privada inválida (formato PEM/OpenSSH)")
		}
		m[sshKeyVar] = k + "\n"
	}
	for _, kv := range d.Extra {
		k := strings.TrimSpace(kv.Key)
		if k == "" {
			continue
		}
		if !gen.IdentRe.MatchString(k) {
			return nil, fmt.Errorf("variável inválida: %q", k)
		}
		if err := checkConnVar(k, kv.Value); err != nil {
			return nil, err
		}
		if _, dup := m[k]; dup || k == sshKeyVar {
			return nil, fmt.Errorf("variável duplicada/reservada: %q", k)
		}
		m[k] = kv.Value
	}
	return m, nil
}

func vaultFromMap(m map[string]string) VaultData {
	d := VaultData{AnsibleUser: m["ansible_user"], AnsiblePassword: m["ansible_password"],
		BecomePassword: m["ansible_become_password"], SSHPrivateKey: m[sshKeyVar], Extra: []VaultKV{}}
	keys := []string{}
	for k := range m {
		switch k {
		case "ansible_user", "ansible_password", "ansible_become_password", sshKeyVar:
		default:
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		d.Extra = append(d.Extra, VaultKV{k, m[k]})
	}
	return d
}

// vaultExec executa ansible-vault com a senha em arquivo 0600 temporário.
func (s *Server) vaultExec(ctx context.Context, password string, stdin []byte, args ...string) ([]byte, error) {
	dir, cleanup, err := s.runDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	pw := filepath.Join(dir, ".vault-pass")
	if err := os.WriteFile(pw, []byte(password), 0o600); err != nil {
		return nil, err
	}
	full := append([]string{"ansible-vault"}, args...)
	full = append(full[:2], append([]string{"--vault-password-file", pw}, full[2:]...)...)
	so, se, code, err := sysinfo.Run(ctx, 60*time.Second, stdin, s.toolEnv(), full...)
	if err != nil {
		return nil, fmt.Errorf("ansible-vault indisponível: %w", err)
	}
	if code != 0 {
		if strings.Contains(se, "Decryption failed") || strings.Contains(se, "no vault secrets") {
			return nil, errBadVaultPassword
		}
		return nil, fmt.Errorf("ansible-vault: %s", lastLines(se, 3))
	}
	return []byte(so), nil
}

var errBadVaultPassword = errors.New("senha do vault incorreta")

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " ")
}

func (s *Server) vaultEncrypt(ctx context.Context, password string, m map[string]string) (string, error) {
	plain, _ := json.MarshalIndent(m, "", "  ")
	out, err := s.vaultExec(ctx, password, plain, "encrypt", "--output", "-", "-")
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(out, []byte("$ANSIBLE_VAULT;")) {
		return "", errors.New("saída inesperada do ansible-vault")
	}
	return string(out), nil
}

func (s *Server) vaultDecrypt(ctx context.Context, password, ct string) (map[string]string, error) {
	out, err := s.vaultExec(ctx, password, []byte(ct), "decrypt", "--output", "-", "-")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, errors.New("conteúdo do vault não está no formato do doomctl")
	}
	return m, nil
}

type vaultMeta struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Server) listVaults(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, name, description, created_by, created_at, updated_at FROM ansible_vaults ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []vaultMeta{}
	for rows.Next() {
		var v vaultMeta
		rows.Scan(&v.ID, &v.Name, &v.Description, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

var vaultNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (s *Server) createVault(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Password    string    `json:"password"`
		Data        VaultData `json:"data"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !vaultNameRe.MatchString(in.Name) {
		fail(w, 400, "nome inválido (letras, números, . _ -)")
		return
	}
	if len(in.Password) < 8 {
		fail(w, 400, "a senha do vault deve ter ao menos 8 caracteres")
		return
	}
	m, err := in.Data.toMap()
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ct, err := s.vaultEncrypt(r.Context(), in.Password, m)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var id int64
	if err := s.db.QueryRow(`INSERT INTO ansible_vaults(name, description, ciphertext, created_by) VALUES ($1,$2,$3,$4) RETURNING id`,
		in.Name, truncate(in.Description, 300), ct, u.Username).Scan(&id); err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "vault_create", in.Name, u, "success", fmt.Sprintf("vault criado (%d variáveis)", len(m)), nil)
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) vaultByID(ctx context.Context, id int64) (name, ct string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT name, ciphertext FROM ansible_vaults WHERE id=$1`, id).Scan(&name, &ct)
	return
}

func (s *Server) viewVault(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !s.rl.allow(fmt.Sprintf("vault:%d:%d", u.ID, id), 10, 10*time.Minute) {
		fail(w, http.StatusTooManyRequests, "muitas tentativas; aguarde")
		return
	}
	name, ct, err := s.vaultByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	m, err := s.vaultDecrypt(r.Context(), in.Password, ct)
	if err != nil {
		code := 500
		if errors.Is(err, errBadVaultPassword) {
			code = 401
		}
		s.audit("ansible", "vault_view", name, u, "failed", err.Error(), nil)
		fail(w, code, err.Error())
		return
	}
	s.audit("ansible", "vault_view", name, u, "info", "conteúdo do vault visualizado", nil)
	writeJSON(w, 200, map[string]any{"name": name, "data": vaultFromMap(m)})
}

func (s *Server) updateVault(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		Password    string    `json:"password"`
		NewPassword string    `json:"new_password"`
		Description string    `json:"description"`
		Data        VaultData `json:"data"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	name, ct, err := s.vaultByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	// a senha atual é exigida para editar
	if _, err := s.vaultDecrypt(r.Context(), in.Password, ct); err != nil {
		code := 500
		if errors.Is(err, errBadVaultPassword) {
			code = 401
		}
		fail(w, code, err.Error())
		return
	}
	pw := in.Password
	if in.NewPassword != "" {
		if len(in.NewPassword) < 8 {
			fail(w, 400, "a nova senha deve ter ao menos 8 caracteres")
			return
		}
		pw = in.NewPassword
	}
	m, err := in.Data.toMap()
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	nct, err := s.vaultEncrypt(r.Context(), pw, m)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if _, err := s.db.Exec(`UPDATE ansible_vaults SET ciphertext=$2, description=$3, updated_at=now() WHERE id=$1`,
		id, nct, truncate(in.Description, 300)); err != nil {
		failErr(w, err)
		return
	}
	msg := "vault atualizado"
	if in.NewPassword != "" {
		msg += " (senha alterada)"
	}
	s.audit("ansible", "vault_edit", name, u, "success", msg, nil)
	ok(w)
}

func (s *Server) deleteVault(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM ansible_vaults WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "vault_delete", name, u, "info", "vault excluído", nil)
	ok(w)
}

// ---------- playbooks ----------

type Playbook struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Content     string    `json:"content,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var docNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,79}$`)

func (s *Server) taskTypes(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, gen.TaskTypes)
}

func (s *Server) generatePlaybook(w http.ResponseWriter, r *http.Request, _ *User) {
	var spec gen.PlaybookSpec
	if err := decode(r, &spec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	y, err := gen.Playbook(spec)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"content": y})
}

func (s *Server) listPlaybooks(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, name, description, updated_at FROM ansible_playbooks ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []Playbook{}
	for rows.Next() {
		var p Playbook
		rows.Scan(&p.ID, &p.Name, &p.Description, &p.UpdatedAt)
		out = append(out, p)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getPlaybook(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var p Playbook
	if err := s.db.QueryRow(`SELECT id, name, description, content, updated_at FROM ansible_playbooks WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.Content, &p.UpdatedAt); err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) savePlaybook(w http.ResponseWriter, r *http.Request, u *User) {
	var p Playbook
	if err := decode(r, &p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if !docNameRe.MatchString(p.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if strings.TrimSpace(p.Content) == "" || len(p.Content) > 1<<20 {
		fail(w, 400, "conteúdo vazio ou maior que 1 MiB")
		return
	}
	var err error
	if r.Method == http.MethodPut {
		p.ID, err = pathID(r)
		if err == nil {
			_, err = s.db.Exec(`UPDATE ansible_playbooks SET name=$2, description=$3, content=$4, updated_at=now() WHERE id=$1`,
				p.ID, p.Name, truncate(p.Description, 300), p.Content)
		}
	} else {
		err = s.db.QueryRow(`INSERT INTO ansible_playbooks(name, description, content) VALUES ($1,$2,$3) RETURNING id`,
			p.Name, truncate(p.Description, 300), p.Content).Scan(&p.ID)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "playbook_save", p.Name, u, "info", "playbook salvo", nil)
	writeJSON(w, 200, map[string]any{"id": p.ID})
}

func (s *Server) deletePlaybook(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM ansible_playbooks WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("ansible", "playbook_delete", name, u, "info", "playbook excluído", nil)
	ok(w)
}

// prepareAnsibleRun grava inventário, playbook e (opcional) vault no diretório da execução.
type ansibleRun struct {
	dir     string
	cleanup func()
	args    []string // argumentos extras (-e @vault, --vault-password-file, --private-key)
	mask    []string
}

func (s *Server) prepareAnsibleRun(ctx context.Context, vaultID int64, vaultPassword string) (*ansibleRun, int, error) {
	dir, cleanup, err := s.runDir()
	if err != nil {
		return nil, 0, err
	}
	run := &ansibleRun{dir: dir, cleanup: cleanup}
	inv, n, err := s.inventoryJSON(ctx)
	if err != nil {
		cleanup()
		return nil, 0, err
	}
	if err := os.WriteFile(filepath.Join(dir, "inventory.json"), inv, 0o600); err != nil {
		cleanup()
		return nil, 0, err
	}
	if vaultID > 0 {
		_, ct, err := s.vaultByID(ctx, vaultID)
		if err != nil {
			cleanup()
			return nil, 0, errors.New("vault não encontrado")
		}
		m, err := s.vaultDecrypt(ctx, vaultPassword, ct)
		if err != nil {
			cleanup()
			return nil, 0, err
		}
		os.WriteFile(filepath.Join(dir, "vault.yml"), []byte(ct), 0o600)
		os.WriteFile(filepath.Join(dir, ".vault-pass"), []byte(vaultPassword), 0o600)
		run.args = append(run.args, "-e", "@vault.yml", "--vault-password-file", ".vault-pass")
		if key := m[sshKeyVar]; key != "" {
			os.WriteFile(filepath.Join(dir, "id_key"), []byte(key), 0o600)
			run.args = append(run.args, "--private-key", "id_key")
		}
		run.mask = append(run.mask, vaultPassword, m["ansible_password"], m["ansible_become_password"])
	}
	return run, n, nil
}

func (s *Server) loadPlaybook(ctx context.Context, id int64) (Playbook, error) {
	var p Playbook
	err := s.db.QueryRowContext(ctx, `SELECT id, name, content FROM ansible_playbooks WHERE id=$1`, id).Scan(&p.ID, &p.Name, &p.Content)
	return p, err
}

func (s *Server) checkPlaybook(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Lint bool }
	decode(r, &in)
	p, err := s.loadPlaybook(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	run, _, err := s.prepareAnsibleRun(r.Context(), 0, "")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	os.WriteFile(filepath.Join(run.dir, "playbook.yml"), []byte(p.Content), 0o600)
	steps := []jobs.Step{{Name: "ansible-playbook --syntax-check", Args: []string{"ansible-playbook", "--syntax-check", "-i", "inventory.json", "playbook.yml"}}}
	action := "syntax-check"
	if in.Lint {
		if !sysinfo.Has(r.Context(), "ansible-lint") {
			run.cleanup()
			fail(w, 400, "ansible-lint não está instalado no servidor")
			return
		}
		steps = append(steps, jobs.Step{Name: "ansible-lint", Args: []string{"ansible-lint", "--offline", "--nocolor", "playbook.yml"}})
		action = "syntax-check+lint"
	}
	s.startJob(w, u, jobs.Spec{Module: "ansible", Action: action, Target: p.Name, Input: map[string]any{"playbook_id": id},
		Steps: steps, Dir: run.dir, Timeout: 5 * time.Minute, Cleanup: run.cleanup})
}

func (s *Server) runPlaybook(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		VaultID       int64  `json:"vault_id"`
		VaultPassword string `json:"vault_password"`
		Check         bool   `json:"check"`
		Diff          bool   `json:"diff"`
		Limit         string `json:"limit"`
		Tags          string `json:"tags"`
		Verbosity     int    `json:"verbosity"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Limit != "" && !patternRe.MatchString(in.Limit) {
		fail(w, 400, "limit inválido")
		return
	}
	if in.Tags != "" && !tagsRe.MatchString(in.Tags) {
		fail(w, 400, "tags inválidas")
		return
	}
	p, err := s.loadPlaybook(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	run, n, err := s.prepareAnsibleRun(r.Context(), in.VaultID, in.VaultPassword)
	if err != nil {
		code := 500
		if errors.Is(err, errBadVaultPassword) {
			code = 401
		}
		fail(w, code, err.Error())
		return
	}
	if n == 0 {
		run.cleanup()
		fail(w, 400, "o inventário está vazio: cadastre hosts antes de executar")
		return
	}
	os.WriteFile(filepath.Join(run.dir, "playbook.yml"), []byte(p.Content), 0o600)
	args := []string{"ansible-playbook", "-i", "inventory.json", "playbook.yml"}
	args = append(args, run.args...)
	if in.Check {
		args = append(args, "--check")
	}
	if in.Diff {
		args = append(args, "--diff")
	}
	if in.Limit != "" {
		args = append(args, "--limit", in.Limit)
	}
	if in.Tags != "" {
		args = append(args, "--tags", in.Tags)
	}
	if in.Verbosity > 0 && in.Verbosity <= 4 {
		args = append(args, "-"+strings.Repeat("v", in.Verbosity))
	}
	action := "run"
	if in.Check {
		action = "check"
	}
	s.startJob(w, u, jobs.Spec{Module: "ansible", Action: "playbook_" + action, Target: p.Name,
		Input: map[string]any{"playbook_id": id, "vault_id": in.VaultID, "check": in.Check, "diff": in.Diff, "limit": in.Limit, "tags": in.Tags},
		Steps: []jobs.Step{{Args: args}}, Dir: run.dir, Mask: run.mask, Cleanup: run.cleanup, Timeout: 3 * time.Hour})
}

// ---------- roles ----------

type Role struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Tasks       string    `json:"tasks"`
	Handlers    string    `json:"handlers"`
	Meta        string    `json:"meta"`
	Defaults    string    `json:"defaults"`
	Vars        string    `json:"vars"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT id, name, description, updated_at FROM ansible_roles ORDER BY name`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var ro Role
		rows.Scan(&ro.ID, &ro.Name, &ro.Description, &ro.UpdatedAt)
		out = append(out, ro)
	}
	writeJSON(w, 200, out)
}

func (s *Server) loadRole(ctx context.Context, id int64) (Role, error) {
	var ro Role
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, tasks, handlers, meta, defaults, vars, updated_at
		FROM ansible_roles WHERE id=$1`, id).Scan(&ro.ID, &ro.Name, &ro.Description, &ro.Tasks, &ro.Handlers, &ro.Meta,
		&ro.Defaults, &ro.Vars, &ro.UpdatedAt)
	return ro, err
}

func (s *Server) getRole(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ro, err := s.loadRole(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, ro)
}

func (s *Server) roleSkeleton(w http.ResponseWriter, r *http.Request, u *User) {
	name := r.URL.Query().Get("name")
	if !gen.RoleNameRe.MatchString(name) {
		fail(w, 400, "nome de role inválido (minúsculas, números e _; começa com letra)")
		return
	}
	writeJSON(w, 200, gen.RoleSkeleton(name, r.URL.Query().Get("description"), u.FullName))
}

func roleFiles(ro Role) map[string]string {
	return map[string]string{"tasks/main.yml": ro.Tasks, "handlers/main.yml": ro.Handlers, "meta/main.yml": ro.Meta,
		"defaults/main.yml": ro.Defaults, "vars/main.yml": ro.Vars}
}

// materializeRole grava a role em roles_path para uso nos playbooks.
func (s *Server) materializeRole(ro Role) error {
	base := s.cfg.Path("ansible", "roles", ro.Name)
	for rel, content := range roleFiles(ro) {
		p := filepath.Join(base, rel)
		if strings.TrimSpace(content) == "" {
			os.Remove(p)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// SyncRoles recria as roles em disco a partir do banco (chamado na inicialização).
func (s *Server) SyncRoles(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM ansible_roles`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if ro, err := s.loadRole(ctx, id); err == nil {
			s.materializeRole(ro)
		}
	}
}

func (s *Server) saveRole(w http.ResponseWriter, r *http.Request, u *User) {
	var ro Role
	if err := decode(r, &ro); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !gen.RoleNameRe.MatchString(ro.Name) {
		fail(w, 400, "nome de role inválido (minúsculas, números e _; começa com letra)")
		return
	}
	if strings.TrimSpace(ro.Tasks) == "" {
		fail(w, 400, "tasks/main.yml não pode ficar vazio")
		return
	}
	var err error
	var oldName string
	if r.Method == http.MethodPut {
		ro.ID, err = pathID(r)
		if err == nil {
			s.db.QueryRow(`SELECT name FROM ansible_roles WHERE id=$1`, ro.ID).Scan(&oldName)
			_, err = s.db.Exec(`UPDATE ansible_roles SET name=$2, description=$3, tasks=$4, handlers=$5, meta=$6, defaults=$7, vars=$8,
				updated_at=now() WHERE id=$1`, ro.ID, ro.Name, truncate(ro.Description, 300), ro.Tasks, ro.Handlers, ro.Meta, ro.Defaults, ro.Vars)
		}
	} else {
		err = s.db.QueryRow(`INSERT INTO ansible_roles(name, description, tasks, handlers, meta, defaults, vars)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, ro.Name, truncate(ro.Description, 300), ro.Tasks, ro.Handlers,
			ro.Meta, ro.Defaults, ro.Vars).Scan(&ro.ID)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	if oldName != "" && oldName != ro.Name {
		os.RemoveAll(s.cfg.Path("ansible", "roles", oldName))
	}
	if err := s.materializeRole(ro); err != nil {
		fail(w, 500, "role salva no banco, mas falhou ao gravar em disco: "+err.Error())
		return
	}
	s.audit("ansible", "role_save", ro.Name, u, "info", "role salva (tasks/handlers/meta/defaults/vars)", nil)
	writeJSON(w, 200, map[string]any{"id": ro.ID})
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRow(`DELETE FROM ansible_roles WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	if gen.RoleNameRe.MatchString(name) {
		os.RemoveAll(s.cfg.Path("ansible", "roles", name))
	}
	s.audit("ansible", "role_delete", name, u, "info", "role excluída", nil)
	ok(w)
}

func (s *Server) downloadRole(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ro, err := s.loadRole(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	files := map[string]string{}
	for rel, c := range roleFiles(ro) {
		if strings.TrimSpace(c) != "" {
			files[ro.Name+"/"+rel] = c
		}
	}
	files[ro.Name+"/README.md"] = "# " + ro.Name + "\n\n" + ro.Description + "\n\nGerada pelo doomctl.\n"
	writeTarGz(w, ro.Name+".tar.gz", files)
}

// writeTarGz envia arquivos (caminho → conteúdo) como .tar.gz.
func writeTarGz(w http.ResponseWriter, filename string, files map[string]string) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	now := time.Now()
	for _, n := range names {
		c := files[n]
		mode := int64(0o644)
		if strings.HasSuffix(n, ".sh") {
			mode = 0o755
		}
		tw.WriteHeader(&tar.Header{Name: n, Mode: mode, Size: int64(len(c)), ModTime: now, Typeflag: tar.TypeReg})
		tw.Write([]byte(c))
	}
	tw.Close()
	gz.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write(buf.Bytes())
}

// ---------- ad-hoc (terminal) ----------

var adhocValueFlags = map[string]bool{
	"-m": true, "--module-name": true, "-a": true, "--args": true, "-f": true, "--forks": true, "-l": true, "--limit": true,
	"-u": true, "--user": true, "--become-user": true, "--become-method": true, "-T": true, "--timeout": true,
	"-e": true, "--extra-vars": true, "-B": true, "--background": true, "-P": true, "--poll": true,
}
var adhocBoolFlags = map[string]bool{
	"-b": true, "--become": true, "-C": true, "--check": true, "-D": true, "--diff": true, "-o": true, "--one-line": true,
	"--list-hosts": true, "-v": true, "-vv": true, "-vvv": true, "-vvvv": true,
}

// módulos que gravam/leem arquivos do próprio servidor doomctl
var adhocBlockedModules = map[string]bool{"fetch": true, "ansible.builtin.fetch": true, "copy": true,
	"ansible.builtin.copy": true, "template": true, "ansible.builtin.template": true, "script": true,
	"ansible.builtin.script": true, "unarchive": true, "ansible.builtin.unarchive": true}

// parseAdhoc valida a linha de comando do terminal e devolve o argv seguro.
func parseAdhoc(line string) ([]string, string, error) {
	args, err := gen.SplitArgs(line)
	if err != nil {
		return nil, "", err
	}
	if len(args) == 0 {
		return nil, "", errors.New("comando vazio")
	}
	for _, a := range args {
		if strings.Contains(a, "{{") || strings.Contains(a, "{%") || strings.Contains(a, "lookup(") {
			return nil, "", errors.New("templates Jinja/lookups não são permitidos no terminal (use um playbook)")
		}
	}
	switch args[0] {
	case "ansible":
	case "ansible-inventory":
		for _, a := range args[1:] {
			if a != "--graph" && a != "--list" && a != "--yaml" && !patternRe.MatchString(a) {
				return nil, "", fmt.Errorf("argumento não permitido: %s", a)
			}
		}
		return append([]string{"ansible-inventory", "-i", "inventory.json"}, args[1:]...), "inventory", nil
	case "ansible-doc":
		if len(args) != 2 && !(len(args) == 3 && (args[1] == "-s" || args[1] == "-l")) {
			return nil, "", errors.New("uso: ansible-doc <módulo> | ansible-doc -s <módulo>")
		}
		for _, a := range args[1:] {
			if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(a) {
				return nil, "", fmt.Errorf("argumento não permitido: %s", a)
			}
		}
		return args, "doc", nil
	default:
		return nil, "", errors.New("somente ansible, ansible-inventory e ansible-doc são permitidos no terminal")
	}
	out := []string{"ansible", "-i", "inventory.json"}
	pattern := ""
	module := "command"
	for i := 1; i < len(args); i++ {
		a := args[i]
		flag, val, hasEq := strings.Cut(a, "=")
		if strings.HasPrefix(a, "-") && hasEq && adhocValueFlags[flag] {
			args = append(args[:i+1], args[i:]...)
			args[i], args[i+1] = flag, val
			a = flag
		}
		switch {
		case adhocBoolFlags[a]:
			out = append(out, a)
		case adhocValueFlags[a]:
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s exige um valor", a)
			}
			v := args[i+1]
			i++
			if (a == "-e" || a == "--extra-vars") && strings.HasPrefix(strings.TrimSpace(v), "@") {
				return nil, "", errors.New("-e @arquivo não é permitido no terminal")
			}
			if (a == "-e" || a == "--extra-vars") && strings.Contains(v, "ansible_connection") {
				return nil, "", errors.New("ansible_connection não pode ser alterado no terminal")
			}
			if a == "-m" || a == "--module-name" {
				module = v
				if adhocBlockedModules[v] {
					return nil, "", fmt.Errorf("o módulo %s acessa arquivos do servidor doomctl e não é permitido no terminal (use um playbook)", v)
				}
			}
			out = append(out, a, v)
		case strings.HasPrefix(a, "-"):
			return nil, "", fmt.Errorf("opção não permitida no terminal: %s", a)
		default:
			if pattern != "" {
				return nil, "", fmt.Errorf("argumento inesperado: %s", a)
			}
			if !patternRe.MatchString(a) {
				return nil, "", fmt.Errorf("padrão de hosts inválido: %s", a)
			}
			low := strings.ToLower(a)
			if low == "localhost" || strings.HasPrefix(low, "127.") || low == "::1" {
				return nil, "", errors.New("execução no próprio servidor doomctl não é permitida")
			}
			if ip, err := netip.ParseAddr(a); err == nil && ip.IsLoopback() {
				return nil, "", errors.New("execução no próprio servidor doomctl não é permitida")
			}
			pattern = a
		}
	}
	if pattern == "" {
		return nil, "", errors.New("informe o padrão de hosts (ex.: ansible all -m ping)")
	}
	out = append(out[:3], append([]string{pattern}, out[3:]...)...)
	return out, module, nil
}

func (s *Server) adhoc(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Command       string `json:"command"`
		VaultID       int64  `json:"vault_id"`
		VaultPassword string `json:"vault_password"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	argv, module, err := parseAdhoc(strings.TrimSpace(in.Command))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	run, _, err := s.prepareAnsibleRun(r.Context(), in.VaultID, in.VaultPassword)
	if err != nil {
		code := 500
		if errors.Is(err, errBadVaultPassword) {
			code = 401
		}
		fail(w, code, err.Error())
		return
	}
	if argv[0] == "ansible" {
		argv = append(argv, run.args...)
	}
	s.startJob(w, u, jobs.Spec{Module: "ansible", Action: "adhoc", Target: truncate(in.Command, 200),
		Input: map[string]any{"command": in.Command, "module": module, "vault_id": in.VaultID},
		Steps: []jobs.Step{{Args: argv}}, Dir: run.dir, Mask: run.mask, Cleanup: run.cleanup, Timeout: 30 * time.Minute})
}
