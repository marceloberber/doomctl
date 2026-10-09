package gen

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Catálogo de templates Kubernetes (beta). Cada template declara seus campos
// (usados para montar o formulário no frontend) e uma função de renderização.

type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | int | bool | select | textarea
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Help     string   `json:"help,omitempty"`
	Required bool     `json:"required,omitempty"`
}

type K8sTemplate struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Fields      []Field `json:"fields"`
	Multi       bool    `json:"multi"` // gera vários arquivos (download .tar.gz)
	render      func(v Values) (map[string]string, error)
}

type Values map[string]string

func (v Values) S(k string) string { return strings.TrimSpace(v[k]) }
func (v Values) I(k string, d int) int {
	n, err := strconv.Atoi(v.S(k))
	if err != nil {
		return d
	}
	return n
}
func (v Values) B(k string) bool { b, _ := strconv.ParseBool(v.S(k)); return b }

var (
	qtyRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|Ki|Mi|Gi|Ti|k|M|G|T)?$`)
	cronRe = regexp.MustCompile(`^(@(yearly|annually|monthly|weekly|daily|hourly)|(\S+\s+){4}\S+)$`)
	hostRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

func meta(name, ns string, labels Map) Map {
	m := Map{}.Set("name", name)
	if ns != "" {
		m = m.Set("namespace", ns)
	}
	return m.SetIf("labels", labels)
}

func appLabels(app string) Map {
	return Map{}.Set("app.kubernetes.io/name", app).Set("app.kubernetes.io/managed-by", "doomctl")
}

func selector(app string) Map { return Map{}.Set("app.kubernetes.io/name", app) }

func doc(parts ...Map) string {
	var out []string
	for _, p := range parts {
		out = append(out, YAML(p))
	}
	return strings.Join(out, "---\n")
}

func needName(v Values, k string) (string, error) {
	n := v.S(k)
	if !NameRe.MatchString(n) {
		return "", fmt.Errorf("%q: use nome DNS-1123 (minúsculas, números e hífen, até 63)", k)
	}
	return n, nil
}

func ns(v Values) (string, error) {
	n := v.S("namespace")
	if n == "" {
		return "default", nil
	}
	if !NameRe.MatchString(n) {
		return "", fmt.Errorf("namespace inválido")
	}
	return n, nil
}

func qty(v Values, k string) (string, error) {
	q := v.S(k)
	if q != "" && !qtyRe.MatchString(q) {
		return "", fmt.Errorf("quantidade inválida em %q: %q (ex.: 250m, 512Mi)", k, q)
	}
	return q, nil
}

func resources(v Values) (Map, error) {
	cr, err := qty(v, "cpu_request")
	if err != nil {
		return nil, err
	}
	cl, err := qty(v, "cpu_limit")
	if err != nil {
		return nil, err
	}
	mr, err := qty(v, "mem_request")
	if err != nil {
		return nil, err
	}
	ml, err := qty(v, "mem_limit")
	if err != nil {
		return nil, err
	}
	req := Map{}.SetIf("cpu", cr).SetIf("memory", mr)
	lim := Map{}.SetIf("cpu", cl).SetIf("memory", ml)
	return Map{}.SetIf("requests", req).SetIf("limits", lim), nil
}

func containerSecCtx(readOnly bool) Map {
	return Map{}.Set("allowPrivilegeEscalation", false).Set("readOnlyRootFilesystem", readOnly).
		Set("runAsNonRoot", true).Set("capabilities", Map{}.Set("drop", List{"ALL"})).
		Set("seccompProfile", Map{}.Set("type", "RuntimeDefault"))
}

func envList(s string) List {
	l := List{}
	for _, kv := range KVLines(s) {
		l = append(l, Map{}.Set("name", kv[0]).Set("value", kv[1]))
	}
	return l
}

func container(v Values, name string, port int) (Map, error) {
	img := v.S("image")
	if !ValidImage(img) {
		return nil, fmt.Errorf("imagem inválida: %q", img)
	}
	res, err := resources(v)
	if err != nil {
		return nil, err
	}
	c := Map{}.Set("name", name).Set("image", img).Set("imagePullPolicy", "IfNotPresent")
	if cmd := v.S("command"); cmd != "" {
		args, err := SplitArgs(cmd)
		if err != nil {
			return nil, err
		}
		l := List{}
		for _, a := range args {
			l = append(l, a)
		}
		c = c.Set("command", l)
	}
	if port > 0 {
		c = c.Set("ports", List{Map{}.Set("name", "http").Set("containerPort", port).Set("protocol", "TCP")})
	}
	c = c.SetIf("env", envList(v.S("env")))
	var envFrom List
	if cm := v.S("configmap"); cm != "" {
		envFrom = append(envFrom, Map{}.Set("configMapRef", Map{}.Set("name", cm)))
	}
	if sec := v.S("secret"); sec != "" {
		envFrom = append(envFrom, Map{}.Set("secretRef", Map{}.Set("name", sec)))
	}
	c = c.SetIf("envFrom", envFrom)
	c = c.SetIf("resources", res)
	if p := v.S("probe_path"); p != "" && port > 0 {
		hg := Map{}.Set("path", p).Set("port", "http")
		c = c.Set("startupProbe", Map{}.Set("httpGet", hg).Set("failureThreshold", 30).Set("periodSeconds", 5))
		c = c.Set("readinessProbe", Map{}.Set("httpGet", hg).Set("periodSeconds", 10).Set("timeoutSeconds", 2))
		c = c.Set("livenessProbe", Map{}.Set("httpGet", hg).Set("periodSeconds", 20).Set("timeoutSeconds", 2).Set("failureThreshold", 3))
	}
	c = c.Set("securityContext", containerSecCtx(v.B("read_only_root")))
	if v.B("read_only_root") {
		c = c.Set("volumeMounts", List{Map{}.Set("name", "tmp").Set("mountPath", "/tmp")})
	}
	return c, nil
}

func podSpec(v Values, c Map) Map {
	ps := Map{}
	if sa := v.S("service_account"); sa != "" {
		ps = ps.Set("serviceAccountName", sa)
	}
	ps = ps.Set("automountServiceAccountToken", v.S("service_account") != "")
	ps = ps.Set("securityContext", Map{}.Set("runAsNonRoot", true).Set("runAsUser", 10001).Set("runAsGroup", 10001).
		Set("fsGroup", 10001).Set("seccompProfile", Map{}.Set("type", "RuntimeDefault")))
	ps = ps.Set("containers", List{c})
	if v.B("read_only_root") {
		ps = ps.Set("volumes", List{Map{}.Set("name", "tmp").Set("emptyDir", Map{})})
	}
	return ps
}

var resourceFields = []Field{
	{Name: "cpu_request", Label: "CPU request", Type: "text", Default: "100m"},
	{Name: "cpu_limit", Label: "CPU limit", Type: "text", Default: "500m"},
	{Name: "mem_request", Label: "Memória request", Type: "text", Default: "128Mi"},
	{Name: "mem_limit", Label: "Memória limit", Type: "text", Default: "256Mi"},
}

func workloadFields(port string) []Field {
	f := []Field{
		{Name: "name", Label: "Nome", Type: "text", Default: "web", Required: true},
		{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
		{Name: "image", Label: "Imagem (tag fixa ou digest)", Type: "text", Default: "nginxinc/nginx-unprivileged:1.29-alpine", Required: true},
		{Name: "replicas", Label: "Réplicas", Type: "int", Default: "2"},
		{Name: "port", Label: "Porta do container", Type: "int", Default: port},
	}
	f = append(f, resourceFields...)
	return append(f,
		Field{Name: "probe_path", Label: "Path das probes HTTP (vazio = sem probes)", Type: "text", Default: "/"},
		Field{Name: "env", Label: "Variáveis (CHAVE=valor por linha)", Type: "textarea"},
		Field{Name: "configmap", Label: "ConfigMap (envFrom)", Type: "text"},
		Field{Name: "secret", Label: "Secret (envFrom)", Type: "text"},
		Field{Name: "service_account", Label: "ServiceAccount", Type: "text"},
		Field{Name: "read_only_root", Label: "readOnlyRootFilesystem (+ /tmp emptyDir)", Type: "bool", Default: "true"},
	)
}

// K8sTemplates devolve o catálogo ordenado por categoria.
func K8sTemplates() []K8sTemplate {
	t := []K8sTemplate{
		// ---------- Workloads ----------
		{ID: "deployment", Name: "Deployment", Category: "Workloads",
			Description: "Aplicação stateless com probes, requests/limits, SecurityContext endurecido e rolling update.",
			Fields: append(workloadFields("8080"),
				Field{Name: "max_surge", Label: "Rolling update: maxSurge", Type: "text", Default: "25%"},
				Field{Name: "max_unavailable", Label: "Rolling update: maxUnavailable", Type: "text", Default: "0"}),
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				c, err := container(v, name, v.I("port", 0))
				if err != nil {
					return nil, err
				}
				d := Map{}.Set("apiVersion", "apps/v1").Set("kind", "Deployment").Set("metadata", meta(name, n, appLabels(name))).
					Set("spec", Map{}.Set("replicas", v.I("replicas", 2)).Set("revisionHistoryLimit", 5).
						Set("selector", Map{}.Set("matchLabels", selector(name))).
						Set("strategy", Map{}.Set("type", "RollingUpdate").Set("rollingUpdate",
							Map{}.Set("maxSurge", intOrStr(def(v.S("max_surge"), "25%"))).Set("maxUnavailable", intOrStr(def(v.S("max_unavailable"), "0"))))).
						Set("template", Map{}.Set("metadata", Map{}.Set("labels", appLabels(name))).Set("spec", podSpec(v, c))))
				return one(name+"-deployment.yaml", doc(d)), nil
			}},
		{ID: "statefulset", Name: "StatefulSet", Category: "Workloads",
			Description: "Bancos e aplicações stateful com Service headless e volumeClaimTemplates.",
			Fields: append(workloadFields("5432"),
				Field{Name: "storage", Label: "Tamanho do volume", Type: "text", Default: "10Gi"},
				Field{Name: "storage_class", Label: "StorageClass (vazio = padrão)", Type: "text"},
				Field{Name: "mount_path", Label: "Montagem do volume", Type: "text", Default: "/data"}),
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				port := v.I("port", 0)
				c, err := container(v, name, port)
				if err != nil {
					return nil, err
				}
				st, err := qty(v, "storage")
				if err != nil || st == "" {
					return nil, fmt.Errorf("tamanho do volume inválido")
				}
				vm := List{Map{}.Set("name", "data").Set("mountPath", def(v.S("mount_path"), "/data"))}
				if v.B("read_only_root") {
					vm = append(vm, Map{}.Set("name", "tmp").Set("mountPath", "/tmp"))
				}
				c = replaceKey(c, "volumeMounts", vm)
				pvcSpec := Map{}.Set("accessModes", List{"ReadWriteOnce"}).SetIf("storageClassName", v.S("storage_class")).
					Set("resources", Map{}.Set("requests", Map{}.Set("storage", st)))
				svc := Map{}.Set("apiVersion", "v1").Set("kind", "Service").Set("metadata", meta(name+"-headless", n, appLabels(name))).
					Set("spec", Map{}.Set("clusterIP", "None").Set("selector", selector(name)).
						Set("ports", List{Map{}.Set("name", "tcp").Set("port", port).Set("targetPort", port)}))
				ss := Map{}.Set("apiVersion", "apps/v1").Set("kind", "StatefulSet").Set("metadata", meta(name, n, appLabels(name))).
					Set("spec", Map{}.Set("serviceName", name+"-headless").Set("replicas", v.I("replicas", 1)).
						Set("selector", Map{}.Set("matchLabels", selector(name))).
						Set("template", Map{}.Set("metadata", Map{}.Set("labels", appLabels(name))).Set("spec", podSpec(v, c))).
						Set("volumeClaimTemplates", List{Map{}.Set("metadata", Map{}.Set("name", "data")).Set("spec", pvcSpec)}))
				return one(name+"-statefulset.yaml", doc(svc, ss)), nil
			}},
		{ID: "job", Name: "Job", Category: "Workloads", Description: "Tarefa batch executada até concluir.",
			Fields: append([]Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "migrate", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "image", Label: "Imagem", Type: "text", Default: "busybox:1.37", Required: true},
				{Name: "command", Label: "Comando", Type: "text", Default: "sh -c 'echo hello'"},
				{Name: "backoff", Label: "backoffLimit", Type: "int", Default: "3"},
				{Name: "ttl", Label: "ttlSecondsAfterFinished", Type: "int", Default: "3600"},
			}, resourceFields...),
			render: func(v Values) (map[string]string, error) {
				name, n, spec, err := batchSpec(v)
				if err != nil {
					return nil, err
				}
				j := Map{}.Set("apiVersion", "batch/v1").Set("kind", "Job").Set("metadata", meta(name, n, appLabels(name))).Set("spec", spec)
				return one(name+"-job.yaml", doc(j)), nil
			}},
		{ID: "cronjob", Name: "CronJob", Category: "Workloads", Description: "Tarefa agendada (sintaxe cron, fuso configurável).",
			Fields: append([]Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "backup", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "schedule", Label: "Agenda (cron)", Type: "text", Default: "0 3 * * *", Required: true},
				{Name: "timezone", Label: "timeZone", Type: "text", Default: "America/Sao_Paulo"},
				{Name: "concurrency", Label: "concurrencyPolicy", Type: "select", Options: []string{"Forbid", "Replace", "Allow"}, Default: "Forbid"},
				{Name: "image", Label: "Imagem", Type: "text", Default: "busybox:1.37", Required: true},
				{Name: "command", Label: "Comando", Type: "text", Default: "sh -c 'date'"},
				{Name: "backoff", Label: "backoffLimit", Type: "int", Default: "2"},
				{Name: "ttl", Label: "ttlSecondsAfterFinished", Type: "int", Default: "86400"},
			}, resourceFields...),
			render: func(v Values) (map[string]string, error) {
				name, n, spec, err := batchSpec(v)
				if err != nil {
					return nil, err
				}
				if !cronRe.MatchString(v.S("schedule")) {
					return nil, fmt.Errorf("agenda cron inválida")
				}
				cj := Map{}.Set("apiVersion", "batch/v1").Set("kind", "CronJob").Set("metadata", meta(name, n, appLabels(name))).
					Set("spec", Map{}.Set("schedule", v.S("schedule")).SetIf("timeZone", v.S("timezone")).
						Set("concurrencyPolicy", def(v.S("concurrency"), "Forbid")).Set("successfulJobsHistoryLimit", 3).
						Set("failedJobsHistoryLimit", 3).Set("jobTemplate", Map{}.Set("spec", spec)))
				return one(name+"-cronjob.yaml", doc(cj)), nil
			}},

		// ---------- Rede ----------
		{ID: "service", Name: "Service", Category: "Rede", Description: "Exposição e comunicação entre workloads.",
			Fields: []Field{
				{Name: "name", Label: "Nome (= label app)", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "type", Label: "Tipo", Type: "select", Options: []string{"ClusterIP", "NodePort", "LoadBalancer"}, Default: "ClusterIP"},
				{Name: "port", Label: "Porta do Service", Type: "int", Default: "80"},
				{Name: "target_port", Label: "Porta do container", Type: "int", Default: "8080"},
				{Name: "node_port", Label: "NodePort (30000–32767, opcional)", Type: "int"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				p := Map{}.Set("name", "http").Set("port", v.I("port", 80)).Set("targetPort", v.I("target_port", 8080)).Set("protocol", "TCP")
				typ := def(v.S("type"), "ClusterIP")
				if np := v.I("node_port", 0); np > 0 && typ != "ClusterIP" {
					if np < 30000 || np > 32767 {
						return nil, fmt.Errorf("nodePort fora da faixa 30000–32767")
					}
					p = p.Set("nodePort", np)
				}
				s := Map{}.Set("apiVersion", "v1").Set("kind", "Service").Set("metadata", meta(name, n, appLabels(name))).
					Set("spec", Map{}.Set("type", typ).Set("selector", selector(name)).Set("ports", List{p}))
				return one(name+"-service.yaml", doc(s)), nil
			}},
		{ID: "ingress", Name: "Ingress", Category: "Rede", Description: "Exposição HTTP/HTTPS externa via Ingress controller.",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "class", Label: "ingressClassName", Type: "text", Default: "nginx"},
				{Name: "host", Label: "Host", Type: "text", Default: "app.exemplo.local", Required: true},
				{Name: "path", Label: "Path", Type: "text", Default: "/"},
				{Name: "service", Label: "Service de destino", Type: "text", Default: "web", Required: true},
				{Name: "service_port", Label: "Porta do Service", Type: "int", Default: "80"},
				{Name: "tls_secret", Label: "Secret TLS (vazio = sem TLS)", Type: "text"},
				{Name: "cert_manager_issuer", Label: "cert-manager ClusterIssuer (opcional)", Type: "text"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				host := strings.ToLower(v.S("host"))
				if !hostRe.MatchString(host) {
					return nil, fmt.Errorf("host inválido")
				}
				md := meta(name, n, appLabels(name))
				if iss := v.S("cert_manager_issuer"); iss != "" {
					md = md.Set("annotations", Map{}.Set("cert-manager.io/cluster-issuer", iss))
				}
				spec := Map{}.SetIf("ingressClassName", v.S("class"))
				if tls := v.S("tls_secret"); tls != "" {
					spec = spec.Set("tls", List{Map{}.Set("hosts", List{host}).Set("secretName", tls)})
				}
				backend := Map{}.Set("service", Map{}.Set("name", v.S("service")).Set("port", Map{}.Set("number", v.I("service_port", 80))))
				spec = spec.Set("rules", List{Map{}.Set("host", host).Set("http", Map{}.Set("paths", List{
					Map{}.Set("path", def(v.S("path"), "/")).Set("pathType", "Prefix").Set("backend", backend)}))})
				ing := Map{}.Set("apiVersion", "networking.k8s.io/v1").Set("kind", "Ingress").Set("metadata", md).Set("spec", spec)
				return one(name+"-ingress.yaml", doc(ing)), nil
			}},
		{ID: "gateway", Name: "Gateway API (Gateway + HTTPRoute)", Category: "Rede",
			Description: "Exposição HTTP/HTTPS com Gateway API (gateway.networking.k8s.io/v1).",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "gateway_class", Label: "gatewayClassName", Type: "text", Default: "cilium", Required: true},
				{Name: "host", Label: "Hostname", Type: "text", Default: "app.exemplo.local", Required: true},
				{Name: "service", Label: "Service de destino", Type: "text", Default: "web", Required: true},
				{Name: "service_port", Label: "Porta do Service", Type: "int", Default: "80"},
				{Name: "tls_secret", Label: "Secret TLS (listener HTTPS, opcional)", Type: "text"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				host := strings.ToLower(v.S("host"))
				if !hostRe.MatchString(host) {
					return nil, fmt.Errorf("hostname inválido")
				}
				listeners := List{Map{}.Set("name", "http").Set("protocol", "HTTP").Set("port", 80).Set("hostname", host)}
				if tls := v.S("tls_secret"); tls != "" {
					listeners = append(listeners, Map{}.Set("name", "https").Set("protocol", "HTTPS").Set("port", 443).Set("hostname", host).
						Set("tls", Map{}.Set("mode", "Terminate").Set("certificateRefs", List{Map{}.Set("kind", "Secret").Set("name", tls)})))
				}
				gw := Map{}.Set("apiVersion", "gateway.networking.k8s.io/v1").Set("kind", "Gateway").Set("metadata", meta(name+"-gw", n, nil)).
					Set("spec", Map{}.Set("gatewayClassName", v.S("gateway_class")).Set("listeners", listeners))
				rt := Map{}.Set("apiVersion", "gateway.networking.k8s.io/v1").Set("kind", "HTTPRoute").Set("metadata", meta(name, n, appLabels(name))).
					Set("spec", Map{}.Set("parentRefs", List{Map{}.Set("name", name+"-gw")}).Set("hostnames", List{host}).
						Set("rules", List{Map{}.Set("matches", List{Map{}.Set("path", Map{}.Set("type", "PathPrefix").Set("value", "/"))}).
							Set("backendRefs", List{Map{}.Set("name", v.S("service")).Set("port", v.I("service_port", 80))})}))
				return one(name+"-gateway.yaml", doc(gw, rt)), nil
			}},
		{ID: "networkpolicy", Name: "NetworkPolicy", Category: "Rede",
			Description: "Default deny no namespace + liberação explícita para o app (pods e porta).",
			Fields: []Field{
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default", Required: true},
				{Name: "app", Label: "App protegido (label app.kubernetes.io/name)", Type: "text", Default: "web", Required: true},
				{Name: "from_app", Label: "App autorizado a acessar", Type: "text", Default: "frontend"},
				{Name: "from_namespace", Label: "Namespace de origem (vazio = mesmo)", Type: "text"},
				{Name: "port", Label: "Porta liberada", Type: "int", Default: "8080"},
				{Name: "allow_dns", Label: "Liberar saída DNS (kube-dns)", Type: "bool", Default: "true"},
			},
			render: func(v Values) (map[string]string, error) {
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				app, err := needName(v, "app")
				if err != nil {
					return nil, err
				}
				deny := Map{}.Set("apiVersion", "networking.k8s.io/v1").Set("kind", "NetworkPolicy").
					Set("metadata", meta("default-deny-all", n, nil)).
					Set("spec", Map{}.Set("podSelector", Map{}).Set("policyTypes", List{"Ingress", "Egress"}))
				peer := Map{}
				if fa := v.S("from_app"); fa != "" {
					peer = peer.Set("podSelector", Map{}.Set("matchLabels", selector(fa)))
				}
				if fn := v.S("from_namespace"); fn != "" {
					peer = peer.Set("namespaceSelector", Map{}.Set("matchLabels", Map{}.Set("kubernetes.io/metadata.name", fn)))
				}
				ports := List{Map{}.Set("protocol", "TCP").Set("port", v.I("port", 8080))}
				ingress := Map{}
				if len(peer) > 0 {
					ingress = ingress.Set("from", List{peer})
				}
				ingress = ingress.Set("ports", ports)
				allow := Map{}.Set("apiVersion", "networking.k8s.io/v1").Set("kind", "NetworkPolicy").
					Set("metadata", meta("allow-"+app, n, nil)).
					Set("spec", Map{}.Set("podSelector", Map{}.Set("matchLabels", selector(app))).Set("policyTypes", List{"Ingress"}).
						Set("ingress", List{ingress}))
				parts := []Map{deny, allow}
				if v.B("allow_dns") {
					dns := Map{}.Set("apiVersion", "networking.k8s.io/v1").Set("kind", "NetworkPolicy").Set("metadata", meta("allow-dns-egress", n, nil)).
						Set("spec", Map{}.Set("podSelector", Map{}).Set("policyTypes", List{"Egress"}).Set("egress", List{
							Map{}.Set("to", List{Map{}.Set("namespaceSelector", Map{}.Set("matchLabels", Map{}.Set("kubernetes.io/metadata.name", "kube-system"))).
								Set("podSelector", Map{}.Set("matchLabels", Map{}.Set("k8s-app", "kube-dns")))}).
								Set("ports", List{Map{}.Set("protocol", "UDP").Set("port", 53), Map{}.Set("protocol", "TCP").Set("port", 53)})}))
					parts = append(parts, dns)
				}
				return one("networkpolicy-"+app+".yaml", doc(parts...)), nil
			}},

		// ---------- Configuração ----------
		{ID: "namespace", Name: "Namespace", Category: "Configuração",
			Description: "Isolamento lógico com Pod Security Admission (labels enforce/audit/warn).",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "app-prod", Required: true},
				{Name: "pss", Label: "Pod Security Standard", Type: "select", Options: []string{"restricted", "baseline", "privileged"}, Default: "restricted"},
				{Name: "quota_cpu", Label: "ResourceQuota: CPU (limits, opcional)", Type: "text", Default: "8"},
				{Name: "quota_mem", Label: "ResourceQuota: memória (limits, opcional)", Type: "text", Default: "16Gi"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				lvl := def(v.S("pss"), "restricted")
				lb := Map{}.Set("pod-security.kubernetes.io/enforce", lvl).Set("pod-security.kubernetes.io/audit", lvl).
					Set("pod-security.kubernetes.io/warn", lvl)
				nsDoc := Map{}.Set("apiVersion", "v1").Set("kind", "Namespace").Set("metadata", Map{}.Set("name", name).Set("labels", lb))
				parts := []Map{nsDoc}
				qc, err := qty(v, "quota_cpu")
				if err != nil {
					return nil, err
				}
				qm, err := qty(v, "quota_mem")
				if err != nil {
					return nil, err
				}
				if qc != "" || qm != "" {
					hard := Map{}.SetIf("limits.cpu", qc).SetIf("limits.memory", qm)
					parts = append(parts, Map{}.Set("apiVersion", "v1").Set("kind", "ResourceQuota").
						Set("metadata", meta("quota", name, nil)).Set("spec", Map{}.Set("hard", hard)))
				}
				return one("namespace-"+name+".yaml", doc(parts...)), nil
			}},
		{ID: "configmap", Name: "ConfigMap", Category: "Configuração", Description: "Configurações não sensíveis.",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "web-config", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "data", Label: "Dados (CHAVE=valor por linha)", Type: "textarea", Default: "LOG_LEVEL=info\nAPP_ENV=production"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				d := Map{}
				for _, kv := range KVLines(v.S("data")) {
					d = d.Set(kv[0], kv[1])
				}
				cm := Map{}.Set("apiVersion", "v1").Set("kind", "ConfigMap").Set("metadata", meta(name, n, nil)).Set("data", d)
				return one(name+"-configmap.yaml", doc(cm)), nil
			}},
		{ID: "secret", Name: "Secret", Category: "Configuração",
			Description: "Credenciais. Prefira Sealed Secrets/External Secrets/SOPS — nunca versione este arquivo em texto claro.",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "web-secret", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "type", Label: "Tipo", Type: "select", Options: []string{"Opaque", "kubernetes.io/dockerconfigjson", "kubernetes.io/tls"}, Default: "Opaque"},
				{Name: "data", Label: "Dados (CHAVE=valor) — use placeholders", Type: "textarea", Default: "DB_USER=<usuario>\nDB_PASSWORD=<senha>"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				d := Map{}
				for _, kv := range KVLines(v.S("data")) {
					d = d.Set(kv[0], kv[1])
				}
				s := Map{}.Set("apiVersion", "v1").Set("kind", "Secret").Set("metadata", meta(name, n, nil)).
					Set("type", def(v.S("type"), "Opaque")).Set("stringData", d)
				return one(name+"-secret.yaml", "# ATENÇÃO: não versione em texto claro. Use kubeseal / External Secrets / SOPS.\n"+doc(s)), nil
			}},

		// ---------- Armazenamento ----------
		{ID: "pvc", Name: "PersistentVolumeClaim", Category: "Armazenamento", Description: "Solicitação de armazenamento persistente.",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "data", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "size", Label: "Tamanho", Type: "text", Default: "10Gi", Required: true},
				{Name: "access_mode", Label: "Access mode", Type: "select", Options: []string{"ReadWriteOnce", "ReadWriteOncePod", "ReadWriteMany", "ReadOnlyMany"}, Default: "ReadWriteOnce"},
				{Name: "storage_class", Label: "StorageClass (vazio = padrão)", Type: "text"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				size, err := qty(v, "size")
				if err != nil || size == "" {
					return nil, fmt.Errorf("tamanho inválido")
				}
				p := Map{}.Set("apiVersion", "v1").Set("kind", "PersistentVolumeClaim").Set("metadata", meta(name, n, nil)).
					Set("spec", Map{}.Set("accessModes", List{def(v.S("access_mode"), "ReadWriteOnce")}).SetIf("storageClassName", v.S("storage_class")).
						Set("resources", Map{}.Set("requests", Map{}.Set("storage", size))))
				return one(name+"-pvc.yaml", doc(p)), nil
			}},
		{ID: "pv", Name: "PersistentVolume (NFS)", Category: "Armazenamento", Description: "Volume estático em NFS (provisionamento manual).",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "nfs-data", Required: true},
				{Name: "size", Label: "Capacidade", Type: "text", Default: "50Gi", Required: true},
				{Name: "access_mode", Label: "Access mode", Type: "select", Options: []string{"ReadWriteMany", "ReadWriteOnce", "ReadOnlyMany"}, Default: "ReadWriteMany"},
				{Name: "reclaim", Label: "Reclaim policy", Type: "select", Options: []string{"Retain", "Delete"}, Default: "Retain"},
				{Name: "nfs_server", Label: "Servidor NFS", Type: "text", Default: "192.168.0.10", Required: true},
				{Name: "nfs_path", Label: "Export NFS", Type: "text", Default: "/srv/nfs/data", Required: true},
				{Name: "storage_class", Label: "StorageClass (para bind com PVC)", Type: "text", Default: "nfs-manual"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				size, err := qty(v, "size")
				if err != nil || size == "" {
					return nil, fmt.Errorf("capacidade inválida")
				}
				p := Map{}.Set("apiVersion", "v1").Set("kind", "PersistentVolume").Set("metadata", Map{}.Set("name", name)).
					Set("spec", Map{}.Set("capacity", Map{}.Set("storage", size)).Set("accessModes", List{def(v.S("access_mode"), "ReadWriteMany")}).
						Set("persistentVolumeReclaimPolicy", def(v.S("reclaim"), "Retain")).SetIf("storageClassName", v.S("storage_class")).
						Set("mountOptions", List{"nfsvers=4.1"}).
						Set("nfs", Map{}.Set("server", v.S("nfs_server")).Set("path", v.S("nfs_path"))))
				return one(name+"-pv.yaml", doc(p)), nil
			}},

		// ---------- Acesso ----------
		{ID: "serviceaccount", Name: "ServiceAccount", Category: "Acesso e RBAC", Description: "Identidade do workload (token não montado por padrão).",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				s := Map{}.Set("apiVersion", "v1").Set("kind", "ServiceAccount").Set("metadata", meta(name, n, appLabels(name))).
					Set("automountServiceAccountToken", false)
				return one(name+"-sa.yaml", doc(s)), nil
			}},
		{ID: "rbac", Name: "RBAC (Role/ClusterRole + Binding)", Category: "Acesso e RBAC", Description: "Permissões mínimas para uma ServiceAccount.",
			Fields: []Field{
				{Name: "name", Label: "Nome", Type: "text", Default: "pod-reader", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "scope", Label: "Escopo", Type: "select", Options: []string{"Role", "ClusterRole"}, Default: "Role"},
				{Name: "api_groups", Label: "apiGroups (vírgula; vazio = core)", Type: "text"},
				{Name: "resources", Label: "Recursos (vírgula)", Type: "text", Default: "pods,pods/log", Required: true},
				{Name: "verbs", Label: "Verbos (vírgula)", Type: "text", Default: "get,list,watch", Required: true},
				{Name: "subject", Label: "ServiceAccount", Type: "text", Default: "web", Required: true},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				verbs := csv(v.S("verbs"))
				for _, vb := range verbs {
					if vb == "*" {
						return nil, fmt.Errorf("verbo '*' não permitido: aplique menor privilégio")
					}
				}
				groups := csv(v.S("api_groups"))
				if len(groups) == 0 {
					groups = List{""}
				}
				rule := Map{}.Set("apiGroups", groups).Set("resources", csv(v.S("resources"))).Set("verbs", verbs)
				scope := def(v.S("scope"), "Role")
				rmd := meta(name, n, nil)
				bindKind := "RoleBinding"
				if scope == "ClusterRole" {
					rmd = Map{}.Set("name", name)
					bindKind = "ClusterRoleBinding"
				}
				role := Map{}.Set("apiVersion", "rbac.authorization.k8s.io/v1").Set("kind", scope).Set("metadata", rmd).Set("rules", List{rule})
				bmd := meta(name, n, nil)
				if bindKind == "ClusterRoleBinding" {
					bmd = Map{}.Set("name", name)
				}
				bind := Map{}.Set("apiVersion", "rbac.authorization.k8s.io/v1").Set("kind", bindKind).Set("metadata", bmd).
					Set("subjects", List{Map{}.Set("kind", "ServiceAccount").Set("name", v.S("subject")).Set("namespace", n)}).
					Set("roleRef", Map{}.Set("apiGroup", "rbac.authorization.k8s.io").Set("kind", scope).Set("name", name))
				return one(name+"-rbac.yaml", doc(role, bind)), nil
			}},

		// ---------- Escala e resiliência ----------
		{ID: "hpa", Name: "HorizontalPodAutoscaler", Category: "Escala e resiliência", Description: "Autoscaling horizontal (autoscaling/v2) por CPU/memória.",
			Fields: []Field{
				{Name: "name", Label: "Deployment alvo", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "min", Label: "Mín. réplicas", Type: "int", Default: "2"},
				{Name: "max", Label: "Máx. réplicas", Type: "int", Default: "10"},
				{Name: "cpu", Label: "Alvo de CPU (%)", Type: "int", Default: "70"},
				{Name: "mem", Label: "Alvo de memória (%, 0 = não usar)", Type: "int", Default: "0"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				mn, mx := v.I("min", 2), v.I("max", 10)
				if mn < 1 || mx < mn {
					return nil, fmt.Errorf("réplicas mín/máx inválidas")
				}
				metrics := List{}
				for _, r := range []struct{ res, key string }{{"cpu", "cpu"}, {"memory", "mem"}} {
					if p := v.I(r.key, 0); p > 0 {
						metrics = append(metrics, Map{}.Set("type", "Resource").Set("resource", Map{}.Set("name", r.res).
							Set("target", Map{}.Set("type", "Utilization").Set("averageUtilization", p))))
					}
				}
				if len(metrics) == 0 {
					return nil, fmt.Errorf("defina ao menos um alvo (CPU ou memória)")
				}
				h := Map{}.Set("apiVersion", "autoscaling/v2").Set("kind", "HorizontalPodAutoscaler").Set("metadata", meta(name, n, nil)).
					Set("spec", Map{}.Set("scaleTargetRef", Map{}.Set("apiVersion", "apps/v1").Set("kind", "Deployment").Set("name", name)).
						Set("minReplicas", mn).Set("maxReplicas", mx).Set("metrics", metrics))
				return one(name+"-hpa.yaml", "# Requer metrics-server no cluster e requests definidos nos containers.\n"+doc(h)), nil
			}},
		{ID: "pdb", Name: "PodDisruptionBudget", Category: "Escala e resiliência", Description: "Garante réplicas mínimas durante drains/upgrades.",
			Fields: []Field{
				{Name: "name", Label: "App", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "min_available", Label: "minAvailable", Type: "text", Default: "1"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				p := Map{}.Set("apiVersion", "policy/v1").Set("kind", "PodDisruptionBudget").Set("metadata", meta(name, n, nil)).
					Set("spec", Map{}.Set("minAvailable", intOrStr(def(v.S("min_available"), "1"))).Set("selector", Map{}.Set("matchLabels", selector(name))))
				return one(name+"-pdb.yaml", doc(p)), nil
			}},

		// ---------- Empacotamento ----------
		{ID: "helm", Name: "Helm Chart", Category: "Empacotamento", Multi: true,
			Description: "Chart parametrizado (Chart.yaml, values.yaml, templates com helpers).",
			Fields: []Field{
				{Name: "name", Label: "Nome do chart", Type: "text", Default: "web", Required: true},
				{Name: "image", Label: "Imagem (repositório)", Type: "text", Default: "nginxinc/nginx-unprivileged", Required: true},
				{Name: "tag", Label: "Tag", Type: "text", Default: "1.29-alpine"},
				{Name: "port", Label: "Porta do container", Type: "int", Default: "8080"},
				{Name: "replicas", Label: "Réplicas", Type: "int", Default: "2"},
			},
			render: renderHelm},
		{ID: "kustomize", Name: "Kustomize (base + overlays)", Category: "Empacotamento", Multi: true,
			Description: "Base com Deployment/Service e overlays dev/prod (réplicas e tag por ambiente).",
			Fields: []Field{
				{Name: "name", Label: "App", Type: "text", Default: "web", Required: true},
				{Name: "image", Label: "Imagem (sem tag)", Type: "text", Default: "nginxinc/nginx-unprivileged", Required: true},
				{Name: "tag_dev", Label: "Tag dev", Type: "text", Default: "1.29-alpine"},
				{Name: "tag_prod", Label: "Tag prod", Type: "text", Default: "1.29-alpine"},
				{Name: "port", Label: "Porta", Type: "int", Default: "8080"},
				{Name: "replicas_prod", Label: "Réplicas em prod", Type: "int", Default: "3"},
			},
			render: renderKustomize},

		// ---------- Observabilidade ----------
		{ID: "observability", Name: "Observabilidade (ServiceMonitor + PrometheusRule)", Category: "Observabilidade",
			Description: "Scrape via Prometheus Operator e alertas de restart/indisponibilidade (kube-state-metrics).",
			Fields: []Field{
				{Name: "name", Label: "App", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "port_name", Label: "Nome da porta do Service", Type: "text", Default: "http"},
				{Name: "path", Label: "Path de métricas", Type: "text", Default: "/metrics"},
				{Name: "release", Label: "Label release do Prometheus", Type: "text", Default: "kube-prometheus-stack"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				lb := Map{}.Set("release", def(v.S("release"), "kube-prometheus-stack"))
				sm := Map{}.Set("apiVersion", "monitoring.coreos.com/v1").Set("kind", "ServiceMonitor").Set("metadata", meta(name, n, lb)).
					Set("spec", Map{}.Set("selector", Map{}.Set("matchLabels", selector(name))).
						Set("endpoints", List{Map{}.Set("port", def(v.S("port_name"), "http")).Set("path", def(v.S("path"), "/metrics")).Set("interval", "30s")}))
				restartExpr := fmt.Sprintf(`increase(kube_pod_container_status_restarts_total{namespace="%s", pod=~"%s-.*"}[15m]) > 3`, n, name)
				availExpr := fmt.Sprintf(`kube_deployment_status_replicas_available{namespace="%s", deployment="%s"} < kube_deployment_spec_replicas{namespace="%s", deployment="%s"}`, n, name, n, name)
				pr := Map{}.Set("apiVersion", "monitoring.coreos.com/v1").Set("kind", "PrometheusRule").Set("metadata", meta(name+"-alerts", n, lb)).
					Set("spec", Map{}.Set("groups", List{Map{}.Set("name", name+".rules").Set("rules", List{
						Map{}.Set("alert", "PodRestartingFrequently").Set("expr", restartExpr).Set("for", "5m").
							Set("labels", Map{}.Set("severity", "warning")).
							Set("annotations", Map{}.Set("summary", "Pods de "+name+" reiniciando com frequência")),
						Map{}.Set("alert", "DeploymentReplicasUnavailable").Set("expr", availExpr).Set("for", "10m").
							Set("labels", Map{}.Set("severity", "critical")).
							Set("annotations", Map{}.Set("summary", "Réplicas indisponíveis em "+name)),
					})}))
				return one(name+"-observability.yaml", "# Requer Prometheus Operator (CRDs monitoring.coreos.com) e kube-state-metrics.\n"+doc(sm, pr)), nil
			}},

		// ---------- CI/CD e GitOps ----------
		{ID: "cicd", Name: "Pipeline CI/CD (build → scan → registry → deploy)", Category: "CI/CD e GitOps",
			Description: "GitHub Actions ou GitLab CI com Trivy (bloqueia CVE crítica com correção) antes do push.",
			Fields: []Field{
				{Name: "platform", Label: "Plataforma", Type: "select", Options: []string{"github", "gitlab"}, Default: "github"},
				{Name: "image", Label: "Imagem no registry (sem tag)", Type: "text", Default: "registry.exemplo.local/time/web", Required: true},
				{Name: "deployment", Label: "Deployment a atualizar", Type: "text", Default: "web"},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "trivy_version", Label: "Versão do Trivy (fixe também o digest)", Type: "text", Default: "0.75.0"},
			},
			render: renderCICD},
		{ID: "gitops", Name: "GitOps (Argo CD ou Flux)", Category: "CI/CD e GitOps",
			Description: "Application do Argo CD ou GitRepository + Kustomization do Flux.",
			Fields: []Field{
				{Name: "tool", Label: "Ferramenta", Type: "select", Options: []string{"argocd", "flux"}, Default: "argocd"},
				{Name: "name", Label: "Nome", Type: "text", Default: "web", Required: true},
				{Name: "repo", Label: "Repositório Git", Type: "text", Default: "https://git.exemplo.local/infra/k8s.git", Required: true},
				{Name: "revision", Label: "Branch/tag", Type: "text", Default: "main"},
				{Name: "path", Label: "Path no repositório", Type: "text", Default: "overlays/prod"},
				{Name: "namespace", Label: "Namespace de destino", Type: "text", Default: "default"},
				{Name: "auto_sync", Label: "Sync automático (prune + selfHeal)", Type: "bool", Default: "true"},
			},
			render: renderGitOps},

		// ---------- Operação ----------
		{ID: "rollout", Name: "Rolling update / rollback (runbook)", Category: "Operação",
			Description: "Script com diff, aplicação, acompanhamento do rollout e rollback.",
			Fields: []Field{
				{Name: "name", Label: "Deployment", Type: "text", Default: "web", Required: true},
				{Name: "namespace", Label: "Namespace", Type: "text", Default: "default"},
				{Name: "container", Label: "Container", Type: "text", Default: "web"},
				{Name: "image", Label: "Nova imagem", Type: "text", Default: "nginxinc/nginx-unprivileged:1.29-alpine"},
			},
			render: func(v Values) (map[string]string, error) {
				name, err := needName(v, "name")
				if err != nil {
					return nil, err
				}
				n, err := ns(v)
				if err != nil {
					return nil, err
				}
				img := v.S("image")
				if !ValidImage(img) {
					return nil, fmt.Errorf("imagem inválida")
				}
				c := def(v.S("container"), name)
				script := fmt.Sprintf(`#!/usr/bin/env bash
