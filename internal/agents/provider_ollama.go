package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaProvider fala com a API REST do Ollama (/api/chat, /api/tags, /api/pull...).
type OllamaProvider struct {
	Host     string
	Model    string // modelo padrão da conexão
	HTTP     *http.Client
	headers  map[string]string
	auth     *authorizer
	external bool
}

// NewOllamaProvider cria o provedor do Ollama padrão (.env): sem autenticação e tratado como local.
func NewOllamaProvider(host, model string, timeout time.Duration) *OllamaProvider {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host != "" && !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	return &OllamaProvider{Host: host, Model: model, HTTP: httpClient(timeout), auth: newAuthorizer(AuthConfig{}, "")}
}

// NewOllamaConnection cria um provedor Ollama cadastrado (outro servidor): aplica autenticação e
// cabeçalhos (ex.: proxy reverso) e é "externo" quando o endereço não é local/rede privada.
func NewOllamaConnection(host, model string, timeout time.Duration, headers map[string]string, auth AuthConfig) *OllamaProvider {
	o := NewOllamaProvider(host, model, timeout)
	o.headers, o.auth, o.external = headers, newAuthorizer(auth, ""), !isPrivateURL(o.Host)
	return o
}

func (o *OllamaProvider) Kind() string   { return "ollama" }
func (o *OllamaProvider) External() bool { return o.external }

func (o *OllamaProvider) prepare(ctx context.Context, req *http.Request) error {
	for k, v := range o.headers {
		req.Header.Set(k, v)
	}
	if o.auth == nil {
		return nil
	}
	return o.auth.apply(ctx, req)
}

type ollamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type ollamaMsg struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaReq struct {
	Model     string           `json:"model"`
	Messages  []ollamaMsg      `json:"messages"`
	Stream    bool             `json:"stream"`
	Format    any              `json:"format,omitempty"`
	Think     *bool            `json:"think,omitempty"`
	Options   map[string]any   `json:"options"`
	KeepAlive string           `json:"keep_alive,omitempty"`
	Tools     []map[string]any `json:"tools,omitempty"`
}

type ollamaChunk struct {
	Message         ollamaMsg `json:"message"`
	Done            bool      `json:"done"`
	Error           string    `json:"error"`
	PromptEvalCount int       `json:"prompt_eval_count"`
	EvalCount       int       `json:"eval_count"`
}

func (o *OllamaProvider) model(opt GenOptions) string {
	if opt.Model != "" {
		return opt.Model
	}
	return o.Model
}

func toOllamaMsgs(msgs []Message) []ollamaMsg {
	out := make([]ollamaMsg, 0, len(msgs))
	for _, m := range msgs {
		om := ollamaMsg{Role: m.Role, Content: m.Content, ToolName: m.ToolName}
		for _, c := range m.ToolCalls {
			var tc ollamaToolCall
			tc.Function.Name, tc.Function.Arguments = c.Name, c.Args
			om.ToolCalls = append(om.ToolCalls, tc)
		}
		out = append(out, om)
	}
	return out
}

// options monta o mapa "options" — com os valores padrão é idêntico ao original
// (temperature, top_p, repeat_penalty, num_ctx).
func ollamaOptions(opt GenOptions) map[string]any {
	m := map[string]any{"temperature": opt.Temperature, "top_p": opt.TopP, "repeat_penalty": opt.RepeatPenalty, "num_ctx": opt.NumCtx}
	if opt.TopP == 0 {
		m["top_p"] = 0.9
	}
	if opt.RepeatPenalty == 0 {
		m["repeat_penalty"] = 1.1
	}
	if opt.NumCtx == 0 {
		m["num_ctx"] = 8192
	}
	if opt.MaxTokens > 0 {
		m["num_predict"] = opt.MaxTokens
	}
	if opt.NumGPU != nil {
		m["num_gpu"] = *opt.NumGPU
	}
	if opt.NumThread > 0 {
		m["num_thread"] = opt.NumThread
	}
	return m
}

func (o *OllamaProvider) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Host+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := o.prepare(ctx, req); err != nil {
		return nil, err
	}
	resp, err := o.HTTP.Do(req)
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

func (o *OllamaProvider) Stream(ctx context.Context, msgs []Message, opt GenOptions, tools []ToolSpec, run ToolRunner, maxTools int, w io.Writer) (string, Usage, error) {
	var full strings.Builder
	var usage Usage
	conv := append([]Message(nil), msgs...)
	calls := 0
	for {
		req := ollamaReq{Model: o.model(opt), Messages: toOllamaMsgs(conv), Stream: true, Think: opt.Think, Options: ollamaOptions(opt), KeepAlive: opt.KeepAlive}
		offerTools := len(tools) > 0 && run != nil && calls < maxTools
		if offerTools {
			req.Tools = toolsJSON(tools)
		}
		resp, err := o.post(ctx, "/api/chat", req)
		if err != nil {
			return full.String(), usage, err
		}
		var turn strings.Builder
		var pending []ToolCall
		dec := json.NewDecoder(resp.Body)
		for {
			var c ollamaChunk
			if err := dec.Decode(&c); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				resp.Body.Close()
				return full.String(), usage, err
			}
			if c.Error != "" {
				resp.Body.Close()
				return full.String(), usage, errors.New(c.Error)
			}
			// Apenas o conteúdo final; o campo "thinking" (se houver) é ignorado.
			if c.Message.Content != "" {
				if _, err := io.WriteString(w, c.Message.Content); err != nil {
					resp.Body.Close()
					return full.String(), usage, err
				}
				turn.WriteString(c.Message.Content)
				full.WriteString(c.Message.Content)
			}
			for i, tc := range c.Message.ToolCalls {
				pending = append(pending, ToolCall{ID: fmt.Sprintf("call_%d_%d", calls, i), Name: tc.Function.Name, Args: tc.Function.Arguments})
			}
			if c.Done {
				usage.PromptTokens += c.PromptEvalCount
				usage.OutputTokens += c.EvalCount
				break
			}
		}
		resp.Body.Close()
		if len(pending) == 0 || !offerTools {
			return full.String(), usage, nil
		}
		conv = append(conv, Message{Role: "assistant", Content: turn.String(), ToolCalls: pending})
		for _, tc := range pending {
			calls++
			out, err := run(ctx, tc)
			if err != nil {
				out = "ERRO ao executar a ferramenta: " + err.Error()
			}
			conv = append(conv, Message{Role: "tool", Content: out, ToolName: tc.Name, ToolCallID: tc.ID})
		}
		if full.Len() > 0 && !strings.HasSuffix(full.String(), "\n") {
			io.WriteString(w, "\n\n")
			full.WriteString("\n\n")
		}
	}
}

