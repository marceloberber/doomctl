package finops

import (
	"sort"
	"strconv"
	"strings"
)

// Leitor mínimo de HCL para estimativa de custo: entende blocos, atributos,
// strings com interpolação simples, listas, mapas, heredocs e comentários.
// Expressões que não são literais nem referências a var./local. ficam "desconhecidas".

type hclAttr struct {
	Expr string
	Line int
}

type hclBlock struct {
	Type   string
	Labels []string
	Body   *hclBody
	Line   int
	File   string
}

type hclBody struct {
	Attrs  map[string]hclAttr
	Blocks []*hclBlock
}

func (b *hclBody) blocks(t string) []*hclBlock {
	var out []*hclBlock
	for _, x := range b.Blocks {
		if x.Type == t {
			out = append(out, x)
		}
	}
	return out
}

type hclParser struct {
	s    []rune
	pos  int
	line int
	file string
}

func parseHCL(file, src string) *hclBody {
	p := &hclParser{s: []rune(src), line: 1, file: file}
	return p.body(false)
}

func (p *hclParser) peek(off int) rune {
	if p.pos+off < len(p.s) {
		return p.s[p.pos+off]
	}
	return 0
}

func (p *hclParser) adv() rune {
	r := p.s[p.pos]
	p.pos++
	if r == '\n' {
		p.line++
	}
	return r
}

// skip pula espaços, quebras de linha e comentários.
func (p *hclParser) skip(newlines bool) {
	for p.pos < len(p.s) {
		r := p.peek(0)
		switch {
		case r == ' ' || r == '\t' || r == '\r' || r == ',':
			p.adv()
		case r == '\n':
			if !newlines {
				return
			}
			p.adv()
		case r == '#' || (r == '/' && p.peek(1) == '/'):
			for p.pos < len(p.s) && p.peek(0) != '\n' {
				p.adv()
			}
		case r == '/' && p.peek(1) == '*':
			p.adv()
			p.adv()
			for p.pos < len(p.s) && !(p.peek(0) == '*' && p.peek(1) == '/') {
				p.adv()
			}
			if p.pos < len(p.s) {
				p.adv()
				p.adv()
			}
		default:
			return
		}
	}
}

func isIdent(r rune, first bool) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (!first && (r == '-' || (r >= '0' && r <= '9')))
}

func (p *hclParser) ident() string {
	start := p.pos
	for p.pos < len(p.s) && isIdent(p.peek(0), p.pos == start) {
		p.adv()
	}
	return string(p.s[start:p.pos])
}

