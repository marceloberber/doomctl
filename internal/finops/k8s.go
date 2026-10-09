package finops

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------- quantidades Kubernetes ----------

// ParseCPU converte uma quantidade de CPU ("250m", "1", "1.5", "500000n") em núcleos.
func ParseCPU(q string) float64 {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(q, "m"):
		mult, q = 1e-3, q[:len(q)-1]
	case strings.HasSuffix(q, "u"):
		mult, q = 1e-6, q[:len(q)-1]
	case strings.HasSuffix(q, "n"):
		mult, q = 1e-9, q[:len(q)-1]
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0
	}
	return v * mult
}

var memSuffix = []struct {
	s string
	m float64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50}, {"Ei", 1 << 60},
	{"k", 1e3}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"E", 1e18},
}

// ParseMemGiB converte uma quantidade de memória ("512Mi", "1Gi", "1e9", "128974848") em GiB.
func ParseMemGiB(q string) float64 {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0
	}
	for _, s := range memSuffix {
		if strings.HasSuffix(q, s.s) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(q, s.s), 64)
			if err != nil {
				return 0
			}
			return v * s.m / (1 << 30)
		}
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0
	}
	return v / (1 << 30)
}

// ---------- modelo ----------

type k8sMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Labels          map[string]string `json:"labels"`
	OwnerReferences []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"ownerReferences"`
}

type k8sResources struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

type k8sPod struct {
	Metadata k8sMeta `json:"metadata"`
	Spec     struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name      string       `json:"name"`
			Resources k8sResources `json:"resources"`
		} `json:"containers"`
		InitContainers []struct {
			Resources k8sResources `json:"resources"`
		} `json:"initContainers"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type k8sNode struct {
	Metadata k8sMeta `json:"metadata"`
	Spec     struct {
		ProviderID string `json:"providerID"`
	} `json:"spec"`
	Status struct {
		Capacity    map[string]string `json:"capacity"`
		Allocatable map[string]string `json:"allocatable"`
	} `json:"status"`
}