func (o *OllamaProvider) ClassifyJSON(ctx context.Context, msgs []Message, opt GenOptions, schema map[string]any) (string, error) {
	options := map[string]any{"temperature": 0.0, "num_predict": 64}
	// opções de GPU só quando configuradas (o padrão mantém o corpo original)
	if opt.NumGPU != nil {
		options["num_gpu"] = *opt.NumGPU
	}
	if opt.NumThread > 0 {
		options["num_thread"] = opt.NumThread
	}
	resp, err := o.post(ctx, "/api/chat", ollamaReq{Model: o.model(opt), Messages: toOllamaMsgs(msgs), Stream: false, Format: schema,
		Think: opt.Think, Options: options, KeepAlive: opt.KeepAlive})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var c ollamaChunk
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return "", err
	}
	return c.Message.Content, nil
}

func (o *OllamaProvider) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Host+path, nil)
	if err != nil {
		return err
	}
	if err := o.prepare(ctx, req); err != nil {
		return err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return readErr(resp, "ollama")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// OllamaModel descreve um modelo local.
type OllamaModel struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Details    struct {
		Family            string `json:"family"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
}

func (o *OllamaProvider) ListModels(ctx context.Context) ([]OllamaModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var tags struct {
		Models []OllamaModel `json:"models"`
	}
	if err := o.get(ctx, "/api/tags", &tags); err != nil {
		return nil, err
	}
	if tags.Models == nil {
		tags.Models = []OllamaModel{}
	}
	return tags.Models, nil
}

func (o *OllamaProvider) Models(ctx context.Context) ([]string, error) {
	ms, err := o.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out, nil
}

func (o *OllamaProvider) Status(ctx context.Context, model string) Status {
	if model == "" {
		model = o.Model
	}
	st := Status{Model: model}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ms, err := o.Models(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Online = true
	st.Models = ms
	for _, m := range ms {
		if m == model || strings.TrimSuffix(m, ":latest") == model {
			st.ModelFound = true
		}
	}
	return st
}

// RunningModel é um item de /api/ps (modelos carregados).
type RunningModel struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SizeVRAM  int64     `json:"size_vram"`
	ExpiresAt time.Time `json:"expires_at"`
	Context   int       `json:"context_length"`
	Processor string    `json:"processor"`
}

func (o *OllamaProvider) PS(ctx context.Context) ([]RunningModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var ps struct {
		Models []RunningModel `json:"models"`
	}
	if err := o.get(ctx, "/api/ps", &ps); err != nil {
		return nil, err
	}
	out := []RunningModel{}
	for _, m := range ps.Models {
		switch {
		case m.Size <= 0:
			m.Processor = "—"
		case m.SizeVRAM <= 0:
			m.Processor = "100% CPU"
		case m.SizeVRAM >= m.Size:
			m.Processor = "100% GPU"
		default:
			g := float64(m.SizeVRAM) / float64(m.Size) * 100
			m.Processor = fmt.Sprintf("%.0f%%/%.0f%% CPU/GPU", 100-g, g)
		}
		out = append(out, m)
	}
	return out, nil
}

// Load carrega (keepAlive vazio = padrão) ou descarrega (keepAlive "0") um modelo.
func (o *OllamaProvider) Load(ctx context.Context, model, keepAlive string, opt GenOptions) error {
	body := map[string]any{"model": model}
	if keepAlive != "" {
		body["keep_alive"] = keepAlive
	}
	if keepAlive != "0" {
		body["options"] = ollamaOptions(opt)
	}
	resp, err := o.post(ctx, "/api/generate", body)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Pull baixa um modelo; progress recebe status e bytes concluídos/total.
func (o *OllamaProvider) Pull(ctx context.Context, model string, progress func(status string, done, total int64)) error {
	cl := &http.Client{} // sem timeout global: downloads longos
	b, _ := json.Marshal(map[string]any{"model": model, "name": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Host+"/api/pull", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := o.prepare(ctx, req); err != nil {
		return err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return readErr(resp, "ollama pull")
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var ev struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		progress(ev.Status, ev.Completed, ev.Total)
	}
}

func (o *OllamaProvider) Delete(ctx context.Context, model string) error {
	b, _ := json.Marshal(map[string]string{"model": model, "name": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, o.Host+"/api/delete", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := o.prepare(ctx, req); err != nil {
		return err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return readErr(resp, "ollama delete")
	}
	return nil
}

// Version devolve a versão do servidor Ollama.
func (o *OllamaProvider) Version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var v struct {
		Version string `json:"version"`
	}
	if o.get(ctx, "/api/version", &v) != nil {
		return ""
	}
	return v.Version
}
