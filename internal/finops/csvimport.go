package finops

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func normHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")))
	r := strings.NewReplacer("ç", "c", "ã", "a", "á", "a", "â", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", " ", "_", "-", "_")
	return r.Replace(s)
}

var costCols = map[string][]string{
	"date":        {"date", "day", "data", "dia", "usage_date"},
	"provider":    {"provider", "provedor", "cloud", "nuvem"},
	"account":     {"account", "conta", "account_id", "subscription", "tenancy"},
	"service":     {"service", "servico", "product", "produto"},
	"region":      {"region", "regiao", "location"},
	"project":     {"project", "projeto", "app", "aplicacao", "application"},
	"environment": {"environment", "ambiente", "env", "stage"},
	"team":        {"team", "equipe", "time", "cost_center", "centro_de_custo", "owner"},
	"cost":        {"cost", "custo", "amount", "valor", "total"},
	"currency":    {"currency", "moeda"},
	"usage_type":  {"usage_type", "tipo_de_uso", "tipo_uso", "sku", "sku_name"},
	"usage_qty":   {"usage_qty", "quantidade", "usage_quantity", "qty"},
	"usage_unit":  {"usage_unit", "unidade", "unit"},
}

var invCols = map[string][]string{
	"provider":     {"provider", "provedor"},
	"region":       {"region", "regiao"},
	"resource_id":  {"resource_id", "id", "recurso", "resource"},
	"type":         {"type", "resource_type", "tipo"},
	"name":         {"name", "nome"},
	"sku":          {"sku", "instance_type", "shape", "volume_type", "class", "classe"},
	"size_gb":      {"size_gb", "size", "tamanho_gb", "tamanho"},
	"state":        {"state", "estado", "status"},
	"monthly_cost": {"monthly_cost", "custo_mensal", "cost", "custo"},
	"currency":     {"currency", "moeda"},
	"cpu_avg":      {"cpu_avg", "cpu_media", "cpu"},
	"cpu_max":      {"cpu_max", "cpu_maxima"},
	"mem_avg":      {"mem_avg", "memoria_media", "mem"},
	"attached":     {"attached", "anexado", "associado", "in_use", "em_uso"},
	"age_days":     {"age_days", "idade_dias", "idade"},
	"tags":         {"tags", "labels"},
	"multi_az":     {"multi_az"},
	"storage_type": {"storage_type"},
}

type csvTable struct {
	idx  map[string]int
	rows [][]string
	line []int
}

func readCSV(r io.Reader, cols map[string][]string, required []string) (*csvTable, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	cr := csv.NewReader(strings.NewReader(text))
	if strings.Count(first, ";") > strings.Count(first, ",") {
		cr.Comma = ';'
	} else if strings.Count(first, "\t") > strings.Count(first, ",") {
		cr.Comma = '\t'
	}
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	hdr, err := cr.Read()
	if err != nil {
		return nil, errors.New("CSV vazio ou inválido")
	}
	t := &csvTable{idx: map[string]int{}}
	for i, h := range hdr {
		n := normHeader(h)
		for key, aliases := range cols {
			if _, done := t.idx[key]; done {
				continue
			}
			for _, a := range aliases {
				if n == a {
					t.idx[key] = i
					break
				}
			}
		}
	}
	var miss []string
	for _, r := range required {
		if _, ok := t.idx[r]; !ok {
			miss = append(miss, r)
		}
	}
	if len(miss) > 0 {
		return nil, fmt.Errorf("colunas obrigatórias ausentes: %s (cabeçalho lido: %s)", strings.Join(miss, ", "), strings.Join(hdr, ", "))
	}
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("linha %d: %v", line, err)
		}
		empty := true
		for _, x := range rec {
			if strings.TrimSpace(x) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		t.rows = append(t.rows, rec)
		t.line = append(t.line, line)
		if len(t.rows) > 500000 {
			return nil, errors.New("CSV com mais de 500 mil linhas: divida o arquivo")
		}
	}
	return t, nil
}

