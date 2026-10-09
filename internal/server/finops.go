package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/doomctl/doomctl/internal/finops"
	"github.com/doomctl/doomctl/internal/rbac"
	"github.com/doomctl/doomctl/internal/safeenv"
)

// ---------- leitura do banco ----------

var (
	finopsNameRe   = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._()\[\]/-]{0,79}$`)
	finopsRegionRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+){1,4}$`)
	finopsTagRe    = regexp.MustCompile(`^[\p{L}\p{N} _.:/=+@-]{1,128}$`)
	finopsCurRe    = regexp.MustCompile(`^[A-Z]{3}$`)
)

func (s *Server) finopsSettings(ctx context.Context) finops.Settings {
	st := finops.DefaultSettings()
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM finops_settings WHERE key='settings'`).Scan(&raw); err == nil {
		json.Unmarshal(raw, &st)
	}
	st.Normalize()
	return st
}

// finopsData carrega custos, alocação e uso entre from (inclusivo) e to (exclusivo).
func (s *Server) finopsData(ctx context.Context, from, to time.Time) (finops.Data, error) {
	var d finops.Data
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, day, provider, account, service, region, cost::float8, currency, usage_qty::float8, usage_unit
		FROM finops_costs WHERE day >= $1 AND day < $2`, from, to)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var c finops.CostRow
		if err := rows.Scan(&c.SourceID, &c.Day, &c.Provider, &c.Account, &c.Service, &c.Region, &c.Cost, &c.Currency, &c.UsageQty, &c.UsageUnit); err != nil {
			rows.Close()
			return d, err
		}
		d.Costs = append(d.Costs, c)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT source_id, day, provider, service, dim, value, cost::float8, currency
		FROM finops_alloc WHERE day >= $1 AND day < $2`, from, to)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var a finops.AllocRow
		if err := rows.Scan(&a.SourceID, &a.Day, &a.Provider, &a.Service, &a.Dim, &a.Value, &a.Cost, &a.Currency); err != nil {
			rows.Close()
			return d, err
		}
		d.Alloc = append(d.Alloc, a)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT source_id, day, provider, service, usage_type, category, cost::float8, currency, quantity::float8, unit
		FROM finops_usage WHERE day >= $1 AND day < $2`, from, to)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var u finops.UsageRow
		if err := rows.Scan(&u.SourceID, &u.Day, &u.Provider, &u.Service, &u.UsageType, &u.Category, &u.Cost, &u.Currency, &u.Quantity, &u.Unit); err != nil {
			return d, err
		}
		d.Usage = append(d.Usage, u)
	}
	return d, rows.Err()
}

