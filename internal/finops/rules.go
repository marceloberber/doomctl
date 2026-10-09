package finops

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- tags ----------

var tagAliases = map[string][]string{
	"environment": {"environment", "env", "ambiente", "stage"},
	"project":     {"project", "projeto", "app", "application", "aplicacao"},
	"owner":       {"owner", "dono", "responsavel", "team", "equipe"},
}

// TagLookup procura uma tag por nome (sem diferenciar maiúsculas) e pelos aliases comuns.
func TagLookup(tags map[string]string, name string) string {
	want := lower(name)
	cands := []string{want}
	if al, ok := tagAliases[want]; ok {
		cands = al
	}
	for _, c := range cands {
		for k, v := range tags {
			if lower(k) == c && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return ""
}

// MissingTags devolve as tags obrigatórias ausentes (comparação exata de chave, sem caixa).
func MissingTags(tags map[string]string, required []string) []string {
	var miss []string
	for _, r := range required {
		found := false
		for k, v := range tags {
			if strings.EqualFold(k, r) && strings.TrimSpace(v) != "" {
				found = true
				break
			}
		}
		if !found {
			miss = append(miss, r)
		}
	}
	return miss
}

// ---------- custo estimado ----------

// Coster estima o custo mensal de recursos a partir do catálogo de preços.
type Coster struct {
	Prices *Prices
	Conv   Converter
}

// Monthly devolve o custo mensal (moeda base) de qty unidades do SKU.
// Para preços por hora, qty é a quantidade de recursos ligados o mês inteiro;
// para gb-month, qty é o volume em GB.
func (c Coster) Monthly(provider, region, sku string, qty float64) (float64, bool) {
	p, ok := c.Prices.Get(provider, region, sku)
	if !ok {
		return 0, false
	}
	v, ok := c.Conv.To(p.Price, p.Currency)
	if !ok {
		return 0, false
	}
	switch p.Unit {
	case "hour", "gb-hour":
		return v * HoursPerMonth * qty, true
	default: // gb-month, month, unit
		return v * qty, true
	}
}

// ResourceMonthly estima o custo mensal de um recurso do inventário (moeda base).
func (c Coster) ResourceMonthly(r Resource) (float64, bool) {
	if r.MonthlyCost != nil {
		return c.Conv.To(*r.MonthlyCost, r.Currency)
	}
	switch r.Type {
	case "instance":
		if r.State == "stopped" || r.State == "stopping" {
			return 0, true
		}
		return c.Monthly(r.Provider, r.Region, "instance:"+r.SKU, 1)
	case "database":
		mult := 1.0
		if b, _ := r.Raw["multi_az"].(bool); b {
			mult = 2
		}
		v, ok := c.Monthly(r.Provider, r.Region, "db:"+r.SKU, mult)
		if !ok {
			return 0, false
		}
		st, _ := r.Raw["storage_type"].(string)
		if s, ok2 := c.Monthly(r.Provider, r.Region, "db-storage:"+st, r.SizeGB*mult); ok2 {
			v += s
		}
		return v, true
	case "volume":
		return c.Monthly(r.Provider, r.Region, "volume:"+r.SKU, r.SizeGB)
	case "snapshot":
		return c.Monthly(r.Provider, r.Region, "snapshot", r.SizeGB)
	case "public_ip":
		return c.Monthly(r.Provider, r.Region, "public_ip", 1)
	case "nat_gateway":
		return c.Monthly(r.Provider, r.Region, "nat_gateway", 1)
	case "load_balancer":
		if v, ok := c.Monthly(r.Provider, r.Region, "load_balancer:"+r.SKU, 1); ok {
			return v, true
		}
		return c.Monthly(r.Provider, r.Region, "load_balancer", 1)
	case "bucket":
		return c.Monthly(r.Provider, r.Region, "storage:"+firstNonEmpty(r.SKU, "standard"), r.SizeGB)
	}
	return 0, false
}

// ---------- tamanhos de instância ----------

var sizeLadder = []string{"nano", "micro", "small", "medium", "large", "xlarge", "2xlarge", "3xlarge", "4xlarge", "6xlarge", "8xlarge",
	"9xlarge", "12xlarge", "16xlarge", "18xlarge", "24xlarge", "32xlarge", "48xlarge"}

var halfOf = map[string]string{
	"48xlarge": "24xlarge", "32xlarge": "16xlarge", "24xlarge": "12xlarge", "18xlarge": "9xlarge", "16xlarge": "8xlarge",
	"12xlarge": "6xlarge", "9xlarge": "4xlarge", "8xlarge": "4xlarge", "6xlarge": "3xlarge", "4xlarge": "2xlarge",
	"3xlarge": "xlarge", "2xlarge": "xlarge", "xlarge": "large", "large": "medium", "medium": "small", "small": "micro", "micro": "nano",
}

// SplitInstanceType separa "db.m5.xlarge" em ("db.", "m5", "xlarge").
func SplitInstanceType(t string) (prefix, family, size string) {
	if strings.HasPrefix(t, "db.") || strings.HasPrefix(t, "cache.") {
		i := strings.Index(t, ".")
		prefix, t = t[:i+1], t[i+1:]
	}
	i := strings.LastIndex(t, ".")
	if i < 0 {
		return prefix, t, ""
	}
	return prefix, t[:i], t[i+1:]
}

// Downsize sugere o tamanho imediatamente menor (metade da capacidade) na mesma família.
func Downsize(t string) string {
	prefix, fam, size := SplitInstanceType(t)
	next, ok := halfOf[size]
	if !ok || fam == "" {
		return ""
	}
	burst := strings.HasPrefix(fam, "t")
	graviton := strings.Contains(strings.TrimLeft(fam, "abcdefghijklmnopqrstuvwxyz"), "g")
	switch {
	case burst:
		if next == "nano" && prefix != "" {
			return ""
		}
	case graviton:
		if next == "small" || next == "micro" || next == "nano" {
			return ""
		}
	default:
		if next == "medium" || next == "small" || next == "micro" || next == "nano" {
			return ""
		}
	}
	return prefix + fam + "." + next
}

var previousGen = map[string]string{"t1": "t3", "t2": "t3", "m1": "m5", "m2": "r5", "m3": "m5", "m4": "m5", "c1": "c5", "c3": "c5",
	"c4": "c5", "r3": "r5", "r4": "r5", "i2": "i3", "d2": "d3", "g2": "g4dn", "p2": "p3", "x1": "x2iedn"}

// Modernize sugere a família de geração atual para tipos antigos (AWS) ou shapes antigos (OCI).
func Modernize(provider, t string) string {
	if provider == "oci" {
		if strings.HasPrefix(t, "VM.Standard1.") || strings.HasPrefix(t, "VM.Standard2.") || strings.HasPrefix(t, "VM.Standard.E2.") ||
			strings.HasPrefix(t, "BM.Standard1.") || strings.HasPrefix(t, "BM.Standard2.") {
			return "VM.Standard.E5.Flex"
		}
		return ""
	}
	prefix, fam, size := SplitInstanceType(t)
	if n, ok := previousGen[fam]; ok && size != "" {
		return prefix + n + "." + size
	}
	return ""
}

func isSpotFriendly(r Resource) bool {
	hay := lower(r.Name + " " + TagLookup(r.Tags, "role") + " " + TagLookup(r.Tags, "workload") + " " + TagLookup(r.Tags, "project"))
	for _, k := range []string{"worker", "batch", "runner", "build", "ci-", "ci_", "jenkins-agent", "spark", "emr", "render", "queue", "consumer", "etl", "scraper"} {
		if strings.Contains(hay, k) {
			return true
		}
	}
	return false
}

func envMatches(env string, list []string) bool {
	e := lower(env)
	for _, x := range list {
		if e == lower(x) {
			return true
		}
	}
	return false
}

// ---------- regras de inventário ----------

// RuleInput reúne o inventário e as configurações para as regras de otimização.
type RuleInput struct {
	Resources      []Resource
	Prices         *Prices
	Settings       Settings
	AWSRightsizing map[int64][]map[string]any // por fonte: recomendações brutas do Cost Explorer
	Now            time.Time
}

func fkey(rule string, r Resource) string {
	return rule + "|" + r.Provider + "|" + r.Region + "|" + r.ResourceID
}

func displayName(r Resource) string {
	if r.Name != "" && r.Name != r.ResourceID {
		return r.Name + " (" + r.ResourceID + ")"
	}
	return r.ResourceID
}

// EvaluateResources aplica as regras de rightsizing, ociosidade, desperdício,
// agendamento, modernização, Spot e tags ao inventário.
func EvaluateResources(in RuleInput) []Finding {
	st := in.Settings
	c := Coster{Prices: in.Prices, Conv: st.Converter()}
	cur := st.BaseCurrency
	var out []Finding
	byID := map[string]Resource{}
	for _, r := range in.Resources {
		byID[r.ResourceID] = r
	}
	add := func(f Finding, r Resource) {
		f.Provider, f.Region, f.ResourceID, f.ResourceType, f.SourceID = r.Provider, r.Region, r.ResourceID, r.Type, r.SourceID
		if f.Name == "" {
			f.Name = displayName(r)
		}
		f.Currency = cur
		if f.Key == "" {
			f.Key = fkey(f.Rule, r)
		}
		out = append(out, f)
	}
	for _, r := range in.Resources {
		monthly, known := c.ResourceMonthly(r)
		savings := func(frac float64) *float64 {
			if !known || monthly <= 0 {
				return nil
			}
			return f64(round2(monthly * frac))
		}
		env := TagLookup(r.Tags, "environment")
		aws := r.Provider == "aws"
		region := r.Region
		switch r.Type {
		case "volume":
			if r.Attached != nil && !*r.Attached {
				f := Finding{Rule: "unattached_volume", Category: "waste", Severity: "high",
					Title:          "Volume sem uso (" + strconv.FormatFloat(r.SizeGB, 'f', 0, 64) + " GB " + r.SKU + ")",
					Detail:         "Volume não está anexado a nenhuma instância há " + strconv.Itoa(r.AgeDays) + " dias (idade do volume).",
					Recommendation: "Confirme com o responsável, crie um snapshot de segurança e exclua o volume.",
					MonthlySavings: savings(1)}
				if aws {
					f.Action = "delete_volume"
					f.Remediation = "# 1) snapshot de segurança\naws ec2 create-snapshot --region " + region + " --volume-id " + r.ResourceID +
						" --description \"backup antes de remover (doomctl)\"\n# 2) simule e depois exclua (remova --dry-run)\naws ec2 delete-volume --region " + region + " --volume-id " + r.ResourceID + " --dry-run"
				} else if r.Provider == "oci" {
					f.Remediation = "# crie um backup antes:\noci bv backup create --volume-id " + r.ResourceID + "\n# 🛑 exclusão definitiva:\noci bv volume delete --volume-id " + r.ResourceID
				}
				add(f, r)
			}
			if lower(r.SKU) == "gp2" {
				f := Finding{Rule: "gp2_to_gp3", Category: "modernize", Severity: "medium",
					Title:          "Volume gp2 pode migrar para gp3",
					Detail:         "gp3 tem preço por GB menor e 3.000 IOPS/125 MiB/s de base, independente do tamanho.",
					Recommendation: "Migre com Elastic Volumes (sem parada). Para volumes gp2 > 1 TB, configure IOPS no gp3 equivalentes ao atual (3 IOPS/GB).",
				}
				if gp2, ok := c.Monthly(r.Provider, region, "volume:gp2", r.SizeGB); ok {
					if gp3, ok := c.Monthly(r.Provider, region, "volume:gp3", r.SizeGB); ok && gp2 > gp3 {
						f.MonthlySavings = f64(round2(gp2 - gp3))
					}
				}
				if f.MonthlySavings == nil && known {
					f.MonthlySavings = f64(round2(monthly * 0.2))
					f.SavingsNote = "estimativa de 20% (cadastre volume:gp2 e volume:gp3 no catálogo para precisão)"
				}
				if aws {
					f.Action = "modify_volume_gp3"
					f.Remediation = "aws ec2 modify-volume --region " + region + " --volume-id " + r.ResourceID + " --volume-type gp3 --dry-run"
				}
				add(f, r)
			}
		case "public_ip":
			if r.Attached != nil && !*r.Attached {
				f := Finding{Rule: "unassociated_ip", Category: "waste", Severity: "medium", Title: "IP público reservado sem associação",
					Detail:         "IPs públicos IPv4 são cobrados por hora, associados ou não.",
					Recommendation: "Libere o IP se não houver dependência (DNS, allowlists de parceiros).", MonthlySavings: savings(1)}
				if aws {
					f.Action = "release_ip"
					f.Remediation = "aws ec2 release-address --region " + region + " --allocation-id " + r.ResourceID + " --dry-run"
				}
				add(f, r)
			}
		case "snapshot":
			if r.AgeDays >= st.SnapshotDays {
				f := Finding{Rule: "old_snapshot", Category: "waste", Severity: "low",
					Title:          "Snapshot com " + strconv.Itoa(r.AgeDays) + " dias",
					Detail:         "Snapshot mais antigo que a retenção configurada (" + strconv.Itoa(st.SnapshotDays) + " dias).",
					Recommendation: "Verifique se ainda é necessário (AMIs, conformidade); exclua ou mova para o arquivamento de snapshots.",
					MonthlySavings: savings(1), SavingsNote: "limite superior: snapshots são incrementais"}
				if aws {
					f.Action = "delete_snapshot"
					f.Remediation = "aws ec2 delete-snapshot --region " + region + " --snapshot-id " + r.ResourceID + " --dry-run"
				}
				add(f, r)
			}
		case "nat_gateway", "load_balancer":
			if r.Attached != nil && !*r.Attached {
				what := map[string]string{"nat_gateway": "NAT Gateway sem tráfego", "load_balancer": "Load balancer sem destinos"}[r.Type]
				add(Finding{Rule: "orphan_" + r.Type, Category: "waste", Severity: "medium", Title: what,
					Detail: "Recurso cobrado por hora sem uso aparente.", Recommendation: "Confirme e remova (via IaC, se gerenciado por OpenTofu).",
					MonthlySavings: savings(1)}, r)
			}
		case "instance":
			running := r.State == "" || r.State == "running"
			if r.State == "stopped" {
				var vol float64
				vk := true
				if ids, ok := r.Raw["volumes"].([]string); ok {
					for _, id := range ids {
						if v, ok := byID[id]; ok {
							m, ok := c.ResourceMonthly(v)
							vk = vk && ok
							vol += m
						}
					}
				} else if ids, ok := r.Raw["volumes"].([]any); ok {
					for _, x := range ids {
						if v, ok := byID[fmt.Sprint(x)]; ok {
							m, ok := c.ResourceMonthly(v)
							vk = vk && ok
							vol += m
						}
					}
				}
				f := Finding{Rule: "stopped_instance", Category: "waste", Severity: "low", Title: "Instância parada",
					Detail:         "Instâncias paradas não cobram computação, mas os volumes anexados e IPs continuam sendo cobrados.",
					Recommendation: "Se não for religada, crie uma imagem (AMI/custom image) e encerre a instância."}
				if vol > 0 && vk {
					f.MonthlySavings = f64(round2(vol))
					f.SavingsNote = "custo dos volumes anexados"
				}
				if aws {
					f.Remediation = "aws ec2 create-image --region " + region + " --instance-id " + r.ResourceID + " --name \"" + r.ResourceID + "-doomctl-backup\" --no-reboot\n# 🛑 após validar a imagem:\naws ec2 terminate-instances --region " + region + " --instance-ids " + r.ResourceID + " --dry-run"
				}
				add(f, r)
				continue
			}
			if !running {
				continue
			}
			idle := r.CPUAvg != nil && *r.CPUAvg < st.IdleCPU && (r.CPUMax == nil || *r.CPUMax < st.IdleCPU*4)
			if idle {
				f := Finding{Rule: "idle_instance", Category: "idle", Severity: "high", Title: "Instância ociosa",
					Detail:         fmt.Sprintf("CPU média %.1f%%%s nos últimos dias.", *r.CPUAvg, maxStr(r.CPUMax)),
					Recommendation: "Confirme com o responsável; desligue ou encerre. Se for usada só em horário comercial, agende liga/desliga.",
					MonthlySavings: savings(1)}
				if aws {
					f.Action = "stop_instance"
					f.Remediation = "aws ec2 stop-instances --region " + region + " --instance-ids " + r.ResourceID + " --dry-run"
				} else if r.Provider == "oci" {
					f.Remediation = "oci compute instance action --action SOFTSTOP --instance-id " + r.ResourceID
				}
				add(f, r)
			} else if r.CPUMax != nil && *r.CPUMax < st.RightsizeCPU && (r.MemAvg == nil || *r.MemAvg < st.RightsizeMem) {
				if target := Downsize(r.SKU); target != "" {
					f := Finding{Rule: "rightsize_instance", Category: "rightsizing", Severity: "medium",
						Title:          "Superdimensionada: " + r.SKU + " → " + target,
						Detail:         fmt.Sprintf("CPU máxima %.1f%%%s; abaixo do limite de %.0f%%.", *r.CPUMax, memStr(r.MemAvg), st.RightsizeCPU),
						Recommendation: "Reduza para " + target + " em janela de manutenção (exige parar a instância). Valide memória antes, se o agente do CloudWatch não estiver instalado."}
					if cur, ok := c.Monthly(r.Provider, region, "instance:"+r.SKU, 1); ok {
						if nw, ok := c.Monthly(r.Provider, region, "instance:"+target, 1); ok && cur > nw {
							f.MonthlySavings = f64(round2(cur - nw))
						}
					}
					if f.MonthlySavings == nil && known {
						f.MonthlySavings = f64(round2(monthly * 0.5))
						f.SavingsNote = "estimativa de 50% (cadastre o preço de " + target + " para precisão)"
					}
					if aws {
						f.Remediation = "# janela de manutenção — a instância será reiniciada\naws ec2 stop-instances --region " + region + " --instance-ids " + r.ResourceID +
							"\naws ec2 wait instance-stopped --region " + region + " --instance-ids " + r.ResourceID +
							"\naws ec2 modify-instance-attribute --region " + region + " --instance-id " + r.ResourceID + " --instance-type \"{\\\"Value\\\": \\\"" + target + "\\\"}\"" +
							"\naws ec2 start-instances --region " + region + " --instance-ids " + r.ResourceID
					}
					add(f, r)
				}
			}
			if mod := Modernize(r.Provider, r.SKU); mod != "" {
				f := Finding{Rule: "previous_generation", Category: "modernize", Severity: "low", Title: "Geração anterior: " + r.SKU + " → " + mod,
					Detail:         "Famílias de geração atual costumam oferecer melhor preço/desempenho.",
					Recommendation: "Migre para " + mod + " (ou equivalente Graviton/Ampere, após validar a compatibilidade ARM)."}
				if cur, ok := c.Monthly(r.Provider, region, "instance:"+r.SKU, 1); ok {
					if nw, ok := c.Monthly(r.Provider, region, "instance:"+mod, 1); ok && cur > nw {
						f.MonthlySavings = f64(round2(cur - nw))
					}
				}
				add(f, r)
			}
			if env != "" && envMatches(env, st.OffHoursEnvs) && !idle {
				frac := 1 - st.OffHoursHours/168
				f := Finding{Rule: "off_hours", Category: "schedule", Severity: "medium",
					Title:          "Ambiente " + env + " ligado 24×7",
					Detail:         fmt.Sprintf("Agendando %.0f h/semana ligadas (ex.: 12 h × 5 dias), o custo cai %.0f%%.", st.OffHoursHours, frac*100),
					Recommendation: "Agende parada fora do horário comercial (EventBridge Scheduler/Instance Scheduler na AWS; Resource Scheduler na OCI).",
					MonthlySavings: savings(frac)}
				if aws {
					f.Remediation = "# Requer uma role do EventBridge Scheduler com ec2:StopInstances/StartInstances\n" +
						"aws scheduler create-schedule --region " + region + " --name doomctl-stop-" + r.ResourceID +
						" --schedule-expression \"cron(0 20 ? * MON-FRI *)\" --schedule-expression-timezone \"America/Sao_Paulo\" --flexible-time-window Mode=OFF" +
						" --target '{\"Arn\":\"arn:aws:scheduler:::aws-sdk:ec2:stopInstances\",\"RoleArn\":\"<ROLE_ARN>\",\"Input\":\"{\\\"InstanceIds\\\":[\\\"" + r.ResourceID + "\\\"]}\"}'\n" +
						"aws scheduler create-schedule --region " + region + " --name doomctl-start-" + r.ResourceID +
						" --schedule-expression \"cron(0 8 ? * MON-FRI *)\" --schedule-expression-timezone \"America/Sao_Paulo\" --flexible-time-window Mode=OFF" +
						" --target '{\"Arn\":\"arn:aws:scheduler:::aws-sdk:ec2:startInstances\",\"RoleArn\":\"<ROLE_ARN>\",\"Input\":\"{\\\"InstanceIds\\\":[\\\"" + r.ResourceID + "\\\"]}\"}'"
				}
				add(f, r)
			}
			lc, _ := r.Raw["lifecycle"].(string)
			if lc != "spot" && isSpotFriendly(r) {
				add(Finding{Rule: "spot_candidate", Category: "spot", Severity: "low", Title: "Candidata a Spot/Preemptible",
					Detail:         "Nome/tags indicam workload tolerante a interrupção (worker, batch, CI...).",
					Recommendation: "Mova para Spot (AWS) ou Preemptible (OCI) com Auto Scaling/instance pools e tratamento de interrupção (aviso de 2 min na AWS).",
					MonthlySavings: savings(st.SpotDiscount), SavingsNote: fmt.Sprintf("premissa de %.0f%% de desconto (configurável)", st.SpotDiscount*100)}, r)
			}
		case "database":
			if r.CPUMax != nil && *r.CPUMax < st.RightsizeCPU {
				if target := Downsize(r.SKU); target != "" {
					f := Finding{Rule: "rightsize_database", Category: "database", Severity: "medium",
						Title:          "Banco superdimensionado: " + r.SKU + " → " + target,
						Detail:         fmt.Sprintf("CPU máxima %.1f%% no período.", *r.CPUMax),
						Recommendation: "Reduza a classe em janela de manutenção; verifique antes FreeableMemory, conexões e IOPS."}
					if cur, ok := c.Monthly(r.Provider, region, "db:"+r.SKU, 1); ok {
						if nw, ok := c.Monthly(r.Provider, region, "db:"+target, 1); ok && cur > nw {
							mult := 1.0
							if b, _ := r.Raw["multi_az"].(bool); b {
								mult = 2
							}
							f.MonthlySavings = f64(round2((cur - nw) * mult))
						}
					}
					if f.MonthlySavings == nil && known {
						f.MonthlySavings = f64(round2(monthly * 0.4))
						f.SavingsNote = "estimativa (cadastre db:" + target + " no catálogo)"
					}
					if aws {
						f.Remediation = "aws rds modify-db-instance --region " + region + " --db-instance-identifier " + r.ResourceID + " --db-instance-class " + target + " --no-apply-immediately"
					}
					add(f, r)
				}
			}
			if b, _ := r.Raw["multi_az"].(bool); b && env != "" && envMatches(env, st.OffHoursEnvs) {
				f := Finding{Rule: "multiaz_nonprod", Category: "database", Severity: "medium", Title: "Multi-AZ em ambiente " + env,
					Detail: "Multi-AZ dobra o custo da instância; em ambientes não produtivos raramente é necessário.", Recommendation: "Desative o Multi-AZ fora de produção.",
					MonthlySavings: savings(0.5)}
				if aws {
					f.Remediation = "aws rds modify-db-instance --region " + region + " --db-instance-identifier " + r.ResourceID + " --no-multi-az --no-apply-immediately"
				}
				add(f, r)
			}
			if stt, _ := r.Raw["storage_type"].(string); stt == "gp2" {
				add(Finding{Rule: "db_gp2", Category: "database", Severity: "low", Title: "Armazenamento gp2 no banco",
					Detail: "gp3 oferece 3.000 IOPS de base e menor custo por GB.", Recommendation: "Altere o storage para gp3 (modificação online).",
					Remediation: "aws rds modify-db-instance --region " + region + " --db-instance-identifier " + r.ResourceID + " --storage-type gp3 --apply-immediately"}, r)
			}
			if r.State == "stopped" {
				add(Finding{Rule: "stopped_database", Category: "database", Severity: "low", Title: "Banco parado",
					Detail:         "Bancos RDS parados voltam a ligar automaticamente após 7 dias; o armazenamento continua sendo cobrado.",
					Recommendation: "Se não for mais usado, gere um snapshot final e exclua a instância."}, r)
			}
		}
		if len(st.RequiredTags) > 0 && r.Type != "snapshot" {
			if miss := MissingTags(r.Tags, st.RequiredTags); len(miss) > 0 {
				f := Finding{Rule: "missing_tags", Category: "tagging", Severity: "low", Title: "Tags obrigatórias ausentes: " + strings.Join(miss, ", "),
					Detail: "Sem essas tags o custo não é alocado a projeto/ambiente/responsável (showback/chargeback).", Recommendation: "Adicione as tags e ative-as como cost allocation tags."}
				if aws && r.Type != "public_ip" {
					var kv []string
					for _, m := range miss {
						kv = append(kv, "Key="+m+",Value=<preencha>")
					}
					f.Remediation = "aws ec2 create-tags --region " + region + " --resources " + r.ResourceID + " --tags " + strings.Join(kv, " ")
				}
				add(f, r)
			}
		}
	}
	// recomendações do AWS Cost Explorer
	for src, recs := range in.AWSRightsizing {
		for _, rec := range recs {
			if f, ok := ceRightsizing(rec, st, src); ok {
				out = append(out, f)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := savingsOf(out[i]), savingsOf(out[j])
		if si != sj {
			return si > sj
		}
		return sevRank(out[i].Severity) < sevRank(out[j].Severity)
	})
	if out == nil {
		out = []Finding{}
	}
	return out
}

func maxStr(p *float64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(", máxima %.1f%%", *p)
}

func memStr(p *float64) string {
	if p == nil {
		return " (memória sem métrica)"
	}
	return fmt.Sprintf(", memória média %.1f%%", *p)
}

func savingsOf(f Finding) float64 {
	if f.MonthlySavings == nil {
		return 0
	}
	return *f.MonthlySavings
}

func sevRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	}
	return 3
}

func dig(m any, keys ...string) any {
	cur := m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func digStr(m any, keys ...string) string {
	switch v := dig(m, keys...).(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// ceRightsizing converte uma recomendação do Cost Explorer em Finding.
func ceRightsizing(rec map[string]any, st Settings, src int64) (Finding, bool) {
	id := digStr(rec, "CurrentInstance", "ResourceId")
	if id == "" {
		return Finding{}, false
	}
	curType := digStr(rec, "CurrentInstance", "ResourceDetails", "EC2ResourceDetails", "InstanceType")
	region := digStr(rec, "CurrentInstance", "ResourceDetails", "EC2ResourceDetails", "Region")
	name := digStr(rec, "CurrentInstance", "InstanceName")
	f := Finding{Rule: "aws_ce_rightsizing", Category: "rightsizing", Severity: "medium", Provider: "aws", Region: region, ResourceID: id,
		ResourceType: "instance", Name: firstNonEmpty(name, id), Currency: st.BaseCurrency, SourceID: src}
	cpu := digStr(rec, "CurrentInstance", "ResourceUtilization", "EC2ResourceUtilization", "MaxCpuUtilizationPercentage")
	var amount, currency string
	switch digStr(rec, "RightsizingType") {
	case "Terminate":
		f.Title = "AWS recomenda encerrar " + curType
		f.Recommendation = "O Cost Explorer não encontrou uso relevante; confirme com o responsável e encerre a instância."
		amount = digStr(rec, "TerminateRecommendationDetail", "EstimatedMonthlySavings")
		currency = digStr(rec, "TerminateRecommendationDetail", "CurrencyCode")
		f.Severity = "high"
	default:
		targets, _ := dig(rec, "ModifyRecommendationDetail", "TargetInstances").([]any)
		var best any
		for _, t := range targets {
			if b, _ := dig(t, "DefaultTargetInstance").(bool); b || best == nil {
				best = t
			}
		}
		if best == nil {
			return Finding{}, false
		}
		target := digStr(best, "ResourceDetails", "EC2ResourceDetails", "InstanceType")
		f.Title = "AWS recomenda " + curType + " → " + target
		f.Recommendation = "Recomendação do AWS Cost Explorer (considera descontos vigentes). Aplique em janela de manutenção."
		amount = digStr(best, "EstimatedMonthlySavings")
		currency = digStr(best, "CurrencyCode")
		if target != "" {
			f.Remediation = "aws ec2 stop-instances --region " + region + " --instance-ids " + id + "\naws ec2 wait instance-stopped --region " + region + " --instance-ids " + id +
				"\naws ec2 modify-instance-attribute --region " + region + " --instance-id " + id + " --instance-type \"{\\\"Value\\\": \\\"" + target + "\\\"}\"\naws ec2 start-instances --region " + region + " --instance-ids " + id
		}
	}
	if cpu != "" {
		f.Detail = "CPU máxima " + cpu + "% (Cost Explorer, últimos 14 dias)."
	}
	if v, err := strconv.ParseFloat(amount, 64); err == nil {
		if c, ok := st.Converter().To(v, firstNonEmpty(currency, "USD")); ok {
			f.MonthlySavings = f64(round2(c))
		}
	}
	f.Key = "aws_ce_rightsizing|aws|" + region + "|" + id
	return f, true
}

// ---------- políticas de custo ----------

// Policy é uma regra de governança de custo avaliada no inventário e no IaC.
type Policy struct {
	ID      int64          `json:"id"`
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`  // max_monthly_cost | deny_instance_types | allowed_regions | require_tags | max_volume_gb
	Match   string         `json:"match"` // glob do ambiente (tag Environment); "*" = todos
	Value   map[string]any `json:"value"`
	Action  string         `json:"action"` // warn | block
	Enabled bool           `json:"enabled"`
}

var PolicyKinds = map[string]string{
	"max_monthly_cost":    "Custo mensal máximo por recurso",
	"deny_instance_types": "Tipos de instância proibidos",
	"allowed_regions":     "Regiões permitidas",
	"require_tags":        "Tags obrigatórias",
	"max_volume_gb":       "Tamanho máximo de volume (GB)",
}

type Violation struct {
	PolicyID   int64  `json:"policy_id"`
	Policy     string `json:"policy"`
	Action     string `json:"action"`
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	Message    string `json:"message"`
}

func (p Policy) matchesEnv(env string) bool {
	m := strings.TrimSpace(p.Match)
	if m == "" || m == "*" {
		return true
	}
	if env == "" {
		return false
	}
	for _, pat := range strings.Split(m, ",") {
		if ok, _ := path.Match(lower(pat), lower(env)); ok {
			return true
		}
	}
	return false
}

func valStrings(v any) []string {
	switch x := v.(type) {
	case []any:
		var out []string
		for _, e := range x {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	case string:
		var out []string
		for _, s := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
			out = append(out, strings.TrimSpace(s))
		}
		return out
	}
	return nil
}

func valFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.ReplaceAll(x, ",", "."), 64)
		return f
	case int:
		return float64(x)
	}
	return 0
}

// PolicySubject é o que uma política avalia (recurso do inventário ou do IaC).
type PolicySubject struct {
	ID, Name, Type, SKU, Region, Env string
	Tags                             map[string]string
	SizeGB                           float64
	Monthly                          *float64
}

// CheckPolicy avalia uma política sobre um recurso.
func CheckPolicy(p Policy, s PolicySubject, cur string) (string, bool) {
	if !p.Enabled || !p.matchesEnv(s.Env) {
		return "", false
	}
	switch p.Kind {
	case "max_monthly_cost":
		max := valFloat(p.Value["amount"])
		if s.Monthly != nil && max > 0 && *s.Monthly > max {
			return fmt.Sprintf("custo estimado %s/mês acima do limite de %s", money(*s.Monthly, cur), money(max, cur)), true
		}
	case "deny_instance_types":
		if s.Type != "instance" && s.Type != "database" {
			return "", false
		}
		for _, pat := range valStrings(p.Value["patterns"]) {
			if ok, _ := path.Match(lower(pat), lower(s.SKU)); ok {
				return "tipo " + s.SKU + " proibido (" + pat + ")", true
			}
		}
	case "allowed_regions":
		regs := valStrings(p.Value["regions"])
		if s.Region == "" || len(regs) == 0 {
			return "", false
		}
		for _, r := range regs {
			if strings.EqualFold(r, s.Region) {
				return "", false
			}
		}
		return "região " + s.Region + " não permitida", true
	case "require_tags":
		if miss := MissingTags(s.Tags, valStrings(p.Value["tags"])); len(miss) > 0 {
			return "tags ausentes: " + strings.Join(miss, ", "), true
		}
	case "max_volume_gb":
		max := valFloat(p.Value["gb"])
		if (s.Type == "volume" || s.Type == "database") && max > 0 && s.SizeGB > max {
			return fmt.Sprintf("volume de %.0f GB acima do limite de %.0f GB", s.SizeGB, max), true
		}
	}
	return "", false
}

// EvaluatePolicies aplica as políticas ao inventário.
func EvaluatePolicies(policies []Policy, res []Resource, c Coster) []Violation {
	out := []Violation{}
	for _, r := range res {
		s := PolicySubject{ID: r.ResourceID, Name: displayName(r), Type: r.Type, SKU: r.SKU, Region: r.Region,
			Env: TagLookup(r.Tags, "environment"), Tags: r.Tags, SizeGB: r.SizeGB}
		if m, ok := c.ResourceMonthly(r); ok {
			s.Monthly = &m
		}
		for _, p := range policies {
			if msg, bad := CheckPolicy(p, s, c.Conv.Base); bad {
				out = append(out, Violation{PolicyID: p.ID, Policy: p.Name, Action: p.Action, ResourceID: r.ResourceID, Name: s.Name, Message: msg})
			}
		}
	}
	return out
}
