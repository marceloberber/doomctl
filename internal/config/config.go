// Package config carrega a configuração do doomctl a partir de variáveis de ambiente.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen      string
	TLSMode     string // self-signed | files | off
	TLSCert     string
	TLSKey      string
	DatabaseURL string
	SecretKey   string
	DataDir     string

	AdminUser     string
	AdminPassword string

	SessionTTL time.Duration
	MaxJobs    int

	OllamaHost        string
	OllamaModel       string
	RouterTemperature float64
	OllamaThink       *bool
	// AIGPU registra como o Ollama em container foi instalado (./install.sh --gpu): off | nvidia | amd | "" (não informado).
	AIGPU string
	// OllamaContainer: nome/ID do container do Ollama (vazio = descobre pelo rótulo do compose).
	OllamaContainer string

	PortainerImage string
	PortainerPort  int

	DockerSocket string
	Version      string
}

func Load() (*Config, error) {
	c := &Config{
		Listen:          env("DOOMCTL_LISTEN", ":8443"),
		TLSMode:         strings.ToLower(env("DOOMCTL_TLS_MODE", "self-signed")),
		TLSCert:         os.Getenv("DOOMCTL_TLS_CERT"),
		TLSKey:          os.Getenv("DOOMCTL_TLS_KEY"),
		DatabaseURL:     env("DOOMCTL_DATABASE_URL", "postgres://doomctl:doomctl@127.0.0.1:5432/doomctl?sslmode=disable"),
		SecretKey:       os.Getenv("DOOMCTL_SECRET_KEY"),
		DataDir:         env("DOOMCTL_DATA_DIR", "/var/lib/doomctl"),
		AdminUser:       env("DOOMCTL_ADMIN_USER", "admin"),
		AdminPassword:   os.Getenv("DOOMCTL_ADMIN_PASSWORD"),
		OllamaHost:      strings.TrimRight(env("OLLAMA_HOST", "http://127.0.0.1:11434"), "/"),
		OllamaModel:     env("OLLAMA_MODEL", "qwen3.8"),
		PortainerImage:  env("DOOMCTL_PORTAINER_IMAGE", "portainer/portainer-ce:lts"),
		DockerSocket:    env("DOOMCTL_DOCKER_SOCKET", "/var/run/docker.sock"),
		AIGPU:           strings.ToLower(os.Getenv("DOOMCTL_AI_GPU")),
		OllamaContainer: os.Getenv("DOOMCTL_OLLAMA_CONTAINER"),
	}
	switch c.AIGPU {
	case "", "off", "nvidia", "amd":
	case "0", "false", "cpu":
		c.AIGPU = "off"
	case "1", "true":
		c.AIGPU = "nvidia"
	default:
		return nil, fmt.Errorf("DOOMCTL_AI_GPU inválido: %q (use off, nvidia ou amd)", c.AIGPU)
	}
	if !strings.HasPrefix(c.OllamaHost, "http://") && !strings.HasPrefix(c.OllamaHost, "https://") {
		c.OllamaHost = "http://" + c.OllamaHost
	}
	var err error
	if c.SessionTTL, err = time.ParseDuration(env("DOOMCTL_SESSION_TTL", "12h")); err != nil {
		return nil, fmt.Errorf("DOOMCTL_SESSION_TTL inválido: %w", err)
	}
	if c.MaxJobs, err = strconv.Atoi(env("DOOMCTL_MAX_JOBS", "4")); err != nil || c.MaxJobs < 1 {
		return nil, fmt.Errorf("DOOMCTL_MAX_JOBS inválido")
	}
	if c.PortainerPort, err = strconv.Atoi(env("DOOMCTL_PORTAINER_PORT", "9443")); err != nil {
		return nil, fmt.Errorf("DOOMCTL_PORTAINER_PORT inválido")
	}

	c.RouterTemperature = 0.25
	if v := os.Getenv("ROUTER_TEMPERATURE"); v != "" {
		if c.RouterTemperature, err = strconv.ParseFloat(v, 64); err != nil {
			return nil, fmt.Errorf("ROUTER_TEMPERATURE inválida: %w", err)
		}
	}
	if c.RouterTemperature < 0.2 || c.RouterTemperature > 0.3 {
		return nil, fmt.Errorf("ROUTER_TEMPERATURE %.2f fora da faixa permitida (0.2–0.3)", c.RouterTemperature)
	}
	if v := os.Getenv("OLLAMA_THINK"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("OLLAMA_THINK inválido: %w", err)
		}
		c.OllamaThink = &b
	}
	switch c.TLSMode {
	case "self-signed", "off":
	case "files":
		if c.TLSCert == "" || c.TLSKey == "" {
			return nil, fmt.Errorf("DOOMCTL_TLS_MODE=files exige DOOMCTL_TLS_CERT e DOOMCTL_TLS_KEY")
		}
	default:
		return nil, fmt.Errorf("DOOMCTL_TLS_MODE inválido: %q (use self-signed, files ou off)", c.TLSMode)
	}
	c.DataDir, _ = filepath.Abs(c.DataDir)
	return c, nil
}

// Path retorna um caminho dentro do diretório de dados.
func (c *Config) Path(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir}, parts...)...)
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
