package finops

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// IaCItem é um recurso do projeto OpenTofu/Terraform com custo estimado.
type IaCItem struct {
	Address     string            `json:"address"`
	Type        string            `json:"type"`
	File        string            `json:"file"`
	Line        int               `json:"line"`
	Provider    string            `json:"provider"`
	Region      string            `json:"region"`
	Count       int               `json:"count"`
	Description string            `json:"description"`
	Monthly     *float64          `json:"monthly"`
	Components  []IaCComponent    `json:"components"`
	Note        string            `json:"note,omitempty"`
	Env         string            `json:"env"`
	Tags        map[string]string `json:"tags"`
}

type IaCComponent struct {
	Label   string   `json:"label"`
	SKU     string   `json:"sku"`
	Qty     float64  `json:"qty"`
	Unit    string   `json:"unit"`
	Monthly *float64 `json:"monthly"`
}

// IaCFix é uma otimização aplicável automaticamente ao código.
type IaCFix struct {
	ID      string   `json:"id"`
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Old     string   `json:"old"`
	New     string   `json:"new"`
	Reason  string   `json:"reason"`
	Address string   `json:"address"`
	Savings *float64 `json:"savings"`
}

type IaCEstimate struct {
	Currency    string      `json:"currency"`
	Region      string      `json:"region"`
	Env         string      `json:"env"`
	Items       []IaCItem   `json:"items"`
	Total       float64     `json:"total"`
	Unpriced    []string    `json:"unpriced"`
	Free        []string    `json:"free"`
	Unsupported []string    `json:"unsupported"`
	Violations  []Violation `json:"violations"`
	Fixes       []IaCFix    `json:"fixes"`
	Notes       []string    `json:"notes"`
}

// IaCOptions ajusta a estimativa.
type IaCOptions struct {
	Vars     map[string]string `json:"vars"`   // sobrescreve defaults de variáveis
	Region   string            `json:"region"` // força a região
	Env      string            `json:"env"`    // ambiente para políticas (quando não houver tag)
	Policies []Policy          `json:"-"`
}

// recursos sem custo fixo (cobrados por uso ou gratuitos)
var iacFree = map[string]bool{
	"aws_vpc": true, "aws_subnet": true, "aws_security_group": true, "aws_internet_gateway": true, "aws_route_table": true,
	"aws_route_table_association": true, "aws_route": true, "aws_vpc_security_group_ingress_rule": true, "aws_vpc_security_group_egress_rule": true,
	"aws_security_group_rule": true, "aws_iam_role": true, "aws_iam_policy": true, "aws_iam_role_policy_attachment": true, "aws_iam_instance_profile": true,
	"aws_key_pair": true, "aws_s3_bucket_public_access_block": true, "aws_s3_bucket_versioning": true, "aws_s3_bucket_server_side_encryption_configuration": true,
	"aws_s3_bucket_policy": true, "aws_s3_bucket_lifecycle_configuration": true, "aws_network_acl": true, "aws_launch_template": true,
	"aws_lb_target_group": true, "aws_lb_listener": true, "aws_lb_target_group_attachment": true, "aws_db_subnet_group": true, "aws_eip_association": true,
	"aws_volume_attachment": true, "aws_vpc_endpoint_route_table_association": true, "aws_cloudwatch_log_group": true,
	"oci_core_vcn": true, "oci_core_subnet": true, "oci_core_internet_gateway": true, "oci_core_route_table": true, "oci_core_security_list": true,
	"oci_core_network_security_group": true, "oci_core_network_security_group_security_rule": true, "oci_core_volume_attachment": true,
	"oci_core_nat_gateway": true, "oci_core_service_gateway": true, "oci_identity_compartment": true, "oci_core_default_route_table": true,
	"random_password": true, "random_id": true, "tls_private_key": true, "null_resource": true, "terraform_data": true, "local_file": true,
}