# Runbook de rollout — %[1]s (%[2]s). Gerado pelo doomctl.
set -euo pipefail
NS=%[2]s
DEPLOY=%[1]s

# 1) Estado atual e histórico
kubectl -n "$NS" get deploy "$DEPLOY" -o wide
kubectl -n "$NS" rollout history deploy/"$DEPLOY"

# 2) Simulação (server-side) da troca de imagem
kubectl -n "$NS" set image deploy/"$DEPLOY" %[3]s=%[4]s --dry-run=server -o yaml | grep -n "image:"

# 3) Aplicar e registrar o motivo
kubectl -n "$NS" set image deploy/"$DEPLOY" %[3]s=%[4]s
kubectl -n "$NS" annotate deploy/"$DEPLOY" kubernetes.io/change-cause="imagem %[4]s" --overwrite

# 4) Acompanhar (falha se não concluir em 5 min)
if ! kubectl -n "$NS" rollout status deploy/"$DEPLOY" --timeout=5m; then
  echo "Rollout falhou — eventos recentes:"
  kubectl -n "$NS" get events --sort-by=.lastTimestamp | tail -n 20
  # 🛑 Rollback para a revisão anterior:
  kubectl -n "$NS" rollout undo deploy/"$DEPLOY"
  kubectl -n "$NS" rollout status deploy/"$DEPLOY" --timeout=5m
  exit 1
