package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/doomctl/doomctl/internal/finops"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/rbac"
)

var (
	regexpDigits12 = regexp.MustCompile(`^\d{12}$`)
	regexpAWSKey   = regexp.MustCompile(`^(AKIA|ASIA)[A-Z0-9]{12,124}$`)
	regexpProvider = regexp.MustCompile(`^[a-z][a-z0-9-]{1,19}$`)
	finopsSyncBusy sync.Map // source id → sincronização em andamento
)

// ---------- fontes ----------

type finopsSource struct {
	ID             int64               `json:"id"`
	Name           string              `json:"name"`
	Provider       string              `json:"provider"`
	AccountLabel   string              `json:"account_label"`
	Config         finops.SourceConfig `json:"config"`
	Currency       string              `json:"currency"`
	AutoSync       bool                `json:"auto_sync"`
	HasCredentials bool                `json:"has_credentials"`
	CredentialHint string              `json:"credential_hint"`
	LastSyncAt     *time.Time          `json:"last_sync_at"`
	LastScanAt     *time.Time          `json:"last_scan_at"`
	LastStatus     string              `json:"last_status"`
	LastError      string              `json:"last_error"`
	CreatedAt      time.Time           `json:"created_at"`
	Rows           int64               `json:"rows"`
	Resources      int64               `json:"resources"`
	credsEnc       []byte
}

func (s *Server) finopsSourceList(ctx context.Context) ([]finopsSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, provider, account_label, config, currency, auto_sync, credentials_enc, last_sync_at, last_scan_at,
		last_status, last_error, created_at,
		(SELECT count(*) FROM finops_costs c WHERE c.source_id=s.id), (SELECT count(*) FROM finops_resources r WHERE r.source_id=s.id)
		FROM finops_sources s ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []finopsSource{}
	for rows.Next() {
		var x finopsSource
		var cfg []byte
		if err := rows.Scan(&x.ID, &x.Name, &x.Provider, &x.AccountLabel, &cfg, &x.Currency, &x.AutoSync, &x.credsEnc, &x.LastSyncAt, &x.LastScanAt,
			&x.LastStatus, &x.LastError, &x.CreatedAt, &x.Rows, &x.Resources); err != nil {
			return nil, err
		}
		json.Unmarshal(cfg, &x.Config)
		x.Config.Normalize(x.Provider)
		x.HasCredentials = len(x.credsEnc) > 0
		if x.HasCredentials {
			x.CredentialHint = s.credHint(x)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Server) finopsSource(ctx context.Context, id int64) (finopsSource, error) {
	list, err := s.finopsSourceList(ctx)
	if err != nil {
		return finopsSource{}, err
	}
	for _, x := range list {
		if x.ID == id {
			return x, nil
		}
	}
	return finopsSource{}, sql.ErrNoRows
}

type finopsCreds struct {
	AccessKey   string `json:"access_key_id,omitempty"`
	SecretKey   string `json:"secret_access_key,omitempty"`
	Session     string `json:"session_token,omitempty"`
	Tenancy     string `json:"tenancy_ocid,omitempty"`
	User        string `json:"user_ocid,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
	Region      string `json:"region,omitempty"`
}

func (s *Server) sourceCreds(x finopsSource) (finopsCreds, error) {
	var c finopsCreds
	if len(x.credsEnc) == 0 {
		return c, errors.New("a fonte não tem credenciais cadastradas")
	}
	b, err := s.box.Open(x.credsEnc)
	if err != nil {
		return c, errors.New("não foi possível abrir as credenciais (chave mestra alterada?)")
	}
	json.Unmarshal(b, &c)
	return c, nil
}

func (s *Server) credHint(x finopsSource) string {
	c, err := s.sourceCreds(x)
	if err != nil {
		return "credenciais ilegíveis"
	}
	switch x.Provider {
	case "aws":
		k := c.AccessKey
		if len(k) > 8 {
			k = k[:4] + "…" + k[len(k)-4:]
		}
		if c.Session != "" {
			k += " (temporária)"
		}
		return k
	case "oci":
		u := c.User
		if len(u) > 12 {
			u = "…" + u[len(u)-8:]
		}
		return "usuário " + u + " · " + c.Region
	}
	return ""
}

func (s *Server) finopsSources(w http.ResponseWriter, r *http.Request, _ *User) {
	list, err := s.finopsSourceList(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sources": list})
}

type sourceInput struct {
	Name         string              `json:"name"`
	Provider     string              `json:"provider"`
	AccountLabel string              `json:"account_label"`
	Currency     string              `json:"currency"`
	AutoSync     bool                `json:"auto_sync"`
	Config       finops.SourceConfig `json:"config"`
}

func (s *Server) finopsSaveSource(w http.ResponseWriter, r *http.Request, u *User) {
	var in sourceInput
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !finopsNameRe.MatchString(in.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if r.Method != http.MethodPut && in.Provider != "aws" && in.Provider != "oci" && in.Provider != "csv" {
		fail(w, 400, "provedor deve ser aws, oci ou csv")
		return
	}
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if !finopsCurRe.MatchString(in.Currency) {
		fail(w, 400, "moeda inválida (código ISO de 3 letras)")
		return
	}
	in.AccountLabel = truncate(strings.TrimSpace(in.AccountLabel), 80)
	if in.AccountLabel == "" {
		in.AccountLabel = in.Name
	}
	cfg := in.Config
	for _, reg := range cfg.Regions {
		if !finopsRegionRe.MatchString(strings.TrimSpace(reg)) {
			fail(w, 400, "região inválida: "+reg)
			return
		}
	}
	if len(cfg.Regions) > 20 {
		fail(w, 400, "máximo de 20 regiões")
		return
	}
	for _, t := range []string{cfg.TagProject, cfg.TagEnvironment, cfg.TagTeam} {
		if t != "" && !finopsTagRe.MatchString(t) {
			fail(w, 400, "chave de tag inválida: "+t)
			return
		}
	}
	if cfg.LinkedAccount != "" && !regexpDigits12.MatchString(cfg.LinkedAccount) {
		fail(w, 400, "conta vinculada deve ter 12 dígitos")
		return
	}
	var id int64
	var err error
	if r.Method == http.MethodPut {
		if id, err = pathID(r); err != nil {
			fail(w, 400, err.Error())
			return
		}
		old, err := s.finopsSource(r.Context(), id)
		if err != nil {
			failErr(w, err)
			return
		}
		if old.Provider == "demo" {
			fail(w, 400, "a fonte de demonstração não pode ser editada")
			return
		}
		in.Provider = old.Provider
		// endpoints alternativos recebem as requisições assinadas: só administradores alteram
		if u.Role != rbac.RoleAdmin {
			cfg.Endpoints = old.Config.Endpoints
		}
	} else if u.Role != rbac.RoleAdmin {
		cfg.Endpoints = nil
	}
	if cfg.Remediation && u.Role != rbac.RoleAdmin {
		if r.Method != http.MethodPut {
			cfg.Remediation = false
		} else if old, _ := s.finopsSource(r.Context(), id); !old.Config.Remediation {
			fail(w, 403, "somente administradores habilitam a remediação automatizada")
			return
		}
	}
	cfg.Normalize(in.Provider)
	raw, _ := json.Marshal(cfg)
	if r.Method == http.MethodPut {
		_, err = s.db.ExecContext(r.Context(), `UPDATE finops_sources SET name=$2, account_label=$3, config=$4, currency=$5, auto_sync=$6, updated_at=now() WHERE id=$1`,
			id, in.Name, in.AccountLabel, string(raw), in.Currency, in.AutoSync)
	} else {
		err = s.db.QueryRowContext(r.Context(), `INSERT INTO finops_sources(name, provider, account_label, config, currency, auto_sync, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.Name, in.Provider, in.AccountLabel, string(raw), in.Currency, in.AutoSync, u.Username).Scan(&id)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "source_save", in.Name, u, "info", in.Provider, map[string]any{"id": id, "config": cfg})
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) finopsDeleteSource(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRowContext(r.Context(), `DELETE FROM finops_sources WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "source_delete", name, u, "info", "fonte e dados removidos", nil)
	ok(w)
}

func (s *Server) finopsSetCreds(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	src, err := s.finopsSource(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	var c finopsCreds
	if err := decode(r, &c); err != nil {
		fail(w, 400, err.Error())
		return
	}
	trim := func(p *string) { *p = strings.TrimSpace(*p) }
	for _, p := range []*string{&c.AccessKey, &c.SecretKey, &c.Session, &c.Tenancy, &c.User, &c.Fingerprint, &c.Region} {
		trim(p)
	}
	switch src.Provider {
	case "aws":
		if !regexpAWSKey.MatchString(c.AccessKey) || len(c.SecretKey) < 20 || len(c.SecretKey) > 128 {
			fail(w, 400, "informe access key ID (AKIA…/ASIA…) e secret access key")
			return
		}
		c = finopsCreds{AccessKey: c.AccessKey, SecretKey: c.SecretKey, Session: c.Session}
	case "oci":
		if !strings.HasPrefix(c.Tenancy, "ocid1.tenancy.") || !strings.HasPrefix(c.User, "ocid1.user.") || c.Fingerprint == "" || !finopsRegionRe.MatchString(c.Region) {
			fail(w, 400, "informe tenancy OCID, user OCID, fingerprint, região (home region) e a chave privada")
			return
		}
		if _, err := finops.ParseRSAKey(c.PrivateKey); err != nil {
			fail(w, 400, err.Error())
			return
		}
		c = finopsCreds{Tenancy: c.Tenancy, User: c.User, Fingerprint: c.Fingerprint, PrivateKey: strings.TrimSpace(c.PrivateKey), Region: c.Region}
	default:
		fail(w, 400, "esta fonte não usa credenciais")
		return
	}
	raw, _ := json.Marshal(c)
	enc, err := s.box.Seal(raw)
	if err != nil {
		failErr(w, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE finops_sources SET credentials_enc=$2, updated_at=now() WHERE id=$1`, id, enc); err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "source_credentials", src.Name, u, "info", "credenciais atualizadas", nil)
	ok(w)
}

