package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAIConfig configura um provedor compatível com a API Chat Completions da OpenAI
// (OpenAI, Azure OpenAI / Azure AI Foundry Models, Google Gemini, OpenRouter, Groq,
// Mistral, vLLM, LM Studio...).
type OpenAIConfig struct {
	BaseURL         string
	ChatPath        string // padrão /chat/completions
	ModelsPath      string // padrão /models
	Query           string // ex.: api-version=2024-10-21
	Headers         map[string]string
	TokenParam      string // max_tokens | max_completion_tokens
	SendTemperature bool
	IncludeUsage    bool
	Model           string
	Auth            AuthConfig
	Timeout         time.Duration
}

type OpenAIProvider struct {
	cfg  OpenAIConfig
	auth *authorizer
	http *http.Client
}

func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.ChatPath == "" {
		cfg.ChatPath = "/chat/completions"
	}
	if cfg.ModelsPath == "" {
		cfg.ModelsPath = "/models"
	}
	if cfg.TokenParam == "" {
		cfg.TokenParam = "max_tokens"
	}
	return &OpenAIProvider{cfg: cfg, auth: newAuthorizer(cfg.Auth, "https://cognitiveservices.azure.com/.default"), http: httpClient(cfg.Timeout)}
}

func (p *OpenAIProvider) Kind() string   { return "openai" }
func (p *OpenAIProvider) External() bool { return !isPrivateURL(p.cfg.BaseURL) }

func (p *OpenAIProvider) url(path string) string {
	u := p.cfg.BaseURL + path
	if q := strings.TrimPrefix(strings.TrimSpace(p.cfg.Query), "?"); q != "" {
		if strings.Contains(u, "?") {
			u += "&" + q
		} else {
			u += "?" + q
		}
	}
	return u
}

func (p *OpenAIProvider) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.url(path), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}
	if err := p.auth.apply(ctx, req); err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, readErr(resp, "API")
	}
	return resp, nil
}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func toOpenAIMsgs(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		om := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, c := range m.ToolCalls {
				args, _ := json.Marshal(c.Args)
				if c.Args == nil {
					args = []byte("{}")
				}
				tcs = append(tcs, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": string(args)}})
			}
			om["tool_calls"] = tcs
		}
		if m.Role == "tool" {
			om["tool_call_id"] = m.ToolCallID
		}
		out = append(out, om)
	}
	return out
}

func (p *OpenAIProvider) body(msgs []Message, opt GenOptions, stream bool) map[string]any {
	model := opt.Model
	if model == "" {
		model = p.cfg.Model
	}
	b := map[string]any{"model": model, "messages": toOpenAIMsgs(msgs), "stream": stream}
	if p.cfg.SendTemperature {
		b["temperature"] = opt.Temperature
		if opt.TopP > 0 && opt.TopP < 1 {
			b["top_p"] = opt.TopP
		}
	}
	if opt.MaxTokens > 0 {
		b[p.cfg.TokenParam] = opt.MaxTokens
	}
	if stream && p.cfg.IncludeUsage {
		b["stream_options"] = map[string]any{"include_usage": true}
	}
	return b
}