func (s *Server) finopsPriceList(ctx context.Context) ([]finops.Price, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, provider, region, sku, unit, price::float8, currency, source, updated_at FROM finops_prices ORDER BY provider, sku, region`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []finops.Price{}
	for rows.Next() {
		var p finops.Price
		if err := rows.Scan(&p.ID, &p.Provider, &p.Region, &p.SKU, &p.Unit, &p.Price, &p.Currency, &p.Source, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Server) finopsPrices(ctx context.Context) *finops.Prices {
	list, _ := s.finopsPriceList(ctx)
	return finops.NewPrices(list)
}

func (s *Server) finopsResources(ctx context.Context) ([]finops.Resource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source_id, provider, region, resource_id, type, name, sku, size_gb, state, monthly_cost, currency,
		cpu_avg, cpu_max, mem_avg, attached, age_days, tags, raw FROM finops_resources ORDER BY provider, region, type, resource_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []finops.Resource{}
	for rows.Next() {
		var r finops.Resource
		var mc, ca, cm, ma sql.NullFloat64
		var at sql.NullBool
		var tags, raw []byte
		if err := rows.Scan(&r.ID, &r.SourceID, &r.Provider, &r.Region, &r.ResourceID, &r.Type, &r.Name, &r.SKU, &r.SizeGB, &r.State, &mc, &r.Currency,
			&ca, &cm, &ma, &at, &r.AgeDays, &tags, &raw); err != nil {
			return nil, err
		}
		opt := func(n sql.NullFloat64) *float64 {
			if !n.Valid {
				return nil
			}
			v := n.Float64
			return &v
		}
		r.MonthlyCost, r.CPUAvg, r.CPUMax, r.MemAvg = opt(mc), opt(ca), opt(cm), opt(ma)
		if at.Valid {
			b := at.Bool
			r.Attached = &b
		}
		json.Unmarshal(tags, &r.Tags)
		json.Unmarshal(raw, &r.Raw)
		if r.Tags == nil {
			r.Tags = map[string]string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Server) finopsBudgets(ctx context.Context) ([]finops.Budget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scope_type, scope_value, amount::float8, currency, thresholds, notify, created_at FROM finops_budgets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []finops.Budget{}
	for rows.Next() {
		var b finops.Budget
		var th []byte
		if err := rows.Scan(&b.ID, &b.Name, &b.ScopeType, &b.ScopeValue, &b.Amount, &b.Currency, &th, &b.Notify, &b.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(th, &b.Thresholds)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Server) finopsPolicies(ctx context.Context) ([]finops.Policy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, match, value, action, enabled FROM finops_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []finops.Policy{}
	for rows.Next() {
		var p finops.Policy
		var v []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Match, &v, &p.Action, &p.Enabled); err != nil {
			return nil, err
		}
		json.Unmarshal(v, &p.Value)
		if p.Value == nil {
			p.Value = map[string]any{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Server) finopsScenarios(ctx context.Context) ([]finops.Scenario, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, items, created_at FROM finops_scenarios ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []finops.Scenario{}
	for rows.Next() {
		var sc finops.Scenario
		var items []byte
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Description, &items, &sc.CreatedAt); err != nil {
			return nil, err
		}
		var ab struct{ A, B []finops.ScenarioItem }
		json.Unmarshal(items, &ab)
		sc.A, sc.B = ab.A, ab.B
		if sc.A == nil {
			sc.A = []finops.ScenarioItem{}
		}
		if sc.B == nil {
			sc.B = []finops.ScenarioItem{}
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Server) finopsDismissed(ctx context.Context) map[string]bool {
	m := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM finops_dismissed`)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		rows.Scan(&k)
		m[k] = true
	}
	return m
}

func (s *Server) finopsRightsizing(ctx context.Context) map[int64][]map[string]any {
	out := map[int64][]map[string]any{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, rightsizing FROM finops_sources WHERE provider='aws'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw []byte
		rows.Scan(&id, &raw)
		var recs []map[string]any
		if json.Unmarshal(raw, &recs) == nil && len(recs) > 0 {
			out[id] = recs
		}
	}
	return out
}

// finopsFindingsAll calcula as recomendações do inventário, rede/armazenamento e Cost Explorer.
func (s *Server) finopsFindingsAll(ctx context.Context, st finops.Settings, now time.Time) ([]finops.Finding, []finops.Resource, error) {
	res, err := s.finopsResources(ctx)
	if err != nil {
		return nil, nil, err
	}
	prices := s.finopsPrices(ctx)
	fs := finops.EvaluateResources(finops.RuleInput{Resources: res, Prices: prices, Settings: st, AWSRightsizing: s.finopsRightsizing(ctx), Now: now})
	from := finops.Day(now).AddDate(0, 0, -62)
	d, err := s.finopsData(ctx, from, finops.Day(now).AddDate(0, 0, 1))
	if err == nil {
		ua := finops.AnalyzeUsage(d.Usage, st, now, 30)
		fs = append(fs, ua.Findings...)
	}
	dis := s.finopsDismissed(ctx)
	for i := range fs {
		fs[i].Dismissed = dis[fs[i].Key]
	}
	return fs, res, nil
}

// ---------- filtros ----------

func finopsFilter(r *http.Request) finops.Filter {
	q := r.URL.Query()
	f := finops.Filter{Provider: q.Get("provider"), Account: q.Get("account"), Service: q.Get("service"), Region: q.Get("region"),
		Project: q.Get("project"), Environment: q.Get("environment"), Team: q.Get("team")}
	f.SourceID, _ = strconv.ParseInt(q.Get("source_id"), 10, 64)
	return f
}

func finopsPeriod(r *http.Request, now time.Time) (time.Time, time.Time) {
	q := r.URL.Query()
	today := finops.Day(now)
	end := today
	start := today.AddDate(0, 0, -30)
	if v := q.Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 400 {
			start = today.AddDate(0, 0, -n)
		}
	}
	if q.Get("start") != "" && q.Get("end") != "" {
		s1, e1 := finops.ParseDate(q.Get("start"))
		s2, e2 := finops.ParseDate(q.Get("end"))
		if e1 == nil && e2 == nil && s2.After(s1) && s2.Sub(s1) <= 400*24*time.Hour {
			start, end = s1, s2
		}
	}
	return start, end
}