type k8sHPA struct {
	Metadata k8sMeta `json:"metadata"`
	Spec     struct {
		ScaleTargetRef struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"scaleTargetRef"`
	} `json:"spec"`
}

// K8sInput são as saídas de kubectl usadas na análise.
type K8sInput struct {
	Pods           string  `json:"pods"`            // kubectl get pods -A -o json
	Nodes          string  `json:"nodes"`           // kubectl get nodes -o json
	HPAs           string  `json:"hpas"`            // kubectl get hpa -A -o json (opcional)
	Top            string  `json:"top"`             // kubectl top pods -A --no-headers (opcional)
	ClusterName    string  `json:"cluster_name"`    // rótulo
	ClusterMonthly float64 `json:"cluster_monthly"` // custo mensal conhecido do cluster (opcional, moeda base)
}

type K8sNodeCost struct {
	Name         string  `json:"name"`
	InstanceType string  `json:"instance_type"`
	Provider     string  `json:"provider"`
	Region       string  `json:"region"`
	CPU          float64 `json:"cpu"`
	MemGiB       float64 `json:"mem_gib"`
	Monthly      float64 `json:"monthly"`
	PriceSource  string  `json:"price_source"`
	Allocated    float64 `json:"allocated"`
	Idle         float64 `json:"idle"`
	CPURequested float64 `json:"cpu_requested"`
	MemRequested float64 `json:"mem_requested"`
	Spot         bool    `json:"spot"`
}

type K8sWorkload struct {
	Namespace  string   `json:"namespace"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	App        string   `json:"app"`
	Pods       int      `json:"pods"`
	CPURequest float64  `json:"cpu_request"`
	MemRequest float64  `json:"mem_request"`
	CPUUsage   *float64 `json:"cpu_usage"`
	MemUsage   *float64 `json:"mem_usage"`
	Monthly    float64  `json:"monthly"`
	NoRequests int      `json:"no_requests"`
	NoMemLimit int      `json:"no_mem_limit"`
	HasHPA     bool     `json:"has_hpa"`
}

type K8sReport struct {
	Cluster     string        `json:"cluster"`
	Currency    string        `json:"currency"`
	Monthly     float64       `json:"monthly"`
	Allocated   float64       `json:"allocated"`
	Idle        float64       `json:"idle"`
	IdlePct     float64       `json:"idle_pct"`
	PricedNodes int           `json:"priced_nodes"`
	Nodes       []K8sNodeCost `json:"nodes"`
	Namespaces  []Bucket      `json:"namespaces"`
	Apps        []Bucket      `json:"apps"`
	Workloads   []K8sWorkload `json:"workloads"`
	Findings    []Finding     `json:"findings"`
	HasUsage    bool          `json:"has_usage"`
	Notes       []string      `json:"notes"`
	CPUCostHour float64       `json:"cpu_cost_hour"` // média por núcleo
	MemCostHour float64       `json:"mem_cost_hour"` // média por GiB
}

// cpuMemRatio é o peso relativo de 1 vCPU vs 1 GiB na divisão do custo do nó
// (mesma proporção dos preços padrão do OpenCost: ~US$0,0316/vCPU-h e ~US$0,0042/GiB-h).
const cpuMemRatio = 7.5

var cronJobSuffix = regexp.MustCompile(`-\d{6,}$`)

func ownerOf(p k8sPod) (kind, name string) {
	if len(p.Metadata.OwnerReferences) == 0 {
		return "Pod", p.Metadata.Name
	}
	o := p.Metadata.OwnerReferences[0]
	switch o.Kind {
	case "ReplicaSet":
		if h := p.Metadata.Labels["pod-template-hash"]; h != "" && strings.HasSuffix(o.Name, "-"+h) {
			return "Deployment", strings.TrimSuffix(o.Name, "-"+h)
		}
		return "ReplicaSet", o.Name
	case "Job":
		if cronJobSuffix.MatchString(o.Name) {
			return "CronJob", cronJobSuffix.ReplaceAllString(o.Name, "")
		}
		return "Job", o.Name
	}
	return o.Kind, o.Name
}

func appOf(p k8sPod, wl string) string {
	for _, k := range []string{"app.kubernetes.io/name", "app", "k8s-app", "app.kubernetes.io/instance"} {
		if v := p.Metadata.Labels[k]; v != "" {
			return v
		}
	}
	return wl
}

func podRequests(p k8sPod) (cpu, mem float64, noReq, noMemLimit bool) {
	var initCPU, initMem float64
	for _, c := range p.Spec.InitContainers {
		initCPU = math.Max(initCPU, ParseCPU(c.Resources.Requests["cpu"]))
		initMem = math.Max(initMem, ParseMemGiB(c.Resources.Requests["memory"]))
	}
	for _, c := range p.Spec.Containers {
		rc, rm := c.Resources.Requests["cpu"], c.Resources.Requests["memory"]
		if rc == "" || rm == "" {
			noReq = true
		}
		if c.Resources.Limits["memory"] == "" {
			noMemLimit = true
		}
		cpu += ParseCPU(rc)
		mem += ParseMemGiB(rm)
	}
	return math.Max(cpu, initCPU), math.Max(mem, initMem), noReq, noMemLimit
}

// ParseTop lê "kubectl top pods -A --no-headers" → namespace/pod → (núcleos, GiB).
func ParseTop(s string) map[string][2]float64 {
	out := map[string][2]float64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] == "NAMESPACE" {
			continue
		}
		out[f[0]+"/"+f[1]] = [2]float64{ParseCPU(f[2]), ParseMemGiB(f[3])}
	}
	return out
}

func nodeProvider(n k8sNode) string {
	p := n.Spec.ProviderID
	switch {
	case strings.HasPrefix(p, "aws://"):
		return "aws"
	case strings.HasPrefix(p, "oci://") || strings.HasPrefix(p, "ocid1."):
		return "oci"
	case strings.HasPrefix(p, "gce://"):
		return "gcp"
	case strings.HasPrefix(p, "azure://"):
		return "azure"
	}
	return "custom"
}