func (p *hclParser) quoted() string {
	p.adv() // "
	var b strings.Builder
	for p.pos < len(p.s) {
		r := p.adv()
		if r == '\\' && p.pos < len(p.s) {
			b.WriteRune(p.adv())
			continue
		}
		if r == '"' || r == '\n' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (p *hclParser) body(nested bool) *hclBody {
	b := &hclBody{Attrs: map[string]hclAttr{}}
	for {
		p.skip(true)
		if p.pos >= len(p.s) {
			return b
		}
		r := p.peek(0)
		if r == '}' {
			p.adv()
			if nested {
				return b
			}
			continue
		}
		var name string
		if r == '"' {
			name = p.quoted()
		} else if isIdent(r, true) {
			name = p.ident()
		} else {
			p.adv() // caractere inesperado: ignora
			continue
		}
		line := p.line
		p.skip(false)
		if p.peek(0) == '=' && p.peek(1) != '=' {
			p.adv()
			b.Attrs[name] = hclAttr{Expr: strings.TrimSpace(p.expr()), Line: line}
			continue
		}
		if p.peek(0) == ':' {
			p.adv()
			b.Attrs[name] = hclAttr{Expr: strings.TrimSpace(p.expr()), Line: line}
			continue
		}
		// bloco: rótulos até "{"
		var labels []string
		for p.pos < len(p.s) {
			p.skip(false)
			c := p.peek(0)
			if c == '{' {
				p.adv()
				blk := &hclBlock{Type: name, Labels: labels, Line: line, File: p.file}
				blk.Body = p.body(true)
				b.Blocks = append(b.Blocks, blk)
				break
			}
			if c == '"' {
				labels = append(labels, p.quoted())
				continue
			}
			if isIdent(c, true) {
				labels = append(labels, p.ident())
				continue
			}
			// sintaxe não suportada nesta linha
			for p.pos < len(p.s) && p.peek(0) != '\n' {
				p.adv()
			}
			break
		}
	}
}

// expr lê uma expressão até o fim da linha (em profundidade 0), tratando
// parênteses, colchetes, chaves, strings, templates e heredocs.
func (p *hclParser) expr() string {
	start := p.pos
	depth := 0
	for p.pos < len(p.s) {
		r := p.peek(0)
		switch {
		case r == '"':
			p.str()
			continue
		case r == '<' && p.peek(1) == '<':
			p.heredoc()
			continue
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			if depth == 0 {
				return string(p.s[start:p.pos])
			}
			depth--
		case r == '\n' && depth == 0:
			return string(p.s[start:p.pos])
		case depth == 0 && (r == '#' || (r == '/' && p.peek(1) == '/')):
			out := string(p.s[start:p.pos])
			for p.pos < len(p.s) && p.peek(0) != '\n' {
				p.adv()
			}
			return out
		case depth == 0 && r == ',':
			return string(p.s[start:p.pos])
		}
		p.adv()
	}
	return string(p.s[start:p.pos])
}

func (p *hclParser) str() {
	p.adv()
	for p.pos < len(p.s) {
		r := p.adv()
		switch {
		case r == '\\':
			if p.pos < len(p.s) {
				p.adv()
			}
		case r == '$' && p.peek(0) == '{':
			p.adv()
			d := 1
			for p.pos < len(p.s) && d > 0 {
				c := p.peek(0)
				if c == '"' {
					p.str()
					continue
				}
				if c == '{' {
					d++
				} else if c == '}' {
					d--
				}
				p.adv()
			}
		case r == '"' || r == '\n':
			return
		}
	}
}

func (p *hclParser) heredoc() {
	p.adv()
	p.adv()
	if p.peek(0) == '-' || p.peek(0) == '~' {
		p.adv()
	}
	marker := p.ident()
	for p.pos < len(p.s) && p.peek(0) != '\n' {
		p.adv()
	}
	for p.pos < len(p.s) {
		p.adv() // \n
		ls := p.pos
		for p.pos < len(p.s) && p.peek(0) != '\n' {
			p.adv()
		}
		if strings.TrimSpace(string(p.s[ls:p.pos])) == marker {
			return
		}
	}
}

// ---------- avaliação ----------

type hclVal struct {
	Kind string // string | number | bool | list | map | unknown
	S    string
	N    float64
	B    bool
	L    []hclVal
	M    map[string]hclVal
}

var unknownVal = hclVal{Kind: "unknown"}

func (v hclVal) Str() (string, bool) {
	switch v.Kind {
	case "string":
		return v.S, true
	case "number":
		return strconv.FormatFloat(v.N, 'f', -1, 64), true
	case "bool":
		return strconv.FormatBool(v.B), true
	}
	return "", false
}

func (v hclVal) Num() (float64, bool) {
	switch v.Kind {
	case "number":
		return v.N, true
	case "string":
		f, err := strconv.ParseFloat(v.S, 64)
		return f, err == nil
	}
	return 0, false
}

type hclScope struct {
	vars   map[string]hclVal
	locals map[string]string
	depth  int
}

// splitTop divide por separadores em profundidade 0 (fora de strings).
func splitTop(s string, seps string) []string {
	var out []string
	depth := 0
	inStr := false
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if inStr {
			if r == '\\' {
				i++
			} else if r == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case r == '"':
			inStr = true
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			depth--
		case depth == 0 && strings.ContainsRune(seps, r):
			out = append(out, string(rs[start:i]))
			start = i + 1
		}
	}
	out = append(out, string(rs[start:]))
	var clean []string
	for _, x := range out {
		x = strings.TrimSpace(x)
		if x != "" && !strings.HasPrefix(x, "#") && !strings.HasPrefix(x, "//") {
			clean = append(clean, x)
		}
	}
	return clean
}

func (sc *hclScope) eval(expr string) hclVal {
	e := strings.TrimSpace(expr)
	if e == "" || sc.depth > 20 {
		return unknownVal
	}
	sc.depth++
	defer func() { sc.depth-- }()
	if q := topIndex(e, '?'); q > 0 {
		// condicional simples: cond ? a : b, com cond var.x / literal booleano
		if c := topIndex(e[q+1:], ':'); c >= 0 {
			cond := sc.eval(e[:q])
			if cond.Kind == "bool" {
				if cond.B {
					return sc.eval(e[q+1 : q+1+c])
				}
				return sc.eval(e[q+2+c:])
			}
		}
		return unknownVal
	}
	switch {
	case singleString(e):
		return sc.template(e[1 : len(e)-1])
	case e == "true" || e == "false":
		return hclVal{Kind: "bool", B: e == "true"}
	case e == "null":
		return unknownVal
	case strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]"):
		v := hclVal{Kind: "list"}
		for _, part := range splitTop(e[1:len(e)-1], ",\n") {
			v.L = append(v.L, sc.eval(part))
		}
		return v
	case strings.HasPrefix(e, "{") && strings.HasSuffix(e, "}"):
		v := hclVal{Kind: "map", M: map[string]hclVal{}}
		for _, part := range splitTop(e[1:len(e)-1], ",\n") {
			kv := splitTop(part, "=:")
			if len(kv) < 2 {
				continue
			}
			k := strings.Trim(strings.TrimSpace(kv[0]), `"`)
			v.M[k] = sc.eval(strings.Join(kv[1:], "="))
		}
		return v
	case strings.HasPrefix(e, "merge(") && strings.HasSuffix(e, ")"):
		v := hclVal{Kind: "map", M: map[string]hclVal{}}
		for _, part := range splitTop(e[6:len(e)-1], ",\n") {
			m := sc.eval(part)
			if m.Kind != "map" {
				continue
			}
			for k, x := range m.M {
				v.M[k] = x
			}
		}
		return v
	case strings.HasPrefix(e, "var."):
		name := strings.TrimPrefix(e, "var.")
		if isRef(name) {
			if v, ok := sc.vars[name]; ok {
				return v
			}
		}
		return unknownVal
	case strings.HasPrefix(e, "local."):
		name := strings.TrimPrefix(e, "local.")
		if isRef(name) {
			if x, ok := sc.locals[name]; ok {
				return sc.eval(x)
			}
		}
		return unknownVal
	}
	if f, err := strconv.ParseFloat(e, 64); err == nil {
		return hclVal{Kind: "number", N: f}
	}
	return unknownVal
}