// variable-based usage resources (custo depende do uso)
var iacUsage = map[string]string{
	"aws_s3_bucket":               "cobrado por GB armazenado, requisições e transferência",
	"aws_lambda_function":         "cobrado por requisições e GB-segundo",
	"aws_dynamodb_table":          "depende do modo de capacidade e do uso",
	"aws_cloudfront_distribution": "cobrado por transferência e requisições",
	"aws_sqs_queue":               "cobrado por requisições",
	"aws_sns_topic":               "cobrado por publicações/entregas",
	"aws_ecr_repository":          "cobrado por GB armazenado",
	"oci_objectstorage_bucket":    "cobrado por GB armazenado e requisições",
	"oci_functions_function":      "cobrado por invocações e GB-segundo",
}

func (sc *hclScope) attrStr(b *hclBody, name, def string) string {
	if a, ok := b.Attrs[name]; ok {
		if s, ok := sc.eval(a.Expr).Str(); ok {
			return s
		}
		return ""
	}
	return def
}

func (sc *hclScope) attrNum(b *hclBody, name string, def float64) (float64, bool) {
	a, ok := b.Attrs[name]
	if !ok {
		return def, true
	}
	return sc.eval(a.Expr).Num()
}

// iacProject é o resultado do parse de todos os arquivos.
type iacProject struct {
	scope     *hclScope
	resources []*hclBlock
	providers []*hclBlock
	varLines  map[string]hclAttr // default de cada variável (para correções)
	varFiles  map[string]string
}

func parseProject(files map[string]string, overrides map[string]string) *iacProject {
	pr := &iacProject{scope: &hclScope{vars: map[string]hclVal{}, locals: map[string]string{}}, varLines: map[string]hclAttr{}, varFiles: map[string]string{}}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var tfvars []*hclBody
	for _, n := range names {
		ext := filepath.Ext(n)
		switch {
		case ext == ".tf":
			body := parseHCL(n, files[n])
			for _, b := range body.Blocks {
				switch b.Type {
				case "variable":
					if len(b.Labels) == 1 {
						if a, ok := b.Body.Attrs["default"]; ok {
							pr.varLines[b.Labels[0]] = hclAttr{Expr: a.Expr, Line: a.Line}
							pr.varFiles[b.Labels[0]] = n
						}
					}
				case "locals":
					for k, a := range b.Body.Attrs {
						pr.scope.locals[k] = a.Expr
					}
				case "resource":
					if len(b.Labels) == 2 {
						pr.resources = append(pr.resources, b)
					}
				case "provider":
					pr.providers = append(pr.providers, b)
				}
			}
		case strings.HasSuffix(n, ".tfvars") || strings.HasSuffix(n, ".auto.tfvars"):
			tfvars = append(tfvars, parseHCL(n, files[n]))
		}
	}
	// defaults → tfvars → overrides (cada um pode referenciar o anterior apenas por literal)
	for k, a := range pr.varLines {
		pr.scope.vars[k] = pr.scope.eval(a.Expr)
	}
	for _, tv := range tfvars {
		for k, a := range tv.Attrs {
			pr.scope.vars[k] = pr.scope.eval(a.Expr)
		}
	}
	for k, v := range overrides {
		v = strings.TrimSpace(v)
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			pr.scope.vars[k] = hclVal{Kind: "number", N: f}
		} else if v == "true" || v == "false" {
			pr.scope.vars[k] = hclVal{Kind: "bool", B: v == "true"}
		} else {
			pr.scope.vars[k] = hclVal{Kind: "string", S: strings.Trim(v, `"`)}
		}
	}
	return pr
}