// ---------- endpoints de leitura ----------

func (s *Server) finopsOverview(w http.ResponseWriter, r *http.Request, _ *User) {
	now := s.now()
	start, end := finopsPeriod(r, now)
	days := int(end.Sub(start).Hours() / 24)
	from := start.AddDate(0, 0, -days)
	if alt := finops.Day(now).AddDate(0, -2, 0); alt.Before(from) {
		from = alt
	}
	to := end
	if t := finops.Day(now).AddDate(0, 0, 1); t.After(to) {
		to = t
	}
	d, err := s.finopsData(r.Context(), from, to)
	if err != nil {
		failErr(w, err)
		return
	}
	st := s.finopsSettings(r.Context())
	writeJSON(w, 200, finops.BuildOverview(d, finopsFilter(r), st, start, end, now))
}

// finopsDimensions lista os valores existentes para os filtros.
func (s *Server) finopsDimensions(w http.ResponseWriter, r *http.Request, _ *User) {
	ctx := r.Context()
	out := map[string]any{}
	q := func(sqlq string) []string {
		vals := []string{}
		rows, err := s.db.QueryContext(ctx, sqlq)
		if err != nil {
			return vals
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			rows.Scan(&v)
			if v != "" {
				vals = append(vals, v)
			}
		}
		return vals
	}
	out["providers"] = q(`SELECT DISTINCT provider FROM finops_costs ORDER BY 1`)
	out["accounts"] = q(`SELECT DISTINCT account FROM finops_costs ORDER BY 1`)
	out["services"] = q(`SELECT DISTINCT service FROM finops_costs ORDER BY 1`)
	out["regions"] = q(`SELECT DISTINCT region FROM finops_costs ORDER BY 1`)
	out["projects"] = q(`SELECT DISTINCT value FROM finops_alloc WHERE dim='project' ORDER BY 1`)
	out["environments"] = q(`SELECT DISTINCT value FROM finops_alloc WHERE dim='environment' ORDER BY 1`)
	teams := q(`SELECT DISTINCT value FROM finops_alloc WHERE dim='team' ORDER BY 1`)
	st := s.finopsSettings(ctx)
	seen := map[string]bool{}
	for _, t := range teams {
		seen[t] = true
	}
	for _, rl := range st.TeamRules {
		if !seen[rl.Team] {
			teams = append(teams, rl.Team)
			seen[rl.Team] = true
		}
	}
	sort.Strings(teams)
	out["teams"] = teams
	srcs, _ := s.finopsSourceList(ctx)
	type opt struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	so := []opt{}
	for _, x := range srcs {
		so = append(so, opt{x.ID, x.Name})
	}
	out["sources"] = so
	out["currency"] = st.BaseCurrency
	writeJSON(w, 200, out)
}

func (s *Server) finopsAnomalies(w http.ResponseWriter, r *http.Request, _ *User) {
	now := s.now()
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 30
	}
	st := s.finopsSettings(r.Context())
	d, err := s.finopsData(r.Context(), finops.Day(now).AddDate(0, 0, -(days+st.AnomalyWindow+1)), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		failErr(w, err)
		return
	}
	an := finops.DetectAnomalies(d, st, now, days)
	writeJSON(w, 200, map[string]any{"anomalies": an, "alerts": s.finopsAlertHistory(r.Context(), 100), "settings": map[string]any{
		"window": st.AnomalyWindow, "pct": st.AnomalyPct, "min_abs": st.AnomalyMinAbs, "z": st.AnomalyZ, "webhook": st.WebhookURL != "", "currency": st.BaseCurrency}})
}