func (s *Server) awsClient(src finopsSource) (*finops.AWSClient, error) {
	c, err := s.sourceCreds(src)
	if err != nil {
		return nil, err
	}
	return finops.NewAWSClient(finops.AWSCreds{AccessKey: c.AccessKey, SecretKey: c.SecretKey, SessionToken: c.Session}, src.Config,
		&http.Client{Timeout: 90 * time.Second}), nil
}

func (s *Server) ociClient(src finopsSource) (*finops.OCIClient, error) {
	c, err := s.sourceCreds(src)
	if err != nil {
		return nil, err
	}
	return &finops.OCIClient{Creds: finops.OCICreds{Tenancy: c.Tenancy, User: c.User, Fingerprint: c.Fingerprint, PrivateKey: c.PrivateKey, Region: c.Region},
		HTTP: &http.Client{Timeout: 90 * time.Second}, Endpoint: src.Config.Endpoints["oci_usage"]}, nil
}

// finopsTestSource consulta 2 dias de custo para validar credenciais e permissões.
func (s *Server) finopsTestSource(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	src, err := s.finopsSource(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	end := finops.Day(s.now())
	start := end.AddDate(0, 0, -2)
	var n int
	switch src.Provider {
	case "aws":
		cl, err := s.awsClient(src)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		g, err := cl.GetCostAndUsage(ctx, start, end, [][2]string{{"DIMENSION", "SERVICE"}}, nil)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		n = len(g)
	case "oci":
		cl, err := s.ociClient(src)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		it, err := cl.RequestSummarizedUsages(ctx, start, end, []string{"service"}, nil)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		n = len(it)
	default:
		fail(w, 400, "fonte sem integração")
		return
	}
	s.audit("finops", "source_test", src.Name, u, "success", fmt.Sprintf("%d linhas", n), nil)
	writeJSON(w, 200, map[string]any{"ok": true, "rows": n, "message": fmt.Sprintf("Conexão OK: %d linhas de custo nos últimos 2 dias.", n)})
}

// ---------- persistência em lote ----------

func copyRows(ctx context.Context, tx *sql.Tx, table string, cols []string, n int, row func(i int) []any) error {
	if n == 0 {
		return nil
	}
	st, err := tx.PrepareContext(ctx, "COPY "+table+" ("+strings.Join(cols, ", ")+") FROM STDIN")
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if _, err := st.ExecContext(ctx, row(i)...); err != nil {
			st.Close()
			return err
		}
	}
	if _, err := st.ExecContext(ctx); err != nil {
		st.Close()
		return err
	}
	return st.Close()
}

