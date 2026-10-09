package finops

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// DemoData são dados sintéticos para conhecer o módulo sem conectar uma nuvem.
type DemoData struct {
	Costs     []CostRow
	Alloc     []AllocRow
	Usage     []UsageRow
	Resources []Resource
	Prices    []Price
	Budgets   []Budget
	Policies  []Policy
	Scenarios []Scenario
}

type demoSvc struct {
	provider, account, service string
	daily                      float64
	weekdayBoost               float64 // fração extra em dias úteis
	growth                     float64 // crescimento diário composto
	regions                    map[string]float64
	startAgo                   int // começa N dias atrás (0 = desde o início)
}

// Demo gera ~120 dias de custos (AWS + OCI), inventário com desperdícios,
// preços de exemplo, orçamentos, políticas e cenários.
func Demo(sourceID int64, now time.Time) DemoData {
	rng := rand.New(rand.NewPCG(42, 2026))
	today := Day(now)
	start := today.AddDate(0, 0, -120)
	awsReg := map[string]float64{"us-east-1": 0.7, "sa-east-1": 0.3}
	ociReg := map[string]float64{"sa-saopaulo-1": 1}
	prodAcc, devAcc, oci := "aws-prod (111111111111)", "aws-dev (222222222222)", "oci-tenancy"
	svcs := []demoSvc{
		{"aws", prodAcc, "Amazon Elastic Compute Cloud - Compute", 118, 0.12, 0.0012, awsReg, 0},
		{"aws", devAcc, "Amazon Elastic Compute Cloud - Compute", 34, 0.35, 0.001, map[string]float64{"us-east-1": 1}, 0},
		{"aws", prodAcc, "EC2 - Other", 26, 0.05, 0.0008, awsReg, 0},
		{"aws", prodAcc, "Amazon Relational Database Service", 46, 0, 0.0005, awsReg, 0},
		{"aws", devAcc, "Amazon Relational Database Service", 12, 0, 0, map[string]float64{"us-east-1": 1}, 0},
		{"aws", prodAcc, "Amazon Simple Storage Service", 11, 0.02, 0.003, map[string]float64{"us-east-1": 1}, 0},
		{"aws", prodAcc, "Amazon Elastic Kubernetes Service", 7.2, 0, 0, awsReg, 0},
		{"aws", prodAcc, "AWS Lambda", 2.6, 0.3, 0.002, map[string]float64{"us-east-1": 1}, 0},
		{"aws", prodAcc, "Amazon CloudFront", 5.4, 0.15, 0.001, map[string]float64{"global": 1}, 0},
		{"aws", prodAcc, "AmazonCloudWatch", 3.8, 0, 0.001, awsReg, 0},
		{"aws", prodAcc, "Amazon ElastiCache", 9.5, 0, 0, map[string]float64{"us-east-1": 1}, 0},
		{"aws", prodAcc, "Amazon SageMaker", 36, 0.1, 0, map[string]float64{"us-east-1": 1}, 3},
		{"oci", oci, "Compute", 38, 0.05, 0.0006, ociReg, 0},
		{"oci", oci, "Block Storage", 7.5, 0, 0.001, ociReg, 0},
		{"oci", oci, "Object Storage", 2.8, 0, 0.002, ociReg, 0},
		{"oci", oci, "Database", 19, 0, 0, ociReg, 0},
		{"oci", oci, "Networking", 1.9, 0.2, 0, ociReg, 0},
	}
	projects := []struct {
		name, team string
		w          float64
	}{{"portal", "Produto Web", 0.34}, {"api", "Backend", 0.3}, {"data", "Dados", 0.2}, {"plataforma", "SRE", 0.11}, {"", "", 0.05}}
	envs := []struct {
		name string
		w    float64
	}{{"prod", 0.64}, {"staging", 0.15}, {"dev", 0.16}, {"", 0.05}}
	devEnvs := []struct {
		name string
		w    float64
	}{{"dev", 0.62}, {"staging", 0.33}, {"", 0.05}}

	var d DemoData
	allocM := map[string]*AllocRow{}
	addAlloc := func(day time.Time, s demoSvc, dim, val string, cost float64) {
		k := dayKey(day) + "|" + s.provider + "|" + s.service + "|" + dim + "|" + val
		a := allocM[k]
		if a == nil {
			a = &AllocRow{Day: day, SourceID: sourceID, Provider: s.provider, Service: s.service, Dim: dim, Value: val, Currency: "USD"}
			allocM[k] = a
		}
		a.Cost += cost
	}
	for t := start; t.Before(today); t = t.AddDate(0, 0, 1) {
		i := float64(t.Sub(start).Hours() / 24)
		ago := int(today.Sub(t).Hours() / 24)
		wd := t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
		for _, s := range svcs {
			if s.startAgo > 0 && ago > s.startAgo {
				continue
			}
			v := s.daily * math.Pow(1+s.growth, i)
			if wd {
				v *= 1 + s.weekdayBoost
			} else {
				v *= 1 - s.weekdayBoost/2
			}
			v *= 1 + (rng.Float64()-0.5)*0.08
			// incidente: NAT Gateway processando tráfego anormal há 5–3 dias
			if s.service == "EC2 - Other" && ago <= 5 && ago >= 3 {
				v += 64 + rng.Float64()*6
			}
			for reg, rw := range s.regions {
				cost := round2(v * rw)
				d.Costs = append(d.Costs, CostRow{Day: t, SourceID: sourceID, Provider: s.provider, Account: s.account, Service: s.service,
					Region: reg, Cost: cost, Currency: "USD"})
			}
			pw := projects
			if s.service == "Amazon SageMaker" {
				pw = pw[2:3] // só "data"
			}
			var tw float64
			for _, p := range pw {
				tw += p.w
			}
			for _, p := range pw {
				c := v * p.w / tw
				addAlloc(t, s, "project", p.name, c)
				addAlloc(t, s, "team", p.team, c)
			}
			ew := envs
			if s.account == devAcc {
				ew = devEnvs
			}
			for _, e := range ew {
				addAlloc(t, s, "environment", e.name, v*e.w)
			}
		}
		// uso (rede/armazenamento) dos últimos 70 dias
		if ago <= 70 {
			nat := 9.0
			if ago <= 5 && ago >= 3 {
				nat += 64
			}
			for _, u := range []struct {
				svc, ut string
				cost    float64
				unit    string
			}{
				{"EC2 - Other", "USE1-NatGateway-Bytes", nat, "GB"},
				{"EC2 - Other", "USE1-NatGateway-Hours", 3.24, "Hrs"},
				{"EC2 - Other", "USE1-DataTransfer-Regional-Bytes", 3.1, "GB"},
				{"EC2 - Other", "USE1-EBS:VolumeUsage.gp2", 5.2, "GB-Mo"},
				{"EC2 - Other", "USE1-EBS:VolumeUsage.gp3", 2.9, "GB-Mo"},
				{"EC2 - Other", "USE1-EBS:SnapshotUsage", 2.4, "GB-Mo"},
				{"EC2 - Other", "USE1-PublicIPv4:InUseAddress", 0.6, "Hrs"},
				{"Amazon Simple Storage Service", "USE1-TimedStorage-ByteHrs", 9.6, "GB-Mo"},
				{"Amazon Simple Storage Service", "USE1-Requests-Tier1", 0.9, "Requests"},
				{"Amazon CloudFront", "US-DataTransfer-Out-Bytes", 4.4, "GB"},
				{"Amazon Elastic Compute Cloud - Compute", "USE1-DataTransfer-Out-Bytes", 2.2, "GB"},
				{"Amazon Elastic Compute Cloud - Compute", "USE1-LoadBalancerUsage", 1.62, "Hrs"},
				{"Object Storage", "Object Storage - Storage", 2.5, "GB-Mo"},
				{"Networking", "Outbound Data Transfer Zone 1", 1.2, "GB"},
			} {
				prov := "aws"
				if !strings.HasPrefix(u.svc, "Amazon") && !strings.HasPrefix(u.svc, "EC2") && !strings.HasPrefix(u.svc, "AWS") {
					prov = "oci"
				}
				c := round2(u.cost * (1 + (rng.Float64()-0.5)*0.1))
				d.Usage = append(d.Usage, UsageRow{Day: t, SourceID: sourceID, Provider: prov, Service: u.svc, UsageType: u.ut,
					Category: ClassifyUsage(prov, u.svc, u.ut), Cost: c, Currency: "USD", Unit: u.unit})
			}
		}
	}
	for _, a := range allocM {
		a.Cost = round2(a.Cost)
		d.Alloc = append(d.Alloc, *a)
	}

	// inventário
	b := func(v bool) *bool { return &v }
	fp := func(v float64) *float64 { return &v }
	tg := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	d.Resources = []Resource{
		{Provider: "aws", Region: "us-east-1", ResourceID: "i-0a1b2c3d4e5f60001", Type: "instance", Name: "portal-web-1", SKU: "m5.2xlarge", State: "running",
			CPUAvg: fp(9.5), CPUMax: fp(27), MemAvg: fp(31), AgeDays: 410, Tags: tg("Name", "portal-web-1", "Project", "portal", "Environment", "prod", "Owner", "web@empresa.com")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "i-0a1b2c3d4e5f60002", Type: "instance", Name: "api-legacy", SKU: "m4.xlarge", State: "running",
			CPUAvg: fp(38), CPUMax: fp(81), AgeDays: 980, Tags: tg("Name", "api-legacy", "Project", "api", "Environment", "prod", "Owner", "backend@empresa.com"),
			Raw: map[string]any{"volumes": []any{"vol-0a00000000000a001"}}},
		{Provider: "aws", Region: "us-east-1", ResourceID: "i-0a1b2c3d4e5f60003", Type: "instance", Name: "relatorios-antigo", SKU: "t2.large", State: "running",
			CPUAvg: fp(0.8), CPUMax: fp(2.1), AgeDays: 700, Tags: tg("Name", "relatorios-antigo", "Project", "data")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "i-0a1b2c3d4e5f60004", Type: "instance", Name: "dev-api-1", SKU: "m5.large", State: "running",
			CPUAvg: fp(22), CPUMax: fp(64), AgeDays: 120, Tags: tg("Name", "dev-api-1", "Project", "api", "Environment", "dev", "Owner", "backend@empresa.com")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "i-0a1b2c3d4e5f60005", Type: "instance", Name: "staging-portal", SKU: "m5.xlarge", State: "running",
			CPUAvg: fp(18), CPUMax: fp(55), AgeDays: 200, Tags: tg("Name", "staging-portal", "Project", "portal", "Environment", "staging", "Owner", "web@empresa.com")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "i-0a1b2c3d4e5f60006", Type: "instance", Name: "etl-worker-1", SKU: "c5.2xlarge", State: "running",
			CPUAvg: fp(46), CPUMax: fp(97), AgeDays: 90, Tags: tg("Name", "etl-worker-1", "Project", "data", "Environment", "prod", "Owner", "dados@empresa.com")},
		{Provider: "aws", Region: "sa-east-1", ResourceID: "i-0a1b2c3d4e5f60007", Type: "instance", Name: "bastion-antigo", SKU: "t3.small", State: "stopped",
			AgeDays: 400, Tags: tg("Name", "bastion-antigo", "Project", "plataforma", "Environment", "prod", "Owner", "sre@empresa.com"),
			Raw: map[string]any{"volumes": []any{"vol-0a00000000000a002"}}},
		{Provider: "aws", Region: "us-east-1", ResourceID: "vol-0a00000000000a001", Type: "volume", SKU: "gp2", SizeGB: 200, State: "in-use", Attached: b(true), AgeDays: 980,
			Tags: tg("Project", "api", "Environment", "prod", "Owner", "backend@empresa.com")},
		{Provider: "aws", Region: "sa-east-1", ResourceID: "vol-0a00000000000a002", Type: "volume", SKU: "gp3", SizeGB: 50, State: "in-use", Attached: b(true), AgeDays: 400,
			Tags: tg("Project", "plataforma", "Environment", "prod", "Owner", "sre@empresa.com")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "vol-0a00000000000a003", Type: "volume", SKU: "gp2", SizeGB: 500, State: "available", Attached: b(false), AgeDays: 160,
			Name: "backup-migracao", Tags: tg("Name", "backup-migracao", "Project", "data")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "vol-0a00000000000a004", Type: "volume", SKU: "io1", SizeGB: 100, State: "available", Attached: b(false), AgeDays: 45,
			Tags: tg("Project", "api", "Environment", "staging", "Owner", "backend@empresa.com")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "eipalloc-0a000000000000001", Type: "public_ip", Name: "54.0.0.10", Attached: b(false), State: "unassociated",
			Tags: tg("Project", "portal")},
		{Provider: "aws", Region: "us-east-1", ResourceID: "snap-0a00000000000a001", Type: "snapshot", SKU: "snapshot", SizeGB: 500, AgeDays: 420, State: "completed",
			Name: "pre-upgrade-2025"},
		{Provider: "aws", Region: "us-east-1", ResourceID: "snap-0a00000000000a002", Type: "snapshot", SKU: "snapshot", SizeGB: 200, AgeDays: 15, State: "completed",
			Name: "diario"},
		{Provider: "aws", Region: "us-east-1", ResourceID: "orders-staging", Type: "database", Name: "orders-staging", SKU: "db.m5.xlarge", SizeGB: 200, State: "available",
			CPUAvg: fp(6), CPUMax: fp(19), AgeDays: 300, Tags: tg("Project", "api", "Environment", "staging", "Owner", "backend@empresa.com"),
			Raw: map[string]any{"engine": "postgres", "multi_az": true, "storage_type": "gp2"}},
		{Provider: "aws", Region: "us-east-1", ResourceID: "orders-prod", Type: "database", Name: "orders-prod", SKU: "db.r5.large", SizeGB: 500, State: "available",
			CPUAvg: fp(41), CPUMax: fp(78), AgeDays: 700, Tags: tg("Project", "api", "Environment", "prod", "Owner", "backend@empresa.com"),
			Raw: map[string]any{"engine": "postgres", "multi_az": true, "storage_type": "gp3"}},
		{Provider: "aws", Region: "us-east-1", ResourceID: "app/old-promo/50dc6c495c0c9188", Type: "load_balancer", Name: "old-promo", SKU: "application", Attached: b(false),
			State: "active", AgeDays: 380, Tags: tg("Project", "portal", "Environment", "prod", "Owner", "web@empresa.com")},
		{Provider: "oci", Region: "sa-saopaulo-1", ResourceID: "ocid1.instance.oc1.sa-saopaulo-1.demo0001", Type: "instance", Name: "erp-app", SKU: "VM.Standard2.2",
			State: "running", CPUAvg: fp(24), CPUMax: fp(61), AgeDays: 900, Tags: tg("Project", "plataforma", "Environment", "prod", "Owner", "sre@empresa.com")},
		{Provider: "oci", Region: "sa-saopaulo-1", ResourceID: "ocid1.volume.oc1.sa-saopaulo-1.demo0002", Type: "volume", SKU: "balanced", SizeGB: 1024,
			State: "available", Attached: b(false), AgeDays: 75, MonthlyCost: fp(43.52), Currency: "USD", Tags: tg("Project", "data", "Environment", "dev")},
	}
	for i := range d.Resources {
		d.Resources[i].SourceID = sourceID
		if d.Resources[i].Currency == "" {
			d.Resources[i].Currency = "USD"
		}
		if d.Resources[i].Raw == nil {
			d.Resources[i].Raw = map[string]any{}
		}
	}

	// preços de exemplo (região "*"): valores ilustrativos, confirme no provedor
	pr := func(prov, sku, unit string, v float64) Price {
		return Price{Provider: prov, Region: "*", SKU: sku, Unit: unit, Price: v, Currency: "USD", Source: "demo"}
	}
	d.Prices = []Price{
		pr("aws", "instance:t3.micro", "hour", 0.0104), pr("aws", "instance:t3.small", "hour", 0.0208), pr("aws", "instance:t3.medium", "hour", 0.0416),
		pr("aws", "instance:t3.large", "hour", 0.0832), pr("aws", "instance:t2.large", "hour", 0.0928), pr("aws", "instance:t2.medium", "hour", 0.0464),
		pr("aws", "instance:m5.large", "hour", 0.096), pr("aws", "instance:m5.xlarge", "hour", 0.192), pr("aws", "instance:m5.2xlarge", "hour", 0.384),
		pr("aws", "instance:m4.large", "hour", 0.10), pr("aws", "instance:m4.xlarge", "hour", 0.20), pr("aws", "instance:c5.xlarge", "hour", 0.17),
		pr("aws", "instance:c5.2xlarge", "hour", 0.34), pr("aws", "instance:r5.large", "hour", 0.126),
		pr("aws", "volume:gp2", "gb-month", 0.10), pr("aws", "volume:gp3", "gb-month", 0.08), pr("aws", "volume:io1", "gb-month", 0.125),
		pr("aws", "snapshot", "gb-month", 0.05), pr("aws", "public_ip", "hour", 0.005), pr("aws", "nat_gateway", "hour", 0.045),
		pr("aws", "load_balancer:application", "hour", 0.0225), pr("aws", "load_balancer:network", "hour", 0.0225),
		pr("aws", "db:db.m5.large", "hour", 0.171), pr("aws", "db:db.m5.xlarge", "hour", 0.342), pr("aws", "db:db.r5.large", "hour", 0.25),
		pr("aws", "db:db.t3.medium", "hour", 0.068), pr("aws", "db-storage:gp2", "gb-month", 0.115), pr("aws", "db-storage:gp3", "gb-month", 0.115),
		pr("aws", "eks_cluster", "hour", 0.10),
		pr("oci", "instance:VM.Standard2.2", "hour", 0.1276), pr("oci", "ocpu:VM.Standard.E5.Flex", "hour", 0.03), pr("oci", "memory:VM.Standard.E5.Flex", "hour", 0.002),
		pr("oci", "ocpu:VM.Standard.E4.Flex", "hour", 0.025), pr("oci", "memory:VM.Standard.E4.Flex", "hour", 0.0015),
		pr("oci", "volume:storage", "gb-month", 0.0255), pr("oci", "volume:vpu", "gb-month", 0.0017),
		pr("k8s", "k8s:vcpu", "hour", 0.0316), pr("k8s", "k8s:memory_gb", "hour", 0.0042),
	}
	// orçamentos de exemplo calibrados pelo gasto dos últimos 30 dias:
	// dois com folga e um com previsão acima do limite
	var total, dev, data float64
	for _, c := range d.Costs {
		if c.Day.Before(today.AddDate(0, 0, -30)) {
			continue
		}
		total += c.Cost
		if c.Account == devAcc {
			dev += c.Cost
		}
	}
	for _, a := range d.Alloc {
		if a.Dim == "project" && a.Value == "data" && !a.Day.Before(today.AddDate(0, 0, -30)) {
			data += a.Cost
		}
	}
	round100 := func(v float64) float64 { return math.Max(100, math.Round(v/100)*100) }
	d.Budgets = []Budget{
		{Name: "[exemplo] Nuvem — total mensal", ScopeType: "all", Amount: round100(total * 1.12), Currency: "USD", Thresholds: []int{80, 100}, Notify: true},
		{Name: "[exemplo] Projeto data", ScopeType: "project", ScopeValue: "data", Amount: round100(data * 0.9), Currency: "USD", Thresholds: []int{80, 100}, Notify: true},
		{Name: "[exemplo] AWS dev", ScopeType: "account", ScopeValue: devAcc, Amount: round100(dev * 1.3), Currency: "USD", Thresholds: []int{80, 100}, Notify: false},
	}
	d.Policies = []Policy{
		{Name: "[exemplo] Produção: até US$ 400/mês por recurso", Kind: "max_monthly_cost", Match: "prod*", Value: map[string]any{"amount": 400.0}, Action: "warn", Enabled: true},
		{Name: "[exemplo] Dev/staging sem instâncias grandes", Kind: "deny_instance_types", Match: "dev,staging", Value: map[string]any{"patterns": []any{"*.2xlarge", "*.4xlarge", "*.metal", "p*", "g*"}}, Action: "block", Enabled: true},
		{Name: "[exemplo] Tags obrigatórias", Kind: "require_tags", Match: "*", Value: map[string]any{"tags": []any{"Project", "Environment", "Owner"}}, Action: "warn", Enabled: true},
	}
	d.Scenarios = []Scenario{
		{Name: "[exemplo] Nós do cluster: 3 → 10", Description: "Quanto custaria aumentar o node group de 3 para 10 nós m5.large?",
			A: []ScenarioItem{{Label: "Nós m5.large", Provider: "aws", Region: "us-east-1", SKU: "instance:m5.large", Qty: 3}, {Label: "Discos dos nós (100 GB gp3)", Provider: "aws", Region: "us-east-1", SKU: "volume:gp3", Qty: 3, Usage: 100}, {Label: "Plano de controle EKS", Provider: "aws", Region: "us-east-1", SKU: "eks_cluster", Qty: 1}},
			B: []ScenarioItem{{Label: "Nós m5.large", Provider: "aws", Region: "us-east-1", SKU: "instance:m5.large", Qty: 10}, {Label: "Discos dos nós (100 GB gp3)", Provider: "aws", Region: "us-east-1", SKU: "volume:gp3", Qty: 10, Usage: 100}, {Label: "Plano de controle EKS", Provider: "aws", Region: "us-east-1", SKU: "eks_cluster", Qty: 1}}},
		{Name: "[exemplo] AWS × OCI — aplicação 4 vCPU/16 GB", Description: "Comparação entre provedores (preços de exemplo).",
			A: []ScenarioItem{{Label: "EC2 m5.xlarge", Provider: "aws", Region: "us-east-1", SKU: "instance:m5.xlarge", Qty: 2}, {Label: "EBS gp3 200 GB", Provider: "aws", Region: "us-east-1", SKU: "volume:gp3", Qty: 2, Usage: 200}},
			B: []ScenarioItem{{Label: "OCI E5.Flex — 2 OCPU", Provider: "oci", Region: "sa-saopaulo-1", SKU: "ocpu:VM.Standard.E5.Flex", Qty: 4}, {Label: "OCI E5.Flex — 16 GB", Provider: "oci", Region: "sa-saopaulo-1", SKU: "memory:VM.Standard.E5.Flex", Qty: 32}, {Label: "Block Volume 200 GB", Provider: "oci", Region: "sa-saopaulo-1", SKU: "volume:storage", Qty: 2, Usage: 200}, {Label: "Performance 10 VPU/GB", Provider: "oci", Region: "sa-saopaulo-1", SKU: "volume:vpu", Qty: 2, Usage: 2000}}},
	}
	return d
}

