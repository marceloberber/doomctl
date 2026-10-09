// doomctl: plataforma self-hosted de ferramentas DevOps & Cloud e Redes & Segurança.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/doomctl/doomctl/internal/config"
	"github.com/doomctl/doomctl/internal/secure"
	"github.com/doomctl/doomctl/internal/server"
	"github.com/doomctl/doomctl/internal/store"
	"github.com/doomctl/doomctl/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "cost-check" {
		os.Exit(costCheck(os.Args[2:]))
	}
	showVersion := flag.Bool("version", false, "mostra a versão e sai")
	healthcheck := flag.Bool("healthcheck", false, "verifica /api/health (uso no HEALTHCHECK do Docker)")
	debug := flag.Bool("debug", os.Getenv("DOOMCTL_DEBUG") == "1", "logs detalhados")
	flag.Parse()
	if *showVersion {
		fmt.Println("doomctl", version)
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	cfg, err := config.Load()
	if err != nil {
		fatal("configuração", err)
	}
	cfg.Version = version
	if *healthcheck {
		os.Exit(runHealthcheck(cfg))
	}

	// Processo não "dumpable": impede que processos filhos (playbooks, tofu...)
	// leiam /proc/<pid>/environ e a memória do doomctl (onde ficam os segredos).
	syscall.RawSyscall(syscall.SYS_PRCTL, 4 /* PR_SET_DUMPABLE */, 0, 0)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fatal("diretório de dados", err)
	}
	key, generated, err := secure.LoadKey(cfg.SecretKey, cfg.Path("secret.key"))
	if err != nil {
		fatal("chave mestra", err)
	}
	if generated {
		slog.Warn("DOOMCTL_SECRET_KEY não definida: chave gerada em " + cfg.Path("secret.key") +
			" — prefira definir a variável (o arquivo em disco é legível por processos do mesmo usuário). FAÇA BACKUP desta chave.")
	} else if cfg.SecretKey == "" {
		slog.Warn("usando chave mestra do arquivo " + cfg.Path("secret.key") + " (prefira DOOMCTL_SECRET_KEY)")
	}
	os.Unsetenv("DOOMCTL_SECRET_KEY")
	box, err := secure.NewBox(key)
	if err != nil {
		fatal("chave mestra", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("PostgreSQL", err)
	}
	defer db.Close()
	os.Unsetenv("DOOMCTL_DATABASE_URL")
	if err := store.Migrate(ctx, db); err != nil {
		fatal("migrations", err)
	}
	if err := server.EnsureAdmin(ctx, db, cfg.AdminUser, cfg.AdminPassword); err != nil {
		fatal("administrador inicial", err)
	}
	os.Unsetenv("DOOMCTL_ADMIN_PASSWORD")

	srv, err := server.New(cfg, db, box, web.Static())
	if err != nil {
		fatal("servidor", err)
	}
	srv.Jobs().MarkInterrupted(ctx)
	srv.SyncRoles(ctx)
	srv.StartFinOps(ctx)

	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	switch cfg.TLSMode {
	case "self-signed":
		cert, err := selfSigned(cfg.Path("tls"))
		if err != nil {
			fatal("TLS autoassinado", err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	case "files":
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			fatal("TLS", err)
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	go func() {
		scheme := "http"
		if hs.TLSConfig != nil {
			scheme = "https"
		}
		slog.Info("doomctl no ar", "versao", version, "endereco", scheme+"://"+cfg.Listen, "dados", cfg.DataDir)
		var err error
		if hs.TLSConfig != nil {
			err = hs.ListenAndServeTLS("", "")
		} else {
			err = hs.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("HTTP", err)
		}
	}()

	<-ctx.Done()
	slog.Info("encerrando...")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hs.Shutdown(sctx)
}

func runHealthcheck(cfg *config.Config) int {
	scheme := "https"
	if cfg.TLSMode == "off" {
		scheme = "http"
	}
	port := cfg.Listen
	if port[0] == ':' {
		port = "127.0.0.1" + port
	}
	cl := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // somente loopback, certificado local
	}}
	resp, err := cl.Get(scheme + "://" + port + "/api/health")
	if err != nil || resp.StatusCode != 200 {
		return 1
	}
	return 0
}

func fatal(what string, err error) {
	slog.Error(what, "erro", err)
	os.Exit(1)
}