// storeCosts substitui custos/alocação no intervalo [start, end) e uso a partir de usageStart.
func (s *Server) storeCosts(ctx context.Context, sourceID int64, start, end, usageStart time.Time, costs []finops.CostRow, alloc []finops.AllocRow, usage []finops.UsageRow, replaceUsage bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"finops_costs", "finops_alloc"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE source_id=$1 AND day >= $2 AND day < $3`, sourceID, start, end); err != nil {
			return err
		}
	}
	if replaceUsage {
		if _, err := tx.ExecContext(ctx, `DELETE FROM finops_usage WHERE source_id=$1 AND day >= $2 AND day < $3`, sourceID, usageStart, end); err != nil {
			return err
		}
	}
	if err := copyRows(ctx, tx, "finops_costs", []string{"source_id", "day", "provider", "account", "service", "region", "cost", "currency", "usage_qty", "usage_unit"},
		len(costs), func(i int) []any {
			c := costs[i]
			return []any{sourceID, c.Day.Format("2006-01-02"), c.Provider, truncate(c.Account, 200), truncate(c.Service, 200), truncate(c.Region, 60), c.Cost, c.Currency, c.UsageQty, truncate(c.UsageUnit, 40)}
		}); err != nil {
		return err
	}
	if err := copyRows(ctx, tx, "finops_alloc", []string{"source_id", "day", "provider", "service", "dim", "value", "cost", "currency"},
		len(alloc), func(i int) []any {
			a := alloc[i]
			return []any{sourceID, a.Day.Format("2006-01-02"), a.Provider, truncate(a.Service, 200), a.Dim, truncate(a.Value, 200), a.Cost, a.Currency}
		}); err != nil {
		return err
	}
	if err := copyRows(ctx, tx, "finops_usage", []string{"source_id", "day", "provider", "service", "usage_type", "category", "cost", "currency", "quantity", "unit"},
		len(usage), func(i int) []any {
			u := usage[i]
			return []any{sourceID, u.Day.Format("2006-01-02"), u.Provider, truncate(u.Service, 200), truncate(u.UsageType, 200), u.Category, u.Cost, u.Currency, u.Quantity, truncate(u.Unit, 40)}
		}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) storeResources(ctx context.Context, sourceID int64, res []finops.Resource, replace bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM finops_resources WHERE source_id=$1`, sourceID); err != nil {
			return err
		}
	}
	for _, x := range res {
		tags, _ := json.Marshal(x.Tags)
		raw, _ := json.Marshal(x.Raw)
		if x.Tags == nil {
			tags = []byte("{}")
		}
		if x.Raw == nil {
			raw = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO finops_resources(source_id, provider, region, resource_id, type, name, sku, size_gb, state, monthly_cost, currency,
			cpu_avg, cpu_max, mem_avg, attached, age_days, tags, raw) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (source_id, resource_id) DO UPDATE SET provider=EXCLUDED.provider, region=EXCLUDED.region, type=EXCLUDED.type, name=EXCLUDED.name,
			sku=EXCLUDED.sku, size_gb=EXCLUDED.size_gb, state=EXCLUDED.state, monthly_cost=EXCLUDED.monthly_cost, currency=EXCLUDED.currency,
			cpu_avg=EXCLUDED.cpu_avg, cpu_max=EXCLUDED.cpu_max, mem_avg=EXCLUDED.mem_avg, attached=EXCLUDED.attached, age_days=EXCLUDED.age_days,
			tags=EXCLUDED.tags, raw=EXCLUDED.raw, updated_at=now()`,
			sourceID, x.Provider, truncate(x.Region, 60), truncate(x.ResourceID, 300), x.Type, truncate(x.Name, 200), truncate(x.SKU, 100), x.SizeGB, truncate(x.State, 40),
			x.MonthlyCost, firstNonEmptyStr(x.Currency, "USD"), x.CPUAvg, x.CPUMax, x.MemAvg, x.Attached, x.AgeDays, string(tags), string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func firstNonEmptyStr(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---------- sincronização e inventário ----------

// finopsSyncSpec monta o job de sincronização (também usado pelo agendador).
func (s *Server) finopsSyncSpec(src finopsSource, days int) (jobs.Spec, error) {
	if src.Provider != "aws" && src.Provider != "oci" {
		return jobs.Spec{}, errors.New("esta fonte não sincroniza com a nuvem (importe CSV)")
	}
	if !src.HasCredentials {
		return jobs.Spec{}, errors.New("cadastre as credenciais da fonte antes de sincronizar")
	}
	end := finops.Day(s.now())
	if days <= 0 {
		days = src.Config.HistoryDays
		if src.LastSyncAt != nil {
			days = 7 // reprocessa a última semana (a AWS/OCI ajustam custos recentes)
		}
	}
	if days > 395 {
		days = 395
	}
	start := end.AddDate(0, 0, -days)
	step := func(ctx context.Context, w io.Writer) error {
		if _, busy := finopsSyncBusy.LoadOrStore(src.ID, true); busy {
			return errors.New("já existe uma sincronização em andamento para esta fonte")
		}
		defer finopsSyncBusy.Delete(src.ID)
		var res finops.SyncResult
		var err error
		switch src.Provider {
		case "aws":
			var cl *finops.AWSClient
			if cl, err = s.awsClient(src); err == nil {
				res, err = finops.SyncAWS(ctx, cl, src.Config, src.ID, src.AccountLabel, start, end, w)
			}
		case "oci":
			var cl *finops.OCIClient
			if cl, err = s.ociClient(src); err == nil {
				res, err = finops.SyncOCI(ctx, cl, src.Config, src.ID, src.AccountLabel, start, end, w)
			}
		}
		if err != nil {
			s.db.Exec(`UPDATE finops_sources SET last_status='failed', last_error=$2 WHERE id=$1`, src.ID, truncate(err.Error(), 500))
			return err
		}
		us := res.UsageStart
		if us.IsZero() {
			us = end
		}
		fmt.Fprintln(w, "→ gravando no banco...")
		if err := s.storeCosts(ctx, src.ID, start, end, us, res.Costs, res.Alloc, res.Usage, !res.UsageStart.IsZero()); err != nil {
			return err
		}
		warn := strings.Join(res.Warnings, " | ")
		status := "success"
		if warn != "" {
			status = "warning"
		}
		if res.Rightsizing != nil {
			raw, _ := json.Marshal(res.Rightsizing)
			s.db.Exec(`UPDATE finops_sources SET rightsizing=$2 WHERE id=$1`, src.ID, string(raw))
		}
		s.db.Exec(`UPDATE finops_sources SET last_sync_at=now(), last_status=$2, last_error=$3 WHERE id=$1`, src.ID, status, truncate(warn, 500))
		fmt.Fprintf(w, "✔ %d linhas de custo, %d de alocação e %d de uso gravadas (%s a %s)\n", len(res.Costs), len(res.Alloc), len(res.Usage),
			start.Format("02/01/2006"), end.AddDate(0, 0, -1).Format("02/01/2006"))
		return nil
	}
	return jobs.Spec{Module: "finops", Action: "sync", Target: src.Name, Input: map[string]any{"source_id": src.ID, "days": days},
		Steps: []jobs.Step{{Name: "sincronizar custos (" + strings.ToUpper(src.Provider) + ")", Func: step}}, Timeout: 30 * time.Minute}, nil
}

func (s *Server) finopsSync(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		Days int `json:"days"`
	}
	decode(r, &in)
	src, err := s.finopsSource(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	spec, err := s.finopsSyncSpec(src, in.Days)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.startJob(w, u, spec)
}

func (s *Server) finopsScan(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	src, err := s.finopsSource(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	if src.Provider != "aws" {
		fail(w, 400, "o inventário automático está disponível para AWS; para outras nuvens importe um CSV de inventário")
		return
	}
	cl, err := s.awsClient(src)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	step := func(ctx context.Context, w io.Writer) error {
		res, warns, err := finops.ScanAWS(ctx, cl, src.Config, src.ID, w)
		if err != nil {
			s.db.Exec(`UPDATE finops_sources SET last_status='failed', last_error=$2 WHERE id=$1`, src.ID, truncate(err.Error(), 500))
			return err
		}
		if err := s.storeResources(ctx, src.ID, res, true); err != nil {
			return err
		}
		s.db.Exec(`UPDATE finops_sources SET last_scan_at=now(), last_error=$2 WHERE id=$1`, src.ID, truncate(strings.Join(warns, " | "), 500))
		fmt.Fprintf(w, "✔ %d recursos no inventário\n", len(res))
		return nil
	}
	s.startJob(w, u, jobs.Spec{Module: "finops", Action: "scan", Target: src.Name, Input: map[string]any{"source_id": id, "regions": src.Config.Regions},
		Steps: []jobs.Step{{Name: "inventário AWS (" + strings.Join(src.Config.Regions, ", ") + ")", Func: step}}, Timeout: 30 * time.Minute})
}

// ---------- importação CSV ----------

func readCSVBody(r *http.Request) (string, bool, error) {
	var in struct {
		CSV     string `json:"csv"`
		Replace bool   `json:"replace"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 48<<20))
	if err := dec.Decode(&in); err != nil {
		return "", false, errors.New("JSON inválido ou arquivo maior que 48 MiB")
	}
	if strings.TrimSpace(in.CSV) == "" {
		return "", false, errors.New("arquivo CSV vazio")
	}
	return in.CSV, in.Replace, nil
}