// EstimateIaC estima o custo mensal de um projeto OpenTofu/Terraform a partir do código
// (sem executar plan), usando o catálogo de preços.
func EstimateIaC(files map[string]string, opt IaCOptions, prices *Prices, st Settings) IaCEstimate {
	pr := parseProject(files, opt.Vars)
	sc := pr.scope
	c := Coster{Prices: prices, Conv: st.Converter()}
	est := IaCEstimate{Currency: st.BaseCurrency, Items: []IaCItem{}, Unpriced: []string{}, Free: []string{}, Unsupported: []string{},
		Violations: []Violation{}, Fixes: []IaCFix{}, Notes: []string{}}

	// região e tags padrão por provedor
	regions := map[string]string{}
	defaultTags := map[string]map[string]string{}
	for _, p := range pr.providers {
		if len(p.Labels) == 0 {
			continue
		}
		if _, ok := p.Body.Attrs["alias"]; ok {
			continue
		}
		regions[p.Labels[0]] = sc.attrStr(p.Body, "region", "")
		for _, dt := range p.Body.blocks("default_tags") {
			if a, ok := dt.Body.Attrs["tags"]; ok {
				defaultTags[p.Labels[0]] = sc.eval(a.Expr).StringMap()
			}
		}
	}
	envVar := ""
	for _, k := range []string{"environment", "env", "ambiente", "stage"} {
		if v, ok := sc.vars[k]; ok {
			if s, ok := v.Str(); ok {
				envVar = s
				break
			}
		}
	}
	for _, rb := range pr.resources {
		typ, name := rb.Labels[0], rb.Labels[1]
		addr := typ + "." + name
		prov := strings.SplitN(typ, "_", 2)[0]
		region := firstNonEmpty(opt.Region, regions[prov])
		if region == "" {
			if v, ok := sc.vars["region"]; ok {
				region, _ = v.Str()
			}
		}
		if region == "" {
			region = "*"
		}
		it := IaCItem{Address: addr, Type: typ, File: rb.File, Line: rb.Line, Provider: prov, Region: region, Count: 1, Components: []IaCComponent{}}
		// count / for_each
		if a, ok := rb.Body.Attrs["count"]; ok {
			if n, ok := sc.eval(a.Expr).Num(); ok {
				it.Count = int(n)
			} else {
				it.Note = "count dinâmico: considerado 1"
			}
		} else if a, ok := rb.Body.Attrs["for_each"]; ok {
			v := sc.eval(a.Expr)
			switch v.Kind {
			case "map":
				it.Count = len(v.M)
			case "list":
				it.Count = len(v.L)
			default:
				it.Note = "for_each dinâmico: considerado 1"
			}
		}
		// tags
		tags := map[string]string{}
		for k, v := range defaultTags[prov] {
			tags[k] = v
		}
		for _, tn := range []string{"tags", "freeform_tags"} {
			if a, ok := rb.Body.Attrs[tn]; ok {
				for k, v := range sc.eval(a.Expr).StringMap() {
					tags[k] = v
				}
			}
		}
		it.Tags = tags
		it.Env = firstNonEmpty(TagLookup(tags, "environment"), envVar, opt.Env)
		if it.Count == 0 {
			continue
		}
		comp := func(label, sku string, qty float64) *float64 {
			cp := IaCComponent{Label: label, SKU: sku, Qty: qty}
			if p, ok := prices.Get(prov, region, sku); ok {
				cp.Unit = p.Unit
			}
			if v, ok := c.Monthly(prov, region, sku, qty); ok {
				cp.Monthly = f64(round2(v * float64(it.Count)))
			}
			it.Components = append(it.Components, cp)
			return cp.Monthly
		}
		b := rb.Body
		known := true
		switch typ {
		case "aws_instance":
			itype := sc.attrStr(b, "instance_type", "")
			it.Description = firstNonEmpty(itype, "(tipo dinâmico)")
			if itype == "" || comp("Instância "+itype, "instance:"+itype, 1) == nil {
				known = false
			}
			for _, rbd := range b.blocks("root_block_device") {
				size, _ := sc.attrNum(rbd.Body, "volume_size", 8)
				vt := sc.attrStr(rbd.Body, "volume_type", "gp3")
				if comp(fmt.Sprintf("Disco raiz %.0f GB %s", size, vt), "volume:"+vt, size) == nil {
					known = false
				}
			}
			if len(b.blocks("root_block_device")) == 0 {
				if comp("Disco raiz 8 GB gp3 (padrão da AMI, assumido)", "volume:gp3", 8) == nil {
					known = false
				}
			}
			for _, eb := range b.blocks("ebs_block_device") {
				size, _ := sc.attrNum(eb.Body, "volume_size", 8)
				vt := sc.attrStr(eb.Body, "volume_type", "gp2")
				if comp(fmt.Sprintf("Volume %.0f GB %s", size, vt), "volume:"+vt, size) == nil {
					known = false
				}
			}
		case "aws_ebs_volume":
			size, _ := sc.attrNum(b, "size", 0)
			vt := sc.attrStr(b, "type", "gp2")
			it.Description = fmt.Sprintf("%.0f GB %s", size, vt)
			if size <= 0 || comp("Volume "+it.Description, "volume:"+vt, size) == nil {
				known = false
			}
		case "aws_eip":
			it.Description = "IPv4 público"
			known = comp("IPv4 público", "public_ip", 1) != nil
		case "aws_nat_gateway":
			it.Description = "NAT Gateway (+ processamento por GB)"
			known = comp("NAT Gateway (horas)", "nat_gateway", 1) != nil
			it.Note = strings.TrimSpace(it.Note + " dados processados são cobrados à parte")
		case "aws_lb", "aws_alb":
			lt := sc.attrStr(b, "load_balancer_type", "application")
			it.Description = "Load balancer " + lt
			known = comp("Load balancer "+lt+" (horas)", "load_balancer:"+lt, 1) != nil
			it.Note = strings.TrimSpace(it.Note + " LCU/NLCU cobradas à parte")
		case "aws_db_instance":
			class := sc.attrStr(b, "instance_class", "")
			stt := sc.attrStr(b, "storage_type", "gp2")
			size, _ := sc.attrNum(b, "allocated_storage", 20)
			multi := 1.0
			if a, ok := b.Attrs["multi_az"]; ok {
				if v := sc.eval(a.Expr); v.Kind == "bool" && v.B {
					multi = 2
				}
			}
			it.Description = fmt.Sprintf("%s, %.0f GB %s%s", firstNonEmpty(class, "(classe dinâmica)"), size, stt, map[bool]string{true: ", Multi-AZ"}[multi == 2])
			if class == "" || comp("Instância "+class, "db:"+class, multi) == nil {
				known = false
			}
			if comp(fmt.Sprintf("Armazenamento %.0f GB %s", size, stt), "db-storage:"+stt, size*multi) == nil {
				known = false
			}
		case "aws_elasticache_cluster":
			nt := sc.attrStr(b, "node_type", "")
			n, _ := sc.attrNum(b, "num_cache_nodes", 1)
			it.Description = fmt.Sprintf("%.0f × %s", n, nt)
			known = nt != "" && comp("Nós "+nt, "cache:"+nt, n) != nil
		case "aws_eks_cluster":
			it.Description = "Plano de controle EKS"
			known = comp("Plano de controle", "eks_cluster", 1) != nil
		case "aws_eks_node_group":
			desired := 1.0
			for _, scb := range b.blocks("scaling_config") {
				desired, _ = sc.attrNum(scb.Body, "desired_size", 1)
			}
			itype := "t3.medium"
			if a, ok := b.Attrs["instance_types"]; ok {
				if v := sc.eval(a.Expr); v.Kind == "list" && len(v.L) > 0 {
					itype, _ = v.L[0].Str()
				}
			}
			disk, _ := sc.attrNum(b, "disk_size", 20)
			it.Description = fmt.Sprintf("%.0f × %s", desired, itype)
			if comp(fmt.Sprintf("%.0f nós %s", desired, itype), "instance:"+itype, desired) == nil {
				known = false
			}
			if comp(fmt.Sprintf("Discos %.0f × %.0f GB", desired, disk), "volume:gp2", desired*disk) == nil {
				known = false
			}
		case "oci_core_instance":
			shape := sc.attrStr(b, "shape", "")
			it.Description = firstNonEmpty(shape, "(shape dinâmico)")
			if strings.HasSuffix(shape, ".Flex") {
				ocpus, mem := 1.0, 16.0
				for _, scb := range b.blocks("shape_config") {
					ocpus, _ = sc.attrNum(scb.Body, "ocpus", 1)
					mem, _ = sc.attrNum(scb.Body, "memory_in_gbs", ocpus*16)
				}
				it.Description = fmt.Sprintf("%s (%.0f OCPU, %.0f GB)", shape, ocpus, mem)
				if comp(fmt.Sprintf("%.0f OCPU", ocpus), "ocpu:"+shape, ocpus) == nil {
					known = false
				}
				if comp(fmt.Sprintf("%.0f GB de memória", mem), "memory:"+shape, mem) == nil {
					known = false
				}
			} else if shape == "" || comp("Shape "+shape, "instance:"+shape, 1) == nil {
				known = false
			}
			boot := 47.0
			for _, sd := range b.blocks("source_details") {
				boot, _ = sc.attrNum(sd.Body, "boot_volume_size_in_gbs", 47)
			}
			if comp(fmt.Sprintf("Boot volume %.0f GB", boot), "volume:storage", boot) == nil {
				known = false
			}
			comp("Performance do boot volume (10 VPU/GB)", "volume:vpu", boot*10)
		case "oci_core_volume":
			size, _ := sc.attrNum(b, "size_in_gbs", 1024)
			vpus, _ := sc.attrNum(b, "vpus_per_gb", 10)
			it.Description = fmt.Sprintf("%.0f GB, %.0f VPU/GB", size, vpus)
			if comp(fmt.Sprintf("Armazenamento %.0f GB", size), "volume:storage", size) == nil {
				known = false
			}
			if vpus > 0 && comp(fmt.Sprintf("Performance %.0f VPU/GB", vpus), "volume:vpu", size*vpus) == nil {
				known = false
			}
		case "oci_load_balancer_load_balancer", "oci_load_balancer":
			it.Description = "Load balancer"
			known = comp("Load balancer (horas)", "load_balancer", 1) != nil
		case "oci_core_public_ip":
			it.Description = "IP público reservado"
			known = comp("IP público reservado", "public_ip", 1) != nil
		default:
			if iacFree[typ] {
				est.Free = append(est.Free, addr)
				continue
			}
			if why, ok := iacUsage[typ]; ok {
				it.Description = "custo por uso"
				it.Note = why
				est.Items = append(est.Items, it)
				continue
			}
			est.Unsupported = append(est.Unsupported, addr)
			continue
		}
		if known {
			var sum float64
			for _, cp := range it.Components {
				if cp.Monthly != nil {
					sum += *cp.Monthly
				}
			}
			it.Monthly = f64(round2(sum))
			est.Total += sum
		} else {
			est.Unpriced = append(est.Unpriced, addr)
			var miss []string
			for _, cp := range it.Components {
				if cp.Monthly == nil {
					miss = append(miss, cp.SKU)
				}
			}
			if len(miss) > 0 {
				it.Note = strings.TrimSpace(it.Note + " sem preço no catálogo: " + strings.Join(miss, ", "))
			}
			// soma parcial dos componentes conhecidos também entra no total (marcado como parcial)
			var sum float64
			for _, cp := range it.Components {
				if cp.Monthly != nil {
					sum += *cp.Monthly
				}
			}
			est.Total += sum
		}
		est.Items = append(est.Items, it)
		if region == "*" && est.Region == "" {
			est.Notes = append(est.Notes, "Região não identificada: usando preços com região \"*\" do catálogo.")
			est.Region = "*"
		} else if est.Region == "" {
			est.Region = region
		}
		if est.Env == "" {
			est.Env = it.Env
		}
		// políticas
		// políticas valem por recurso: com count/for_each, divide o custo pelas instâncias
		var per *float64
		if it.Monthly != nil && it.Count > 0 {
			per = f64(round2(*it.Monthly / float64(it.Count)))
		}
		subj := PolicySubject{ID: addr, Name: addr, Region: region, Env: it.Env, Tags: tags, Monthly: per}
		switch typ {
		case "aws_instance", "oci_core_instance":
			subj.Type, subj.SKU = "instance", strings.Split(it.Description, " ")[0]
		case "aws_db_instance":
			subj.Type, subj.SKU = "database", sc.attrStr(b, "instance_class", "")
			subj.SizeGB, _ = sc.attrNum(b, "allocated_storage", 20)
		case "aws_ebs_volume":
			subj.Type = "volume"
			subj.SizeGB, _ = sc.attrNum(b, "size", 0)
		case "oci_core_volume":
			subj.Type = "volume"
			subj.SizeGB, _ = sc.attrNum(b, "size_in_gbs", 1024)
		}
		if subj.Region == "*" {
			subj.Region = ""
		}
		for _, p := range opt.Policies {
			if msg, bad := CheckPolicy(p, subj, est.Currency); bad {
				est.Violations = append(est.Violations, Violation{PolicyID: p.ID, Policy: p.Name, Action: p.Action, ResourceID: addr, Name: addr, Message: msg})
			}
		}
		est.Fixes = append(est.Fixes, iacFixes(pr, rb, it, c)...)
	}
	est.Total = round2(est.Total)
	if len(est.Unpriced) > 0 {
		est.Notes = append(est.Notes, fmt.Sprintf("%d recurso(s) sem preço completo no catálogo — o total é parcial. Cadastre os SKUs indicados em Fontes & configurações → Catálogo de preços (ou busque no AWS Price List).", len(est.Unpriced)))
	}
	if len(est.Unsupported) > 0 {
		est.Notes = append(est.Notes, "Tipos de recurso sem modelo de custo: "+strings.Join(est.Unsupported, ", ")+".")
	}
	sort.SliceStable(est.Items, func(i, j int) bool {
		a, b := 0.0, 0.0
		if est.Items[i].Monthly != nil {
			a = *est.Items[i].Monthly
		}
		if est.Items[j].Monthly != nil {
			b = *est.Items[j].Monthly
		}
		return a > b
	})
	return est
}

