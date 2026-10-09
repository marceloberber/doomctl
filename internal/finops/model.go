// Package finops implementa a análise de custos de nuvem do doomctl: cost
// analysis, anomalias, orçamentos, previsão, otimização (rightsizing, ociosos,
// desperdício), custo de Kubernetes, estimativa de custo de IaC, políticas,
// tags e relatórios. Integrações AWS (SigV4) e OCI (assinatura de requisição)
// usam somente a biblioteca padrão. O pacote não acessa o banco.
package finops

import (
	"math"
	"net/url"
	"path"
	"strings"
	"time"
)

const HoursPerMonth = 730.0

// CostRow é uma linha de custo normalizada (diária).
type CostRow struct {
	Day         time.Time `json:"day"`
	SourceID    int64     `json:"source_id"`
	Provider    string    `json:"provider"`
	Account     string    `json:"account"`
	Service     string    `json:"service"`
	Region      string    `json:"region"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Team        string    `json:"team"`
	Cost        float64   `json:"cost"`
	Currency    string    `json:"currency"`
	UsageQty    float64   `json:"usage_qty"`
	UsageUnit   string    `json:"usage_unit"`
}

// AllocRow é o custo de um dia distribuído por uma dimensão de alocação (tag),
// mantendo o serviço. Cada dimensão soma o total do dia; valor vazio = sem tag.
type AllocRow struct {
	Day      time.Time `json:"day"`
	SourceID int64     `json:"source_id"`
	Provider string    `json:"provider"`
	Service  string    `json:"service"`
	Dim      string    `json:"dim"` // project | environment | team
	Value    string    `json:"value"`
	Cost     float64   `json:"cost"`
	Currency string    `json:"currency"`
}

// UsageRow é o custo diário por tipo de uso (rede, armazenamento...).
type UsageRow struct {
	Day       time.Time `json:"day"`
	SourceID  int64     `json:"source_id"`
	Provider  string    `json:"provider"`
	Service   string    `json:"service"`
	UsageType string    `json:"usage_type"`
	Category  string    `json:"category"`
	Cost      float64   `json:"cost"`
	Currency  string    `json:"currency"`
	Quantity  float64   `json:"quantity"`
	Unit      string    `json:"unit"`
}

// Resource é um item de inventário (scan AWS, CSV ou dados de exemplo).
type Resource struct {
	ID          int64             `json:"id"`
	SourceID    int64             `json:"source_id"`
	Provider    string            `json:"provider"`
	Region      string            `json:"region"`
	ResourceID  string            `json:"resource_id"`
	Type        string            `json:"type"` // instance | volume | snapshot | public_ip | database | load_balancer | nat_gateway | bucket | other
	Name        string            `json:"name"`
	SKU         string            `json:"sku"` // tipo da instância, tipo do volume, shape...
	SizeGB      float64           `json:"size_gb"`
	State       string            `json:"state"`
	MonthlyCost *float64          `json:"monthly_cost"`
	Currency    string            `json:"currency"`
	CPUAvg      *float64          `json:"cpu_avg"`
	CPUMax      *float64          `json:"cpu_max"`
	MemAvg      *float64          `json:"mem_avg"`
	Attached    *bool             `json:"attached"`
	AgeDays     int               `json:"age_days"`
	Tags        map[string]string `json:"tags"`
	Raw         map[string]any    `json:"raw,omitempty"`
}

// Price é uma entrada do catálogo de preços (sempre estimativa).
type Price struct {
	ID        int64     `json:"id"`
	Provider  string    `json:"provider"`
	Region    string    `json:"region"` // "*" = qualquer região
	SKU       string    `json:"sku"`
	Unit      string    `json:"unit"` // hour | gb-month | month | gb-hour | unit
	Price     float64   `json:"price"`
	Currency  string    `json:"currency"`
	Source    string    `json:"source"` // manual | csv | aws-pricing | demo
	UpdatedAt time.Time `json:"updated_at"`
}

// Prices indexa o catálogo para consulta (provider, região, sku).
type Prices struct {
	m map[string]Price
}

func NewPrices(list []Price) *Prices {
	p := &Prices{m: map[string]Price{}}
	for _, x := range list {
		p.m[strings.ToLower(x.Provider+"|"+x.Region+"|"+x.SKU)] = x
	}
	return p
}

// Get procura o SKU na região e, em seguida, com região "*".
func (p *Prices) Get(provider, region, sku string) (Price, bool) {
	if p == nil {
		return Price{}, false
	}
	if x, ok := p.m[strings.ToLower(provider+"|"+region+"|"+sku)]; ok {
		return x, true
	}
	x, ok := p.m[strings.ToLower(provider+"|*|"+sku)]
	return x, ok
}

// Settings são as configurações do módulo (persistidas como JSON).
type Settings struct {
	BaseCurrency  string             `json:"base_currency"`
	FXRates       map[string]float64 `json:"fx_rates"` // 1 unidade da moeda X = N na moeda base
	RequiredTags  []string           `json:"required_tags"`
	AnomalyWindow int                `json:"anomaly_window"` // dias de linha de base
	AnomalyPct    float64            `json:"anomaly_pct"`    // aumento mínimo sobre a mediana (%)
	AnomalyMinAbs float64            `json:"anomaly_min_abs"`
	AnomalyZ      float64            `json:"anomaly_z"`
	IdleCPU       float64            `json:"idle_cpu"`      // CPU média abaixo = ocioso
	RightsizeCPU  float64            `json:"rightsize_cpu"` // CPU máxima abaixo = superdimensionado
	RightsizeMem  float64            `json:"rightsize_mem"`
	SnapshotDays  int                `json:"snapshot_days"`
	OffHoursEnvs  []string           `json:"off_hours_envs"`
	OffHoursHours float64            `json:"off_hours_hours"` // horas ligadas por semana no agendamento
	WebhookURL    string             `json:"webhook_url"`
	AutoSyncHours int                `json:"auto_sync_hours"`
	// Premissas de desconto (fração 0–1) usadas em estimativas de compromisso e Spot.
	// São premissas configuráveis, não preços oficiais: confirme na calculadora do provedor.
	CommitDiscount1y float64 `json:"commit_discount_1y"`
	CommitDiscount3y float64 `json:"commit_discount_3y"`
	SpotDiscount     float64 `json:"spot_discount"`
	// Regras de alocação: projeto/serviço → equipe (centro de custo). A primeira que casar vence.
	TeamRules []TeamRule `json:"team_rules"`
	// Aumento máximo aceito em verificações de custo de IaC (CI/CD), em % e valor absoluto mensal.
	IaCMaxIncreasePct float64 `json:"iac_max_increase_pct"`
	IaCMaxIncreaseAbs float64 `json:"iac_max_increase_abs"`
}

// TeamRule mapeia um projeto (tag) ou serviço para uma equipe/centro de custo.
type TeamRule struct {
	Dim     string `json:"dim"`     // project | service
	Pattern string `json:"pattern"` // glob (path.Match), sem diferenciar maiúsculas
	Team    string `json:"team"`
}

func DefaultSettings() Settings {
	return Settings{
		BaseCurrency: "USD", FXRates: map[string]float64{}, RequiredTags: []string{"Project", "Environment", "Owner"},
		AnomalyWindow: 14, AnomalyPct: 40, AnomalyMinAbs: 5, AnomalyZ: 3,
		IdleCPU: 5, RightsizeCPU: 40, RightsizeMem: 50, SnapshotDays: 90,
		OffHoursEnvs: []string{"dev", "develop", "test", "teste", "qa", "hml", "homolog", "staging", "sandbox"}, OffHoursHours: 60,
		AutoSyncHours: 24, CommitDiscount1y: 0.20, CommitDiscount3y: 0.40, SpotDiscount: 0.50,
		TeamRules: []TeamRule{}, IaCMaxIncreasePct: 10, IaCMaxIncreaseAbs: 50,
	}
}

// Normalize corrige valores fora de faixa.
func (s *Settings) Normalize() {
	d := DefaultSettings()
	s.BaseCurrency = strings.ToUpper(strings.TrimSpace(s.BaseCurrency))
	if len(s.BaseCurrency) != 3 {
		s.BaseCurrency = d.BaseCurrency
	}
	if s.FXRates == nil {
		s.FXRates = map[string]float64{}
	}
	for k, v := range s.FXRates {
		if v <= 0 || len(k) != 3 {
			delete(s.FXRates, k)
		}
	}
	if s.AnomalyWindow < 7 || s.AnomalyWindow > 60 {
		s.AnomalyWindow = d.AnomalyWindow
	}
	if s.AnomalyPct <= 0 {
		s.AnomalyPct = d.AnomalyPct
	}
	if s.AnomalyMinAbs < 0 {
		s.AnomalyMinAbs = d.AnomalyMinAbs
	}
	if s.AnomalyZ <= 0 {
		s.AnomalyZ = d.AnomalyZ
	}
	if s.IdleCPU <= 0 || s.IdleCPU > 50 {
		s.IdleCPU = d.IdleCPU
	}
	if s.RightsizeCPU <= 0 || s.RightsizeCPU > 90 {
		s.RightsizeCPU = d.RightsizeCPU
	}
	if s.RightsizeMem <= 0 || s.RightsizeMem > 95 {
		s.RightsizeMem = d.RightsizeMem
	}
	if s.SnapshotDays < 7 {
		s.SnapshotDays = d.SnapshotDays
	}
	if s.OffHoursHours <= 0 || s.OffHoursHours > 168 {
		s.OffHoursHours = d.OffHoursHours
	}
	if s.AutoSyncHours < 0 {
		s.AutoSyncHours = 0
	}
	if s.CommitDiscount1y <= 0 || s.CommitDiscount1y >= 1 {
		s.CommitDiscount1y = d.CommitDiscount1y
	}
	if s.CommitDiscount3y <= 0 || s.CommitDiscount3y >= 1 {
		s.CommitDiscount3y = d.CommitDiscount3y
	}
	if s.SpotDiscount <= 0 || s.SpotDiscount >= 1 {
		s.SpotDiscount = d.SpotDiscount
	}
	var rules []TeamRule
	for _, r := range s.TeamRules {
		r.Dim, r.Pattern, r.Team = lower(r.Dim), strings.TrimSpace(r.Pattern), strings.TrimSpace(r.Team)
		if (r.Dim == "project" || r.Dim == "service") && r.Pattern != "" && r.Team != "" {
			rules = append(rules, r)
		}
	}
	if rules == nil {
		rules = []TeamRule{}
	}
	s.TeamRules = rules
	if s.RequiredTags == nil {
		s.RequiredTags = []string{}
	}
	if s.OffHoursEnvs == nil {
		s.OffHoursEnvs = []string{}
	}
	if s.IaCMaxIncreasePct < 0 {
		s.IaCMaxIncreasePct = d.IaCMaxIncreasePct
	}
	if s.IaCMaxIncreaseAbs < 0 {
		s.IaCMaxIncreaseAbs = d.IaCMaxIncreaseAbs
	}
	if !validURL(s.WebhookURL) {
		s.WebhookURL = ""
	}
}

func validURL(u string) bool {
	if u == "" {
		return true
	}
	x, err := url.Parse(u)
	return err == nil && (x.Scheme == "https" || x.Scheme == "http") && x.Host != ""
}

// TeamFor aplica as regras de alocação.
func (s Settings) TeamFor(project, service string) string {
	for _, r := range s.TeamRules {
		v := project
		if r.Dim == "service" {
			v = service
		}
		if v == "" {
			continue
		}
		if ok, _ := path.Match(strings.ToLower(r.Pattern), strings.ToLower(v)); ok {
			return r.Team
		}
	}
	return ""
}

// Converter converte valores para a moeda base.
type Converter struct {
	Base  string
	Rates map[string]float64
}

func (s Settings) Converter() Converter { return Converter{Base: s.BaseCurrency, Rates: s.FXRates} }

// To devolve o valor na moeda base; ok=false se não houver taxa cadastrada.
func (c Converter) To(amount float64, currency string) (float64, bool) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" || cur == c.Base {
		return amount, true
	}
	if r, ok := c.Rates[cur]; ok && r > 0 {
		return amount * r, true
	}
	return 0, false
}

// Finding é uma oportunidade de otimização ou problema de governança.
type Finding struct {
	Key            string   `json:"key"`
	Rule           string   `json:"rule"`
	Category       string   `json:"category"` // rightsizing | idle | waste | schedule | modernize | tagging | commitment | kubernetes
	Severity       string   `json:"severity"` // high | medium | low | info
	Provider       string   `json:"provider"`
	Region         string   `json:"region"`
	ResourceID     string   `json:"resource_id"`
	ResourceType   string   `json:"resource_type"`
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	Recommendation string   `json:"recommendation"`
	MonthlySavings *float64 `json:"monthly_savings"`
	Currency       string   `json:"currency"`
	SavingsNote    string   `json:"savings_note,omitempty"`
	Remediation    string   `json:"remediation,omitempty"`
	Action         string   `json:"action,omitempty"` // ação automatizada disponível (ver RemediationActions)
	SourceID       int64    `json:"source_id,omitempty"`
	Dismissed      bool     `json:"dismissed"`
}

func f64(v float64) *float64 { return &v }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Catalog é o arquivo exportado para verificações de custo fora do servidor
// (ex.: "doomctl cost-check" em pipelines de CI/CD).
type Catalog struct {
	Version  int       `json:"version"`
	Exported time.Time `json:"exported"`
	Settings Settings  `json:"settings"`
	Prices   []Price   `json:"prices"`
	Policies []Policy  `json:"policies"`
}
