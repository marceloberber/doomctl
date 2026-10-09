// Package safeenv monta o ambiente dos processos filhos (ansible, tofu, trivy...)
// a partir de uma lista de permissão, para que segredos do doomctl
// (DOOMCTL_SECRET_KEY, DOOMCTL_DATABASE_URL etc.) nunca vazem para playbooks.
package safeenv

import (
	"os"
	"strings"
)

var allowed = []string{"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "USER", "LOGNAME", "SSL_CERT_FILE",
	"SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "TMPDIR"}

// Env devolve as variáveis permitidas do processo atual + extra.
func Env(extra ...string) []string {
	out := make([]string, 0, len(allowed)+len(extra))
	for _, k := range allowed {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	if _, ok := os.LookupEnv("LANG"); !ok {
		out = append(out, "LANG=C.UTF-8")
	}
	// HOME padrão (ferramentas Python falham sem ele); extra pode sobrescrever —
	// o os/exec mantém o último valor de chaves duplicadas.
	out = append(out, "HOME="+os.TempDir())
	for _, e := range extra {
		if strings.Contains(e, "=") {
			out = append(out, e)
		}
	}
	return out
}