type finopsAlert struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	Delivered bool      `json:"delivered"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) finopsAlertHistory(ctx context.Context, limit int) []finopsAlert {
	out := []finopsAlert{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, severity, message, delivered, error, created_at FROM finops_alerts ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a finopsAlert
		rows.Scan(&a.ID, &a.Kind, &a.Severity, &a.Message, &a.Delivered, &a.Error, &a.CreatedAt)
		out = append(out, a)
	}
	return out
}

func (s *Server) finopsForecast(w http.ResponseWriter, r *http.Request, _ *User) {
	now := s.now()
	d, err := s.finopsData(r.Context(), finops.Day(now).AddDate(0, 0, -70), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		failErr(w, err)
		return
	}
	st := s.finopsSettings(r.Context())
	h, _ := strconv.Atoi(r.URL.Query().Get("horizon"))
	if h < 0 || h > 120 {
		h = 0
	}
	writeJSON(w, 200, map[string]any{"currency": st.BaseCurrency, "forecast": finops.ForecastFor(d, finopsFilter(r), st, now, h)})
}

func (s *Server) finopsBudgetStatus(ctx context.Context, now time.Time, st finops.Settings) ([]finops.BudgetStatus, error) {
	bs, err := s.finopsBudgets(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.finopsData(ctx, finops.Day(now).AddDate(0, 0, -70), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	return finops.EvaluateBudgets(bs, d, st, now), nil
}

func (s *Server) finopsBudgetList(w http.ResponseWriter, r *http.Request, _ *User) {
	st := s.finopsSettings(r.Context())
	out, err := s.finopsBudgetStatus(r.Context(), s.now(), st)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"budgets": out, "scopes": finops.BudgetScopes, "currency": st.BaseCurrency})
}

func (s *Server) finopsFindings(w http.ResponseWriter, r *http.Request, _ *User) {
	ctx := r.Context()
	now := s.now()
	st := s.finopsSettings(ctx)
	fs, res, err := s.finopsFindingsAll(ctx, st, now)
	if err != nil {
		failErr(w, err)
		return
	}
	type cat struct {
		Count   int     `json:"count"`
		Savings float64 `json:"savings"`
	}
	sum := map[string]*cat{}
	var total float64
	for _, f := range fs {
		if f.Dismissed {
			continue
		}
		c := sum[f.Category]
		if c == nil {
			c = &cat{}
			sum[f.Category] = c
		}
		c.Count++
		if f.MonthlySavings != nil {
			c.Savings += *f.MonthlySavings
			total += *f.MonthlySavings
		}
	}
	pol, _ := s.finopsPolicies(ctx)
	viol := finops.EvaluatePolicies(pol, res, finops.Coster{Prices: s.finopsPrices(ctx), Conv: st.Converter()})
	remed := map[int64]bool{}
	srcs, _ := s.finopsSourceList(ctx)
	for _, x := range srcs {
		remed[x.ID] = x.Provider == "aws" && x.Config.Remediation && x.HasCredentials
	}
	for i := range fs {
		if fs[i].Action != "" && !remed[fs[i].SourceID] {
			fs[i].Action = ""
		}
	}
	writeJSON(w, 200, map[string]any{"findings": fs, "summary": sum, "total_savings": total, "violations": viol,
		"currency": st.BaseCurrency, "resources": len(res), "actions": finops.RemediationActions})
}

func (s *Server) finopsResourceList(w http.ResponseWriter, r *http.Request, _ *User) {
	ctx := r.Context()
	res, err := s.finopsResources(ctx)
	if err != nil {
		failErr(w, err)
		return
	}
	st := s.finopsSettings(ctx)
	c := finops.Coster{Prices: s.finopsPrices(ctx), Conv: st.Converter()}
	type row struct {
		finops.Resource
		Estimated *float64 `json:"estimated"`
		Missing   []string `json:"missing_tags"`
	}
	out := []row{}
	for _, x := range res {
		rw := row{Resource: x, Missing: finops.MissingTags(x.Tags, st.RequiredTags)}
		if v, ok := c.ResourceMonthly(x); ok {
			rw.Estimated = &v
		}
		if rw.Missing == nil {
			rw.Missing = []string{}
		}
		rw.Raw = nil
		out = append(out, rw)
	}
	writeJSON(w, 200, map[string]any{"resources": out, "currency": st.BaseCurrency, "required_tags": st.RequiredTags})
}

func (s *Server) finopsUsage(w http.ResponseWriter, r *http.Request, _ *User) {
	now := s.now()
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 30
	}
	d, err := s.finopsData(r.Context(), finops.Day(now).AddDate(0, 0, -2*days-1), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, finops.AnalyzeUsage(d.Usage, s.finopsSettings(r.Context()), now, days))
}

func (s *Server) finopsCommitments(w http.ResponseWriter, r *http.Request, _ *User) {
	now := s.now()
	d, err := s.finopsData(r.Context(), finops.Day(now).AddDate(0, 0, -32), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, finops.AnalyzeCommitments(d, s.finopsSettings(r.Context()), now))
}

func (s *Server) finopsReport(w http.ResponseWriter, r *http.Request, u *User) {
	var req finops.ReportRequest
	if err := decode(r, &req); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	now := s.now()
	start, end, err := req.ResolvePeriod(now)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	days := int(end.Sub(start).Hours() / 24)
	from := start.AddDate(0, 0, -days)
	if alt := finops.Day(now).AddDate(0, 0, -100); alt.Before(from) {
		from = alt
	}
	to := end
	if t := finops.Day(now).AddDate(0, 0, 1); t.After(to) {
		to = t
	}
	d, err := s.finopsData(ctx, from, to)
	if err != nil {
		failErr(w, err)
		return
	}
	st := s.finopsSettings(ctx)
	bs := finops.EvaluateBudgets(mustBudgets(s, ctx), d, st, now)
	fs, _, _ := s.finopsFindingsAll(ctx, st, now)
	an := finops.DetectAnomalies(d, st, now, 60)
	rep, err := finops.BuildReport(req, d, st, now, bs, fs, an)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.audit("finops", "report", rep.Mode+" por "+rep.GroupBy, u, "success", rep.Title, req)
	writeJSON(w, 200, rep)
}

func mustBudgets(s *Server, ctx context.Context) []finops.Budget {
	b, _ := s.finopsBudgets(ctx)
	return b
}

func (s *Server) finopsGetSettings(w http.ResponseWriter, r *http.Request, u *User) {
	st := s.finopsSettings(r.Context())
	// a URL do webhook pode conter token: só quem gerencia vê o valor completo
	if !s.can(u, "finops", rbac.Manage) && st.WebhookURL != "" {
		st.WebhookURL = "(configurado)"
	}
	writeJSON(w, 200, map[string]any{"settings": st, "defaults": finops.DefaultSettings(), "policy_kinds": finops.PolicyKinds,
		"report_groups": finops.ReportGroups, "usage_labels": finops.UsageCategoryLabels})
}

func (s *Server) finopsPolicyList(w http.ResponseWriter, r *http.Request, _ *User) {
	p, err := s.finopsPolicies(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"policies": p, "kinds": finops.PolicyKinds})
}

func (s *Server) finopsScenarioList(w http.ResponseWriter, r *http.Request, _ *User) {
	ctx := r.Context()
	sc, err := s.finopsScenarios(ctx)
	if err != nil {
		failErr(w, err)
		return
	}
	st := s.finopsSettings(ctx)
	prices := s.finopsPrices(ctx)
	type out struct {
		finops.Scenario
		Result finops.ScenarioResult `json:"result"`
	}
	res := []out{}
	for _, x := range sc {
		res = append(res, out{x, finops.CompareScenario(x, prices, st)})
	}
	writeJSON(w, 200, map[string]any{"scenarios": res, "currency": st.BaseCurrency})
}

func (s *Server) finopsScenarioPrice(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		A       []finops.ScenarioItem `json:"a"`
		B       []finops.ScenarioItem `json:"b"`
		Regions []string              `json:"regions"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(in.A)+len(in.B) > 200 || len(in.Regions) > 20 {
		fail(w, 400, "cenário grande demais")
		return
	}
	ctx := r.Context()
	st := s.finopsSettings(ctx)
	prices := s.finopsPrices(ctx)
	out := map[string]any{"result": finops.CompareScenario(finops.Scenario{A: in.A, B: in.B}, prices, st)}
	if len(in.Regions) > 0 {
		out["regions"] = finops.CompareRegions(in.A, in.Regions, prices, st)
	}
	writeJSON(w, 200, out)
}

