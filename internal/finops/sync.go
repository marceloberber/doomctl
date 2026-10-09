package finops

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SourceConfig é a configuração (não secreta) de uma fonte de custos.
type SourceConfig struct {
	Regions        []string          `json:"regions"`         // AWS: regiões do inventário
	TagProject     string            `json:"tag_project"`     // chave da tag de projeto
	TagEnvironment string            `json:"tag_environment"` // chave da tag de ambiente
	TagTeam        string            `json:"tag_team"`        // opcional
	LinkedAccount  string            `json:"linked_account"`  // AWS: filtra uma conta da organização
	Usage          bool              `json:"usage"`           // detalhar tipos de uso (rede/armazenamento)
	Rightsizing    bool              `json:"rightsizing"`     // AWS: recomendações do Cost Explorer
	Metrics        bool              `json:"metrics"`         // AWS: CPU via CloudWatch no inventário
	Remediation    bool              `json:"remediation"`     // permitir ações automatizadas
	HistoryDays    int               `json:"history_days"`    // primeira sincronização
	Endpoints      map[string]string `json:"endpoints"`       // ce, ec2, rds, cloudwatch, pricing, oci_usage
}

func (c *SourceConfig) Normalize(provider string) {
	if c.TagProject == "" {
		c.TagProject = "Project"
	}
	if c.TagEnvironment == "" {
		c.TagEnvironment = "Environment"
	}
	if c.HistoryDays <= 0 {
		c.HistoryDays = 90
	}
	if c.HistoryDays > 395 {
		c.HistoryDays = 395
	}
	var regs []string
	for _, r := range c.Regions {
		if r = strings.TrimSpace(r); r != "" {
			regs = append(regs, r)
		}
	}
	if regs == nil {
		regs = []string{}
	}
	c.Regions = regs
	if c.Endpoints == nil {
		c.Endpoints = map[string]string{}
	}
	for k, v := range c.Endpoints {
		if v == "" || !validURL(v) {
			delete(c.Endpoints, k)
		}
	}
	if provider != "aws" {
		c.Rightsizing, c.Metrics, c.Remediation, c.LinkedAccount = false, false, false, ""
	}
}

// NewAWSClient cria o cliente com os endpoints opcionais da configuração.
func NewAWSClient(creds AWSCreds, cfg SourceConfig, hc *http.Client) *AWSClient {
	return &AWSClient{Creds: creds, HTTP: hc, CEEndpoint: cfg.Endpoints["ce"], EC2Endpoint: cfg.Endpoints["ec2"], RDSEndpoint: cfg.Endpoints["rds"],
		CloudWatchEndpoint: cfg.Endpoints["cloudwatch"], PricingEndpoint: cfg.Endpoints["pricing"]}
}

// SyncResult reúne o que foi coletado numa sincronização.
type SyncResult struct {
	Start, End  time.Time
	Costs       []CostRow
	Alloc       []AllocRow
	Usage       []UsageRow
	UsageStart  time.Time
	Rightsizing []map[string]any
	Warnings    []string
}

func logf(w io.Writer, f string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, f+"\n", a...)
	}
}

