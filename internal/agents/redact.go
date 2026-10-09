package agents

import (
	"regexp"
	"strings"
)

// Redação de segredos antes de enviar texto a provedores externos.

type redactRule struct {
	name string
	re   *regexp.Regexp
	repl string
}

var redactRules = []redactRule{
	{"chave privada", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "[REDIGIDO: chave privada]"},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), "[REDIGIDO: AWS access key]"},
	{"AWS secret", regexp.MustCompile(`(?i)(aws_secret_access_key\s*[=:]\s*["']?)[A-Za-z0-9/+=]{40}`), "${1}[REDIGIDO]"},
	{"token GitHub", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{20,}\b`), "[REDIGIDO: token GitHub]"},
	{"token GitLab", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`), "[REDIGIDO: token GitLab]"},
	{"token Slack", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`), "[REDIGIDO: token Slack]"},
	{"chave Google", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), "[REDIGIDO: chave Google]"},
	{"chave OpenAI", regexp.MustCompile(`\bsk-(proj-|ant-)?[A-Za-z0-9_\-]{20,}\b`), "[REDIGIDO: chave de API]"},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`), "[REDIGIDO: JWT]"},
	{"Bearer", regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[A-Za-z0-9._\-+/=]{12,}`), "${1}[REDIGIDO]"},
	{"URL com senha", regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`), "${1}[REDIGIDO]@"},
	{"senha", regexp.MustCompile(`(?i)\b((?:password|passwd|senha|secret|token|api[_-]?key|client[_-]?secret)\s*[=:]\s*["']?)([^\s"'<>{}$]{6,})`), "${1}[REDIGIDO]"},
}

// Redact mascara segredos conhecidos e devolve o texto e a lista de tipos encontrados.
func Redact(s string) (string, []string) {
	var found []string
	for _, r := range redactRules {
		if r.re.MatchString(s) {
			found = append(found, r.name)
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return s, found
}

// termMatch casa um termo já normalizado: termos com espaço ou pontuação (".", "_", "#"...)
// casam como substring do texto normalizado; os demais, como palavra inteira.
func termMatch(norm, text, term string) bool {
	if term == "" {
		return false
	}
	if strings.Contains(term, " ") || cleanRe.MatchString(term) || strings.ContainsAny(term, "/.") {
		return strings.Contains(norm, term)
	}
	return strings.Contains(text, " "+term+" ")
}

// BlockedTerm devolve o primeiro termo bloqueado presente na pergunta (sem acento/caixa).
func BlockedTerm(q string, terms []string) string {
	norm := normalize(q)
	text := tokenText(norm)
	for _, t := range terms {
		if termMatch(norm, text, normalize(strings.TrimSpace(t))) {
			return t
		}
	}
	return ""
}