// iacFixes sugere correções automáticas (gp2→gp3, gerações antigas).
func iacFixes(pr *iacProject, rb *hclBlock, it IaCItem, c Coster) []IaCFix {
	var out []IaCFix
	addr := it.Address
	// gp2 → gp3
	sc := pr.scope
	check := func(body *hclBody, attr, sizeAttr, prefix string, defSize float64) {
		a, ok := body.Attrs[attr]
		if !ok {
			return
		}
		if strings.Trim(strings.TrimSpace(a.Expr), `"`) == "gp2" && strings.HasPrefix(strings.TrimSpace(a.Expr), `"`) {
			f := IaCFix{File: rb.File, Line: a.Line, Old: `"gp2"`, New: `"gp3"`, Address: addr,
				Reason: "gp3: menor custo por GB e 3.000 IOPS de base"}
			size, _ := sc.attrNum(body, sizeAttr, defSize)
			gb := size * float64(it.Count)
			if g2, ok := c.Monthly("aws", it.Region, prefix+"gp2", gb); ok {
				if g3, ok := c.Monthly("aws", it.Region, prefix+"gp3", gb); ok && g2 > g3 {
					f.Savings = f64(round2(g2 - g3))
				}
			}
			out = append(out, f)
		}
	}
	switch rb.Labels[0] {
	case "aws_instance":
		for _, x := range rb.Body.blocks("root_block_device") {
			check(x.Body, "volume_type", "volume_size", "volume:", 8)
		}
		for _, x := range rb.Body.blocks("ebs_block_device") {
			check(x.Body, "volume_type", "volume_size", "volume:", 8)
		}
	case "aws_ebs_volume":
		check(rb.Body, "type", "size", "volume:", 0)
	case "aws_db_instance":
		check(rb.Body, "storage_type", "allocated_storage", "db-storage:", 20)
	}
	// gerações antigas (literal no recurso ou no default da variável)
	attr := map[string]string{"aws_instance": "instance_type", "aws_db_instance": "instance_class"}[rb.Labels[0]]
	if attr != "" {
		if a, ok := rb.Body.Attrs[attr]; ok {
			expr := strings.TrimSpace(a.Expr)
			file, line := rb.File, a.Line
			if strings.HasPrefix(expr, "var.") {
				vn := strings.TrimPrefix(expr, "var.")
				if d, ok := pr.varLines[vn]; ok {
					expr, file, line = strings.TrimSpace(d.Expr), pr.varFiles[vn], d.Line
				}
			}
			if strings.HasPrefix(expr, `"`) {
				cur := strings.Trim(expr, `"`)
				if mod := Modernize("aws", cur); mod != "" {
					f := IaCFix{File: file, Line: line, Old: `"` + cur + `"`, New: `"` + mod + `"`, Address: addr,
						Reason: "geração anterior (" + cur + ") → " + mod}
					prefix := "instance:"
					if attr == "instance_class" {
						prefix = "db:"
					}
					if a, ok := c.Monthly("aws", it.Region, prefix+cur, float64(it.Count)); ok {
						if b, ok := c.Monthly("aws", it.Region, prefix+mod, float64(it.Count)); ok && a > b {
							f.Savings = f64(round2(a - b))
						}
					}
					out = append(out, f)
				}
			}
		}
	}
	for i := range out {
		out[i].ID = fmt.Sprintf("%s:%d:%s", out[i].File, out[i].Line, out[i].New)
	}
	return out
}

