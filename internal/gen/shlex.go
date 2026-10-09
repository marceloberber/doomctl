package gen

import (
	"errors"
	"strings"
)

// SplitArgs divide uma linha de comando no estilo POSIX (aspas simples, duplas
// e barra invertida), SEM expansão de variáveis, globs ou substituição de comandos.
func SplitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inArg = true, true
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("aspas não fechadas")
	}
	if escaped {
		return nil, errors.New("barra invertida no final da linha")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
