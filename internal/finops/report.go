package finops

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ReportRequest struct {
	Title      string `json:"title"`
	Month      string `json:"month"` // AAAA-MM (alternativa a start/end)
	Start      string `json:"start"`
	End        string `json:"end"` // exclusivo
	GroupBy    string `json:"group_by"`
	Mode       string `json:"mode"` // showback | chargeback
	Distribute bool   `json:"distribute"`
	Filter     Filter `json:"filter"`
}

type ReportLine struct {
	Key      string   `json:"key"`
	Direct   float64  `json:"direct"`
	Shared   float64  `json:"shared"`
	Total    float64  `json:"total"`
	Prev     float64  `json:"prev"`
	DeltaPct *float64 `json:"delta_pct"`
	Share    float64  `json:"share"`
}

type Report struct {
	Title       string         `json:"title"`
	Currency    string         `json:"currency"`
	Start       string         `json:"start"`
	End         string         `json:"end"`
	Mode        string         `json:"mode"`
	GroupBy     string         `json:"group_by"`
	Total       float64        `json:"total"`
	PrevTotal   float64        `json:"prev_total"`
	DeltaPct    *float64       `json:"delta_pct"`
	Unallocated float64        `json:"unallocated"`
	Lines       []ReportLine   `json:"lines"`
	Increases   []Bucket       `json:"increases"`
	Budgets     []BudgetStatus `json:"budgets"`
	Findings    []Finding      `json:"findings"`
	Savings     float64        `json:"savings"`
	Anomalies   []Anomaly      `json:"anomalies"`
	Forecast    Forecast       `json:"forecast"`
	Series      []SeriesPoint  `json:"series"`
	SeriesKeys  []string       `json:"series_keys"`
	Notes       []string       `json:"notes"`
	Markdown    string         `json:"markdown"`
	CSV         string         `json:"csv"`
}

var ReportGroups = []string{"service", "project", "environment", "team", "account", "region", "provider"}

// ResolvePeriod converte mês ou início/fim em datas (fim exclusivo).
func (r ReportRequest) ResolvePeriod(now time.Time) (time.Time, time.Time, error) {
	if r.Month != "" {
		m, err := time.Parse("2006-01", r.Month)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("mês inválido %q (use AAAA-MM)", r.Month)
		}
		return m, m.AddDate(0, 1, 0), nil
	}
	if r.Start != "" && r.End != "" {
		s, err1 := ParseDate(r.Start)
		e, err2 := ParseDate(r.End)
		if err1 != nil || err2 != nil || !e.After(s) {
			return time.Time{}, time.Time{}, fmt.Errorf("período inválido")
		}
		if e.Sub(s) > 400*24*time.Hour {
			return time.Time{}, time.Time{}, fmt.Errorf("período máximo de 400 dias")
		}
		return s, e, nil
	}
	ms := monthStart(Day(now))
	return ms.AddDate(0, -1, 0), ms, nil
}