// ApplyIaCFixes aplica as correções selecionadas e devolve os arquivos alterados.
func ApplyIaCFixes(files map[string]string, fixes []IaCFix) (map[string]string, int) {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	n := 0
	for _, f := range fixes {
		src, ok := out[f.File]
		if !ok || f.Line < 1 {
			continue
		}
		lines := strings.Split(src, "\n")
		if f.Line > len(lines) || !strings.Contains(lines[f.Line-1], f.Old) {
			continue
		}
		lines[f.Line-1] = strings.Replace(lines[f.Line-1], f.Old, f.New, 1)
		out[f.File] = strings.Join(lines, "\n")
		n++
	}
	return out, n
}

// ---------- verificação de custo para CI/CD ----------

type IaCDiffItem struct {
	Address string   `json:"address"`
	Change  string   `json:"change"` // added | removed | changed
	Before  *float64 `json:"before"`
	After   *float64 `json:"after"`
	Delta   float64  `json:"delta"`
	Detail  string   `json:"detail"`
}

type IaCCheck struct {
	Currency   string        `json:"currency"`
	Before     float64       `json:"before"`
	After      float64       `json:"after"`
	Delta      float64       `json:"delta"`
	DeltaPct   *float64      `json:"delta_pct"`
	MaxPct     float64       `json:"max_pct"`
	MaxAbs     float64       `json:"max_abs"`
	Pass       bool          `json:"pass"`
	Reasons    []string      `json:"reasons"`
	Items      []IaCDiffItem `json:"items"`
	Violations []Violation   `json:"violations"`
	Notes      []string      `json:"notes"`
}