func label(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

var nonProdNS = regexp.MustCompile(`(?i)(^|[-_.])(dev|develop|test|teste|qa|hml|homolog|staging|stg|sandbox|preview)($|[-_.])`)

// AnalyzeK8s calcula o custo por nó, namespace, aplicação e workload e gera recomendações.
func AnalyzeK8s(in K8sInput, prices *Prices, st Settings) (K8sReport, error) {
	rep := K8sReport{Cluster: in.ClusterName, Currency: st.BaseCurrency, Notes: []string{}, Findings: []Finding{}}
	var podList struct{ Items []k8sPod }
	var nodeList struct{ Items []k8sNode }
	if err := json.Unmarshal([]byte(in.Pods), &podList); err != nil {
		return rep, errors.New("JSON de pods inválido (use kubectl get pods -A -o json): " + err.Error())
	}
	if err := json.Unmarshal([]byte(in.Nodes), &nodeList); err != nil {
		return rep, errors.New("JSON de nós inválido (use kubectl get nodes -o json): " + err.Error())
	}
	if len(nodeList.Items) == 0 {
		return rep, errors.New("nenhum nó encontrado")
	}
	hpa := map[string]bool{}
	if strings.TrimSpace(in.HPAs) != "" {
		var hl struct{ Items []k8sHPA }
		if json.Unmarshal([]byte(in.HPAs), &hl) == nil {
			for _, h := range hl.Items {
				hpa[h.Metadata.Namespace+"/"+h.Spec.ScaleTargetRef.Kind+"/"+h.Spec.ScaleTargetRef.Name] = true
			}
		}
	}
	top := ParseTop(in.Top)
	rep.HasUsage = len(top) > 0
	c := Coster{Prices: prices, Conv: st.Converter()}

	// custo dos nós
	type nodeRate struct{ cpuH, memH float64 }
	rates := map[string]nodeRate{}
	var totalCPU, totalMem float64
	nodes := map[string]*K8sNodeCost{}
	for _, n := range nodeList.Items {
		nc := &K8sNodeCost{Name: n.Metadata.Name, Provider: nodeProvider(n),
			InstanceType: label(n.Metadata.Labels, "node.kubernetes.io/instance-type", "beta.kubernetes.io/instance-type"),
			Region:       label(n.Metadata.Labels, "topology.kubernetes.io/region", "failure-domain.beta.kubernetes.io/region"),
			CPU:          ParseCPU(firstNonEmpty(n.Status.Capacity["cpu"], n.Status.Allocatable["cpu"])),
			MemGiB:       ParseMemGiB(firstNonEmpty(n.Status.Capacity["memory"], n.Status.Allocatable["memory"]))}
		lc := lower(label(n.Metadata.Labels, "eks.amazonaws.com/capacityType", "karpenter.sh/capacity-type", "node.kubernetes.io/lifecycle"))
		nc.Spot = lc == "spot" || lc == "preemptible"
		totalCPU += nc.CPU
		totalMem += nc.MemGiB
		if nc.InstanceType != "" {
			if v, ok := c.Monthly(nc.Provider, nc.Region, "instance:"+nc.InstanceType, 1); ok {
				nc.Monthly, nc.PriceSource = v, "catálogo (instance:"+nc.InstanceType+")"
			}
		}
		if nc.PriceSource == "" {
			cpuP, ok1 := c.Monthly("k8s", "*", "k8s:vcpu", nc.CPU)
			memP, ok2 := c.Monthly("k8s", "*", "k8s:memory_gb", nc.MemGiB)
			if ok1 && ok2 {
				nc.Monthly, nc.PriceSource = cpuP+memP, "catálogo (k8s:vcpu + k8s:memory_gb)"
			}
		}
		nodes[nc.Name] = nc
		rep.Nodes = append(rep.Nodes, *nc)
	}
	priced := 0
	for _, nc := range nodes {
		if nc.PriceSource != "" {
			priced++
		}
	}
	rep.PricedNodes = priced
	if in.ClusterMonthly > 0 {
		// custo informado: substitui os preços e é distribuído pelo peso de CPU/memória
		var w float64
		for _, nc := range nodes {
			w += cpuMemRatio*nc.CPU + nc.MemGiB
		}
		for _, nc := range nodes {
			nc.Monthly, nc.PriceSource = in.ClusterMonthly*(cpuMemRatio*nc.CPU+nc.MemGiB)/w, "custo informado do cluster"
		}
		rep.PricedNodes = len(nodes)
		rep.Notes = append(rep.Notes, "Custo mensal informado distribuído entre os nós pelo peso de CPU e memória.")
	} else if priced < len(nodes) {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d de %d nós sem preço: cadastre instance:<tipo> (região do nó) ou k8s:vcpu e k8s:memory_gb no catálogo, ou informe o custo mensal do cluster.", len(nodes)-priced, len(nodes)))
	}
	for name, nc := range nodes {
		w := cpuMemRatio*nc.CPU + nc.MemGiB
		if w <= 0 || nc.Monthly <= 0 {
			continue
		}
		rates[name] = nodeRate{cpuH: nc.Monthly * cpuMemRatio / w, memH: nc.Monthly / w}
		rep.Monthly += nc.Monthly
	}
	if totalCPU > 0 && rep.Monthly > 0 {
		var w float64 = cpuMemRatio*totalCPU + totalMem
		rep.CPUCostHour = round4(rep.Monthly * cpuMemRatio / w / HoursPerMonth)
		rep.MemCostHour = round4(rep.Monthly / w / HoursPerMonth)
	}

	// pods
	wls := map[string]*K8sWorkload{}
	nsCost, appCost := map[string]float64{}, map[string]float64{}
	for _, p := range podList.Items {
		if p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" || p.Spec.NodeName == "" {
			continue
		}
		kind, name := ownerOf(p)
		ns := p.Metadata.Namespace
		key := ns + "/" + kind + "/" + name
		wl := wls[key]
		if wl == nil {
			wl = &K8sWorkload{Namespace: ns, Kind: kind, Name: name, App: appOf(p, name), HasHPA: hpa[key]}
			wls[key] = wl
		}
		cpu, mem, noReq, noLim := podRequests(p)
		wl.Pods++
		wl.CPURequest += cpu
		wl.MemRequest += mem
		if noReq {
			wl.NoRequests++
		}
		if noLim {
			wl.NoMemLimit++
		}
		ucpu, umem := cpu, mem
		if u, ok := top[ns+"/"+p.Metadata.Name]; ok {
			if wl.CPUUsage == nil {
				wl.CPUUsage, wl.MemUsage = f64(0), f64(0)
			}
			*wl.CPUUsage += u[0]
			*wl.MemUsage += u[1]
			ucpu, umem = math.Max(cpu, u[0]), math.Max(mem, u[1])
		}
		if r, ok := rates[p.Spec.NodeName]; ok {
			cost := ucpu*r.cpuH + umem*r.memH
			wl.Monthly += cost
			nsCost[ns] += cost
			appCost[wl.App] += cost
			if nc := nodes[p.Spec.NodeName]; nc != nil {
				nc.Allocated += cost
				nc.CPURequested += cpu
				nc.MemRequested += mem
			}
		}
	}
	rep.Nodes = rep.Nodes[:0]
	for _, n := range nodeList.Items {
		nc := nodes[n.Metadata.Name]
		nc.Monthly, nc.Allocated = round2(nc.Monthly), round2(math.Min(nc.Allocated, nc.Monthly))
		nc.Idle = round2(math.Max(nc.Monthly-nc.Allocated, 0))
		nc.CPURequested, nc.MemRequested = round2(nc.CPURequested), round2(nc.MemRequested)
		rep.Allocated += nc.Allocated
		rep.Idle += nc.Idle
		rep.Nodes = append(rep.Nodes, *nc)
	}
	rep.Monthly, rep.Allocated, rep.Idle = round2(rep.Monthly), round2(rep.Allocated), round2(rep.Idle)
	if rep.Monthly > 0 {
		rep.IdlePct = round2(rep.Idle / rep.Monthly * 100)
	}
	toBuckets := func(m map[string]float64) []Bucket {
		var out []Bucket
		for k, v := range m {
			b := Bucket{Key: k, Cost: round2(v)}
			if rep.Monthly > 0 {
				b.Share = round2(v / rep.Monthly * 100)
			}
			out = append(out, b)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
		return out
	}
	if rep.Idle > 0 {
		nsCost["(capacidade ociosa)"] = rep.Idle
	}
	rep.Namespaces = toBuckets(nsCost)
	rep.Apps = toBuckets(appCost)
	for _, wl := range wls {
		wl.Monthly = round2(wl.Monthly)
		wl.CPURequest, wl.MemRequest = round3(wl.CPURequest), round3(wl.MemRequest)
		if wl.CPUUsage != nil {
			*wl.CPUUsage, *wl.MemUsage = round3(*wl.CPUUsage), round3(*wl.MemUsage)
		}
		rep.Workloads = append(rep.Workloads, *wl)
	}
	sort.Slice(rep.Workloads, func(i, j int) bool { return rep.Workloads[i].Monthly > rep.Workloads[j].Monthly })

	// recomendações
	cur := st.BaseCurrency
	cpuMonthly := rep.CPUCostHour * HoursPerMonth
	memMonthly := rep.MemCostHour * HoursPerMonth
	for _, wl := range rep.Workloads {
		id := wl.Namespace + "/" + strings.ToLower(wl.Kind) + "/" + wl.Name
		base := Finding{Provider: "k8s", Region: in.ClusterName, ResourceID: id, ResourceType: "workload", Name: id, Currency: cur, Category: "kubernetes"}
		if wl.NoRequests > 0 {
			f := base
			f.Rule, f.Severity, f.Title = "k8s_no_requests", "medium", "Sem requests de CPU/memória"
			f.Detail = fmt.Sprintf("%d de %d pods sem requests: o scheduler não reserva capacidade e o custo não é alocado corretamente (QoS BestEffort).", wl.NoRequests, wl.Pods)
			f.Recommendation = "Defina requests com base no consumo observado (p95) e limits de memória; use LimitRange no namespace como padrão."
			f.Remediation = "kubectl -n " + wl.Namespace + " set resources " + strings.ToLower(wl.Kind) + "/" + wl.Name + " --requests=cpu=100m,memory=128Mi --limits=memory=256Mi  # ajuste aos valores observados"
			f.Key = f.Rule + "|" + id
			rep.Findings = append(rep.Findings, f)
		}
		if wl.CPUUsage != nil && wl.CPURequest >= 0.1 && *wl.CPUUsage < wl.CPURequest*0.3 {
			target := math.Max(*wl.CPUUsage*1.3, 0.05)
			f := base
			f.Rule, f.Severity = "k8s_cpu_overrequest", "medium"
			f.Title = fmt.Sprintf("Request de CPU superdimensionado (%.2f → %.2f núcleos)", wl.CPURequest, target)
			f.Detail = fmt.Sprintf("Uso atual %.3f núcleos para %.3f solicitados (%.0f%%). Medição pontual do metrics-server: confirme com o histórico (Prometheus).", *wl.CPUUsage, wl.CPURequest, *wl.CPUUsage/wl.CPURequest*100)
			f.Recommendation = "Reduza o request de CPU (ou use o VPA em modo recomendação) para liberar capacidade e permitir consolidar nós."
			if cpuMonthly > 0 {
				f.MonthlySavings = f64(round2((wl.CPURequest - target) * cpuMonthly))
				f.SavingsNote = "capacidade liberada; vira economia ao reduzir nós (cluster autoscaler/Karpenter)"
			}
			f.Key = f.Rule + "|" + id
			rep.Findings = append(rep.Findings, f)
		}
		if wl.MemUsage != nil && wl.MemRequest >= 0.25 && *wl.MemUsage < wl.MemRequest*0.4 {
			target := math.Max(*wl.MemUsage*1.3, 0.0625)
			f := base
			f.Rule, f.Severity = "k8s_mem_overrequest", "low"
			f.Title = fmt.Sprintf("Request de memória superdimensionado (%.2f → %.2f GiB)", wl.MemRequest, target)
			f.Detail = fmt.Sprintf("Uso atual %.2f GiB para %.2f GiB solicitados.", *wl.MemUsage, wl.MemRequest)
			f.Recommendation = "Reduza o request de memória com margem para picos; mantenha o limit para evitar OOM em outros pods."
			if memMonthly > 0 {
				f.MonthlySavings = f64(round2((wl.MemRequest - target) * memMonthly))
				f.SavingsNote = "capacidade liberada"
			}
			f.Key = f.Rule + "|" + id
			rep.Findings = append(rep.Findings, f)
		}
		if wl.Kind == "Deployment" && wl.Pods >= 3 && nonProdNS.MatchString(wl.Namespace) && !wl.HasHPA {
			f := base
			f.Rule, f.Severity = "k8s_nonprod_replicas", "low"
			f.Title = fmt.Sprintf("%d réplicas em namespace não produtivo", wl.Pods)
			f.Detail = "Ambientes de desenvolvimento/teste raramente precisam de várias réplicas."
			f.Recommendation = "Reduza para 1 réplica ou use HPA com minReplicas=1; considere escalar para zero fora do horário (KEDA/cron)."
			f.Remediation = "kubectl -n " + wl.Namespace + " scale deployment/" + wl.Name + " --replicas=1"
			if wl.Monthly > 0 {
				f.MonthlySavings = f64(round2(wl.Monthly * float64(wl.Pods-1) / float64(wl.Pods)))
			}
			f.Key = f.Rule + "|" + id
			rep.Findings = append(rep.Findings, f)
		}
		if strings.TrimSpace(in.HPAs) != "" && wl.Kind == "Deployment" && wl.Pods >= 2 && !wl.HasHPA && !nonProdNS.MatchString(wl.Namespace) && !strings.HasPrefix(wl.Namespace, "kube-") {
			f := base
			f.Rule, f.Severity = "k8s_no_hpa", "info"
			f.Title = "Réplicas fixas sem HPA"
			f.Detail = fmt.Sprintf("%d réplicas fixas: a capacidade fica dimensionada para o pico o tempo todo.", wl.Pods)
			f.Recommendation = "Avalie um HorizontalPodAutoscaler (CPU/memória ou métricas customizadas) para acompanhar a demanda."
			f.Key = f.Rule + "|" + id
			rep.Findings = append(rep.Findings, f)
		}
	}
	if rep.Monthly > 0 && rep.IdlePct >= 40 {
		rep.Findings = append(rep.Findings, Finding{Key: "k8s_idle_capacity|" + in.ClusterName, Rule: "k8s_idle_capacity", Category: "kubernetes", Severity: "high",
			Provider: "k8s", Region: in.ClusterName, ResourceID: in.ClusterName, ResourceType: "cluster", Name: firstNonEmpty(in.ClusterName, "cluster"), Currency: cur,
			Title:          fmt.Sprintf("%.0f%% da capacidade do cluster está ociosa", rep.IdlePct),
			Detail:         "Custo dos nós não reservado por nenhum pod (requests).",
			Recommendation: "Ative cluster autoscaler/Karpenter com consolidação, ajuste requests superdimensionados e revise o tipo/tamanho dos nós.",
			MonthlySavings: f64(round2(rep.Idle * 0.5)), SavingsNote: "estimativa: metade da capacidade ociosa"})
	}
	spot := 0
	for _, n := range rep.Nodes {
		if n.Spot {
			spot++
		}
	}
	if spot == 0 && rep.Monthly > 0 && len(rep.Nodes) >= 3 {
		rep.Findings = append(rep.Findings, Finding{Key: "k8s_spot|" + in.ClusterName, Rule: "k8s_spot", Category: "spot", Severity: "low",
			Provider: "k8s", Region: in.ClusterName, ResourceID: in.ClusterName, ResourceType: "cluster", Name: firstNonEmpty(in.ClusterName, "cluster"), Currency: cur,
			Title:          "Nenhum nó Spot/Preemptible",
			Detail:         "Workloads stateless e tolerantes a interrupção podem rodar em capacidade Spot.",
			Recommendation: "Crie um node group Spot (diversificando tipos) com taints/tolerations para workloads elegíveis; mantenha sistemas e stateful em on-demand.",
			MonthlySavings: f64(round2(rep.Allocated * 0.3 * st.SpotDiscount)), SavingsNote: fmt.Sprintf("premissa: 30%% da carga em Spot com %.0f%% de desconto", st.SpotDiscount*100)})
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool { return savingsOf(rep.Findings[i]) > savingsOf(rep.Findings[j]) })
	if !rep.HasUsage {
		rep.Notes = append(rep.Notes, "Sem dados de uso (kubectl top): a análise de requests superdimensionados precisa do metrics-server.")
	}
	return rep, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