// singleString informa se e é um único literal de string ("...").
func singleString(e string) bool {
	if len(e) < 2 || e[0] != '"' || e[len(e)-1] != '"' {
		return false
	}
	for i := 1; i < len(e)-1; i++ {
		switch e[i] {
		case '\\':
			i++
		case '"':
			return false
		}
	}
	return true
}

// topIndex devolve a posição de c fora de strings e parênteses/colchetes/chaves (-1 se não houver).
func topIndex(e string, c byte) int {
	depth := 0
	inStr := false
	for i := 0; i < len(e); i++ {
		ch := e[i]
		if inStr {
			if ch == '\\' {
				i++
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case ch == '"':
			inStr = true
		case ch == '(' || ch == '[' || ch == '{':
			depth++
		case ch == ')' || ch == ']' || ch == '}':
			depth--
		case depth == 0 && ch == c:
			return i
		}
	}
	return -1
}

func isRef(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !isIdent(r, i == 0) {
			return false
		}
	}
	return true
}

// template resolve "${var.x}" e "${local.y}"; outras interpolações tornam o valor desconhecido.
func (sc *hclScope) template(s string) hclVal {
	if !strings.Contains(s, "${") {
		return hclVal{Kind: "string", S: strings.ReplaceAll(s, `\"`, `"`)}
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i:], "}")
		if j < 0 {
			return unknownVal
		}
		v := sc.eval(s[i+2 : i+j])
		str, ok := v.Str()
		if !ok {
			return unknownVal
		}
		b.WriteString(str)
		s = s[i+j+1:]
	}
	return hclVal{Kind: "string", S: b.String()}
}

func (v hclVal) StringMap() map[string]string {
	out := map[string]string{}
	if v.Kind != "map" {
		return out
	}
	keys := make([]string, 0, len(v.M))
	for k := range v.M {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := v.M[k].Str(); ok {
			out[k] = s
		} else {
			out[k] = "(dinâmico)"
		}
	}
	return out
}