// CheckIaC compara a estimativa de duas versões (base e proposta) e decide se a
// mudança passa nos limites de aumento e nas políticas com ação "block".
func CheckIaC(base, head map[string]string, opt IaCOptions, prices *Prices, st Settings) IaCCheck {
	b := EstimateIaC(base, opt, prices, st)
	h := EstimateIaC(head, opt, prices, st)
	ck := IaCCheck{Currency: st.BaseCurrency, Before: b.Total, After: h.Total, Delta: round2(h.Total - b.Total),
		MaxPct: st.IaCMaxIncreasePct, MaxAbs: st.IaCMaxIncreaseAbs, Pass: true, Reasons: []string{}, Items: []IaCDiffItem{},
		Violations: h.Violations, Notes: h.Notes}
	ck.DeltaPct = pct(h.Total, b.Total)
	bm := map[string]IaCItem{}
	for _, it := range b.Items {
		bm[it.Address] = it
	}
	hm := map[string]bool{}
	val := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	for _, it := range h.Items {
		hm[it.Address] = true
		old, ok := bm[it.Address]
		switch {
		case !ok:
			ck.Items = append(ck.Items, IaCDiffItem{Address: it.Address, Change: "added", After: it.Monthly, Delta: round2(val(it.Monthly)), Detail: it.Description})
		case val(old.Monthly) != val(it.Monthly) || old.Description != it.Description || old.Count != it.Count:
			desc := func(x IaCItem) string {
				if x.Count != 1 {
					return fmt.Sprintf("%d× %s", x.Count, x.Description)
				}
				return x.Description
			}
			ck.Items = append(ck.Items, IaCDiffItem{Address: it.Address, Change: "changed", Before: old.Monthly, After: it.Monthly,
				Delta: round2(val(it.Monthly) - val(old.Monthly)), Detail: desc(old) + " → " + desc(it)})
		}
	}
	for _, it := range b.Items {
		if !hm[it.Address] {
			ck.Items = append(ck.Items, IaCDiffItem{Address: it.Address, Change: "removed", Before: it.Monthly, Delta: -round2(val(it.Monthly)), Detail: it.Description})
		}
	}
	sort.Slice(ck.Items, func(i, j int) bool { return math.Abs(ck.Items[i].Delta) > math.Abs(ck.Items[j].Delta) })
	if ck.Delta > 0 {
		// Com os dois limites definidos, bloqueia só quando ambos são excedidos
		// (evita bloquear +100% de US$ 1 ou +2% de um projeto grande).
		overPct := ck.MaxPct > 0 && (ck.DeltaPct == nil || *ck.DeltaPct > ck.MaxPct)
		overAbs := ck.MaxAbs > 0 && ck.Delta > ck.MaxAbs
		var block bool
		switch {
		case ck.MaxPct > 0 && ck.MaxAbs > 0:
			block = overPct && overAbs
		case ck.MaxPct > 0:
			block = overPct
		case ck.MaxAbs > 0:
			block = overAbs
		}
		if block {
			ck.Pass = false
			p := "projeto novo"
			if ck.DeltaPct != nil {
				p = strings.Replace(fmt.Sprintf("+%.1f%%", *ck.DeltaPct), ".", ",", 1)
			}
			ck.Reasons = append(ck.Reasons, fmt.Sprintf("aumento de %s/mês (%s) acima dos limites (+%.0f%% e +%s)", money(ck.Delta, ck.Currency), p, ck.MaxPct, money(ck.MaxAbs, ck.Currency)))
		}
	}
	for _, v := range h.Violations {
		if v.Action == "block" {
			ck.Pass = false
			ck.Reasons = append(ck.Reasons, "política \""+v.Policy+"\": "+v.ResourceID+" — "+v.Message)
		}
	}
	return ck
}