func (s *Server) finopsWhatIf(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Global      float64            `json:"global"`
		Adjustments map[string]float64 `json:"adjustments"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	now := s.now()
	d, err := s.finopsData(r.Context(), finops.Day(now).AddDate(0, 0, -31), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, finops.WhatIfSpend(d, s.finopsSettings(r.Context()), now, in.Adjustments, in.Global))
}

// ---------- IaC ----------

type iacRequest struct {
	ProjectID int64             `json:"project_id"`
	Files     map[string]string `json:"files"`
	BaseID    int64             `json:"base_project_id"`
	BaseFiles map[string]string `json:"base_files"`
	Vars      map[string]string `json:"vars"`
	Region    string            `json:"region"`
	Env       string            `json:"env"`
	Fixes     []string          `json:"fixes"`
}

func (s *Server) iacFiles(ctx context.Context, u *User, id int64, files map[string]string) (map[string]string, string, error) {
	if id > 0 {
		if !s.can(u, "opentofu", rbac.Read) {
			return nil, "", errors.New("sem permissão de leitura no módulo OpenTofu")
		}
		p, err := s.loadTofu(ctx, id)
		if err != nil {
			return nil, "", err
		}
		return p.Files, p.Name, nil
	}
	if len(files) == 0 {
		return nil, "", errors.New("informe um projeto OpenTofu ou cole os arquivos .tf")
	}
	total := 0
	for n, c := range files {
		if !tofuFileRe.MatchString(n) {
			return nil, "", errors.New("nome de arquivo inválido: " + n)
		}
		total += len(c)
	}
	if total > 2<<20 || len(files) > 50 {
		return nil, "", errors.New("arquivos excedem o limite (50 arquivos, 2 MiB)")
	}
	return files, "", nil
}

func (s *Server) iacOptions(ctx context.Context, in iacRequest) finops.IaCOptions {
	pol, _ := s.finopsPolicies(ctx)
	vars := map[string]string{}
	for k, v := range in.Vars {
		if len(vars) < 100 && regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`).MatchString(k) {
			vars[k] = truncate(v, 500)
		}
	}
	reg := ""
	if finopsRegionRe.MatchString(in.Region) {
		reg = in.Region
	}
	return finops.IaCOptions{Vars: vars, Region: reg, Env: truncate(in.Env, 40), Policies: pol}
}