func (p *OpenAIProvider) Stream(ctx context.Context, msgs []Message, opt GenOptions, tools []ToolSpec, run ToolRunner, maxTools int, w io.Writer) (string, Usage, error) {
	var full strings.Builder
	var usage Usage
	conv := append([]Message(nil), msgs...)
	calls := 0
	for {
		body := p.body(conv, opt, true)
		offerTools := len(tools) > 0 && run != nil && calls < maxTools
		if offerTools {
			body["tools"] = toolsJSON(tools)
		}
		resp, err := p.do(ctx, http.MethodPost, p.cfg.ChatPath, body)
		if err != nil {
			return full.String(), usage, err
		}
		var turn strings.Builder
		acc := map[int]*oaToolCall{}
		slots := map[int]int{} // índice do delta → posição em acc
		var writeErr error
		err = sseLines(resp.Body, func(_, data string) (bool, error) {
			if strings.TrimSpace(data) == "[DONE]" {
				return true, nil
			}
			var c struct {
				Choices []struct {
					Delta struct {
						Content   *string      `json:"content"`
						ToolCalls []oaToolCall `json:"tool_calls"`
					} `json:"delta"`
					Message *struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
				Error any `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &c); err != nil {
				return false, nil // linha não-JSON: ignora
			}
			if c.Error != nil {
				b, _ := json.Marshal(c.Error)
				return true, fmt.Errorf("API: %s", b)
			}
			if c.Usage != nil {
				usage.PromptTokens += c.Usage.PromptTokens
				usage.OutputTokens += c.Usage.CompletionTokens
			}
			for _, ch := range c.Choices {
				text := ""
				if ch.Delta.Content != nil {
					text = *ch.Delta.Content
				} else if ch.Message != nil {
					text = ch.Message.Content
				}
				if text != "" {
					if _, err := io.WriteString(w, text); err != nil {
						writeErr = err
						return true, err
					}
					turn.WriteString(text)
					full.WriteString(text)
				}
				for _, tc := range ch.Delta.ToolCalls {
					slot, ok := slots[tc.Index]
					if !ok {
						slot = tc.Index
						slots[tc.Index] = slot
					}
					cur := acc[slot]
					// alguns provedores mandam chamadas paralelas sem índice (todas 0): um id novo abre outra chamada
					if cur != nil && tc.ID != "" && cur.ID != "" && tc.ID != cur.ID {
						slot = 1000 + len(acc)
						slots[tc.Index] = slot
						cur = nil
					}
					if cur == nil {
						cp := tc
						acc[slot] = &cp
						continue
					}
					if tc.ID != "" {
						cur.ID = tc.ID
					}
					if tc.Function.Name != "" {
						cur.Function.Name += tc.Function.Name
					}
					cur.Function.Arguments += tc.Function.Arguments
				}
			}
			return false, nil
		})
		resp.Body.Close()
		if writeErr != nil {
			return full.String(), usage, writeErr
		}
		if err != nil {
			return full.String(), usage, err
		}
		if len(acc) == 0 || !offerTools {
			return full.String(), usage, nil
		}
		idx := make([]int, 0, len(acc))
		for i := range acc {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		var pending []ToolCall
		for _, i := range idx {
			tc := acc[i]
			args := map[string]any{}
			if strings.TrimSpace(tc.Function.Arguments) != "" {
				json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d_%d", calls, i)
			}
			pending = append(pending, ToolCall{ID: id, Name: tc.Function.Name, Args: args})
		}
		conv = append(conv, Message{Role: "assistant", Content: turn.String(), ToolCalls: pending})
		for _, tc := range pending {
			calls++
			out, err := run(ctx, tc)
			if err != nil {
				out = "ERRO ao executar a ferramenta: " + err.Error()
			}
			conv = append(conv, Message{Role: "tool", Content: out, ToolCallID: tc.ID, ToolName: tc.Name})
		}
		if full.Len() > 0 && !strings.HasSuffix(full.String(), "\n") {
			io.WriteString(w, "\n\n")
			full.WriteString("\n\n")
		}
	}
}

func (p *OpenAIProvider) ClassifyJSON(ctx context.Context, msgs []Message, opt GenOptions, _ map[string]any) (string, error) {
	opt.Temperature, opt.MaxTokens = 0, 64
	body := p.body(msgs, opt, false)
	resp, err := p.do(ctx, http.MethodPost, p.cfg.ChatPath, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("resposta sem choices")
	}
	return extractJSONObject(r.Choices[0].Message.Content), nil
}

// extractJSONObject devolve o primeiro objeto {...} do texto (modelos podem cercar com ```json).
func extractJSONObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

func (p *OpenAIProvider) Models(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := p.do(ctx, http.MethodGet, p.cfg.ModelsPath, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range r.Data {
		out = append(out, strings.TrimPrefix(firstNonEmpty(m.ID, m.Name), "models/"))
	}
	for _, m := range r.Models {
		out = append(out, strings.TrimPrefix(m.Name, "models/"))
	}
	sort.Strings(out)
	return out, nil
}

func (p *OpenAIProvider) Status(ctx context.Context, model string) Status {
	if model == "" {
		model = p.cfg.Model
	}
	st := Status{Model: model}
	ms, err := p.Models(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Online, st.Models = true, ms
	for _, m := range ms {
		if m == model {
			st.ModelFound = true
		}
	}
	if len(ms) == 0 {
		st.ModelFound = true
	}
	return st
}

// Ping faz uma geração mínima (1 token) para validar credenciais, modelo e deployment.
func (p *OpenAIProvider) Ping(ctx context.Context, model string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_, _, err := p.Stream(ctx, []Message{{Role: "user", Content: "Responda apenas: ok"}}, GenOptions{Model: model, MaxTokens: 5}, nil, nil, 0, io.Discard)
	return err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