// Markdown gera o resumo para comentário em pull/merge request.
func (ck IaCCheck) Markdown() string {
	var b strings.Builder
	status := "✅ Custo aprovado"
	if !ck.Pass {
		status = "❌ Custo bloqueado"
	}
	b.WriteString("### " + status + " — doomctl FinOps\n\n")
	sign := "+"
	if ck.Delta < 0 {
		sign = ""
	}
	p := "n/d"
	if ck.DeltaPct != nil {
		p = sign + strings.Replace(strconv.FormatFloat(*ck.DeltaPct, 'f', 1, 64), ".", ",", 1) + "%"
	}
	fmt.Fprintf(&b, "| Antes | Depois | Diferença |\n|---:|---:|---:|\n| %s | %s | %s%s (%s) |\n\n", money(ck.Before, ck.Currency), money(ck.After, ck.Currency), sign, money(ck.Delta, ck.Currency), p)
	if len(ck.Reasons) > 0 {
		b.WriteString("**Motivos do bloqueio:**\n")
		for _, r := range ck.Reasons {
			b.WriteString("- " + r + "\n")
		}
		b.WriteString("\n")
	}
	if len(ck.Items) > 0 {
		b.WriteString("| Recurso | Mudança | Δ mensal | Detalhe |\n|---|---|---:|---|\n")
		for _, it := range ck.Items {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", it.Address, map[string]string{"added": "adicionado", "removed": "removido", "changed": "alterado"}[it.Change], money(it.Delta, ck.Currency), strings.ReplaceAll(it.Detail, "|", "/"))
		}
		b.WriteString("\n")
	}
	for _, v := range ck.Violations {
		if v.Action != "block" {
			b.WriteString("⚠️ política \"" + v.Policy + "\": `" + v.ResourceID + "` — " + v.Message + "\n")
		}
	}
	for _, n := range ck.Notes {
		b.WriteString("\n> " + n + "\n")
	}
	b.WriteString(fmt.Sprintf("\n*Limites: +%.0f%% e +%s/mês. Estimativa estática a partir do código e do catálogo de preços (não substitui a fatura).*\n", ck.MaxPct, money(ck.MaxAbs, ck.Currency)))
	return b.String()
}