func (s *Server) finopsIaCEstimate(w http.ResponseWriter, r *http.Request, u *User) {
	var in iacRequest
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	files, name, err := s.iacFiles(ctx, u, in.ProjectID, in.Files)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failErr(w, err)
			return
		}
		fail(w, 400, err.Error())
		return
	}
	st := s.finopsSettings(ctx)
	est := finops.EstimateIaC(files, s.iacOptions(ctx, in), s.finopsPrices(ctx), st)
	writeJSON(w, 200, map[string]any{"estimate": est, "project": name})
}

func (s *Server) finopsIaCCheck(w http.ResponseWriter, r *http.Request, u *User) {
	var in iacRequest
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	base, _, err := s.iacFiles(ctx, u, in.BaseID, in.BaseFiles)
	if err != nil {
		if in.BaseID == 0 && len(in.BaseFiles) == 0 {
			base = map[string]string{}
		} else {
			fail(w, 400, "versão base: "+err.Error())
			return
		}
	}
	head, _, err := s.iacFiles(ctx, u, in.ProjectID, in.Files)
	if err != nil {
		fail(w, 400, "versão proposta: "+err.Error())
		return
	}
	st := s.finopsSettings(ctx)
	ck := finops.CheckIaC(base, head, s.iacOptions(ctx, in), s.finopsPrices(ctx), st)
	writeJSON(w, 200, map[string]any{"check": ck, "markdown": ck.Markdown()})
}

// finopsIaCFix aplica correções de custo em um projeto OpenTofu salvo (gerenciar FinOps + OpenTofu).
func (s *Server) finopsIaCFix(w http.ResponseWriter, r *http.Request, u *User) {
	var in iacRequest
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.ProjectID <= 0 || len(in.Fixes) == 0 {
		fail(w, 400, "informe o projeto e as correções")
		return
	}
	if !s.can(u, "opentofu", rbac.Manage) {
		fail(w, 403, "aplicar correções exige permissão de gerenciar OpenTofu")
		return
	}
	ctx := r.Context()
	p, err := s.loadTofu(ctx, in.ProjectID)
	if err != nil {
		failErr(w, err)
		return
	}
	if _, busy := tofuBusy.Load(p.ID); busy {
		fail(w, 409, "há uma ação do OpenTofu em andamento neste projeto")
		return
	}
	st := s.finopsSettings(ctx)
	est := finops.EstimateIaC(p.Files, s.iacOptions(ctx, in), s.finopsPrices(ctx), st)
	want := map[string]bool{}
	for _, id := range in.Fixes {
		want[id] = true
	}
	var sel []finops.IaCFix
	for _, f := range est.Fixes {
		if want[f.ID] {
			sel = append(sel, f)
		}
	}
	files, n := finops.ApplyIaCFixes(p.Files, sel)
	if n == 0 {
		fail(w, 400, "nenhuma correção aplicável (o projeto mudou?)")
		return
	}
	raw, _ := json.Marshal(files)
	if _, err := s.db.ExecContext(ctx, `UPDATE tofu_projects SET files=$2, updated_at=now() WHERE id=$1`, p.ID, string(raw)); err != nil {
		failErr(w, err)
		return
	}
	var desc []string
	for _, f := range sel {
		desc = append(desc, f.File+":"+strconv.Itoa(f.Line)+" "+f.Old+" → "+f.New)
	}
	s.audit("finops", "iac_fix", p.Name, u, "success", strings.Join(desc, "\n"), map[string]any{"project_id": p.ID, "fixes": in.Fixes})
	s.audit("opentofu", "project_save", p.Name, u, "info", "correções de custo aplicadas pelo FinOps: "+strconv.Itoa(n), nil)
	writeJSON(w, 200, map[string]any{"applied": n, "files": files})
}