// BuildReport monta o relatório de showback/chargeback com tendências,
// orçamentos, anomalias e oportunidades de economia.
func BuildReport(req ReportRequest, d Data, st Settings, now time.Time, budgets []BudgetStatus, findings []Finding, anomalies []Anomaly) (Report, error) {
	start, end, err := req.ResolvePeriod(now)
	if err != nil {
		return Report{}, err
	}
	group := req.GroupBy
	if group == "" {
		group = "service"
	}
	valid := false
	for _, g := range ReportGroups {
		if g == group {
			valid = true
		}
	}
	if !valid {
		return Report{}, fmt.Errorf("agrupamento inválido %q", group)
	}
	mode := req.Mode
	if mode != "chargeback" {
		mode = "showback"
	}
	ov := BuildOverview(d, req.Filter, st, start, end, now)
	rep := Report{Title: req.Title, Currency: st.BaseCurrency, Start: ov.Start, End: ov.End, Mode: mode, GroupBy: group,
		Total: ov.Total, PrevTotal: ov.PrevTotal, DeltaPct: ov.DeltaPct, Lines: []ReportLine{}, Notes: append([]string{}, ov.Notes...),
		Budgets: budgets, Anomalies: []Anomaly{}, Findings: []Finding{}, Series: ov.Series, SeriesKeys: ov.SeriesKeys}
	if rep.Title == "" {
		kind := "Showback"
		if mode == "chargeback" {
			kind = "Chargeback"
		}
		rep.Title = fmt.Sprintf("%s de custos de nuvem — %s a %s", kind, fmtDate(start), fmtDate(end.AddDate(0, 0, -1)))
	}
	buckets := ov.Breakdowns[group]
	if buckets == nil {
		rep.Notes = append(rep.Notes, "Sem dados de "+dimLabel(group)+" no período.")
	}
	var tot float64
	var unalloc float64
	for _, b := range buckets {
		tot += b.Cost
		if b.Key == Untagged || b.Key == "(sem equipe)" || b.Key == "(vazio)" {
			unalloc += b.Cost
		}
	}
	rep.Unallocated = round2(unalloc)
	allocated := tot - unalloc
	for _, b := range buckets {
		l := ReportLine{Key: b.Key, Direct: b.Cost, Total: b.Cost, Prev: b.Prev, DeltaPct: b.DeltaPct}
		isUn := b.Key == Untagged || b.Key == "(sem equipe)" || b.Key == "(vazio)"
		if req.Distribute && mode == "chargeback" && allocated > 0 {
			if isUn {
				continue
			}
			l.Shared = round2(unalloc * b.Cost / allocated)
			l.Total = round2(b.Cost + l.Shared)
		}
		rep.Lines = append(rep.Lines, l)
	}
	for i := range rep.Lines {
		if tot > 0 {
			rep.Lines[i].Share = round2(rep.Lines[i].Total / tot * 100)
		}
	}
	sort.Slice(rep.Lines, func(i, j int) bool { return rep.Lines[i].Total > rep.Lines[j].Total })
	if mode == "chargeback" && req.Distribute && unalloc > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("Custo sem alocação (%s) rateado proporcionalmente ao custo direto de cada %s.", money(unalloc, rep.Currency), dimLabel(group)))
	}
	// maiores aumentos por serviço
	for _, b := range ov.Breakdowns["service"] {
		if b.Cost-b.Prev > 0.5 {
			rep.Increases = append(rep.Increases, Bucket{Key: b.Key, Cost: b.Cost, Prev: b.Prev, DeltaPct: b.DeltaPct, Share: round2(b.Cost - b.Prev)})
		}
	}
	sort.Slice(rep.Increases, func(i, j int) bool { return rep.Increases[i].Share > rep.Increases[j].Share })
	if len(rep.Increases) > 5 {
		rep.Increases = rep.Increases[:5]
	}
	for _, a := range anomalies {
		ad, _ := time.Parse(dayFmt, a.Day)
		if !ad.Before(start) && ad.Before(end) && a.Scope != "account" {
			rep.Anomalies = append(rep.Anomalies, a)
		}
	}
	if len(rep.Anomalies) > 10 {
		rep.Anomalies = rep.Anomalies[:10]
	}
	for _, f := range findings {
		if f.Dismissed {
			continue
		}
		rep.Savings += savingsOf(f)
		if len(rep.Findings) < 10 && f.MonthlySavings != nil {
			rep.Findings = append(rep.Findings, f)
		}
	}
	rep.Savings = round2(rep.Savings)
	rep.Forecast = ForecastFor(d, req.Filter, st, now, 0)
	rep.Markdown = rep.markdown()
	rep.CSV = rep.csv()
	return rep, nil
}

func fmtDate(t time.Time) string { return t.Format("02/01/2006") }

