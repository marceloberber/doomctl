// Package gen contém os geradores de artefatos do doomctl: Dockerfile,
// docker-compose, playbooks/roles Ansible, OpenTofu (AWS/OCI) e manifests
// Kubernetes. Tudo é gerado com ordem de chaves estável e quoting seguro.
package gen

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Map é um mapa YAML com ordem preservada.
type Map []KV

type KV struct {
	K string
	V any
}

// List é uma sequência YAML.
type List []any

// Literal é um bloco "|" (scripts, arquivos de configuração).
type Literal string

// Raw é emitido sem quoting (use com cuidado: valores já validados).
type Raw string

func (m Map) Set(k string, v any) Map { return append(m, KV{k, v}) }

// SetIf adiciona somente quando o valor não é "vazio".
func (m Map) SetIf(k string, v any) Map {
	if isEmpty(v) {
		return m
	}
	return append(m, KV{k, v})
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case Literal:
		return strings.TrimSpace(string(t)) == ""
	case Map:
		return len(t) == 0
	case List:
		return len(t) == 0
	case []string:
		return len(t) == 0
	}
	return false
}

var (
	plainRe   = regexp.MustCompile(`^[A-Za-z0-9_./+$][A-Za-z0-9 _./:@%+=,$\-]*$`)
	numLikeRe = regexp.MustCompile(`^[0-9:._+\-eE]+$`)
	reserved  = map[string]bool{"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true,
		"null": true, "~": true, "y": true, "n": true}
)

// Q devolve o escalar YAML seguro para uma string.
func Q(s string) string {
	if s != "" && plainRe.MatchString(s) && !reserved[strings.ToLower(s)] && !numLikeRe.MatchString(s) &&
		!strings.HasSuffix(s, " ") && !strings.Contains(s, ": ") && !strings.Contains(s, " #") &&
		!strings.HasPrefix(s, "-") {
		return s
	}
	return dq(s)
}

// DQ força aspas duplas (JSON é um subconjunto válido de escalares YAML).
func dq(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func DQ(s string) string { return dq(s) }

func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return Q(t), true
	case Raw:
		return string(t), true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	case nil:
		return "null", true
	}
	return "", false
}

// YAML serializa o valor (Map, List, escalares).
func YAML(v any) string {
	var b strings.Builder
	writeNode(&b, v, 0)
	return b.String()
}

func pad(n int) string { return strings.Repeat(" ", n) }

func key(k string) string {
	if plainRe.MatchString(k) && !strings.Contains(k, ": ") && !reserved[strings.ToLower(k)] {
		return k
	}
	return dq(k)
}

func writeNode(b *strings.Builder, v any, ind int) {
	switch t := v.(type) {
	case Map:
		for _, kv := range t {
			writeKV(b, kv, ind)
		}
	case List:
		for _, it := range t {
			writeItem(b, it, ind)
		}
	default:
		s, _ := scalar(v)
		b.WriteString(pad(ind) + s + "\n")
	}
}

func writeKV(b *strings.Builder, kv KV, ind int) {
	k := key(kv.K)
	switch t := kv.V.(type) {
	case Map:
		if len(t) == 0 {
			b.WriteString(pad(ind) + k + ": {}\n")
			return
		}
		b.WriteString(pad(ind) + k + ":\n")
		writeNode(b, t, ind+2)
	case List:
		if len(t) == 0 {
			b.WriteString(pad(ind) + k + ": []\n")
			return
		}
		b.WriteString(pad(ind) + k + ":\n")
		writeNode(b, t, ind+2)
	case []string:
		l := make(List, len(t))
		for i, s := range t {
			l[i] = s
		}
		writeKV(b, KV{kv.K, l}, ind)
	case Literal:
		b.WriteString(pad(ind) + k + ": |\n")
		for _, line := range strings.Split(strings.TrimRight(string(t), "\n"), "\n") {
			if line == "" {
				b.WriteString("\n")
			} else {
				b.WriteString(pad(ind+2) + line + "\n")
			}
		}
	default:
		s, _ := scalar(t)
		b.WriteString(pad(ind) + k + ": " + s + "\n")
	}
}

func writeItem(b *strings.Builder, it any, ind int) {
	switch t := it.(type) {
	case Map:
		if len(t) == 0 {
			b.WriteString(pad(ind) + "- {}\n")
			return
		}
		var sub strings.Builder
		writeNode(&sub, t, ind+2)
		s := sub.String()
		// primeira linha vai na mesma linha do "- "
		b.WriteString(pad(ind) + "- " + strings.TrimPrefix(s, pad(ind+2)))
	case List:
		var sub strings.Builder
		writeNode(&sub, t, ind+2)
		b.WriteString(pad(ind) + "- " + strings.TrimPrefix(sub.String(), pad(ind+2)))
	case Literal:
		b.WriteString(pad(ind) + "- |\n")
		for _, line := range strings.Split(strings.TrimRight(string(t), "\n"), "\n") {
			b.WriteString(pad(ind+2) + line + "\n")
		}
	default:
		s, _ := scalar(t)
		b.WriteString(pad(ind) + "- " + s + "\n")
	}
}

// ---------- validações comuns ----------

var (
	NameRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`) // DNS-1123 label
	IdentRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	FileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// Lines quebra um textarea em linhas não vazias.
func Lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// KVLines interpreta linhas "CHAVE=valor".
func KVLines(s string) [][2]string {
	var out [][2]string
	for _, l := range Lines(s) {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			k, v, _ = strings.Cut(l, ":")
		}
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, [2]string{k, strings.TrimSpace(v)})
		}
	}
	return out
}