// DemoK8sInput gera saídas de kubectl de um cluster de exemplo.
func DemoK8sInput() K8sInput {
	type obj = map[string]any
	node := func(name, itype string, cpu, mem string, spot bool) obj {
		lbl := obj{"node.kubernetes.io/instance-type": itype, "topology.kubernetes.io/region": "us-east-1", "kubernetes.io/hostname": name}
		if spot {
			lbl["eks.amazonaws.com/capacityType"] = "SPOT"
		} else {
			lbl["eks.amazonaws.com/capacityType"] = "ON_DEMAND"
		}
		return obj{"metadata": obj{"name": name, "labels": lbl}, "spec": obj{"providerID": "aws:///us-east-1a/i-0" + strings.Repeat("9", 8) + name[len(name)-1:]},
			"status": obj{"capacity": obj{"cpu": cpu, "memory": mem}, "allocatable": obj{"cpu": cpu, "memory": mem}}}
	}
	nodes := obj{"items": []obj{node("ip-10-0-1-11", "m5.xlarge", "4", "16106492Ki", false), node("ip-10-0-1-12", "m5.xlarge", "4", "16106492Ki", false),
		node("ip-10-0-2-13", "m5.xlarge", "4", "16106492Ki", false), node("ip-10-0-2-14", "m5.2xlarge", "8", "32212984Ki", false)}}
	var pods []obj
	var top strings.Builder
	pod := func(ns, name, nodeName, ownerKind, ownerName, hash, app, cpu, mem, memLimit, useCPU, useMem string) {
		lbl := obj{"app": app}
		if hash != "" {
			lbl["pod-template-hash"] = hash
		}
		res := obj{}
		if cpu != "" || mem != "" {
			req := obj{}
			if cpu != "" {
				req["cpu"] = cpu
			}
			if mem != "" {
				req["memory"] = mem
			}
			res["requests"] = req
		}
		if memLimit != "" {
			res["limits"] = obj{"memory": memLimit}
		}
		meta := obj{"name": name, "namespace": ns, "labels": lbl}
		if ownerKind != "" {
			meta["ownerReferences"] = []obj{{"kind": ownerKind, "name": ownerName}}
		}
		pods = append(pods, obj{"metadata": meta, "spec": obj{"nodeName": nodeName, "containers": []obj{{"name": app, "resources": res}}}, "status": obj{"phase": "Running"}})
		if useCPU != "" {
			fmt.Fprintf(&top, "%s %s %s %s\n", ns, name, useCPU, useMem)
		}
	}
	nodesN := []string{"ip-10-0-1-11", "ip-10-0-1-12", "ip-10-0-2-13", "ip-10-0-2-14"}
	for i := 0; i < 4; i++ {
		pod("portal", fmt.Sprintf("portal-web-7c9d8f6b5-%c%c%c%c%c", 'a'+i, 'k', 'x', 'p', 'q'), nodesN[i%4], "ReplicaSet", "portal-web-7c9d8f6b5", "7c9d8f6b5", "portal-web", "1", "2Gi", "2Gi", "120m", "640Mi")
	}
	for i := 0; i < 3; i++ {
		pod("api", fmt.Sprintf("orders-api-5f7b9c-%c%c%c%c%c", 'a'+i, 'm', 'n', 'r', 's'), nodesN[(i+1)%4], "ReplicaSet", "orders-api-5f7b9c", "5f7b9c", "orders-api", "500m", "1Gi", "1Gi", "380m", "720Mi")
	}
	for i := 0; i < 3; i++ {
		pod("api-staging", fmt.Sprintf("orders-api-6d8e1a-%c%c%c%c%c", 'a'+i, 'b', 'c', 'd', 'e'), nodesN[(i+2)%4], "ReplicaSet", "orders-api-6d8e1a", "6d8e1a", "orders-api", "500m", "1Gi", "1Gi", "15m", "210Mi")
	}
	pod("data", "spark-driver-0", nodesN[3], "StatefulSet", "spark-driver", "", "spark", "2", "8Gi", "8Gi", "1850m", "6900Mi")
	pod("data", "relatorio-noturno-29123456-x7k2p", nodesN[3], "Job", "relatorio-noturno-29123456", "", "relatorio", "", "", "", "300m", "400Mi")
	pod("legacy", "cache-sem-dono", nodesN[0], "", "", "", "redis", "", "", "", "20m", "90Mi")
	pod("kube-system", "coredns-5d78c9869d-abcde", nodesN[0], "ReplicaSet", "coredns-5d78c9869d", "5d78c9869d", "coredns", "100m", "70Mi", "170Mi", "4m", "22Mi")
	pod("kube-system", "coredns-5d78c9869d-fghij", nodesN[1], "ReplicaSet", "coredns-5d78c9869d", "5d78c9869d", "coredns", "100m", "70Mi", "170Mi", "3m", "20Mi")
	pod("monitoring", "prometheus-0", nodesN[2], "StatefulSet", "prometheus", "", "prometheus", "1", "4Gi", "6Gi", "420m", "3100Mi")
	pj, _ := json.Marshal(obj{"items": pods})
	nj, _ := json.Marshal(nodes)
	hj, _ := json.Marshal(obj{"items": []obj{{"metadata": obj{"name": "orders-api", "namespace": "api"}, "spec": obj{"scaleTargetRef": obj{"kind": "Deployment", "name": "orders-api"}}}}})
	return K8sInput{Pods: string(pj), Nodes: string(nj), HPAs: string(hj), Top: top.String(), ClusterName: "eks-demo"}
}
