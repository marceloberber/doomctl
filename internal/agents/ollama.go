// Package agents implementa o router das personas Atlas (Cloud & DevOps) e
// Sentinela (Redes & Segurança) e o cliente da API REST do Ollama.
// Somente stdlib. Ver AGENTS.md (fonte única de verdade das personas).
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Host        string
	Model       string
	Temperature float64
	Think       *bool // nil = não envia o campo
	Debug       bool
}

// LoadConfig lê as variáveis documentadas no AGENTS.md §7.5.
func LoadConfig() (Config, error) {
	c := Config{
		Host:        strings.TrimRight(envOr("OLLAMA_HOST", "http://127.0.0.1:11434"), "/"),
		Model:       envOr("OLLAMA_MODEL", "qwen3.8"),
		Temperature: 0.25,
		Debug:       os.Getenv("ROUTER_DEBUG") == "1",
	}
	if !strings.HasPrefix(c.Host, "http://") && !strings.HasPrefix(c.Host, "https://") {
		c.Host = "http://" + c.Host
	}
	if v := os.Getenv("ROUTER_TEMPERATURE"); v != "" {
		t, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return c, fmt.Errorf("ROUTER_TEMPERATURE invalida: %w", err)
		}
		c.Temperature = t
	}
	if err := ValidateTemperature(c.Temperature); err != nil {
		return c, err
	}
	if v := os.Getenv("OLLAMA_THINK"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("OLLAMA_THINK invalido: %w", err)
		}
		c.Think = &b
	}
	return c, nil
}

func ValidateTemperature(t float64) error {
	if t < 0.2 || t > 0.3 {
		return fmt.Errorf("temperatura %.2f fora da faixa permitida (0.2–0.3)", t)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------- Cliente Ollama (/api/chat) ----------

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Campos de ferramentas (MCP). Os provedores convertem para o formato de cada API.
	ToolCalls  []ToolCall `json:"-"`
	ToolCallID string     `json:"-"`
	ToolName   string     `json:"-"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Format   any            `json:"format,omitempty"`
	Think    *bool          `json:"think,omitempty"`
	Options  map[string]any `json:"options"`
}

type chatChunk struct {
	Message Message `json:"message"`
	Done    bool    `json:"done"`
	Error   string  `json:"error"`
}

type Ollama struct {
	cfg  Config
	http *http.Client
}

func NewOllama(cfg Config) *Ollama {
	return &Ollama{cfg: cfg, http: &http.Client{}}
}

func (o *Ollama) Config() Config { return o.cfg }

func (o *Ollama) post(ctx context.Context, req chatRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.Host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(hr)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Classify: temperatura 0 e saída restrita por JSON schema.
func (o *Ollama) Classify(ctx context.Context, question string) (Route, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rota": map[string]any{
				"type": "string",
				"enum": []string{string(RouteCloud), string(RouteNetSec), string(RouteOffTopic)},
			},
		},
		"required": []string{"rota"},
	}
	resp, err := o.post(ctx, chatRequest{
		Model: o.cfg.Model,
		Messages: []Message{
			{Role: "system", Content: classifierPrompt},
			{Role: "user", Content: question},
		},
		Stream:  false,
		Format:  schema,
		Think:   o.cfg.Think,
		Options: map[string]any{"temperature": 0.0, "num_predict": 64},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var c chatChunk
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return "", err
	}
	return parseClassifierOutput(c.Message.Content)
}

func parseClassifierOutput(s string) (Route, error) {
	var out struct {
		Rota string `json:"rota"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &out); err != nil {
		return "", fmt.Errorf("saida do classificador invalida: %q", s)
	}
	switch r := Route(out.Rota); r {
	case RouteCloud, RouteNetSec, RouteOffTopic:
		return r, nil
	default:
		return "", fmt.Errorf("rota desconhecida: %q", out.Rota)
	}
}

// Chat faz streaming da resposta da persona para w e retorna o texto completo.
func (o *Ollama) Chat(ctx context.Context, msgs []Message, w io.Writer) (string, error) {
	resp, err := o.post(ctx, chatRequest{
		Model:    o.cfg.Model,
		Messages: msgs,
		Stream:   true,
		Think:    o.cfg.Think,
		Options: map[string]any{
			"temperature":    o.cfg.Temperature,
			"top_p":          0.9,
			"repeat_penalty": 1.1,
			"num_ctx":        8192,
		},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var full strings.Builder
	dec := json.NewDecoder(resp.Body)
	for {
		var c chatChunk
		if err := dec.Decode(&c); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return full.String(), err
		}
		if c.Error != "" {
			return full.String(), errors.New(c.Error)
		}
		// Apenas o conteúdo final; o campo "thinking" (se houver) é ignorado.
		if c.Message.Content != "" {
			if _, err := io.WriteString(w, c.Message.Content); err != nil {
				return full.String(), err
			}
		}
		full.WriteString(c.Message.Content)
		if c.Done {
			break
		}
	}
	return full.String(), nil
}

// Status consulta /api/tags: Ollama no ar? modelo configurado presente?
type Status struct {
	Online     bool     `json:"online"`
	Model      string   `json:"model"`
	ModelFound bool     `json:"model_found"`
	Models     []string `json:"models"`
	Error      string   `json:"error,omitempty"`
}

func (o *Ollama) Status(ctx context.Context) Status {
	st := Status{Model: o.cfg.Model}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.cfg.Host+"/api/tags", nil)
	resp, err := o.http.Do(req)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		st.Error = err.Error()
		return st
	}
	st.Online = true
	for _, m := range tags.Models {
		st.Models = append(st.Models, m.Name)
		if m.Name == o.cfg.Model || strings.TrimSuffix(m.Name, ":latest") == o.cfg.Model {
			st.ModelFound = true
		}
	}
	return st
}