func (t *csvTable) get(row []string, key string) string {
	i, ok := t.idx[key]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// ParseNumber aceita "1234.56", "1.234,56", "1,234.56", "1234,56", "R$ 10,00" e "$10".
func ParseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == '.' || r == ',' || r == '-' || r == 'e' || r == 'E' || r == '+' {
			return r
		}
		return -1
	}, s)
	if s == "" {
		return 0, errors.New("vazio")
	}
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case lastDot >= 0 && lastComma >= 0:
		if lastComma > lastDot {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastComma >= 0:
		if strings.Count(s, ",") > 1 {
			s = strings.ReplaceAll(s, ",", "")
		} else {
			s = strings.Replace(s, ",", ".", 1)
		}
	case strings.Count(s, ".") > 1:
		s = strings.ReplaceAll(s, ".", "")
	}
	return strconv.ParseFloat(s, 64)
}

// ParseDate aceita AAAA-MM-DD, DD/MM/AAAA, AAAA/MM/DD e RFC3339.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, f := range []string{"2006-01-02", "02/01/2006", "2006/01/02", time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z", "02-01-2006"} {
		if t, err := time.Parse(f, s); err == nil {
			return Day(t), nil
		}
	}
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return Day(t), nil
		}
	}
	return time.Time{}, fmt.Errorf("data inválida %q", s)
}

func parseBool(s string) (bool, bool) {
	switch lower(s) {
	case "true", "1", "yes", "sim", "s", "y", "verdadeiro":
		return true, true
	case "false", "0", "no", "nao", "não", "n", "falso":
		return false, true
	}
	return false, false
}

// ParseTags lê "k=v;k2=v2" (também aceita "|" e "," como separadores).
func ParseTags(s string) map[string]string {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out
	}
	sep := ";"
	switch {
	case strings.Contains(s, ";"):
	case strings.Contains(s, "|"):
		sep = "|"
	case strings.Contains(s, ","):
		sep = ","
	}
	for _, kv := range strings.Split(s, sep) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			k, v, ok = strings.Cut(kv, ":")
		}
		if ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// CostImport é o resultado da leitura do CSV de custos.
type CostImport struct {
	Rows     []CostRow
	Alloc    []AllocRow
	Usage    []UsageRow
	Errors   []string
	MinDay   time.Time
	MaxDay   time.Time
	Total    float64
	Currency map[string]float64
}

// ParseCostCSV lê um CSV de custos diários. Colunas: date, provider, service, cost
// (obrigatórias) e account, region, project, environment, team, currency,
// usage_type, usage_qty, usage_unit (opcionais).
func ParseCostCSV(r io.Reader, sourceID int64, defProvider, defCurrency string) (CostImport, error) {
	t, err := readCSV(r, costCols, []string{"date", "service", "cost"})
	if err != nil {
		return CostImport{}, err
	}
	imp := CostImport{Currency: map[string]float64{}}
	type ukey struct {
		day                     string
		prov, svc, ut, cur, unt string
	}
	usage := map[ukey]*UsageRow{}
	for i, rec := range t.rows {
		ln := t.line[i]
		d, err := ParseDate(t.get(rec, "date"))
		if err != nil {
			imp.errorf("linha %d: %v", ln, err)
			continue
		}
		c, err := ParseNumber(t.get(rec, "cost"))
		if err != nil {
			imp.errorf("linha %d: custo inválido %q", ln, t.get(rec, "cost"))
			continue
		}
		prov := lower(firstNonEmpty(t.get(rec, "provider"), defProvider, "custom"))
		cur := strings.ToUpper(firstNonEmpty(t.get(rec, "currency"), defCurrency, "USD"))
		if len(cur) != 3 {
			imp.errorf("linha %d: moeda inválida %q", ln, cur)
			continue
		}
		row := CostRow{Day: d, SourceID: sourceID, Provider: prov, Account: t.get(rec, "account"), Service: t.get(rec, "service"),
			Region: t.get(rec, "region"), Project: t.get(rec, "project"), Environment: t.get(rec, "environment"), Team: t.get(rec, "team"),
			Cost: c, Currency: cur, UsageUnit: t.get(rec, "usage_unit")}
		if q := t.get(rec, "usage_qty"); q != "" {
			row.UsageQty, _ = ParseNumber(q)
		}
		if row.Service == "" {
			row.Service = "(sem serviço)"
		}
		imp.Rows = append(imp.Rows, row)
		imp.Total += c
		imp.Currency[cur] += c
		if imp.MinDay.IsZero() || d.Before(imp.MinDay) {
			imp.MinDay = d
		}
		if d.After(imp.MaxDay) {
			imp.MaxDay = d
		}
		if ut := t.get(rec, "usage_type"); ut != "" {
			k := ukey{dayKey(d), prov, row.Service, ut, cur, row.UsageUnit}
			u := usage[k]
			if u == nil {
				u = &UsageRow{Day: d, SourceID: sourceID, Provider: prov, Service: row.Service, UsageType: ut, Currency: cur, Unit: row.UsageUnit,
					Category: ClassifyUsage(prov, row.Service, ut)}
				usage[k] = u
			}
			u.Cost += c
			u.Quantity += row.UsageQty
		}
	}
	if len(imp.Rows) == 0 {
		if len(imp.Errors) > 0 {
			return imp, errors.New("nenhuma linha válida: " + imp.Errors[0])
		}
		return imp, errors.New("nenhuma linha de custo encontrada")
	}
	imp.Alloc = DeriveAlloc(imp.Rows)
	for _, u := range usage {
		imp.Usage = append(imp.Usage, *u)
	}
	imp.Rows = AggregateCosts(imp.Rows)
	return imp, nil
}

