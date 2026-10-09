package agents

import (
	"context"
	"regexp"
	"strings"
)

// Route identifica para onde a pergunta vai.
type Route string

const (
	RouteCloud    Route = "cloud_devops"
	RouteNetSec   Route = "redes_seguranca"
	RouteOffTopic Route = "fora_de_escopo"
)

// RefusalMsg é a resposta exata para perguntas fora de escopo (AGENTS.md §3.1).
const RefusalMsg = "Não posso responder a este tipo de pergunta"

// Margem mínima entre as pontuações para a heurística decidir sozinha.
const tieMargin = 2

type keyword struct {
	term   string
	weight int
}

// Termos já normalizados (minúsculas, sem acento). Pesos: 3 forte, 2 médio, 1 fraco.
var cloudKeywords = []keyword{
	{"docker", 3}, {"dockerfile", 3}, {"compose", 2}, {"podman", 3}, {"buildah", 3},
	{"container", 1}, {"containers", 1}, {"conteiner", 1}, {"conteineres", 1}, {"imagem", 1},
	{"terraform", 3}, {"opentofu", 3}, {"tofu", 3}, {"hcl", 3}, {"tfstate", 3}, {"provider", 1}, {"modulo", 1},
	{"ansible", 3}, {"playbook", 3}, {"role", 1}, {"inventario", 2}, {"awx", 3}, {"cloud-init", 3}, {"packer", 3},
	{"kubernetes", 3}, {"k8s", 3}, {"kubectl", 3}, {"helm", 3}, {"kustomize", 3}, {"pod", 2}, {"pods", 2},
	{"deployment", 2}, {"statefulset", 3}, {"ingress", 2}, {"hpa", 3}, {"pvc", 3}, {"crashloopbackoff", 3},
	{"aws", 2}, {"ec2", 3}, {"s3", 3}, {"iam", 3}, {"ami", 3}, {"ebs", 3}, {"imdsv2", 3}, {"bucket", 2},
	{"oci", 2}, {"oracle cloud", 3}, {"block volume", 3}, {"block volumes", 3}, {"shape", 2}, {"vcn", 1},
	{"finops", 3}, {"custo", 2}, {"custos", 2}, {"fatura", 2}, {"savings plan", 3}, {"reserved", 2},
	{"rightsizing", 3}, {"pipeline", 2}, {"ci/cd", 3}, {"gitlab ci", 3}, {"github actions", 3},
	{"devops", 2}, {"iac", 3}, {"cloud", 1}, {"nuvem", 1}, {"deploy", 2}, {"postgresql", 1}, {"backup", 1},
}

var netsecKeywords = []keyword{
	{"subnet", 3}, {"subnets", 3}, {"sub-rede", 3}, {"sub-redes", 3}, {"subrede", 3}, {"mascara", 3},
	{"cidr", 3}, {"vlsm", 3}, {"broadcast", 3}, {"gateway", 1}, {"ipv4", 2}, {"ipv6", 2},
	{"nat", 3}, {"snat", 3}, {"dnat", 3}, {"masquerade", 3}, {"port forwarding", 3},
	{"firewall", 3}, {"iptables", 3}, {"nftables", 3}, {"firewalld", 3}, {"ufw", 3},
	{"roteamento", 3}, {"rota", 2}, {"rotas", 2}, {"route table", 2}, {"bgp", 3}, {"vlan", 3}, {"vpn", 2},
	{"traceroute", 3}, {"tcpdump", 3}, {"mtr", 2},
	{"seguranca", 2}, {"pentest", 3}, {"red team", 3}, {"vulnerabilidade", 3}, {"vulnerabilidades", 3},
	{"cve", 3}, {"cvss", 3}, {"owasp", 3}, {"ptes", 3}, {"stride", 3}, {"ctf", 3}, {"hardening", 3},
	{"trivy", 3}, {"grype", 3}, {"syft", 3}, {"sbom", 3}, {"cosign", 3}, {"sigstore", 3}, {"slsa", 3},
	{"falco", 3}, {"seccomp", 3}, {"apparmor", 3}, {"selinux", 2}, {"capabilities", 2}, {"rootless", 2},
	{"networkpolicy", 3}, {"pod security", 3}, {"kyverno", 3}, {"gatekeeper", 3}, {"cis benchmark", 3},
	{"supply chain", 3}, {"devsecops", 3},
}

// Termos compartilhados: somam nas duas rotas.
var sharedKeywords = []keyword{
	{"vpc", 2}, {"security group", 2}, {"security groups", 2}, {"nacl", 2}, {"security list", 2}, {"nsg", 2},
}

