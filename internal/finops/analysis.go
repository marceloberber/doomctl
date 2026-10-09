package finops

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const dayFmt = "2006-01-02"

func dayKey(t time.Time) string { return t.UTC().Format(dayFmt) }

// Day trunca para a meia-noite UTC.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

// Untagged é o rótulo exibido para custo sem a tag/dimensão.
const Untagged = "(sem tag)"

// ---------- fatos normalizados ----------

// fact é uma linha de custo já convertida para a moeda base.
type fact struct {
	day                                     time.Time
	cost                                    float64
	source                                  int64
	provider, account, service, region, val string
}

// Data reúne as linhas carregadas do banco para as análises.
type Data struct {
	Costs []CostRow
	Alloc []AllocRow
	Usage []UsageRow
}

// Filter restringe as análises. Project/Environment/Team usam as linhas de alocação.
type Filter struct {
	SourceID    int64  `json:"source_id"`
	Provider    string `json:"provider"`
	Account     string `json:"account"`
	Service     string `json:"service"`
	Region      string `json:"region"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Team        string `json:"team"`
}

func (f Filter) allocDim() (string, string) {
	switch {
	case f.Project != "":
		return "project", f.Project
	case f.Environment != "":
		return "environment", f.Environment
	case f.Team != "":
		return "team", f.Team
	}
	return "", ""
}

func eqFold(filter, v string) bool { return filter == "" || strings.EqualFold(filter, v) }

func tagValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return Untagged
	}
	return v
}

// costFacts converte as linhas de custo (com filtro) para a moeda base.
func costFacts(rows []CostRow, f Filter, conv Converter, missing map[string]bool) []fact {
	out := make([]fact, 0, len(rows))
	for _, r := range rows {
		if (f.SourceID != 0 && r.SourceID != f.SourceID) || !eqFold(f.Provider, r.Provider) || !eqFold(f.Account, r.Account) ||
			!eqFold(f.Service, r.Service) || !eqFold(f.Region, r.Region) {
			continue
		}
		v, ok := conv.To(r.Cost, r.Currency)
		if !ok {
			missing[strings.ToUpper(r.Currency)] = true
			continue
		}
		out = append(out, fact{day: Day(r.Day), cost: v, source: r.SourceID, provider: r.Provider, account: r.Account, service: r.Service, region: r.Region})
	}
	return out
}

// TeamRows deriva a dimensão "team": usa linhas de equipe quando a fonte as possui
// (ex.: CSV com coluna team); caso contrário aplica as regras de alocação às linhas de projeto.
func TeamRows(alloc []AllocRow, st Settings) []AllocRow {
	hasTeam := map[int64]bool{}
	for _, a := range alloc {
		if a.Dim == "team" {
			hasTeam[a.SourceID] = true
		}
	}
	var out []AllocRow
	for _, a := range alloc {
		switch {
		case a.Dim == "team":
			out = append(out, a)
		case a.Dim == "project" && !hasTeam[a.SourceID]:
			t := a
			t.Dim = "team"
			t.Value = st.TeamFor(a.Value, a.Service)
			out = append(out, t)
		}
	}
	return out
}

// allocFacts devolve as linhas de alocação da dimensão (com filtro).
func allocFacts(alloc []AllocRow, dim, value string, f Filter, st Settings, missing map[string]bool) []fact {
	rows := alloc
	if dim == "team" {
		rows = TeamRows(alloc, st)
	}
	conv := st.Converter()
	var out []fact
	for _, r := range rows {
		if r.Dim != dim || (f.SourceID != 0 && r.SourceID != f.SourceID) || !eqFold(f.Provider, r.Provider) || !eqFold(f.Service, r.Service) {
			continue
		}
		if value != "" && !strings.EqualFold(tagValue(r.Value), value) && !strings.EqualFold(r.Value, value) {
			continue
		}
		v, ok := conv.To(r.Cost, r.Currency)
		if !ok {
			missing[strings.ToUpper(r.Currency)] = true
			continue
		}
		out = append(out, fact{day: Day(r.Day), cost: v, source: r.SourceID, provider: r.Provider, service: r.Service, val: tagValue(r.Value)})
	}
	return out
}

// baseFacts devolve os fatos que representam o "total" sob o filtro.
func baseFacts(d Data, f Filter, st Settings, missing map[string]bool) ([]fact, bool) {
	if dim, val := f.allocDim(); dim != "" {
		return allocFacts(d.Alloc, dim, val, f, st, missing), true
	}
	return costFacts(d.Costs, f, st.Converter(), missing), false
}

func dailyOf(facts []fact) map[string]float64 {
	m := map[string]float64{}
	for _, x := range facts {
		m[dayKey(x.day)] += x.cost
	}
	return m
}

func sumRange(facts []fact, start, end time.Time) float64 {
	var s float64
	for _, x := range facts {
		if !x.day.Before(start) && x.day.Before(end) {
			s += x.cost
		}
	}
	return s
}

func missingList(m map[string]bool) []string {
	var out []string
	for k := range m {
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- visão geral ----------

type Bucket struct {
	Key      string   `json:"key"`
	Cost     float64  `json:"cost"`
	Prev     float64  `json:"prev"`
	DeltaPct *float64 `json:"delta_pct"`
	Share    float64  `json:"share"`
}

type SeriesPoint struct {
	Day   string             `json:"day"`
	Total float64            `json:"total"`
	Parts map[string]float64 `json:"parts"`
}

type Overview struct {
	Currency    string              `json:"currency"`
	Start       string              `json:"start"`
	End         string              `json:"end"` // exclusivo
	Days        int                 `json:"days"`
	Total       float64             `json:"total"`
	PrevTotal   float64             `json:"prev_total"`
	DeltaPct    *float64            `json:"delta_pct"`
	DailyAvg    float64             `json:"daily_avg"`
	MTD         float64             `json:"mtd"`
	LastMonth   float64             `json:"last_month"`
	ForecastEOM float64             `json:"forecast_eom"`
	Series      []SeriesPoint       `json:"series"`
	SeriesKeys  []string            `json:"series_keys"`
	Breakdowns  map[string][]Bucket `json:"breakdowns"`
	TagCoverage map[string]float64  `json:"tag_coverage"`
	Unconverted []string            `json:"unconverted"`
	Notes       []string            `json:"notes"`
	HasData     bool                `json:"has_data"`
	FirstDay    string              `json:"first_day"`
	LastDay     string              `json:"last_day"`
}

func pct(cur, prev float64) *float64 {
	if prev <= 0.0001 {
		return nil
	}
	v := round2((cur - prev) / prev * 100)
	return &v
}

func bucketize(facts []fact, key func(fact) string, start, end, prevStart time.Time, total float64, limit int) []Bucket {
	cur, prev := map[string]float64{}, map[string]float64{}
	for _, x := range facts {
		k := key(x)
		if k == "" {
			k = "(vazio)"
		}
		switch {
		case !x.day.Before(start) && x.day.Before(end):
			cur[k] += x.cost
		case !x.day.Before(prevStart) && x.day.Before(start):
			prev[k] += x.cost
		}
	}
	var out []Bucket
	for k, v := range cur {
		b := Bucket{Key: k, Cost: round2(v), Prev: round2(prev[k]), DeltaPct: pct(v, prev[k])}
		if total > 0 {
			b.Share = round2(v / total * 100)
		}
		out = append(out, b)
	}
	for k, v := range prev {
		if _, ok := cur[k]; !ok && v > 0.005 {
			out = append(out, Bucket{Key: k, Prev: round2(v), DeltaPct: f64(-100)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Prev > out[j].Prev
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// BuildOverview calcula totais, comparação com o período anterior, série diária
// empilhada pelos principais serviços e quebras por dimensão.
func BuildOverview(d Data, f Filter, st Settings, start, end, now time.Time) Overview {
	start, end, today := Day(start), Day(end), Day(now)
	days := int(end.Sub(start).Hours() / 24)
	if days < 1 {
		days = 1
		end = start.AddDate(0, 0, 1)
	}
	prevStart := start.AddDate(0, 0, -days)
	missing := map[string]bool{}
	base, allocMode := baseFacts(d, f, st, missing)
	o := Overview{Currency: st.BaseCurrency, Start: dayKey(start), End: dayKey(end), Days: days, Breakdowns: map[string][]Bucket{},
		TagCoverage: map[string]float64{}, Notes: []string{}}
	o.Total = round2(sumRange(base, start, end))
	o.PrevTotal = round2(sumRange(base, prevStart, start))
	o.DeltaPct = pct(o.Total, o.PrevTotal)
	o.DailyAvg = round2(o.Total / float64(days))
	ms := monthStart(today)
	o.MTD = round2(sumRange(base, ms, today))
	o.LastMonth = round2(sumRange(base, ms.AddDate(0, -1, 0), ms))
	fc := ForecastDaily(dailyOf(base), now, 0)
	o.ForecastEOM = fc.EOM
	for _, x := range base {
		k := dayKey(x.day)
		if o.FirstDay == "" || k < o.FirstDay {
			o.FirstDay = k
		}
		if k > o.LastDay {
			o.LastDay = k
		}
	}
	o.HasData = len(base) > 0

	// série empilhada pelos 5 maiores serviços do período
	svc := bucketize(base, func(x fact) string { return x.service }, start, end, prevStart, o.Total, 0)
	top := map[string]bool{}
	for i := 0; i < len(svc) && i < 5; i++ {
		if svc[i].Cost > 0 {
			top[svc[i].Key] = true
			o.SeriesKeys = append(o.SeriesKeys, svc[i].Key)
		}
	}
	if len(svc) > len(o.SeriesKeys) {
		o.SeriesKeys = append(o.SeriesKeys, "Outros")
	}
	byDay := map[string]*SeriesPoint{}
	for t := start; t.Before(end); t = t.AddDate(0, 0, 1) {
		p := &SeriesPoint{Day: dayKey(t), Parts: map[string]float64{}}
		byDay[p.Day] = p
	}
	for _, x := range base {
		p := byDay[dayKey(x.day)]
		if p == nil {
			continue
		}
		k := x.service
		if !top[k] {
			k = "Outros"
		}
		p.Parts[k] += x.cost
		p.Total += x.cost
	}
	for t := start; t.Before(end); t = t.AddDate(0, 0, 1) {
		p := byDay[dayKey(t)]
		for k, v := range p.Parts {
			p.Parts[k] = round2(v)
		}
		p.Total = round2(p.Total)
		o.Series = append(o.Series, *p)
	}

	o.Breakdowns["service"] = svc
	o.Breakdowns["provider"] = bucketize(base, func(x fact) string { return strings.ToUpper(x.provider) }, start, end, prevStart, o.Total, 0)
	if allocMode {
		dim, _ := f.allocDim()
		o.Notes = append(o.Notes, "Filtro por "+dimLabel(dim)+": os valores vêm da alocação por tags; quebras por conta e região não se aplicam.")
	} else {
		o.Breakdowns["account"] = bucketize(base, func(x fact) string { return x.account }, start, end, prevStart, o.Total, 0)
		o.Breakdowns["region"] = bucketize(base, func(x fact) string { return x.region }, start, end, prevStart, o.Total, 0)
		if f.Account != "" || f.Region != "" {
			o.Notes = append(o.Notes, "A alocação por projeto/ambiente/equipe não pode ser filtrada por conta ou região (as APIs de custo agrupam no máximo duas dimensões); essas quebras consideram apenas os demais filtros.")
		}
		for _, dim := range []string{"project", "environment", "team"} {
			af := allocFacts(d.Alloc, dim, "", f, st, missing)
			if len(af) == 0 {
				continue
			}
			tot := sumRange(af, start, end)
			bs := bucketize(af, func(x fact) string { return x.val }, start, end, prevStart, tot, 0)
			if dim == "team" {
				for i := range bs {
					if bs[i].Key == Untagged {
						bs[i].Key = "(sem equipe)"
					}
				}
			}
			o.Breakdowns[dim] = bs
			if dim != "team" && tot > 0 {
				var un float64
				for _, b := range bs {
					if b.Key == Untagged {
						un = b.Cost
					}
				}
				o.TagCoverage[dim] = round2((1 - un/tot) * 100)
			}
		}
	}
	o.Unconverted = missingList(missing)
	if len(o.Unconverted) > 0 {
		o.Notes = append(o.Notes, "Custos em "+strings.Join(o.Unconverted, ", ")+" foram ignorados: cadastre a taxa de câmbio em Fontes & configurações.")
	}
	return o
}

func dimLabel(d string) string {
	switch d {
	case "project":
		return "projeto"
	case "environment":
		return "ambiente"
	case "team":
		return "equipe"
	case "service":
		return "serviço"
	case "region":
		return "região"
	case "account":
		return "conta"
	case "provider":
		return "provedor"
	}
	return d
}

// ---------- estatística ----------

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	idx := p / 100 * float64(len(s)-1)
	lo, hi := int(math.Floor(idx)), int(math.Ceil(idx))
	if lo == hi {
		return s[lo]
	}
	return s[lo] + (s[hi]-s[lo])*(idx-float64(lo))
}

// RobustZ devolve mediana, MAD escalado (≈ desvio-padrão) e o z robusto de x.
func RobustZ(baseline []float64, x float64) (med, sigma, z float64) {
	med = median(baseline)
	dev := make([]float64, len(baseline))
	for i, v := range baseline {
		dev[i] = math.Abs(v - med)
	}
	sigma = 1.4826 * median(dev)
	if sigma < 1e-9 {
		// série praticamente constante: usa 5% da mediana como escala mínima
		sigma = math.Max(math.Abs(med)*0.05, 0.01)
	}
	z = (x - med) / sigma
	if z > 99 {
		z = 99
	}
	return
}

// ---------- anomalias ----------

type Driver struct {
	Name  string  `json:"name"`
	Delta float64 `json:"delta"`
}

type Anomaly struct {
	Key      string   `json:"key"`
	Day      string   `json:"day"`
	Scope    string   `json:"scope"` // total | service | region | account | project | environment
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // spike | new
	Actual   float64  `json:"actual"`
	Expected float64  `json:"expected"`
	Delta    float64  `json:"delta"`
	DeltaPct *float64 `json:"delta_pct"`
	Z        float64  `json:"z"`
	Severity string   `json:"severity"`
	Currency string   `json:"currency"`
	Drivers  []Driver `json:"drivers,omitempty"`
}

func (a Anomaly) Message() string {
	cur := a.Currency
	scope := "Custo total"
	if a.Scope != "total" {
		scope = strings.ToUpper(dimLabel(a.Scope)[:1]) + dimLabel(a.Scope)[1:] + " " + a.Name
	}
	if a.Kind == "new" {
		return scope + ": novo custo de " + money(a.Actual, cur) + " em " + a.Day + " (sem custo nos dias anteriores)"
	}
	p := ""
	if a.DeltaPct != nil {
		p = " (+" + strconv.FormatFloat(*a.DeltaPct, 'f', 0, 64) + "%)"
	}
	return scope + ": " + money(a.Actual, cur) + " em " + a.Day + ", esperado ~" + money(a.Expected, cur) + p
}

func money(v float64, cur string) string { return brMoney(v, cur) }

// DetectAnomalies procura picos nos últimos lookback dias (sem incluir hoje, que é parcial),
// comparando cada dia com a mediana/MAD dos AnomalyWindow dias anteriores.
func DetectAnomalies(d Data, st Settings, now time.Time, lookback int) []Anomaly {
	today := Day(now)
	if lookback <= 0 {
		lookback = 30
	}
	missing := map[string]bool{}
	cf := costFacts(d.Costs, Filter{}, st.Converter(), missing)
	if len(cf) == 0 {
		return []Anomaly{}
	}
	first := today
	for _, x := range cf {
		if x.day.Before(first) {
			first = x.day
		}
	}
	type series struct {
		scope, name string
		daily       map[string]float64
	}
	all := map[string]*series{}
	add := func(scope, name string, x fact) {
		k := scope + "|" + name
		s := all[k]
		if s == nil {
			s = &series{scope: scope, name: name, daily: map[string]float64{}}
			all[k] = s
		}
		s.daily[dayKey(x.day)] += x.cost
	}
	for _, x := range cf {
		add("total", "Total", x)
		add("service", x.service, x)
		if x.region != "" {
			add("region", x.region, x)
		}
		if x.account != "" {
			add("account", x.account, x)
		}
	}
	for _, dim := range []string{"project", "environment"} {
		for _, x := range allocFacts(d.Alloc, dim, "", Filter{}, st, missing) {
			add(dim, x.val, x)
		}
	}
	svcSeries := map[string]map[string]float64{}
	for _, s := range all {
		if s.scope == "service" {
			svcSeries[s.name] = s.daily
		}
	}
	win := st.AnomalyWindow
	var out []Anomaly
	for _, s := range all {
		for i := lookback; i >= 1; i-- {
			day := today.AddDate(0, 0, -i)
			bStart := day.AddDate(0, 0, -win)
			if bStart.Before(first) {
				bStart = first
			}
			nb := int(day.Sub(bStart).Hours() / 24)
			if nb < 7 {
				continue
			}
			var base []float64
			var bsum float64
			for t := bStart; t.Before(day); t = t.AddDate(0, 0, 1) {
				v := s.daily[dayKey(t)]
				base = append(base, v)
				bsum += v
			}
			x := s.daily[dayKey(day)]
			if x < st.AnomalyMinAbs {
				continue
			}
			a := Anomaly{Day: dayKey(day), Scope: s.scope, Name: s.name, Actual: round2(x), Currency: st.BaseCurrency}
			if bsum < 0.005 {
				if s.scope == "total" {
					continue
				}
				a.Kind, a.Expected, a.Delta, a.Z, a.Severity = "new", 0, round2(x), 99, "medium"
				if x >= st.AnomalyMinAbs*10 {
					a.Severity = "high"
				}
			} else {
				med, _, z := RobustZ(base, x)
				if med < 0.005 {
					// custo que começou há poucos dias: já reportado como "novo"
					continue
				}
				delta := x - med
				if delta < st.AnomalyMinAbs || z < st.AnomalyZ || (med > 0 && delta/med*100 < st.AnomalyPct) {
					continue
				}
				a.Kind, a.Expected, a.Delta, a.Z = "spike", round2(med), round2(delta), round2(z)
				a.DeltaPct = pct(x, med)
				p := 1000.0
				if a.DeltaPct != nil {
					p = *a.DeltaPct
				}
				switch {
				case p >= 100 || delta >= st.AnomalyMinAbs*20:
					a.Severity = "high"
				case p >= 60:
					a.Severity = "medium"
				default:
					a.Severity = "low"
				}
			}
			a.Key = "anom|" + a.Scope + "|" + a.Name + "|" + a.Day
			if s.scope == "total" {
				a.Drivers = drivers(svcSeries, day, bStart)
			}
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day > out[j].Day
		}
		return out[i].Delta > out[j].Delta
	})
	if out == nil {
		out = []Anomaly{}
	}
	return out
}

func drivers(svc map[string]map[string]float64, day, bStart time.Time) []Driver {
	var ds []Driver
	for name, daily := range svc {
		var base []float64
		for t := bStart; t.Before(day); t = t.AddDate(0, 0, 1) {
			base = append(base, daily[dayKey(t)])
		}
		delta := daily[dayKey(day)] - median(base)
		if delta > 0.5 {
			ds = append(ds, Driver{Name: name, Delta: round2(delta)})
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].Delta > ds[j].Delta })
	if len(ds) > 3 {
		ds = ds[:3]
	}
	return ds
}

// ---------- previsão ----------

type ForecastPoint struct {
	Day    string   `json:"day"`
	Value  float64  `json:"value"`
	Low    float64  `json:"low"`
	High   float64  `json:"high"`
	Actual *float64 `json:"actual,omitempty"`
}

type Forecast struct {
	Method     string          `json:"method"`
	TrainDays  int             `json:"train_days"`
	History    []ForecastPoint `json:"history"`
	Points     []ForecastPoint `json:"points"`
	MTD        float64         `json:"mtd"`
	EOM        float64         `json:"eom"`
	EOMLow     float64         `json:"eom_low"`
	EOMHigh    float64         `json:"eom_high"`
	RunRateEOM float64         `json:"run_rate_eom"`
	NextMonth  float64         `json:"next_month"`
	SlopeDay   float64         `json:"slope_day"`
	Seasonal   bool            `json:"seasonal"`
}

// ForecastDaily projeta o custo diário com regressão linear e sazonalidade semanal
// (quando há ≥ 28 dias), com intervalo de ~80%. O dia de hoje é tratado como previsão.
// horizon = dias à frente (0 = até o fim do mês seguinte).
func ForecastDaily(daily map[string]float64, now time.Time, horizon int) Forecast {
	today := Day(now)
	ms := monthStart(today)
	nextMs := ms.AddDate(0, 1, 0)
	if horizon <= 0 {
		horizon = int(nextMs.AddDate(0, 1, 0).Sub(today).Hours() / 24)
	}
	// janela de treino: até 56 dias completos com dados
	first := ""
	for k := range daily {
		if k < dayKey(today) && (first == "" || k < first) {
			first = k
		}
	}
	f := Forecast{Method: "sem dados", History: []ForecastPoint{}, Points: []ForecastPoint{}}
	for t := ms; t.Before(today); t = t.AddDate(0, 0, 1) {
		f.MTD += daily[dayKey(t)]
	}
	f.MTD = round2(f.MTD)
	if first == "" {
		return f
	}
	fd, _ := time.Parse(dayFmt, first)
	trainStart := today.AddDate(0, 0, -56)
	if fd.After(trainStart) {
		trainStart = fd
	}
	var ys []float64
	var days []time.Time
	for t := trainStart; t.Before(today); t = t.AddDate(0, 0, 1) {
		ys = append(ys, daily[dayKey(t)])
		days = append(days, t)
	}
	n := len(ys)
	f.TrainDays = n
	if n == 0 {
		return f
	}
	// run-rate (média dos últimos 7 dias)
	var rr float64
	k := 0
	for i := n - 1; i >= 0 && k < 7; i-- {
		rr += ys[i]
		k++
	}
	rr /= float64(k)
	remain := int(nextMs.Sub(today).Hours() / 24)
	f.RunRateEOM = round2(f.MTD + rr*float64(remain))

	predict := func(t float64, dow int) (float64, float64) { return rr, 0 }
	if n >= 7 {
		var sx, sy float64
		for i, y := range ys {
			sx += float64(i)
			sy += y
		}
		mx, my := sx/float64(n), sy/float64(n)
		var sxx, sxy float64
		for i, y := range ys {
			sxx += (float64(i) - mx) * (float64(i) - mx)
			sxy += (float64(i) - mx) * (y - my)
		}
		b := 0.0
		if sxx > 0 {
			b = sxy / sxx
		}
		a := my - b*mx
		factor := [7]float64{1, 1, 1, 1, 1, 1, 1}
		if n >= 28 {
			var sum [7]float64
			var cnt [7]int
			for i, y := range ys {
				fit := a + b*float64(i)
				if fit > 0.01 {
					dw := int(days[i].Weekday())
					sum[dw] += y / fit
					cnt[dw]++
				}
			}
			var tot float64
			ok := true
			for dw := 0; dw < 7; dw++ {
				if cnt[dw] == 0 {
					ok = false
					break
				}
				factor[dw] = sum[dw] / float64(cnt[dw])
				tot += factor[dw]
			}
			if ok && tot > 0 {
				for dw := range factor {
					factor[dw] = factor[dw] * 7 / tot
				}
				f.Seasonal = true
			} else {
				factor = [7]float64{1, 1, 1, 1, 1, 1, 1}
			}
		}
		var ss float64
		for i, y := range ys {
			r := y - (a+b*float64(i))*factor[int(days[i].Weekday())]
			ss += r * r
		}
		sd := 0.0
		if n > 2 {
			sd = math.Sqrt(ss / float64(n-2))
		}
		f.SlopeDay = round2(b)
		f.Method = "regressão linear"
		if f.Seasonal {
			f.Method += " + sazonalidade semanal"
		}
		predict = func(t float64, dow int) (float64, float64) {
			v := (a + b*t) * factor[dow]
			se := sd
			if sxx > 0 {
				se = sd * math.Sqrt(1+1/float64(n)+(t-mx)*(t-mx)/sxx)
			}
			return math.Max(v, 0), 1.2816 * se
		}
	} else {
		f.Method = "média dos últimos dias (histórico curto)"
	}
	for i := 0; i < n; i++ {
		v, _ := predict(float64(i), int(days[i].Weekday()))
		act := round2(ys[i])
		f.History = append(f.History, ForecastPoint{Day: dayKey(days[i]), Value: round2(v), Low: round2(v), High: round2(v), Actual: &act})
	}
	var eom, lo, hi, next float64
	for i := 0; i < horizon; i++ {
		t := today.AddDate(0, 0, i)
		v, e := predict(float64(n+i), int(t.Weekday()))
		p := ForecastPoint{Day: dayKey(t), Value: round2(v), Low: round2(math.Max(v-e, 0)), High: round2(v + e)}
		f.Points = append(f.Points, p)
		switch {
		case t.Before(nextMs):
			eom += v
			lo += math.Max(v-e, 0)
			hi += v + e
		case t.Before(nextMs.AddDate(0, 1, 0)):
			next += v
		}
	}
	f.EOM = round2(f.MTD + eom)
	f.EOMLow = round2(f.MTD + lo)
	f.EOMHigh = round2(f.MTD + hi)
	f.NextMonth = round2(next)
	return f
}

// ForecastFor calcula a previsão sob um filtro.
func ForecastFor(d Data, f Filter, st Settings, now time.Time, horizon int) Forecast {
	missing := map[string]bool{}
	base, _ := baseFacts(d, f, st, missing)
	return ForecastDaily(dailyOf(base), now, horizon)
}

// ---------- orçamentos ----------

type Budget struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	ScopeType  string    `json:"scope_type"` // all | provider | source | account | service | region | project | environment | team
	ScopeValue string    `json:"scope_value"`
	Amount     float64   `json:"amount"`
	Currency   string    `json:"currency"`
	Thresholds []int     `json:"thresholds"`
	Notify     bool      `json:"notify"`
	CreatedAt  time.Time `json:"created_at"`
}

var BudgetScopes = []string{"all", "provider", "source", "account", "service", "region", "project", "environment", "team"}

func (b Budget) Filter() Filter {
	var f Filter
	switch b.ScopeType {
	case "provider":
		f.Provider = b.ScopeValue
	case "source":
		f.SourceID, _ = strconv.ParseInt(b.ScopeValue, 10, 64)
	case "account":
		f.Account = b.ScopeValue
	case "service":
		f.Service = b.ScopeValue
	case "region":
		f.Region = b.ScopeValue
	case "project":
		f.Project = b.ScopeValue
	case "environment":
		f.Environment = b.ScopeValue
	case "team":
		f.Team = b.ScopeValue
	}
	return f
}

type BudgetStatus struct {
	Budget
	AmountBase      float64   `json:"amount_base"`
	Actual          float64   `json:"actual"`
	Forecast        float64   `json:"forecast"`
	PctActual       float64   `json:"pct_actual"`
	PctForecast     float64   `json:"pct_forecast"`
	Status          string    `json:"status"` // ok | warning | exceeded | forecast_exceeds | no_rate
	Crossed         []int     `json:"crossed"`
	ForecastCrossed bool      `json:"forecast_crossed"`
	Cumulative      []float64 `json:"cumulative"`
	BaseCurrency    string    `json:"base_currency"`
}

// EvaluateBudgets calcula gasto do mês (até ontem) e previsão de fechamento por orçamento.
func EvaluateBudgets(budgets []Budget, d Data, st Settings, now time.Time) []BudgetStatus {
	today := Day(now)
	ms := monthStart(today)
	out := []BudgetStatus{}
	for _, b := range budgets {
		bs := BudgetStatus{Budget: b, BaseCurrency: st.BaseCurrency, Crossed: []int{}, Cumulative: []float64{}}
		amt, ok := st.Converter().To(b.Amount, b.Currency)
		if !ok || amt <= 0 {
			bs.Status = "no_rate"
			out = append(out, bs)
			continue
		}
		bs.AmountBase = round2(amt)
		missing := map[string]bool{}
		base, _ := baseFacts(d, b.Filter(), st, missing)
		daily := dailyOf(base)
		fc := ForecastDaily(daily, now, 0)
		bs.Actual, bs.Forecast = fc.MTD, fc.EOM
		var cum float64
		for t := ms; t.Before(today); t = t.AddDate(0, 0, 1) {
			cum += daily[dayKey(t)]
			bs.Cumulative = append(bs.Cumulative, round2(cum))
		}
		bs.PctActual = round2(bs.Actual / amt * 100)
		bs.PctForecast = round2(bs.Forecast / amt * 100)
		bs.Status = "ok"
		th := append([]int(nil), b.Thresholds...)
		sort.Ints(th)
		for _, t := range th {
			if bs.PctActual >= float64(t) {
				bs.Crossed = append(bs.Crossed, t)
			}
		}
		switch {
		case bs.PctActual >= 100:
			bs.Status = "exceeded"
		case len(bs.Crossed) > 0:
			bs.Status = "warning"
		case bs.PctForecast >= 100:
			bs.Status = "forecast_exceeds"
		}
		bs.ForecastCrossed = bs.PctForecast >= 100
		out = append(out, bs)
	}
	return out
}

// ---------- compromissos (Savings Plans / Reservas) ----------

type CommitGroup struct {
	Group          string         `json:"group"`
	Label          string         `json:"label"`
	Services       []string       `json:"services"`
	Days           int            `json:"days"`
	DailyAvg       float64        `json:"daily_avg"`
	DailyP10       float64        `json:"daily_p10"`
	DailyMin       float64        `json:"daily_min"`
	BaselineHourly float64        `json:"baseline_hourly"`
	Daily          []float64      `json:"daily"`
	Options        []CommitOption `json:"options"`
	Recommendation string         `json:"recommendation"`
}

type CommitOption struct {
	Term           string  `json:"term"`
	Discount       float64 `json:"discount"`
	HourlyCommit   float64 `json:"hourly_commit"`
	MonthlySavings float64 `json:"monthly_savings"`
}

type CommitAnalysis struct {
	Currency string        `json:"currency"`
	Groups   []CommitGroup `json:"groups"`
	Existing []string      `json:"existing"`
	Notes    []string      `json:"notes"`
}

// CommitClass classifica serviços elegíveis a compromissos de uso.
func CommitClass(provider, service string) string {
	s := lower(service)
	switch {
	case strings.Contains(s, "savings plan") || strings.Contains(s, "reserv"):
		return "existing"
	case provider == "oci" && (s == "compute" || strings.HasPrefix(s, "compute")):
		return "oci_compute"
	case strings.Contains(s, "elastic compute cloud - compute") || s == "amazon ec2" || s == "ec2" ||
		strings.Contains(s, "lambda") || strings.Contains(s, "fargate") || strings.Contains(s, "elastic container service"):
		return "aws_savings_plans"
	case strings.Contains(s, "relational database") || s == "amazon rds" || strings.Contains(s, "elasticache") ||
		strings.Contains(s, "opensearch") || strings.Contains(s, "elasticsearch") || strings.Contains(s, "redshift"):
		return "aws_reservations"
	}
	return ""
}

// AnalyzeCommitments estima a base estável de consumo (percentil 10 do gasto diário nos
// últimos 30 dias) e a economia com compromissos de 1 e 3 anos, usando as premissas
// de desconto configuradas.
func AnalyzeCommitments(d Data, st Settings, now time.Time) CommitAnalysis {
	today := Day(now)
	start := today.AddDate(0, 0, -30)
	ca := CommitAnalysis{Currency: st.BaseCurrency, Groups: []CommitGroup{}, Existing: []string{}, Notes: []string{
		"Estimativa baseada no percentil 10 do gasto diário dos últimos 30 dias (base estável). Descontos são premissas configuráveis — confirme os valores nas recomendações do AWS Cost Explorer ou na proposta comercial da Oracle antes de contratar.",
	}}
	missing := map[string]bool{}
	cf := costFacts(d.Costs, Filter{}, st.Converter(), missing)
	labels := map[string]string{
		"aws_savings_plans": "AWS Compute Savings Plans (EC2, Fargate, Lambda)",
		"aws_reservations":  "AWS Reservas (RDS, ElastiCache, OpenSearch, Redshift)",
		"oci_compute":       "OCI — compromisso anual (Universal Credits) para Compute",
	}
	daily := map[string]map[string]float64{}
	svcs := map[string]map[string]bool{}
	ex := map[string]bool{}
	for _, x := range cf {
		if x.day.Before(start) || !x.day.Before(today) {
			continue
		}
		g := CommitClass(x.provider, x.service)
		if g == "" {
			continue
		}
		if g == "existing" {
			ex[x.service] = true
			continue
		}
		if daily[g] == nil {
			daily[g], svcs[g] = map[string]float64{}, map[string]bool{}
		}
		daily[g][dayKey(x.day)] += x.cost
		svcs[g][x.service] = true
	}
	for s := range ex {
		ca.Existing = append(ca.Existing, s)
	}
	sort.Strings(ca.Existing)
	if len(ca.Existing) > 0 {
		ca.Notes = append(ca.Notes, "Já existem cobranças de compromissos ("+strings.Join(ca.Existing, ", ")+"); a base abaixo considera apenas o gasto sob demanda restante.")
	}
	for _, g := range []string{"aws_savings_plans", "aws_reservations", "oci_compute"} {
		m := daily[g]
		if m == nil {
			continue
		}
		cg := CommitGroup{Group: g, Label: labels[g], Daily: []float64{}, Options: []CommitOption{}}
		var vals []float64
		for t := start; t.Before(today); t = t.AddDate(0, 0, 1) {
			v := m[dayKey(t)]
			vals = append(vals, v)
			cg.Daily = append(cg.Daily, round2(v))
		}
		for s := range svcs[g] {
			cg.Services = append(cg.Services, s)
		}
		sort.Strings(cg.Services)
		cg.Days = len(vals)
		var sum float64
		for _, v := range vals {
			sum += v
		}
		cg.DailyAvg = round2(sum / float64(len(vals)))
		cg.DailyP10 = round2(percentile(vals, 10))
		cg.DailyMin = round2(percentile(vals, 0))
		cg.BaselineHourly = round2(cg.DailyP10 / 24)
		for _, o := range []struct {
			term string
			d    float64
		}{{"1 ano", st.CommitDiscount1y}, {"3 anos", st.CommitDiscount3y}} {
			hourlyOD := cg.DailyP10 / 24
			cg.Options = append(cg.Options, CommitOption{Term: o.term, Discount: o.d,
				HourlyCommit: math.Round(hourlyOD*(1-o.d)*1000) / 1000, MonthlySavings: round2(hourlyOD * HoursPerMonth * o.d)})
		}
		switch {
		case cg.DailyP10 < 1:
			cg.Recommendation = "Consumo baixo ou instável: compromisso não recomendado no momento."
		case cg.DailyAvg > 0 && cg.DailyP10/cg.DailyAvg < 0.5:
			cg.Recommendation = "Consumo muito variável: comprometa no máximo a base (P10) e reavalie em 30 dias; considere Spot/agendamento para a parte variável."
		default:
			cg.Recommendation = "Consumo estável: comprometer até a base P10 tem baixo risco de ociosidade do compromisso."
		}
		ca.Groups = append(ca.Groups, cg)
	}
	return ca
}

// ---------- uso: rede e armazenamento ----------

// ClassifyUsage classifica um tipo de uso (AWS USAGE_TYPE / OCI skuName) em categoria.
func ClassifyUsage(provider, service, usage string) string {
	u := lower(usage)
	s := lower(service)
	switch {
	case strings.Contains(u, "natgateway") || strings.Contains(u, "nat gateway"):
		return "net_nat"
	case strings.Contains(u, "datatransfer-out-bytes") || strings.Contains(u, "outbound data transfer") || strings.Contains(u, "cloudfront-out-bytes"):
		return "net_egress"
	case strings.Contains(u, "datatransfer-regional-bytes"):
		return "net_inter_az"
	case strings.Contains(u, "-aws-out-bytes") || strings.Contains(u, "-aws-in-bytes") || strings.Contains(u, "inter-region") || strings.Contains(u, "inter region"):
		return "net_inter_region"
	case strings.Contains(u, "loadbalancerusage") || strings.Contains(u, "lcuusage") || strings.Contains(u, "load balancer"):
		return "net_lb"
	case strings.Contains(u, "publicipv4") || strings.Contains(u, "elasticip"):
		return "net_ipv4"
	case strings.Contains(u, "vpcendpoint"):
		return "net_endpoint"
	case strings.Contains(u, "vpn"):
		return "net_vpn"
	case strings.Contains(u, "timedstorage-int"):
		return "obj_intelligent"
	case strings.Contains(u, "timedstorage-sia") || strings.Contains(u, "timedstorage-zia") || strings.Contains(u, "infrequent access"):
		return "obj_ia"
	case strings.Contains(u, "glacier") || strings.Contains(u, "timedstorage-gda") || strings.Contains(u, "timedstorage-gir") || strings.Contains(u, "archive storage") || strings.Contains(u, "deeparchive"):
		return "obj_archive"
	case strings.HasSuffix(u, "timedstorage-bytehrs") || strings.Contains(u, "object storage - storage") || strings.Contains(u, "standard storage"):
		return "obj_standard"
	case strings.Contains(u, "requests-tier") || strings.Contains(u, "object storage - requests"):
		return "obj_requests"
	case strings.Contains(u, "ebs:snapshotusage") || strings.Contains(u, "snapshot"):
		return "blk_snapshot"
	case strings.Contains(u, "ebs:volumeusage") || strings.Contains(u, "block volume") || strings.Contains(u, "block storage"):
		return "blk_volume"
	case strings.Contains(u, "ebs:volumep-iops") || strings.Contains(u, "ebs:volumep-throughput") || strings.Contains(u, "ebs:volumeiousage") || strings.Contains(u, "performance units"):
		return "blk_performance"
	case strings.Contains(s, "cloudfront"):
		return "net_egress"
	}
	return ""
}

var UsageCategoryLabels = map[string]string{
	"net_nat": "NAT Gateway", "net_egress": "Saída para a internet", "net_inter_az": "Tráfego entre AZs",
	"net_inter_region": "Tráfego entre regiões", "net_lb": "Load balancers", "net_ipv4": "IPv4 públicos",
	"net_endpoint": "VPC endpoints", "net_vpn": "VPN",
	"obj_standard": "Objetos — classe padrão", "obj_ia": "Objetos — acesso infrequente", "obj_intelligent": "Objetos — Intelligent-Tiering",
	"obj_archive": "Objetos — arquivamento", "obj_requests": "Objetos — requisições",
	"blk_volume": "Volumes (bloco)", "blk_snapshot": "Snapshots", "blk_performance": "IOPS/throughput provisionados",
}

type UsageAnalysis struct {
	Currency   string    `json:"currency"`
	Days       int       `json:"days"`
	Network    []Bucket  `json:"network"`
	Storage    []Bucket  `json:"storage"`
	TopNetwork []Bucket  `json:"top_network"`
	TopStorage []Bucket  `json:"top_storage"`
	Findings   []Finding `json:"findings"`
	HasData    bool      `json:"has_data"`
}

// AnalyzeUsage resume custos de rede e armazenamento dos últimos `days` dias
// (comparando com o período anterior) e gera recomendações.
func AnalyzeUsage(rows []UsageRow, st Settings, now time.Time, days int) UsageAnalysis {
	if days <= 0 {
		days = 30
	}
	today := Day(now)
	start := today.AddDate(0, 0, -days)
	prevStart := start.AddDate(0, 0, -days)
	conv := st.Converter()
	var net, sto []fact
	for _, r := range rows {
		cat := r.Category
		if cat == "" {
			cat = ClassifyUsage(r.Provider, r.Service, r.UsageType)
		}
		if cat == "" {
			continue
		}
		v, ok := conv.To(r.Cost, r.Currency)
		if !ok {
			continue
		}
		x := fact{day: Day(r.Day), cost: v, provider: r.Provider, service: r.Service, val: cat, account: r.UsageType}
		if strings.HasPrefix(cat, "net_") {
			net = append(net, x)
		} else {
			sto = append(sto, x)
		}
	}
	ua := UsageAnalysis{Currency: st.BaseCurrency, Days: days, Findings: []Finding{}}
	ua.HasData = len(net)+len(sto) > 0
	label := func(x fact) string {
		if l, ok := UsageCategoryLabels[x.val]; ok {
			return l
		}
		return x.val
	}
	nt := sumRange(net, start, today)
	stt := sumRange(sto, start, today)
	ua.Network = bucketize(net, label, start, today, prevStart, nt, 0)
	ua.Storage = bucketize(sto, label, start, today, prevStart, stt, 0)
	ua.TopNetwork = bucketize(net, func(x fact) string { return x.account }, start, today, prevStart, nt, 10)
	ua.TopStorage = bucketize(sto, func(x fact) string { return x.account }, start, today, prevStart, stt, 10)
	cat := func(facts []fact, c string) float64 {
		var s float64
		for _, x := range facts {
			if x.val == c && !x.day.Before(start) && x.day.Before(today) {
				s += x.cost
			}
		}
		return s * 30 / float64(days)
	}
	cur := st.BaseCurrency
	add := func(rule, sev, title, detail, rec string) {
		ua.Findings = append(ua.Findings, Finding{Key: "usage|" + rule, Rule: rule, Category: "network", Severity: sev, Title: title,
			Detail: detail, Recommendation: rec, Currency: cur, SavingsNote: "economia depende do padrão de tráfego/acesso"})
	}
	if nat := cat(net, "net_nat"); nat >= st.AnomalyMinAbs*4 {
		add("nat_gateway", "medium", "NAT Gateway custa ~"+money(nat, cur)+"/mês",
			"Processamento de dados e horas de NAT Gateway somam "+strconv.FormatFloat(round2(safeDiv(nat, nt*30/float64(days))*100), 'f', 0, 64)+"% do custo de rede.",
			"Crie VPC Gateway Endpoints (S3 e DynamoDB não cobram processamento), use Interface Endpoints para ECR/STS/CloudWatch quando o volume justificar, e evite tráfego entre AZs passando pelo NAT (um NAT por AZ).")
	}
	if az := cat(net, "net_inter_az"); az >= st.AnomalyMinAbs*4 {
		add("inter_az", "low", "Tráfego entre AZs custa ~"+money(az, cur)+"/mês", "Dados trafegando entre zonas de disponibilidade são cobrados nos dois sentidos.",
			"Mantenha serviços muito comunicativos na mesma AZ, use topology-aware routing no Kubernetes e réplicas de leitura locais.")
	}
	if eg := cat(net, "net_egress"); eg >= st.AnomalyMinAbs*4 {
		add("egress", "low", "Saída para a internet custa ~"+money(eg, cur)+"/mês", "Transferência de dados para fora da nuvem.",
			"Use CDN (CloudFront/OCI CDN) com cache, compressão (gzip/brotli) e revise downloads/backup para fora da região.")
	}
	if ir := cat(net, "net_inter_region"); ir >= st.AnomalyMinAbs*4 {
		add("inter_region", "low", "Tráfego entre regiões custa ~"+money(ir, cur)+"/mês", "Replicação ou chamadas entre regiões.",
			"Verifique replicações (S3 CRR, bancos) e chamadas cross-region desnecessárias; concentre workloads na mesma região.")
	}
	stdObj, iaObj, intObj, arcObj := cat(sto, "obj_standard"), cat(sto, "obj_ia"), cat(sto, "obj_intelligent"), cat(sto, "obj_archive")
	if stdObj >= st.AnomalyMinAbs*4 && (iaObj+intObj+arcObj) < stdObj*0.1 {
		f := Finding{Key: "usage|object_lifecycle", Rule: "object_lifecycle", Category: "storage", Severity: "medium",
			Title:          "Objetos quase todos na classe padrão (~" + money(stdObj, cur) + "/mês)",
			Detail:         "Menos de 10% do custo de armazenamento de objetos está em classes mais baratas.",
			Recommendation: "Ative S3 Intelligent-Tiering (ou OCI Auto-Tiering) para dados com acesso imprevisível e regras de lifecycle para mover dados antigos para IA/Glacier/Archive e expirar versões antigas.",
			Currency:       cur, SavingsNote: "estimativa: até 40% se metade dos dados migrar para acesso infrequente"}
		f.MonthlySavings = f64(round2(stdObj * 0.5 * 0.4))
		ua.Findings = append(ua.Findings, f)
	}
	if snap := cat(sto, "blk_snapshot"); snap >= st.AnomalyMinAbs*4 {
		ua.Findings = append(ua.Findings, Finding{Key: "usage|snapshots", Rule: "snapshot_spend", Category: "storage", Severity: "low",
			Title: "Snapshots custam ~" + money(snap, cur) + "/mês", Detail: "Custo recorrente de snapshots de volumes.",
			Recommendation: "Defina políticas de retenção (Amazon Data Lifecycle Manager / políticas de backup da OCI) e use o arquivamento de snapshots para retenção longa.",
			Currency:       cur})
	}
	if perf := cat(sto, "blk_performance"); perf >= st.AnomalyMinAbs*4 {
		ua.Findings = append(ua.Findings, Finding{Key: "usage|provisioned_iops", Rule: "provisioned_iops", Category: "storage", Severity: "low",
			Title: "IOPS/throughput provisionados custam ~" + money(perf, cur) + "/mês", Detail: "Volumes io1/io2 ou gp3 com desempenho acima do padrão.",
			Recommendation: "Compare o IOPS provisionado com o consumo real (CloudWatch VolumeReadOps/VolumeWriteOps); gp3 oferece 3.000 IOPS sem custo adicional.",
			Currency:       cur})
	}
	return ua
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