// SyncAWS coleta custos diários do Cost Explorer: serviço×região, alocação por tags
// e (opcional) tipos de uso e recomendações de rightsizing.
func SyncAWS(ctx context.Context, cl *AWSClient, cfg SourceConfig, sourceID int64, label string, start, end time.Time, w io.Writer) (SyncResult, error) {
	res := SyncResult{Start: start, End: end}
	var filter map[string]any
	if cfg.LinkedAccount != "" {
		filter = map[string]any{"Dimensions": map[string]any{"Key": "LINKED_ACCOUNT", "Values": []string{cfg.LinkedAccount}}}
	}
	logf(w, "→ Cost Explorer: custo diário por serviço e região (%s a %s)", start.Format(dayFmt), end.AddDate(0, 0, -1).Format(dayFmt))
	groups, err := cl.GetCostAndUsage(ctx, start, end, [][2]string{{"DIMENSION", "SERVICE"}, {"DIMENSION", "REGION"}}, filter)
	if err != nil {
		return res, err
	}
	for _, g := range groups {
		if len(g.Keys) < 2 || g.Amount == 0 {
			continue
		}
		res.Costs = append(res.Costs, CostRow{Day: g.Day, SourceID: sourceID, Provider: "aws", Account: label, Service: g.Keys[0],
			Region: firstNonEmpty(g.Keys[1], "global"), Cost: g.Amount, Currency: firstNonEmpty(g.Unit, "USD")})
	}
	logf(w, "  %d linhas de custo", len(res.Costs))
	for _, t := range []struct{ dim, key string }{{"project", cfg.TagProject}, {"environment", cfg.TagEnvironment}, {"team", cfg.TagTeam}} {
		if t.key == "" {
			continue
		}
		logf(w, "→ Cost Explorer: alocação pela tag %q (%s)", t.key, dimLabel(t.dim))
		gs, err := cl.GetCostAndUsage(ctx, start, end, [][2]string{{"DIMENSION", "SERVICE"}, {"TAG", t.key}}, filter)
		if err != nil {
			res.Warnings = append(res.Warnings, "alocação pela tag "+t.key+": "+err.Error())
			logf(w, "  aviso: %v", err)
			continue
		}
		n := 0
		for _, g := range gs {
			if len(g.Keys) < 2 || g.Amount == 0 {
				continue
			}
			res.Alloc = append(res.Alloc, AllocRow{Day: g.Day, SourceID: sourceID, Provider: "aws", Service: g.Keys[0], Dim: t.dim,
				Value: TagValue(g.Keys[1]), Cost: g.Amount, Currency: firstNonEmpty(g.Unit, "USD")})
			n++
		}
		logf(w, "  %d linhas", n)
	}
	if cfg.Usage {
		us := start
		if end.Sub(us) > 35*24*time.Hour {
			us = end.AddDate(0, 0, -35)
		}
		res.UsageStart = us
		logf(w, "→ Cost Explorer: tipos de uso (rede/armazenamento) desde %s", us.Format(dayFmt))
		gs, err := cl.GetCostAndUsage(ctx, us, end, [][2]string{{"DIMENSION", "SERVICE"}, {"DIMENSION", "USAGE_TYPE"}}, filter)
		if err != nil {
			res.Warnings = append(res.Warnings, "tipos de uso: "+err.Error())
			logf(w, "  aviso: %v", err)
		} else {
			for _, g := range gs {
				if len(g.Keys) < 2 || g.Amount == 0 {
					continue
				}
				cat := ClassifyUsage("aws", g.Keys[0], g.Keys[1])
				if cat == "" {
					continue
				}
				res.Usage = append(res.Usage, UsageRow{Day: g.Day, SourceID: sourceID, Provider: "aws", Service: g.Keys[0], UsageType: g.Keys[1],
					Category: cat, Cost: g.Amount, Currency: firstNonEmpty(g.Unit, "USD")})
			}
			logf(w, "  %d linhas de rede/armazenamento", len(res.Usage))
		}
	}
	if cfg.Rightsizing {
		logf(w, "→ Cost Explorer: recomendações de rightsizing (EC2)")
		recs, err := cl.GetRightsizingRecommendation(ctx)
		if err != nil {
			res.Warnings = append(res.Warnings, "rightsizing: "+err.Error())
			logf(w, "  aviso: %v (ative as recomendações de rightsizing nas preferências do Cost Explorer)", err)
		} else {
			res.Rightsizing = recs
			if res.Rightsizing == nil {
				res.Rightsizing = []map[string]any{}
			}
			logf(w, "  %d recomendações", len(recs))
		}
	}
	return res, nil
}