// Sinais de pedido fora do escopo técnico (poema, receita...). Se aparecerem,
// a heurística não decide sozinha: o classificador LLM é consultado.
var offTopicSignals = []string{
	"poema", "poesia", "receita", "piada", "letra de musica", "horoscopo", "futebol",
	"redacao", "conto", "historia infantil", "namoro", "aposta",
}

var (
	cidrRe  = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}/\d{1,2}\b`)
	maskRe  = regexp.MustCompile(`(^|\s)/(\d|[12]\d|3[0-2])(\s|$)`)
	cleanRe = regexp.MustCompile(`[^a-z0-9/\-\s]+`)
)

var accentReplacer = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "ê", "e", "è", "e", "ë", "e",
	"í", "i", "î", "i", "ì", "i", "ï", "i",
	"ó", "o", "ô", "o", "õ", "o", "ò", "o", "ö", "o",
	"ú", "u", "û", "u", "ù", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)

func normalize(s string) string {
	s = accentReplacer.Replace(strings.ToLower(s))
	return s
}

// tokenText transforma o texto em " tok1 tok2 ... " para casar termos inteiros.
func tokenText(norm string) string {
	t := cleanRe.ReplaceAllString(norm, " ")
	return " " + strings.Join(strings.Fields(t), " ") + " "
}

func score(text string, kws []keyword) int {
	total := 0
	for _, k := range kws {
		if strings.Contains(text, " "+k.term+" ") {
			total += k.weight
		}
	}
	return total
}

// Scores é exposto para debug/testes.
type Scores struct {
	Cloud, NetSec int
	OffSignal     bool
}

func heuristicScores(question string) Scores {
	norm := normalize(question)
	text := tokenText(norm)
	s := Scores{
		Cloud:  score(text, cloudKeywords),
		NetSec: score(text, netsecKeywords),
	}
	shared := score(text, sharedKeywords)
	s.Cloud += shared
	s.NetSec += shared
	if cidrRe.MatchString(norm) || maskRe.MatchString(text) {
		s.NetSec += 3
	}
	for _, sig := range offTopicSignals {
		if strings.Contains(text, " "+sig+" ") {
			s.OffSignal = true
			break
		}
	}
	return s
}

// Classifier é o classificador LLM (temperatura 0). Injetável para testes.
type Classifier func(ctx context.Context, question string) (Route, error)

// Decision é o resultado do roteamento.
type Decision struct {
	Route   Route
	Scores  Scores
	UsedLLM bool
	Reason  string
}

// Decide aplica a heurística e, quando necessário, o classificador LLM (AGENTS.md §7.2).
func Decide(ctx context.Context, question string, classify Classifier) Decision {
	return decide(ctx, question, classify, nil, nil)
}

// extraScore soma peso 3 para cada palavra-chave extra (configurada no navegador) presente.
func extraScore(question string, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	norm := normalize(question)
	text := tokenText(norm)
	total := 0
	for _, t := range terms {
		if termMatch(norm, text, t) {
			total += 3
		}
	}
	return total
}

func decide(ctx context.Context, question string, classify Classifier, extraCloud, extraNetSec []string) Decision {
	s := heuristicScores(question)
	s.Cloud += extraScore(question, extraCloud)
	s.NetSec += extraScore(question, extraNetSec)
	d := Decision{Scores: s}

	if strings.TrimSpace(question) == "" {
		d.Route, d.Reason = RouteOffTopic, "pergunta vazia"
		return d
	}

	zero := s.Cloud == 0 && s.NetSec == 0
	tie := !zero && abs(s.Cloud-s.NetSec) < tieMargin

	if !zero && !tie && !s.OffSignal {
		if s.Cloud > s.NetSec {
			d.Route = RouteCloud
		} else {
			d.Route = RouteNetSec
		}
		d.Reason = "heuristica"
		return d
	}

	d.UsedLLM = true
	r, err := classify(ctx, question)
	if err == nil {
		d.Route, d.Reason = r, "classificador llm"
		return d
	}

	// Falha do classificador.
	switch {
	case zero || s.OffSignal:
		d.Route, d.Reason = RouteOffTopic, "classificador falhou; falha fechada"
	case s.NetSec > s.Cloud:
		d.Route, d.Reason = RouteNetSec, "classificador falhou; maior pontuacao"
	default:
		d.Route, d.Reason = RouteCloud, "classificador falhou; empate -> cloud_devops"
	}
	return d
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