func (s *Server) finopsImportCosts(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	src, err := s.finopsSource(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	text, _, err := readCSVBody(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	defProv := ""
	if src.Provider == "aws" || src.Provider == "oci" {
		defProv = src.Provider
	}
	imp, err := finops.ParseCostCSV(strings.NewReader(text), id, defProv, src.Currency)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	for i := range imp.Rows {
		if imp.Rows[i].Account == "" {
			imp.Rows[i].Account = src.AccountLabel
		}
	}
	end := imp.MaxDay.AddDate(0, 0, 1)
	us := end
	if len(imp.Usage) > 0 {
		us = imp.MinDay
	}
	if err := s.storeCosts(r.Context(), id, imp.MinDay, end, us, imp.Rows, imp.Alloc, imp.Usage, len(imp.Usage) > 0); err != nil {
		failErr(w, err)
		return
	}
	s.db.Exec(`UPDATE finops_sources SET last_sync_at=now(), last_status='success', last_error='' WHERE id=$1`, id)
	msg := fmt.Sprintf("%d linhas de %s a %s (dados existentes no período foram substituídos)", len(imp.Rows), imp.MinDay.Format("02/01/2006"), imp.MaxDay.Format("02/01/2006"))
	s.audit("finops", "import_costs", src.Name, u, "success", msg, map[string]any{"source_id": id, "errors": len(imp.Errors)})
	writeJSON(w, 200, map[string]any{"rows": len(imp.Rows), "errors": imp.Errors, "start": imp.MinDay, "end": imp.MaxDay, "currency": imp.Currency, "message": msg})
}

func (s *Server) finopsImportResources(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	src, err := s.finopsSource(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	text, replace, err := readCSVBody(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	defProv := ""
	if src.Provider == "aws" || src.Provider == "oci" {
		defProv = src.Provider
	}
	res, errs, err := finops.ParseInventoryCSV(strings.NewReader(text), id, defProv)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(res) > 50000 {
		fail(w, 400, "máximo de 50 mil recursos por importação")
		return
	}
	if err := s.storeResources(r.Context(), id, res, replace); err != nil {
		failErr(w, err)
		return
	}
	s.db.Exec(`UPDATE finops_sources SET last_scan_at=now() WHERE id=$1`, id)
	s.audit("finops", "import_resources", src.Name, u, "success", fmt.Sprintf("%d recursos", len(res)), map[string]any{"source_id": id, "replace": replace})
	writeJSON(w, 200, map[string]any{"resources": len(res), "errors": errs})
}

// ---------- dados de exemplo ----------

func (s *Server) finopsLoadDemo(w http.ResponseWriter, r *http.Request, u *User) {
	ctx := r.Context()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO finops_sources(name, provider, account_label, config, auto_sync, created_by, last_status)
		VALUES ('Demonstração', 'demo', 'exemplo', '{}', false, $1, 'success')
		ON CONFLICT (name) DO UPDATE SET updated_at=now() RETURNING id`, u.Username).Scan(&id)
	if err != nil {
		failErr(w, err)
		return
	}
	var prov string
	s.db.QueryRowContext(ctx, `SELECT provider FROM finops_sources WHERE id=$1`, id).Scan(&prov)
	if prov != "demo" {
		fail(w, 409, "já existe uma fonte chamada \"Demonstração\" que não é de exemplo")
		return
	}
	now := s.now()
	dd := finops.Demo(id, now)
	end := finops.Day(now)
	if err := s.storeCosts(ctx, id, end.AddDate(-2, 0, 0), end.AddDate(0, 0, 1), end.AddDate(-2, 0, 0), dd.Costs, dd.Alloc, dd.Usage, true); err != nil {
		failErr(w, err)
		return
	}
	if err := s.storeResources(ctx, id, dd.Resources, true); err != nil {
		failErr(w, err)
		return
	}
	for _, p := range dd.Prices {
		s.db.ExecContext(ctx, `INSERT INTO finops_prices(provider, region, sku, unit, price, currency, source) VALUES ($1,$2,$3,$4,$5,$6,'demo')
			ON CONFLICT (provider, region, sku) DO NOTHING`, p.Provider, p.Region, p.SKU, p.Unit, p.Price, p.Currency)
	}
	for _, b := range dd.Budgets {
		th, _ := json.Marshal(b.Thresholds)
		s.db.ExecContext(ctx, `INSERT INTO finops_budgets(name, scope_type, scope_value, amount, currency, thresholds, notify, demo) VALUES ($1,$2,$3,$4,$5,$6,$7,true)
			ON CONFLICT (name) DO NOTHING`, b.Name, b.ScopeType, b.ScopeValue, b.Amount, b.Currency, string(th), b.Notify)
	}
	for _, p := range dd.Policies {
		v, _ := json.Marshal(p.Value)
		s.db.ExecContext(ctx, `INSERT INTO finops_policies(name, kind, match, value, action, enabled, demo) VALUES ($1,$2,$3,$4,$5,$6,true)
			ON CONFLICT (name) DO NOTHING`, p.Name, p.Kind, p.Match, string(v), p.Action, p.Enabled)
	}
	for _, sc := range dd.Scenarios {
		items, _ := json.Marshal(map[string]any{"a": sc.A, "b": sc.B})
		s.db.ExecContext(ctx, `INSERT INTO finops_scenarios(name, description, items, demo) VALUES ($1,$2,$3,true) ON CONFLICT (name) DO NOTHING`,
			sc.Name, sc.Description, string(items))
	}
	s.db.ExecContext(ctx, `UPDATE finops_sources SET last_sync_at=now(), last_scan_at=now() WHERE id=$1`, id)
	s.audit("finops", "demo_load", "Demonstração", u, "success", fmt.Sprintf("%d linhas de custo, %d recursos", len(dd.Costs), len(dd.Resources)), nil)
	writeJSON(w, 200, map[string]any{"source_id": id, "rows": len(dd.Costs), "resources": len(dd.Resources)})
}

func (s *Server) finopsRemoveDemo(w http.ResponseWriter, r *http.Request, u *User) {
	ctx := r.Context()
	for _, q := range []string{`DELETE FROM finops_sources WHERE provider='demo'`, `DELETE FROM finops_prices WHERE source='demo'`,
		`DELETE FROM finops_budgets WHERE demo`, `DELETE FROM finops_policies WHERE demo`, `DELETE FROM finops_scenarios WHERE demo`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			failErr(w, err)
			return
		}
	}
	s.audit("finops", "demo_remove", "Demonstração", u, "success", "dados de exemplo removidos", nil)
	ok(w)
}

// ---------- orçamentos ----------

func (s *Server) finopsSaveBudget(w http.ResponseWriter, r *http.Request, u *User) {
	var b finops.Budget
	if err := decode(r, &b); err != nil {
		fail(w, 400, err.Error())
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if !finopsNameRe.MatchString(b.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	valid := false
	for _, sc := range finops.BudgetScopes {
		if sc == b.ScopeType {
			valid = true
		}
	}
	if !valid {
		fail(w, 400, "escopo inválido")
		return
	}
	if b.ScopeType == "all" {
		b.ScopeValue = ""
	} else if strings.TrimSpace(b.ScopeValue) == "" {
		fail(w, 400, "informe o valor do escopo")
		return
	}
	b.Currency = strings.ToUpper(strings.TrimSpace(b.Currency))
	if b.Amount <= 0 || b.Amount > 1e12 || !finopsCurRe.MatchString(b.Currency) {
		fail(w, 400, "valor ou moeda inválidos")
		return
	}
	var th []int
	seen := map[int]bool{}
	for _, t := range b.Thresholds {
		if t >= 1 && t <= 1000 && !seen[t] {
			th = append(th, t)
			seen[t] = true
		}
	}
	if len(th) == 0 {
		th = []int{80, 100}
	}
	raw, _ := json.Marshal(th)
	var err error
	id := int64(0)
	if r.Method == http.MethodPut {
		if id, err = pathID(r); err == nil {
			var res sql.Result
			res, err = s.db.ExecContext(r.Context(), `UPDATE finops_budgets SET name=$2, scope_type=$3, scope_value=$4, amount=$5, currency=$6, thresholds=$7, notify=$8 WHERE id=$1`,
				id, b.Name, b.ScopeType, truncate(b.ScopeValue, 200), b.Amount, b.Currency, string(raw), b.Notify)
			if err == nil {
				if n, _ := res.RowsAffected(); n == 0 {
					err = sql.ErrNoRows
				}
			}
		}
	} else {
		err = s.db.QueryRowContext(r.Context(), `INSERT INTO finops_budgets(name, scope_type, scope_value, amount, currency, thresholds, notify) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			b.Name, b.ScopeType, truncate(b.ScopeValue, 200), b.Amount, b.Currency, string(raw), b.Notify).Scan(&id)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "budget_save", b.Name, u, "info", fmt.Sprintf("%s %.2f (%s=%s)", b.Currency, b.Amount, b.ScopeType, b.ScopeValue), nil)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) finopsDeleteRow(table, action string) handler {
	return func(w http.ResponseWriter, r *http.Request, u *User) {
		id, err := pathID(r)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		var name string
		col := "name"
		if table == "finops_prices" {
			col = "provider || ' ' || region || ' ' || sku"
		}
		if err := s.db.QueryRowContext(r.Context(), `DELETE FROM `+table+` WHERE id=$1 RETURNING `+col, id).Scan(&name); err != nil {
			failErr(w, err)
			return
		}
		s.audit("finops", action, name, u, "info", "removido", nil)
		ok(w)
	}
}

// ---------- preços ----------

func validPrice(p *finops.Price) error {
	p.Provider = strings.ToLower(strings.TrimSpace(p.Provider))
	p.Region = strings.TrimSpace(p.Region)
	p.SKU = strings.TrimSpace(p.SKU)
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	if p.Region == "" {
		p.Region = "*"
	}
	if p.Currency == "" {
		p.Currency = "USD"
	}
	switch {
	case !regexpProvider.MatchString(p.Provider):
		return errors.New("provedor inválido")
	case p.Region != "*" && !finopsRegionRe.MatchString(p.Region):
		return errors.New("região inválida")
	case p.SKU == "" || len(p.SKU) > 120:
		return errors.New("SKU inválido")
	case !finops.ValidUnit(p.Unit):
		return errors.New("unidade inválida (hour, gb-month, month, gb-hour, unit)")
	case p.Price < 0 || p.Price > 1e7:
		return errors.New("preço inválido")
	case !finopsCurRe.MatchString(p.Currency):
		return errors.New("moeda inválida")
	}
	return nil
}

func (s *Server) upsertPrice(ctx context.Context, p finops.Price) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO finops_prices(provider, region, sku, unit, price, currency, source) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (provider, region, sku) DO UPDATE SET unit=EXCLUDED.unit, price=EXCLUDED.price, currency=EXCLUDED.currency, source=EXCLUDED.source, updated_at=now()`,
		p.Provider, p.Region, p.SKU, p.Unit, p.Price, p.Currency, p.Source)
	return err
}

func (s *Server) finopsPricesList(w http.ResponseWriter, r *http.Request, _ *User) {
	list, err := s.finopsPriceList(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"prices": list})
}

func (s *Server) finopsSavePrice(w http.ResponseWriter, r *http.Request, u *User) {
	var p finops.Price
	if err := decode(r, &p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := validPrice(&p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	p.Source = "manual"
	if err := s.upsertPrice(r.Context(), p); err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "price_save", p.Provider+" "+p.Region+" "+p.SKU, u, "info", fmt.Sprintf("%s %g/%s", p.Currency, p.Price, p.Unit), nil)
	ok(w)
}

func (s *Server) finopsImportPrices(w http.ResponseWriter, r *http.Request, u *User) {
	text, _, err := readCSVBody(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	list, errs, err := finops.ParsePriceCSV(strings.NewReader(text))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	n := 0
	for _, p := range list {
		if e := validPrice(&p); e != nil {
			if len(errs) < 20 {
				errs = append(errs, p.SKU+": "+e.Error())
			}
			continue
		}
		p.Source = "csv"
		if err := s.upsertPrice(r.Context(), p); err == nil {
			n++
		}
	}
	s.audit("finops", "price_import", "catálogo", u, "info", fmt.Sprintf("%d preços", n), nil)
	writeJSON(w, 200, map[string]any{"imported": n, "errors": errs})
}

// finopsAWSPrice busca preços públicos no AWS Price List usando as credenciais de uma fonte.
func (s *Server) finopsAWSPrice(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		SourceID int64    `json:"source_id"`
		Region   string   `json:"region"`
		Kind     string   `json:"kind"`
		Values   []string `json:"values"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !finopsRegionRe.MatchString(in.Region) || (in.Kind != "ec2" && in.Kind != "ebs") || len(in.Values) == 0 || len(in.Values) > 20 {
		fail(w, 400, "informe região, tipo (ec2/ebs) e até 20 valores")
		return
	}
	src, err := s.finopsSource(r.Context(), in.SourceID)
	if err != nil || src.Provider != "aws" {
		fail(w, 400, "selecione uma fonte AWS com credenciais (permissão pricing:GetProducts)")
		return
	}
	cl, err := s.awsClient(src)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var got []finops.Price
	var errs []string
	for _, v := range in.Values {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 40 {
			continue
		}
		p, err := cl.LookupPrice(ctx, in.Region, finops.PriceLookup{Kind: in.Kind, Value: v})
		if err != nil {
			errs = append(errs, v+": "+err.Error())
			continue
		}
		if err := s.upsertPrice(ctx, p); err != nil {
			errs = append(errs, v+": "+err.Error())
			continue
		}
		got = append(got, p)
	}
	s.audit("finops", "price_lookup", in.Region, u, "info", fmt.Sprintf("%d preços do AWS Price List", len(got)), in)
	writeJSON(w, 200, map[string]any{"prices": got, "errors": errs})
}

// finopsExportPrices exporta o catálogo e as configurações para o "doomctl cost-check" em CI/CD.
func (s *Server) finopsExportPrices(w http.ResponseWriter, r *http.Request, _ *User) {
	list, err := s.finopsPriceList(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	st := s.finopsSettings(r.Context())
	pol, _ := s.finopsPolicies(r.Context())
	st.WebhookURL = ""
	w.Header().Set("Content-Disposition", `attachment; filename="doomctl-finops-catalog.json"`)
	writeJSON(w, 200, finops.Catalog{Version: 1, Exported: s.now().UTC(), Settings: st, Prices: list, Policies: pol})
}

// ---------- configurações, políticas, cenários ----------

func (s *Server) finopsPutSettings(w http.ResponseWriter, r *http.Request, u *User) {
	var st finops.Settings
	if err := decode(r, &st); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if st.WebhookURL == "(configurado)" {
		st.WebhookURL = s.finopsSettings(r.Context()).WebhookURL
	}
	st.Normalize()
	raw, _ := json.Marshal(st)
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO finops_settings(key, value) VALUES ('settings', $1)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, string(raw)); err != nil {
		failErr(w, err)
		return
	}
	logged := st
	if logged.WebhookURL != "" {
		logged.WebhookURL = "(definido)"
	}
	s.audit("finops", "settings", "configurações", u, "info", "configurações do FinOps atualizadas", logged)
	writeJSON(w, 200, map[string]any{"settings": st})
}

func (s *Server) finopsSavePolicy(w http.ResponseWriter, r *http.Request, u *User) {
	var p finops.Policy
	if err := decode(r, &p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if !finopsNameRe.MatchString(p.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if _, ok := finops.PolicyKinds[p.Kind]; !ok {
		fail(w, 400, "tipo de política inválido")
		return
	}
	if p.Action != "block" {
		p.Action = "warn"
	}
	p.Match = truncate(strings.TrimSpace(p.Match), 200)
	if p.Match == "" {
		p.Match = "*"
	}
	if p.Value == nil {
		p.Value = map[string]any{}
	}
	raw, _ := json.Marshal(p.Value)
	if len(raw) > 8<<10 {
		fail(w, 400, "valor muito grande")
		return
	}
	var err error
	id := int64(0)
	if r.Method == http.MethodPut {
		if id, err = pathID(r); err == nil {
			var res sql.Result
			res, err = s.db.ExecContext(r.Context(), `UPDATE finops_policies SET name=$2, kind=$3, match=$4, value=$5, action=$6, enabled=$7 WHERE id=$1`,
				id, p.Name, p.Kind, p.Match, string(raw), p.Action, p.Enabled)
			if err == nil {
				if n, _ := res.RowsAffected(); n == 0 {
					err = sql.ErrNoRows
				}
			}
		}
	} else {
		err = s.db.QueryRowContext(r.Context(), `INSERT INTO finops_policies(name, kind, match, value, action, enabled) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			p.Name, p.Kind, p.Match, string(raw), p.Action, p.Enabled).Scan(&id)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "policy_save", p.Name, u, "info", p.Kind+" ("+p.Action+")", p.Value)
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) finopsSaveScenario(w http.ResponseWriter, r *http.Request, u *User) {
	var sc finops.Scenario
	if err := decode(r, &sc); err != nil {
		fail(w, 400, err.Error())
		return
	}
	sc.Name = strings.TrimSpace(sc.Name)
	if !finopsNameRe.MatchString(sc.Name) {
		fail(w, 400, "nome inválido")
		return
	}
	if len(sc.A)+len(sc.B) == 0 || len(sc.A)+len(sc.B) > 200 {
		fail(w, 400, "o cenário precisa de 1 a 200 itens")
		return
	}
	items, _ := json.Marshal(map[string]any{"a": sc.A, "b": sc.B})
	var err error
	id := int64(0)
	if r.Method == http.MethodPut {
		if id, err = pathID(r); err == nil {
			var res sql.Result
			res, err = s.db.ExecContext(r.Context(), `UPDATE finops_scenarios SET name=$2, description=$3, items=$4 WHERE id=$1`, id, sc.Name, truncate(sc.Description, 500), string(items))
			if err == nil {
				if n, _ := res.RowsAffected(); n == 0 {
					err = sql.ErrNoRows
				}
			}
		}
	} else {
		err = s.db.QueryRowContext(r.Context(), `INSERT INTO finops_scenarios(name, description, items) VALUES ($1,$2,$3) RETURNING id`,
			sc.Name, truncate(sc.Description, 500), string(items)).Scan(&id)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "scenario_save", sc.Name, u, "info", fmt.Sprintf("%d + %d itens", len(sc.A), len(sc.B)), nil)
	writeJSON(w, 200, map[string]any{"id": id})
}

// ---------- recomendações e remediação ----------

func (s *Server) finopsDismiss(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Key     string `json:"key"`
		Reason  string `json:"reason"`
		Restore bool   `json:"restore"`
	}
	if err := decode(r, &in); err != nil || in.Key == "" || len(in.Key) > 500 {
		fail(w, 400, "chave inválida")
		return
	}
	var err error
	if in.Restore {
		_, err = s.db.ExecContext(r.Context(), `DELETE FROM finops_dismissed WHERE key=$1`, in.Key)
	} else {
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO finops_dismissed(key, reason, dismissed_by) VALUES ($1,$2,$3) ON CONFLICT (key) DO UPDATE SET reason=EXCLUDED.reason`,
			in.Key, truncate(in.Reason, 300), u.Username)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	act := "finding_dismiss"
	if in.Restore {
		act = "finding_restore"
	}
	s.audit("finops", act, in.Key, u, "info", in.Reason, nil)
	ok(w)
}

// finopsRemediationScript gera um script (com --dry-run) para as recomendações escolhidas.
func (s *Server) finopsRemediationScript(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Keys []string `json:"keys"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	want := map[string]bool{}
	for _, k := range in.Keys {
		want[k] = true
	}
	st := s.finopsSettings(r.Context())
	fs, _, err := s.finopsFindingsAll(r.Context(), st, s.now())
	if err != nil {
		failErr(w, err)
		return
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n# Gerado pelo doomctl FinOps em " + s.now().Format("02/01/2006 15:04") + "\n")
	b.WriteString("# Revise cada comando. Os comandos destrutivos estão com --dry-run: remova a opção somente após validar.\n")
	b.WriteString("# Requer AWS CLI v2 (aws) / OCI CLI (oci) / kubectl autenticados com permissão de escrita.\nset -euo pipefail\n")
	n := 0
	for _, f := range fs {
		if !want[f.Key] || f.Remediation == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "\n# ── %s — %s\n", f.Title, f.Name)
		if f.MonthlySavings != nil {
			fmt.Fprintf(&b, "# economia estimada: %s %.2f/mês\n", st.BaseCurrency, *f.MonthlySavings)
		}
		b.WriteString(f.Remediation + "\n")
	}
	if n == 0 {
		fail(w, 400, "nenhuma das recomendações selecionadas tem comando de remediação")
		return
	}
	writeJSON(w, 200, map[string]any{"script": b.String(), "count": n})
}

// finopsRemediate executa uma ação automatizada (AWS) — primeiro em dry-run.
func (s *Server) finopsRemediate(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Key     string `json:"key"`
		DryRun  bool   `json:"dry_run"`
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	st := s.finopsSettings(ctx)
	fs, _, err := s.finopsFindingsAll(ctx, st, s.now())
	if err != nil {
		failErr(w, err)
		return
	}
	var f *finops.Finding
	for i := range fs {
		if fs[i].Key == in.Key {
			f = &fs[i]
		}
	}
	if f == nil || f.Action == "" || f.Provider != "aws" {
		fail(w, 400, "recomendação sem ação automatizada")
		return
	}
	act, okA := finops.RemediationActions[f.Action]
	if !okA {
		fail(w, 400, "ação desconhecida")
		return
	}
	src, err := s.finopsSource(ctx, f.SourceID)
	if err != nil || !src.Config.Remediation {
		fail(w, 403, "remediação automatizada desabilitada para esta fonte (um administrador pode habilitá-la)")
		return
	}
	if !in.DryRun && in.Confirm != f.ResourceID {
		fail(w, 400, "para executar, digite o ID do recurso ("+f.ResourceID+") como confirmação")
		return
	}
	cl, err := s.awsClient(src)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	finding := *f
	step := func(ctx context.Context, w io.Writer) error {
		if !in.DryRun && finding.Action == "delete_volume" {
			fmt.Fprintln(w, "→ criando snapshot de segurança antes de excluir o volume")
			msg, err := cl.Remediate(ctx, finding.Region, "snapshot_volume", finding.ResourceID, false)
			if err != nil {
				return fmt.Errorf("snapshot de segurança falhou, exclusão cancelada: %w", err)
			}
			fmt.Fprintln(w, "  "+msg)
		}
		mode := "EXECUÇÃO"
		if in.DryRun {
			mode = "SIMULAÇÃO (DryRun)"
		}
		fmt.Fprintf(w, "→ %s: %s em %s (%s)\n", mode, act.Label, finding.ResourceID, finding.Region)
		msg, err := cl.Remediate(ctx, finding.Region, finding.Action, finding.ResourceID, in.DryRun)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "✔ "+msg)
		return nil
	}
	action := "remediate"
	if in.DryRun {
		action = "remediate_dryrun"
	}
	s.startJob(w, u, jobs.Spec{Module: "finops", Action: action, Target: finding.ResourceID,
		Input: map[string]any{"key": finding.Key, "action": finding.Action, "dry_run": in.DryRun, "region": finding.Region, "source_id": src.ID},
		Steps: []jobs.Step{{Name: act.Label, Func: step}}, Timeout: 5 * time.Minute})
}

// ---------- alertas ----------

func (s *Server) finopsTestAlert(w http.ResponseWriter, r *http.Request, u *User) {
	st := s.finopsSettings(r.Context())
	if st.WebhookURL == "" {
		fail(w, 400, "configure a URL do webhook em Fontes & configurações")
		return
	}
	err := s.sendWebhook(r.Context(), st.WebhookURL, "✅ doomctl FinOps: teste de alerta enviado por "+u.Username)
	status := "success"
	msg := "webhook respondeu com sucesso"
	if err != nil {
		status, msg = "failed", err.Error()
	}
	s.audit("finops", "alert_test", "webhook", u, status, msg, nil)
	if err != nil {
		fail(w, 400, "falha ao enviar: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) finopsEvaluateNow(w http.ResponseWriter, r *http.Request, u *User) {
	n, err := s.finopsEvaluateAlerts(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("finops", "alert_evaluate", "alertas", u, "info", fmt.Sprintf("%d novos alertas", n), nil)
	writeJSON(w, 200, map[string]any{"new": n})
}
