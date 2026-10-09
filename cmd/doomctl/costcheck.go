package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doomctl/doomctl/internal/finops"
)

type kvFlags map[string]string

func (k kvFlags) String() string { return "" }
func (k kvFlags) Set(v string) error {
	a, b, ok := strings.Cut(v, "=")
	if !ok || a == "" {
		return errors.New("use nome=valor")
	}
	k[a] = b
	return nil
}

// readTF lê os arquivos .tf/.tfvars do módulo raiz (sem recursão, como o tofu).
func readTF(dir string) (map[string]string, error) {
	out := map[string]string{}
	if dir == "" {
		return out, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".tf") || strings.HasSuffix(n, ".tfvars")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		out[n] = string(b)
	}
	return out, nil
}

// costCheck implementa "doomctl cost-check": compara o custo estimado de duas
// versões de um projeto OpenTofu/Terraform e falha (exit 1) se o aumento
// ultrapassar os limites ou violar políticas com ação "block".
func costCheck(args []string) int {
	fs := flag.NewFlagSet("cost-check", flag.ContinueOnError)
	base := fs.String("base", "", "diretório da versão atual (ex.: checkout da branch principal); vazio = projeto novo")
	head := fs.String("head", ".", "diretório da versão proposta")
	catalog := fs.String("catalog", "doomctl-finops-catalog.json", "catálogo exportado em FinOps → Fontes & configurações")
	maxPct := fs.Float64("max-pct", -1, "aumento máximo em % (padrão: o do catálogo)")
	maxAbs := fs.Float64("max-abs", -1, "aumento máximo mensal absoluto (padrão: o do catálogo)")
	region := fs.String("region", "", "força a região usada nos preços")
	env := fs.String("env", "", "ambiente para as políticas quando não houver tag")
	md := fs.String("markdown", "", "grava o resumo em Markdown (comentário de PR/MR)")
	asJSON := fs.Bool("json", false, "imprime o resultado em JSON")
	vars := kvFlags{}
	fs.Var(vars, "var", "sobrescreve variável (nome=valor); pode repetir")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "uso: doomctl cost-check --catalog catalogo.json --base <dir> --head <dir> [--max-pct 10] [--max-abs 50] [--markdown saida.md]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	raw, err := os.ReadFile(*catalog)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catálogo:", err)
		return 2
	}
	var cat finops.Catalog
	if err := json.Unmarshal(raw, &cat); err != nil || cat.Version != 1 {
		fmt.Fprintln(os.Stderr, "catálogo inválido (exporte novamente pelo doomctl)")
		return 2
	}
	st := cat.Settings
	st.Normalize()
	if *maxPct >= 0 {
		st.IaCMaxIncreasePct = *maxPct
	}
	if *maxAbs >= 0 {
		st.IaCMaxIncreaseAbs = *maxAbs
	}
	b, err := readTF(*base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "base:", err)
		return 2
	}
	h, err := readTF(*head)
	if err != nil || len(h) == 0 {
		fmt.Fprintln(os.Stderr, "head: nenhum arquivo .tf encontrado em", *head)
		return 2
	}
	ck := finops.CheckIaC(b, h, finops.IaCOptions{Vars: vars, Region: *region, Env: *env, Policies: cat.Policies}, finops.NewPrices(cat.Prices), st)
	if *md != "" {
		if err := os.WriteFile(*md, []byte(ck.Markdown()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "markdown:", err)
			return 2
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(ck)
	} else {
		fmt.Print(ck.Markdown())
	}
	if !ck.Pass {
		return 1
	}
	return 0
}