func (imp *CostImport) errorf(f string, a ...any) {
	if len(imp.Errors) < 20 {
		imp.Errors = append(imp.Errors, fmt.Sprintf(f, a...))
	}
}

// AggregateCosts soma linhas com as mesmas dimensões (conta, serviço, região, dia, moeda).
// Projeto/ambiente/equipe ficam nas linhas de alocação.
func AggregateCosts(rows []CostRow) []CostRow {
	type key struct {
		day                          string
		src                          int64
		prov, acc, svc, reg, cur, un string
	}
	m := map[key]*CostRow{}
	var order []key
	for _, r := range rows {
		k := key{dayKey(r.Day), r.SourceID, r.Provider, r.Account, r.Service, r.Region, r.Currency, r.UsageUnit}
		x := m[k]
		if x == nil {
			c := r
			c.Project, c.Environment, c.Team, c.Cost, c.UsageQty = "", "", "", 0, 0
			x = &c
			m[k] = x
			order = append(order, k)
		}
		x.Cost += r.Cost
		x.UsageQty += r.UsageQty
	}
	out := make([]CostRow, 0, len(order))
	for _, k := range order {
		out = append(out, *m[k])
	}
	return out
}

// DeriveAlloc gera as linhas de alocação (projeto, ambiente e, se houver, equipe)
// a partir de linhas de custo com essas dimensões.
func DeriveAlloc(rows []CostRow) []AllocRow {
	type key struct {
		day                     string
		src                     int64
		prov, svc, dim, val, cr string
	}
	m := map[key]*AllocRow{}
	hasTeam := map[int64]bool{}
	for _, r := range rows {
		if r.Team != "" {
			hasTeam[r.SourceID] = true
		}
	}
	add := func(r CostRow, dim, val string) {
		k := key{dayKey(r.Day), r.SourceID, r.Provider, r.Service, dim, val, r.Currency}
		x := m[k]
		if x == nil {
			x = &AllocRow{Day: Day(r.Day), SourceID: r.SourceID, Provider: r.Provider, Service: r.Service, Dim: dim, Value: val, Currency: r.Currency}
			m[k] = x
		}
		x.Cost += r.Cost
	}
	for _, r := range rows {
		add(r, "project", r.Project)
		add(r, "environment", r.Environment)
		if hasTeam[r.SourceID] {
			add(r, "team", r.Team)
		}
	}
	out := make([]AllocRow, 0, len(m))
	for _, x := range m {
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out
}

// ParseInventoryCSV lê um CSV de inventário de recursos.
func ParseInventoryCSV(r io.Reader, sourceID int64, defProvider string) ([]Resource, []string, error) {
	t, err := readCSV(r, invCols, []string{"resource_id", "type"})
	if err != nil {
		return nil, nil, err
	}
	var out []Resource
	var errs []string
	validTypes := map[string]bool{"instance": true, "volume": true, "snapshot": true, "public_ip": true, "database": true,
		"load_balancer": true, "nat_gateway": true, "bucket": true, "other": true}
	typeAlias := map[string]string{"vm": "instance", "ec2": "instance", "compute": "instance", "instancia": "instance", "disk": "volume",
		"ebs": "volume", "disco": "volume", "block_volume": "volume", "ip": "public_ip", "eip": "public_ip", "elastic_ip": "public_ip",
		"rds": "database", "db": "database", "banco": "database", "lb": "load_balancer", "alb": "load_balancer", "nlb": "load_balancer",
		"nat": "nat_gateway", "s3": "bucket", "object_storage": "bucket"}
	optF := func(s string) *float64 {
		if s == "" {
			return nil
		}
		v, err := ParseNumber(s)
		if err != nil {
			return nil
		}
		return &v
	}
	for i, rec := range t.rows {
		typ := lower(t.get(rec, "type"))
		if a, ok := typeAlias[typ]; ok {
			typ = a
		}
		if !validTypes[typ] {
			if len(errs) < 20 {
				errs = append(errs, fmt.Sprintf("linha %d: tipo %q desconhecido (use instance, volume, snapshot, public_ip, database, load_balancer, nat_gateway, bucket ou other)", t.line[i], t.get(rec, "type")))
			}
			continue
		}
		res := Resource{SourceID: sourceID, Provider: lower(firstNonEmpty(t.get(rec, "provider"), defProvider, "custom")),
			Region: t.get(rec, "region"), ResourceID: t.get(rec, "resource_id"), Type: typ, Name: t.get(rec, "name"), SKU: t.get(rec, "sku"),
			State: lower(t.get(rec, "state")), Currency: strings.ToUpper(firstNonEmpty(t.get(rec, "currency"), "USD")),
			MonthlyCost: optF(t.get(rec, "monthly_cost")), CPUAvg: optF(t.get(rec, "cpu_avg")), CPUMax: optF(t.get(rec, "cpu_max")),
			MemAvg: optF(t.get(rec, "mem_avg")), Tags: ParseTags(t.get(rec, "tags")), Raw: map[string]any{}}
		if res.ResourceID == "" {
			continue
		}
		if v := optF(t.get(rec, "size_gb")); v != nil {
			res.SizeGB = *v
		}
		if v := optF(t.get(rec, "age_days")); v != nil {
			res.AgeDays = int(*v)
		}
		if b, ok := parseBool(t.get(rec, "attached")); ok {
			res.Attached = &b
		}
		if b, ok := parseBool(t.get(rec, "multi_az")); ok {
			res.Raw["multi_az"] = b
		}
		if s := t.get(rec, "storage_type"); s != "" {
			res.Raw["storage_type"] = s
		}
		if res.Name == "" {
			res.Name = res.Tags["Name"]
		}
		out = append(out, res)
	}
	if len(out) == 0 {
		if len(errs) > 0 {
			return nil, errs, errors.New("nenhum recurso válido: " + errs[0])
		}
		return nil, errs, errors.New("nenhum recurso encontrado")
	}
	return out, errs, nil
}

// ParsePriceCSV lê o catálogo de preços: provider, region, sku, unit, price, currency.
func ParsePriceCSV(r io.Reader) ([]Price, []string, error) {
	cols := map[string][]string{"provider": {"provider", "provedor"}, "region": {"region", "regiao"}, "sku": {"sku"},
		"unit": {"unit", "unidade"}, "price": {"price", "preco", "valor"}, "currency": {"currency", "moeda"}}
	t, err := readCSV(r, cols, []string{"provider", "sku", "unit", "price"})
	if err != nil {
		return nil, nil, err
	}
	var out []Price
	var errs []string
	for i, rec := range t.rows {
		p := Price{Provider: lower(t.get(rec, "provider")), Region: firstNonEmpty(t.get(rec, "region"), "*"), SKU: t.get(rec, "sku"),
			Unit: lower(t.get(rec, "unit")), Currency: strings.ToUpper(firstNonEmpty(t.get(rec, "currency"), "USD")), Source: "csv"}
		v, err := ParseNumber(t.get(rec, "price"))
		if err != nil || p.SKU == "" || !ValidUnit(p.Unit) {
			if len(errs) < 20 {
				errs = append(errs, fmt.Sprintf("linha %d: preço, SKU ou unidade inválidos", t.line[i]))
			}
			continue
		}
		p.Price = v
		out = append(out, p)
	}
	return out, errs, nil
}

func ValidUnit(u string) bool {
	switch u {
	case "hour", "gb-month", "month", "gb-hour", "unit":
		return true
	}
	return false
}