// ---------- Kubernetes ----------

func (s *Server) finopsK8s(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		finops.K8sInput
		ClusterID int64 `json:"cluster_id"`
		Demo      bool  `json:"demo"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	st := s.finopsSettings(ctx)
	ki := in.K8sInput
	switch {
	case in.Demo:
		ki = finops.DemoK8sInput()
	case in.ClusterID > 0:
		if !s.can(u, "kubernetes", rbac.Manage) {
			fail(w, 403, "coletar dados do cluster exige permissão de gerenciar Kubernetes (ou cole as saídas do kubectl)")
			return
		}
		out, name, err := s.k8sCollect(ctx, in.ClusterID)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		cm := ki.ClusterMonthly
		ki = out
		ki.ClusterMonthly = cm
		ki.ClusterName = name
		s.audit("finops", "k8s_collect", name, u, "success", "coleta de pods/nós/HPA/top para análise de custo", map[string]any{"cluster_id": in.ClusterID})
	default:
		if len(ki.Pods) > 32<<20 || len(ki.Nodes) > 8<<20 {
			fail(w, 400, "entrada grande demais")
			return
		}
	}
	rep, err := finops.AnalyzeK8s(ki, s.finopsPrices(ctx), st)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Demo {
		rep.Notes = append(rep.Notes, "Cluster de exemplo com preços de exemplo.")
	}
	writeJSON(w, 200, rep)
}

// k8sCollect executa kubectl (somente leitura) para coletar pods, nós, HPAs e uso.
func (s *Server) k8sCollect(ctx context.Context, id int64) (finops.K8sInput, string, error) {
	dir, cleanup, name, base, err := s.kubeRun(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return finops.K8sInput{}, "", errors.New("cluster não encontrado")
		}
		return finops.K8sInput{}, "", err
	}
	defer cleanup()
	run := func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return runCapture(c, dir, s.toolEnv(), 64<<20, append([]string{"kubectl"}, append(append([]string{}, base...), args...)...)...)
	}
	var in finops.K8sInput
	if in.Pods, err = run("get", "pods", "-A", "-o", "json"); err != nil {
		return in, name, errors.New("kubectl get pods: " + err.Error())
	}
	if in.Nodes, err = run("get", "nodes", "-o", "json"); err != nil {
		return in, name, errors.New("kubectl get nodes: " + err.Error())
	}
	in.HPAs, _ = run("get", "hpa", "-A", "-o", "json")
	in.Top, _ = run("top", "pods", "-A", "--no-headers")
	return in, name, nil
}

// limitedBuffer descarta o que exceder o limite (evita estourar memória com saídas grandes).
type limitedBuffer struct {
	b     []byte
	limit int
	over  bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.b)+len(p) > l.limit {
		l.over = true
		p = p[:max(0, l.limit-len(l.b))]
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

// runCapture executa argv (sem shell) em dir e devolve o stdout.
func runCapture(ctx context.Context, dir string, env []string, limit int, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = safeenv.Env(env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out := &limitedBuffer{limit: limit}
	errb := &limitedBuffer{limit: 8 << 10}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(string(errb.b))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(truncate(msg, 400))
	}
	if out.over {
		return "", errors.New("saída maior que o limite")
	}
	return string(out.b), nil
}
