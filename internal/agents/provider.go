package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GenOptions são os parâmetros de geração repassados ao provedor.
type GenOptions struct {
	Model         string
	Temperature   float64
	TopP          float64
	RepeatPenalty float64
	NumCtx        int
	MaxTokens     int // 0 = sem limite
	Think         *bool
	NumGPU        *int
	NumThread     int
	KeepAlive     string
	// Instruções extras (agentes externos com persona própria, ex.: Azure AI Foundry Agent).
	ExtraInstructions string
	// Chave da conversa (agentes com thread no servidor do provedor).
	ConversationKey string
}

// ToolSpec é uma ferramenta oferecida ao modelo (nome já único entre servidores MCP).
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall é um pedido de execução de ferramenta feito pelo modelo.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// ToolRunner executa a ferramenta e devolve o texto do resultado.
type ToolRunner func(ctx context.Context, call ToolCall) (string, error)

// Usage resume o consumo de uma resposta.
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Provider é um backend de modelo (Ollama, API compatível com OpenAI, agente do Azure AI Foundry).
type Provider interface {
	Kind() string
	// Stream gera a resposta em streaming para w. Com ferramentas e runner, faz o
	// laço de chamadas (até maxTools chamadas) antes da resposta final.
	Stream(ctx context.Context, msgs []Message, opt GenOptions, tools []ToolSpec, run ToolRunner, maxTools int, w io.Writer) (string, Usage, error)
	// ClassifyJSON pede uma saída JSON (temperatura 0) — usada pelo classificador de escopo.
	ClassifyJSON(ctx context.Context, msgs []Message, opt GenOptions, schema map[string]any) (string, error)
	Status(ctx context.Context, model string) Status
	Models(ctx context.Context) ([]string, error)
	// External informa se os dados saem do ambiente (para redação de segredos e anexos).
	External() bool
}

// ErrUnsupported indica operação não suportada pelo provedor.
var ErrUnsupported = errors.New("operação não suportada por este provedor")

// ---------- utilidades HTTP ----------

// httpClient: sem tempo limite global por padrão (como o cliente original) — o prazo vem
// do contexto da requisição; timeout > 0 impõe um limite extra.
func httpClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		return &http.Client{}
	}
	return &http.Client{Timeout: timeout}
}

// IsPrivateURL é a versão exportada de isPrivateURL (usada pelo servidor).
func IsPrivateURL(raw string) bool { return isPrivateURL(raw) }

func readErr(resp *http.Response, prefix string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
			if c, ok := v["code"].(string); ok && c != "" {
				msg = c + ": " + msg
			}
		}
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return fmt.Errorf("%s HTTP %d: %s", prefix, resp.StatusCode, msg)
}

func newLineScanner(r io.Reader, buf []byte) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(buf, 16<<20)
	return sc
}

// isPrivateURL indica endereço local/LAN (localhost, RFC 1918, nomes sem domínio,
// .local/.internal) — usado para decidir se os dados "saem" do ambiente.
func isPrivateURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	h := strings.ToLower(host)
	return !strings.Contains(h, ".") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") ||
		strings.HasSuffix(h, ".lan") || h == "localhost"
}

// sseLines lê um corpo text/event-stream e chama fn(event, data) por evento.
func sseLines(r io.Reader, fn func(event, data string) (stop bool, err error)) error {
	buf := make([]byte, 0, 64<<10)
	var event string
	var data []string
	dispatch := func() (bool, error) {
		if len(data) == 0 {
			event = ""
			return false, nil
		}
		stop, err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		return stop, err
	}
	sc := newLineScanner(r, buf)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if stop, err := dispatch(); stop || err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimPrefix(v, " ")
		switch k {
		case "event":
			event = v
		case "data":
			data = append(data, v)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_, err := dispatch()
	return err
}

func toolsJSON(tools []ToolSpec) []map[string]any {
	var out []map[string]any
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": params}})
	}
	return out
}

func clip(s string, n int) string {
	if n > 0 && len(s) > n {
		return s[:n] + "\n…(resultado truncado)"
	}
	return s
}
