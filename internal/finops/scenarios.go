package finops

import (
	"sort"
	"strings"
	"time"
)

// ScenarioItem é uma linha de um cenário de custo.
// Usage: horas/mês para preços por hora (0 = 730), GB para gb-month, unidades para "unit".
type ScenarioItem struct {
	Label    string  `json:"label"`
	Provider string  `json:"provider"`
	Region   string  `json:"region"`
	SKU      string  `json:"sku"`
	Qty      float64 `json:"qty"`
	Usage    float64 `json:"usage"`
}

// Scenario compara duas configurações (A = atual, B = proposta).
type Scenario struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	A           []ScenarioItem `json:"a"`
	B           []ScenarioItem `json:"b"`
	CreatedAt   time.Time      `json:"created_at"`
}

type ScenarioLine struct {
	ScenarioItem
	Unit      string   `json:"unit"`
	UnitPrice *float64 `json:"unit_price"`
	Monthly   *float64 `json:"monthly"`
	Note      string   `json:"note,omitempty"`
}

type ScenarioPrice struct {
	Total   float64        `json:"total"`
	Lines   []ScenarioLine `json:"lines"`
	Missing int            `json:"missing"`
}

type ScenarioResult struct {
	Currency string        `json:"currency"`
	A        ScenarioPrice `json:"a"`
	B        ScenarioPrice `json:"b"`
	Delta    float64       `json:"delta"`
	DeltaPct *float64      `json:"delta_pct"`
}

// PriceItems calcula o custo mensal de uma lista de itens.
func PriceItems(items []ScenarioItem, prices *Prices, st Settings) ScenarioPrice {
	conv := st.Converter()
	out := ScenarioPrice{Lines: []ScenarioLine{}}
	for _, it := range items {
		ln := ScenarioLine{ScenarioItem: it}
		region := firstNonEmpty(it.Region, "*")
		p, ok := prices.Get(lower(it.Provider), region, strings.TrimSpace(it.SKU))
		if !ok {
			ln.Note = "sem preço no catálogo"
			out.Missing++
			out.Lines = append(out.Lines, ln)
			continue
		}
		v, ok := conv.To(p.Price, p.Currency)
		if !ok {
			ln.Note = "sem taxa de câmbio para " + p.Currency
			out.Missing++
			out.Lines = append(out.Lines, ln)
			continue
		}
		ln.Unit, ln.UnitPrice = p.Unit, f64(v)
		qty := it.Qty
		if qty <= 0 {
			qty = 1
		}
		var m float64
		switch p.Unit {
		case "hour", "gb-hour":
			h := it.Usage
			if h <= 0 {
				h = HoursPerMonth
			}
			if h > 744 {
				h = 744
			}
			m = v * h * qty
		case "gb-month":
			gb := it.Usage
			if gb <= 0 {
				gb = 1
				ln.Note = "informe o volume em GB no campo uso"
			}
			m = v * gb * qty
		case "unit":
			u := it.Usage
			if u <= 0 {
				u = 1
			}
			m = v * u * qty
		default:
			m = v * qty
		}
		ln.Monthly = f64(round2(m))
		out.Total += m
		out.Lines = append(out.Lines, ln)
	}
	out.Total = round2(out.Total)
	return out
}

// CompareScenario precifica A e B.
func CompareScenario(sc Scenario, prices *Prices, st Settings) ScenarioResult {
	r := ScenarioResult{Currency: st.BaseCurrency, A: PriceItems(sc.A, prices, st), B: PriceItems(sc.B, prices, st)}
	r.Delta = round2(r.B.Total - r.A.Total)
	r.DeltaPct = pct(r.B.Total, r.A.Total)
	return r
}

// CompareRegions precifica os mesmos itens em várias regiões.
func CompareRegions(items []ScenarioItem, regions []string, prices *Prices, st Settings) map[string]ScenarioPrice {
	out := map[string]ScenarioPrice{}
	for _, reg := range regions {
		reg = strings.TrimSpace(reg)
		if reg == "" {
			continue
		}
		cp := make([]ScenarioItem, len(items))
		for i, it := range items {
			it.Region = reg
			cp[i] = it
		}
		out[reg] = PriceItems(cp, prices, st)
	}
	return out
}

// ---------- what-if sobre o gasto atual ----------

type WhatIfLine struct {
	Service  string  `json:"service"`
	Base     float64 `json:"base"`
	Pct      float64 `json:"pct"`
	Adjusted float64 `json:"adjusted"`
}

type WhatIf struct {
	Currency string       `json:"currency"`
	Base     float64      `json:"base"`
	Adjusted float64      `json:"adjusted"`
	Delta    float64      `json:"delta"`
	Lines    []WhatIfLine `json:"lines"`
}

// WhatIfSpend aplica variações percentuais por serviço (e global) ao gasto dos
// últimos 30 dias, projetando o custo mensal.
func WhatIfSpend(d Data, st Settings, now time.Time, adj map[string]float64, global float64) WhatIf {
	today := Day(now)
	start := today.AddDate(0, 0, -30)
	missing := map[string]bool{}
	cf := costFacts(d.Costs, Filter{}, st.Converter(), missing)
	by := map[string]float64{}
	for _, x := range cf {
		if !x.day.Before(start) && x.day.Before(today) {
			by[x.service] += x.cost
		}
	}
	w := WhatIf{Currency: st.BaseCurrency, Lines: []WhatIfLine{}}
	for svc, v := range by {
		p := global
		for k, a := range adj {
			if strings.EqualFold(k, svc) {
				p = a
			}
		}
		l := WhatIfLine{Service: svc, Base: round2(v), Pct: p, Adjusted: round2(v * (1 + p/100))}
		if l.Adjusted < 0 {
			l.Adjusted = 0
		}
		w.Base += l.Base
		w.Adjusted += l.Adjusted
		w.Lines = append(w.Lines, l)
	}
	sort.Slice(w.Lines, func(i, j int) bool { return w.Lines[i].Base > w.Lines[j].Base })
	w.Base, w.Adjusted = round2(w.Base), round2(w.Adjusted)
	w.Delta = round2(w.Adjusted - w.Base)
	return w
}