fi
echo "Rollout concluído."
`, name, n, c, img)
				return one("rollout-"+name+".sh", script), nil
			}},
	}
	order := map[string]int{"Workloads": 0, "Rede": 1, "Configuração": 2, "Armazenamento": 3, "Acesso e RBAC": 4,
		"Escala e resiliência": 5, "Empacotamento": 6, "Observabilidade": 7, "CI/CD e GitOps": 8, "Operação": 9}
	sort.SliceStable(t, func(i, j int) bool { return order[t[i].Category] < order[t[j].Category] })
	return t
}

// RenderK8s gera os arquivos do template.
func RenderK8s(id string, v Values) (map[string]string, error) {
	for _, t := range K8sTemplates() {
		if t.ID == id {
			for _, f := range t.Fields {
				if v.S(f.Name) == "" && f.Default != "" {
					if _, set := v[f.Name]; !set {
						v[f.Name] = f.Default
					}
				}
				if f.Required && v.S(f.Name) == "" {
					return nil, fmt.Errorf("campo obrigatório: %s", f.Label)
				}
			}
			return t.render(v)
		}
	}
	return nil, fmt.Errorf("template desconhecido: %q", id)
}

func one(name, content string) map[string]string { return map[string]string{name: content} }

func csv(s string) List {
	l := List{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			l = append(l, p)
		}
	}
	return l
}

func intOrStr(s string) any {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

func replaceKey(m Map, k string, v any) Map {
	for i := range m {
		if m[i].K == k {
			m[i].V = v
			return m
		}
	}
	return m.Set(k, v)
}

func batchSpec(v Values) (string, string, Map, error) {
	name, err := needName(v, "name")
	if err != nil {
		return "", "", nil, err
	}
	n, err := ns(v)
	if err != nil {
		return "", "", nil, err
	}
	c, err := container(v, name, 0)
	if err != nil {
		return "", "", nil, err
	}
	ps := podSpec(v, c).Set("restartPolicy", "Never")
	spec := Map{}.Set("backoffLimit", v.I("backoff", 3)).Set("ttlSecondsAfterFinished", v.I("ttl", 3600)).
		Set("template", Map{}.Set("metadata", Map{}.Set("labels", appLabels(name))).Set("spec", ps))
	return name, n, spec, nil
}

func renderHelm(v Values) (map[string]string, error) {
	name, err := needName(v, "name")
	if err != nil {
		return nil, err
	}
	img := v.S("image")
	if !ValidImage(img) {
		return nil, fmt.Errorf("imagem inválida")
	}
	tag := def(v.S("tag"), "latest")
	port := v.I("port", 8080)
	f := map[string]string{}
	f[name+"/Chart.yaml"] = YAML(Map{}.Set("apiVersion", "v2").Set("name", name).Set("description", "Chart gerado pelo doomctl").
		Set("type", "application").Set("version", "0.1.0").Set("appVersion", tag))
	f[name+"/values.yaml"] = YAML(Map{}.Set("replicaCount", v.I("replicas", 2)).
		Set("image", Map{}.Set("repository", img).Set("tag", tag).Set("pullPolicy", "IfNotPresent")).
		Set("service", Map{}.Set("type", "ClusterIP").Set("port", 80)).
		Set("containerPort", port).
		Set("resources", Map{}.Set("requests", Map{}.Set("cpu", "100m").Set("memory", "128Mi")).Set("limits", Map{}.Set("cpu", "500m").Set("memory", "256Mi"))).
		Set("podSecurityContext", Map{}.Set("runAsNonRoot", true).Set("runAsUser", 10001).Set("fsGroup", 10001).Set("seccompProfile", Map{}.Set("type", "RuntimeDefault"))).
		Set("securityContext", containerSecCtx(true)).
		Set("probes", Map{}.Set("path", "/")))
	f[name+"/.helmignore"] = ".git/\n*.swp\n*.bak\n*.tmp\n.vscode/\n.idea/\n"
	f[name+"/templates/_helpers.tpl"] = `{{- define "` + name + `.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "` + name + `.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "` + name + `.labels" -}}
