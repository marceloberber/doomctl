// devops-router: CLI original do router (AGENTS.md §7.5). Roteia perguntas para
// Atlas (Cloud & DevOps) ou Sentinela (Redes & Segurança) via Ollama.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/doomctl/doomctl/internal/agents"
)

func main() {
	cfg, err := agents.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := agents.NewAssistant(agents.NewOllama(cfg))
	ask := func(q string) error {
		_, _, err := a.Ask(ctx, "cli", q, os.Stdout, func(d agents.Decision) {
			if cfg.Debug {
				fmt.Fprintf(os.Stderr, "[router] rota=%s cloud=%d netsec=%d off_signal=%v llm=%v motivo=%q\n",
					d.Route, d.Scores.Cloud, d.Scores.NetSec, d.Scores.OffSignal, d.UsedLLM, d.Reason)
			}
			if d.Route != agents.RouteOffTopic {
				fmt.Printf("[%s]\n", agents.PersonaName(d.Route))
			}
		})
		fmt.Println()
		return err
	}

	// Modo pergunta única.
	if len(os.Args) > 1 {
		if err := ask(strings.Join(os.Args[1:], " ")); err != nil {
			fmt.Fprintln(os.Stderr, "erro:", err)
			os.Exit(1)
		}
		return
	}

	// Modo interativo.
	fmt.Printf("devops-router · modelo %s · temp %.2f · digite 'sair' para encerrar\n", cfg.Model, cfg.Temperature)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // aceita colar arquivos de até 1 MiB
	for {
		fmt.Print("\n> ")
		if !sc.Scan() {
			return
		}
		q := strings.TrimSpace(sc.Text())
		if q == "" {
			continue
		}
		if q == "sair" || q == "exit" || q == "quit" {
			return
		}
		if err := ask(q); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintln(os.Stderr, "erro:", err)
		}
	}
}