// SyncOCI coleta custos diários da Usage API, em janelas de até 31 dias.
func SyncOCI(ctx context.Context, cl *OCIClient, cfg SourceConfig, sourceID int64, label string, start, end time.Time, w io.Writer) (SyncResult, error) {
	res := SyncResult{Start: start, End: end}
	type window struct{ s, e time.Time }
	var wins []window
	for s := start; s.Before(end); {
		e := s.AddDate(0, 0, 31)
		if e.After(end) {
			e = end
		}
		wins = append(wins, window{s, e})
		s = e
	}
	tagSpec := func(k string) []map[string]string {
		ns, key, ok := strings.Cut(k, ".")
		if !ok {
			return []map[string]string{{"key": k}}
		}
		return []map[string]string{{"namespace": ns, "key": key}}
	}
	for _, wn := range wins {
		logf(w, "→ OCI Usage API: %s a %s", wn.s.Format(dayFmt), wn.e.AddDate(0, 0, -1).Format(dayFmt))
		items, err := cl.RequestSummarizedUsages(ctx, wn.s, wn.e, []string{"service", "region"}, nil)
		if err != nil {
			return res, err
		}
		for _, it := range items {
			if it.Amount == 0 || it.Day.IsZero() {
				continue
			}
			res.Costs = append(res.Costs, CostRow{Day: it.Day, SourceID: sourceID, Provider: "oci", Account: label, Service: firstNonEmpty(it.Service, "(sem serviço)"),
				Region: it.Region, Cost: it.Amount, Currency: firstNonEmpty(it.Currency, "USD"), UsageQty: it.Quantity, UsageUnit: it.Unit})
		}
		for _, t := range []struct{ dim, key string }{{"project", cfg.TagProject}, {"environment", cfg.TagEnvironment}, {"team", cfg.TagTeam}} {
			if t.key == "" {
				continue
			}
			items, err := cl.RequestSummarizedUsages(ctx, wn.s, wn.e, []string{"service"}, tagSpec(t.key))
			if err != nil {
				res.Warnings = append(res.Warnings, "alocação pela tag "+t.key+": "+err.Error())
				logf(w, "  aviso (tag %s): %v", t.key, err)
				continue
			}
			for _, it := range items {
				if it.Amount == 0 || it.Day.IsZero() {
					continue
				}
				val := ""
				for _, v := range it.Tags {
					val = v
				}
				res.Alloc = append(res.Alloc, AllocRow{Day: it.Day, SourceID: sourceID, Provider: "oci", Service: firstNonEmpty(it.Service, "(sem serviço)"),
					Dim: t.dim, Value: val, Cost: it.Amount, Currency: firstNonEmpty(it.Currency, "USD")})
			}
		}
		if cfg.Usage && end.Sub(wn.s) <= 36*24*time.Hour {
			items, err := cl.RequestSummarizedUsages(ctx, wn.s, wn.e, []string{"service", "skuName"}, nil)
			if err != nil {
				res.Warnings = append(res.Warnings, "tipos de uso: "+err.Error())
			} else {
				if res.UsageStart.IsZero() {
					res.UsageStart = wn.s
				}
				for _, it := range items {
					name := it.SKUName
					cat := ClassifyUsage("oci", it.Service, name)
					if cat == "" || it.Amount == 0 {
						continue
					}
					res.Usage = append(res.Usage, UsageRow{Day: it.Day, SourceID: sourceID, Provider: "oci", Service: it.Service, UsageType: name,
						Category: cat, Cost: it.Amount, Currency: firstNonEmpty(it.Currency, "USD"), Quantity: it.Quantity, Unit: it.Unit})
				}
			}
		}
	}
	logf(w, "  %d linhas de custo, %d de alocação, %d de uso", len(res.Costs), len(res.Alloc), len(res.Usage))
	return res, nil
}

// ScanAWS coleta o inventário (EC2, EBS, EIPs, snapshots, RDS) e, opcionalmente, CPU.
func ScanAWS(ctx context.Context, cl *AWSClient, cfg SourceConfig, sourceID int64, w io.Writer) ([]Resource, []string, error) {
	if len(cfg.Regions) == 0 {
		return nil, nil, fmt.Errorf("informe ao menos uma região na fonte para o inventário")
	}
	var out []Resource
	var warns []string
	for _, reg := range cfg.Regions {
		logf(w, "→ %s: EC2 (instâncias, volumes, IPs, snapshots)", reg)
		rs, err := cl.ScanEC2(ctx, reg)
		if err != nil {
			return nil, warns, err
		}
		logf(w, "  %d recursos", len(rs))
		logf(w, "→ %s: RDS", reg)
		dbs, err := cl.ScanRDS(ctx, reg)
		if err != nil {
			warns = append(warns, reg+" RDS: "+err.Error())
			logf(w, "  aviso: %v", err)
		} else {
			logf(w, "  %d bancos", len(dbs))
			rs = append(rs, dbs...)
		}
		if cfg.Metrics {
			n := 0
			for i := range rs {
				r := &rs[i]
				ns, dim := "", ""
				switch {
				case r.Type == "instance" && r.State == "running":
					ns, dim = "AWS/EC2", "InstanceId"
				case r.Type == "database" && r.State == "available":
					ns, dim = "AWS/RDS", "DBInstanceIdentifier"
				default:
					continue
				}
				if n >= 300 {
					warns = append(warns, reg+": limite de 300 consultas de métricas atingido")
					break
				}
				n++
				avg, mx, err := cl.CPUStats(ctx, reg, ns, dim, r.ResourceID, 14)
				if err != nil {
					warns = append(warns, reg+" CloudWatch: "+err.Error())
					logf(w, "  aviso CloudWatch: %v", err)
					break
				}
				r.CPUAvg, r.CPUMax = avg, mx
			}
			logf(w, "  CPU (14 dias) consultada para %d recursos", n)
		}
		out = append(out, rs...)
	}
	for i := range out {
		out[i].SourceID = sourceID
		out[i].Currency = "USD"
	}
	return out, warns, nil
}