func brMoney(v float64, cur string) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatFloat(round2(v), 'f', 2, 64)
	intp, dec, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range intp {
		if i > 0 && (len(intp)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	out := cur + " " + b.String() + "," + dec
	if neg {
		out = "-" + out
	}
	return out
}

func pctStr(p *float64) string {
	if p == nil {
		return "—"
	}
	s := strconv.FormatFloat(*p, 'f', 1, 64)
	if *p > 0 {
		s = "+" + s
	}
	return strings.Replace(s, ".", ",", 1) + "%"
}

func (r Report) markdown() string {
	var b strings.Builder
	cur := r.Currency
	fmt.Fprintf(&b, "# %s\n\n", r.Title)
	s, _ := time.Parse(dayFmt, r.Start)
	e, _ := time.Parse(dayFmt, r.End)
	fmt.Fprintf(&b, "Período: **%s a %s** · Moeda: **%s** · Gerado pelo doomctl em %s\n\n", fmtDate(s), fmtDate(e.AddDate(0, 0, -1)), cur, time.Now().Format("02/01/2006 15:04"))
	b.WriteString("## Resumo\n\n| Indicador | Valor |\n|---|---:|\n")
	fmt.Fprintf(&b, "| Custo no período | %s |\n| Período anterior | %s |\n| Variação | %s |\n", brMoney(r.Total, cur), brMoney(r.PrevTotal, cur), pctStr(r.DeltaPct))
	if r.Forecast.EOM > 0 {
		fmt.Fprintf(&b, "| Previsão de fechamento do mês atual | %s (%s a %s) |\n", brMoney(r.Forecast.EOM, cur), brMoney(r.Forecast.EOMLow, cur), brMoney(r.Forecast.EOMHigh, cur))
	}
	if r.Savings > 0 {
		fmt.Fprintf(&b, "| Economia potencial identificada | %s/mês |\n", brMoney(r.Savings, cur))
	}
	if r.Unallocated > 0 {
		fmt.Fprintf(&b, "| Custo sem alocação (sem tag) | %s |\n", brMoney(r.Unallocated, cur))
	}
	b.WriteString("\n")
	title := "Showback"
	if r.Mode == "chargeback" {
		title = "Chargeback"
	}
	fmt.Fprintf(&b, "## %s por %s\n\n", title, dimLabel(r.GroupBy))
	if r.Mode == "chargeback" {
		b.WriteString("| " + strings.ToUpper(dimLabel(r.GroupBy)[:1]) + dimLabel(r.GroupBy)[1:] + " | Custo direto | Rateio | Total a cobrar | % | Período anterior | Variação |\n|---|---:|---:|---:|---:|---:|---:|\n")
		for _, l := range r.Lines {
			fmt.Fprintf(&b, "| %s | %s | %s | **%s** | %s%% | %s | %s |\n", mdEsc(l.Key), brMoney(l.Direct, cur), brMoney(l.Shared, cur), brMoney(l.Total, cur),
				strings.Replace(strconv.FormatFloat(l.Share, 'f', 1, 64), ".", ",", 1), brMoney(l.Prev, cur), pctStr(l.DeltaPct))
		}
	} else {
		b.WriteString("| " + strings.ToUpper(dimLabel(r.GroupBy)[:1]) + dimLabel(r.GroupBy)[1:] + " | Custo | % | Período anterior | Variação |\n|---|---:|---:|---:|---:|\n")
		for _, l := range r.Lines {
			fmt.Fprintf(&b, "| %s | %s | %s%% | %s | %s |\n", mdEsc(l.Key), brMoney(l.Total, cur), strings.Replace(strconv.FormatFloat(l.Share, 'f', 1, 64), ".", ",", 1), brMoney(l.Prev, cur), pctStr(l.DeltaPct))
		}
	}
	if len(r.Increases) > 0 {
		b.WriteString("\n## Maiores aumentos por serviço\n\n| Serviço | Anterior | Atual | Aumento |\n|---|---:|---:|---:|\n")
		for _, x := range r.Increases {
			fmt.Fprintf(&b, "| %s | %s | %s | %s (%s) |\n", mdEsc(x.Key), brMoney(x.Prev, cur), brMoney(x.Cost, cur), brMoney(x.Share, cur), pctStr(x.DeltaPct))
		}
	}
	if len(r.Budgets) > 0 {
		b.WriteString("\n## Orçamentos (mês atual)\n\n| Orçamento | Limite | Gasto | Previsão | Situação |\n|---|---:|---:|---:|---|\n")
		for _, x := range r.Budgets {
			fmt.Fprintf(&b, "| %s | %s | %s (%.0f%%) | %s (%.0f%%) | %s |\n", mdEsc(x.Name), brMoney(x.AmountBase, cur), brMoney(x.Actual, cur), x.PctActual,
				brMoney(x.Forecast, cur), x.PctForecast, budgetLabel(x.Status))
		}
	}
	if len(r.Anomalies) > 0 {
		b.WriteString("\n## Anomalias no período\n\n")
		for _, a := range r.Anomalies {
			b.WriteString("- " + a.Message() + "\n")
		}
	}
	if len(r.Findings) > 0 {
		b.WriteString("\n## Principais oportunidades de economia\n\n| Recomendação | Recurso | Economia/mês |\n|---|---|---:|\n")
		for _, f := range r.Findings {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", mdEsc(f.Title), mdEsc(f.Name), brMoney(savingsOf(f), cur))
		}
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n## Observações\n\n")
		for _, n := range r.Notes {
			b.WriteString("- " + n + "\n")
		}
	}
	b.WriteString("\n---\n*Valores estimados a partir das APIs de custo dos provedores e do catálogo de preços configurado; a fatura oficial prevalece.*\n")
	return b.String()
}

func budgetLabel(s string) string {
	switch s {
	case "exceeded":
		return "🔴 excedido"
	case "warning":
		return "🟠 alerta"
	case "forecast_exceeds":
		return "🟡 previsão acima"
	case "no_rate":
		return "sem câmbio"
	}
	return "🟢 ok"
}

func mdEsc(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func (r Report) csv() string {
	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	f := func(v float64) string {
		return strings.Replace(strconv.FormatFloat(round2(v), 'f', 2, 64), ".", ",", 1)
	}
	w.Write([]string{dimLabel(r.GroupBy), "custo_direto", "rateio", "total", "percentual", "periodo_anterior", "variacao_pct", "moeda", "inicio", "fim"})
	for _, l := range r.Lines {
		dp := ""
		if l.DeltaPct != nil {
			dp = f(*l.DeltaPct)
		}
		w.Write([]string{l.Key, f(l.Direct), f(l.Shared), f(l.Total), f(l.Share), f(l.Prev), dp, r.Currency, r.Start, r.End})
	}
	w.Flush()
	return buf.String()
}