app.kubernetes.io/name: {{ include "` + name + `.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "` + name + `.selectorLabels" -}}
app.kubernetes.io/name: {{ include "` + name + `.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
`
	f[name+"/templates/deployment.yaml"] = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "` + name + `.fullname" . }}
  labels:
    {{- include "` + name + `.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "` + name + `.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "` + name + `.selectorLabels" . | nindent 8 }}
    spec:
      automountServiceAccountToken: false
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: {{ .Chart.Name }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.containerPort }}
          readinessProbe:
            httpGet:
              path: {{ .Values.probes.path }}
              port: http
          livenessProbe:
            httpGet:
              path: {{ .Values.probes.path }}
              port: http
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir: {}
`
	f[name+"/templates/service.yaml"] = `apiVersion: v1
kind: Service
metadata:
  name: {{ include "` + name + `.fullname" . }}
  labels:
    {{- include "` + name + `.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  selector:
    {{- include "` + name + `.selectorLabels" . | nindent 4 }}
  ports:
    - name: http
      port: {{ .Values.service.port }}
      targetPort: http
`
	f[name+"/README.md"] = "# " + name + "\n\n```bash\nhelm lint " + name + "\nhelm template " + name + " ./" + name + " | kubectl diff -f -\nhelm upgrade --install " + name + " ./" + name + " -n <namespace> --create-namespace\n```\n"
	return f, nil
}

func renderKustomize(v Values) (map[string]string, error) {
	name, err := needName(v, "name")
	if err != nil {
		return nil, err
	}
	img := v.S("image")
	if !ValidImage(img) {
		return nil, fmt.Errorf("imagem inválida")
	}
	port := v.I("port", 8080)
	vals := Values{"name": name, "image": img + ":" + def(v.S("tag_prod"), "latest"), "replicas": "1", "port": strconv.Itoa(port),
		"probe_path": "/", "read_only_root": "true", "cpu_request": "100m", "cpu_limit": "500m", "mem_request": "128Mi", "mem_limit": "256Mi"}
	c, err := container(vals, name, port)
	if err != nil {
		return nil, err
	}
	dep := Map{}.Set("apiVersion", "apps/v1").Set("kind", "Deployment").Set("metadata", Map{}.Set("name", name)).
		Set("spec", Map{}.Set("replicas", 1).Set("selector", Map{}.Set("matchLabels", selector(name))).
			Set("template", Map{}.Set("metadata", Map{}.Set("labels", appLabels(name))).Set("spec", podSpec(vals, c))))
	svc := Map{}.Set("apiVersion", "v1").Set("kind", "Service").Set("metadata", Map{}.Set("name", name)).
		Set("spec", Map{}.Set("selector", selector(name)).Set("ports", List{Map{}.Set("name", "http").Set("port", 80).Set("targetPort", port)}))
	f := map[string]string{
		"base/deployment.yaml":    YAML(dep),
		"base/service.yaml":       YAML(svc),
		"base/kustomization.yaml": YAML(Map{}.Set("apiVersion", "kustomize.config.k8s.io/v1beta1").Set("kind", "Kustomization").Set("resources", List{"deployment.yaml", "service.yaml"}).Set("labels", List{Map{}.Set("pairs", Map{}.Set("app.kubernetes.io/managed-by", "doomctl"))})),
	}
	for _, env := range []struct {
		name, tag string
		reps      int
	}{{"dev", def(v.S("tag_dev"), "latest"), 1}, {"prod", def(v.S("tag_prod"), "latest"), v.I("replicas_prod", 3)}} {
		k := Map{}.Set("apiVersion", "kustomize.config.k8s.io/v1beta1").Set("kind", "Kustomization").
			Set("namespace", name+"-"+env.name).Set("resources", List{"../../base"}).
			Set("images", List{Map{}.Set("name", img).Set("newTag", env.tag)}).
			Set("replicas", List{Map{}.Set("name", name).Set("count", env.reps)})
		f["overlays/"+env.name+"/kustomization.yaml"] = YAML(k)
	}
	f["README.md"] = "# " + name + " (kustomize)\n\n```bash\nkubectl kustomize overlays/prod\nkubectl diff -k overlays/prod\nkubectl apply -k overlays/prod\n```\n"
	return f, nil
}

func renderCICD(v Values) (map[string]string, error) {
	img := v.S("image")
	if !ValidImage(img) {
		return nil, fmt.Errorf("imagem inválida")
	}
	dep, err := needName(v, "deployment")
	if err != nil {
		return nil, err
	}
	n, err := ns(v)
	if err != nil {
		return nil, err
	}
	tv := def(v.S("trivy_version"), "0.75.0")
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(tv) {
		return nil, fmt.Errorf("versão do Trivy inválida")
	}
	if v.S("platform") == "gitlab" {
		y := fmt.Sprintf(`# .gitlab-ci.yml — build → scan → registry → deploy (gerado pelo doomctl)
stages: [build, scan, push, deploy]

variables:
  IMAGE: %[1]s
  TAG: $CI_COMMIT_SHORT_SHA
  TRIVY_IMAGE: aquasec/trivy:%[2]s   # fixe também o digest (@sha256:...)

build:
  stage: build
  image: docker:28
  services: [docker:28-dind]
  script:
    - docker build -t "$IMAGE:$TAG" .
    - docker save "$IMAGE:$TAG" -o image.tar
  artifacts:
    paths: [image.tar]
    expire_in: 1 hour

scan:
  stage: scan
  image:
    name: $TRIVY_IMAGE
    entrypoint: [""]
  script:
    # Falha em CVE CRÍTICA com correção disponível
    - trivy image --input image.tar --exit-code 1 --severity CRITICAL --ignore-unfixed --no-progress
    - trivy image --input image.tar --format cyclonedx --output sbom.cdx.json
  artifacts:
    paths: [sbom.cdx.json]

push:
  stage: push
  image: docker:28
  services: [docker:28-dind]
  script:
    - echo "$REGISTRY_PASSWORD" | docker login -u "$REGISTRY_USER" --password-stdin "${IMAGE%%%%/*}"
    - docker load -i image.tar
    - docker push "$IMAGE:$TAG"
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH

deploy:
  stage: deploy
  image: debian:trixie-slim
  variables:
    KUBECTL_VERSION: v1.37.1   # use a versão compatível com o seu cluster (±1 minor)
  before_script:
    - apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
    - curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"
    - echo "$(curl -fsSL https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum -c -
    - chmod +x /usr/local/bin/kubectl
    - mkdir -p ~/.kube && echo "$KUBECONFIG_B64" | base64 -d > ~/.kube/config && chmod 600 ~/.kube/config
  script:
    - kubectl -n %[3]s set image deploy/%[4]s %[4]s="$IMAGE:$TAG" --dry-run=server
    - kubectl -n %[3]s set image deploy/%[4]s %[4]s="$IMAGE:$TAG"
    - kubectl -n %[3]s rollout status deploy/%[4]s --timeout=5m || (kubectl -n %[3]s rollout undo deploy/%[4]s && exit 1)
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
      when: manual
`, img, tv, n, dep)
		return one(".gitlab-ci.yml", y), nil
	}
	y := fmt.Sprintf(`# .github/workflows/deploy.yml — build → scan → registry → deploy (gerado pelo doomctl)
# Fixe as actions por SHA de commit (supply chain).
name: build-scan-deploy

on:
  push:
    branches: [main]

permissions:
  contents: read

env:
  IMAGE: %[1]s
  TRIVY_IMAGE: aquasec/trivy:%[2]s   # fixe também o digest (@sha256:...)

jobs:
  build-scan-push:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Build
        run: docker build -t "$IMAGE:${{ github.sha }}" .

      - name: Trivy — falha em CVE crítica com correção
        run: |
          docker run --rm -v /var/run/docker.sock:/var/run/docker.sock "$TRIVY_IMAGE" \
            image --exit-code 1 --severity CRITICAL --ignore-unfixed --no-progress "$IMAGE:${{ github.sha }}"

      - name: SBOM (CycloneDX)
        run: |
          docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v "$PWD:/out" "$TRIVY_IMAGE" \
            image --format cyclonedx --output /out/sbom.cdx.json "$IMAGE:${{ github.sha }}"

      - name: Push
        run: |
          echo "${{ secrets.REGISTRY_PASSWORD }}" | docker login -u "${{ secrets.REGISTRY_USER }}" --password-stdin "${IMAGE%%%%/*}"
          docker push "$IMAGE:${{ github.sha }}"

  deploy:
    needs: build-scan-push
    runs-on: ubuntu-latest
    environment: production   # exige aprovação manual se configurado
    steps:
      - name: Kubeconfig
        run: |
          mkdir -p ~/.kube
          echo "${{ secrets.KUBECONFIG_B64 }}" | base64 -d > ~/.kube/config
          chmod 600 ~/.kube/config
      - name: Deploy (dry-run server + rollout)
        run: |
          kubectl -n %[3]s set image deploy/%[4]s %[4]s="$IMAGE:${{ github.sha }}" --dry-run=server
          kubectl -n %[3]s set image deploy/%[4]s %[4]s="$IMAGE:${{ github.sha }}"
          kubectl -n %[3]s rollout status deploy/%[4]s --timeout=5m || { kubectl -n %[3]s rollout undo deploy/%[4]s; exit 1; }
`, img, tv, n, dep)
	return one("deploy.yml", y), nil
}

func renderGitOps(v Values) (map[string]string, error) {
	name, err := needName(v, "name")
	if err != nil {
		return nil, err
	}
	n, err := ns(v)
	if err != nil {
		return nil, err
	}
	repo := v.S("repo")
	if !strings.HasPrefix(repo, "https://") && !strings.HasPrefix(repo, "ssh://") && !strings.HasPrefix(repo, "git@") {
		return nil, fmt.Errorf("repositório deve ser https://, ssh:// ou git@")
	}
	rev, path := def(v.S("revision"), "main"), def(v.S("path"), ".")
	if v.S("tool") == "flux" {
		gr := Map{}.Set("apiVersion", "source.toolkit.fluxcd.io/v1").Set("kind", "GitRepository").Set("metadata", meta(name, "flux-system", nil)).
			Set("spec", Map{}.Set("interval", "1m").Set("url", repo).Set("ref", Map{}.Set("branch", rev)))
		ks := Map{}.Set("apiVersion", "kustomize.toolkit.fluxcd.io/v1").Set("kind", "Kustomization").Set("metadata", meta(name, "flux-system", nil)).
			Set("spec", Map{}.Set("interval", "5m").Set("path", "./"+strings.TrimPrefix(path, "./")).Set("prune", v.B("auto_sync")).
				Set("targetNamespace", n).Set("sourceRef", Map{}.Set("kind", "GitRepository").Set("name", name)).Set("wait", true).Set("timeout", "5m"))
		return one(name+"-flux.yaml", doc(gr, ks)), nil
	}
	spec := Map{}.Set("project", "default").
		Set("source", Map{}.Set("repoURL", repo).Set("targetRevision", rev).Set("path", path)).
		Set("destination", Map{}.Set("server", "https://kubernetes.default.svc").Set("namespace", n))
	sp := Map{}.Set("syncOptions", List{"CreateNamespace=true"})
	if v.B("auto_sync") {
		sp = Map{}.Set("automated", Map{}.Set("prune", true).Set("selfHeal", true)).Set("syncOptions", List{"CreateNamespace=true"})
	}
	spec = spec.Set("syncPolicy", sp)
	app := Map{}.Set("apiVersion", "argoproj.io/v1alpha1").Set("kind", "Application").Set("metadata", meta(name, "argocd", nil)).Set("spec", spec)
	return one(name+"-argocd.yaml", doc(app)), nil
}
