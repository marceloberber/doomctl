package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FoundryConfig conecta a agentes do Microsoft Foundry (Azure AI Foundry) Agent Service.
//
// Modos:
//   - agent (padrão): agentes novos (nome + versão), API Responses do projeto com
//     "agent_reference"; o contexto da conversa fica no Foundry (previous_response_id).
//   - application: agente publicado como aplicação (…/applications/<app>/protocols/openai);
//     API Responses sem estado — o doomctl envia o histórico.
//   - classic: agentes clássicos (asst_…), threads/runs (API Assistants; em descontinuação).
type FoundryConfig struct {
	Endpoint   string // projeto: https://<recurso>.services.ai.azure.com/api/projects/<projeto>
	Mode       string // agent | application | classic
	AgentID    string // nome do agente (agent), vazio (application) ou asst_… (classic)
	APIVersion string // agent: v1 (padrão) ou versão preview; application: 2025-11-15-preview; classic: 2025-05-01
	Auth       AuthConfig
	Timeout    time.Duration
}

type FoundryProvider struct {
	cfg     FoundryConfig
	auth    *authorizer
	http    *http.Client
	mu      sync.Mutex
	threads map[string]string // chave da conversa → thread id (classic) ou último response id (agent)
}

func NewFoundryProvider(cfg FoundryConfig) *FoundryProvider {
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	cfg.Endpoint = strings.TrimSuffix(cfg.Endpoint, "/responses")
	switch cfg.Mode {
	case "classic", "application":
	default:
		cfg.Mode = "agent"
	}
	if cfg.APIVersion == "" {
		switch cfg.Mode {
		case "classic":
			cfg.APIVersion = "2025-05-01"
		case "application":
			cfg.APIVersion = "2025-11-15-preview"
		default:
			cfg.APIVersion = "v1"
		}
	}
	return &FoundryProvider{cfg: cfg, auth: newAuthorizer(cfg.Auth, "https://ai.azure.com/.default"), http: httpClient(cfg.Timeout),
		threads: map[string]string{}}
}

func (f *FoundryProvider) Kind() string   { return "foundry_agent" }
func (f *FoundryProvider) External() bool { return !isPrivateURL(f.cfg.Endpoint) }

func (f *FoundryProvider) req(ctx context.Context, method, path string, body any) (*http.Response, error) {
	u := f.cfg.Endpoint + path
	if f.cfg.APIVersion != "v1" || f.cfg.Mode == "classic" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "api-version=" + url.QueryEscape(f.cfg.APIVersion)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if err := f.auth.apply(ctx, r); err != nil {
		return nil, err
	}
	resp, err := f.http.Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, readErr(resp, "Azure AI Foundry")
	}
	return resp, nil
}

func (f *FoundryProvider) json(ctx context.Context, method, path string, body, out any) error {
	resp, err := f.req(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func (f *FoundryProvider) thread(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	id := f.threads[key]
	f.mu.Unlock()
	if id != "" && key != "" {
		return id, nil
	}
	var t struct {
		ID string `json:"id"`
	}
	if err := f.json(ctx, http.MethodPost, "/threads", map[string]any{}, &t); err != nil {
		return "", err
	}
	if t.ID == "" {
		return "", errors.New("Azure AI Foundry não devolveu o id da thread")
	}
	if key != "" {
		f.mu.Lock()
		f.threads[key] = t.ID
		f.mu.Unlock()
	}
	return t.ID, nil
}

// ResetConversation esquece as threads cujas chaves começam com o prefixo.
func (f *FoundryProvider) ResetConversation(prefix string) {
	f.mu.Lock()
	for k := range f.threads {
		if strings.HasPrefix(k, prefix) {
			delete(f.threads, k)
		}
	}
	f.mu.Unlock()
}

func (f *FoundryProvider) Stream(ctx context.Context, msgs []Message, opt GenOptions, _ []ToolSpec, _ ToolRunner, _ int, w io.Writer) (string, Usage, error) {
	if f.cfg.Mode != "classic" {
		return f.streamResponses(ctx, msgs, opt, w)
	}
	var usage Usage
	if f.cfg.AgentID == "" {
		return "", usage, errors.New("informe o ID do agente (asst_...)")
	}
	q := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			q = msgs[i].Content
			break
		}
	}
	f.mu.Lock()
	_, known := f.threads[opt.ConversationKey]
	f.mu.Unlock()
	tid, err := f.thread(ctx, opt.ConversationKey)
	if err != nil {
		return "", usage, err
	}
	if !known {
		// thread nova: repassa o histórico local (ex.: após recarregar a configuração)
		for _, m := range msgs[:max(0, len(msgs)-1)] {
			if (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Content) != "" {
				f.json(ctx, http.MethodPost, "/threads/"+tid+"/messages", map[string]any{"role": m.Role, "content": m.Content}, nil)
			}
		}
	}
	if err := f.json(ctx, http.MethodPost, "/threads/"+tid+"/messages", map[string]any{"role": "user", "content": q}, nil); err != nil {
		// thread expirada/removida no Foundry: recomeça uma nova
		f.ResetConversation(opt.ConversationKey)
		if tid, err = f.thread(ctx, opt.ConversationKey); err != nil {
			return "", usage, err
		}
		if err := f.json(ctx, http.MethodPost, "/threads/"+tid+"/messages", map[string]any{"role": "user", "content": q}, nil); err != nil {
			return "", usage, err
		}
	}
	run := map[string]any{"assistant_id": f.cfg.AgentID, "stream": true}
	if s := strings.TrimSpace(opt.ExtraInstructions); s != "" {
		run["additional_instructions"] = s
	}
	if opt.MaxTokens > 0 {
		run["max_completion_tokens"] = opt.MaxTokens
	}
	resp, err := f.req(ctx, http.MethodPost, "/threads/"+tid+"/runs", run)
	if err != nil {
		return "", usage, err
	}
	defer resp.Body.Close()
	var full strings.Builder
	var runErr error
	var runID, status string
	err = sseLines(resp.Body, func(event, data string) (bool, error) {
		if strings.TrimSpace(data) == "[DONE]" {
			return true, nil
		}
		switch {
		case event == "thread.message.delta":
			var d struct {
				Delta struct {
					Content []struct {
						Type string `json:"type"`
						Text struct {
							Value string `json:"value"`
						} `json:"text"`
					} `json:"content"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &d) == nil {
				for _, c := range d.Delta.Content {
					if c.Text.Value != "" {
						if _, err := io.WriteString(w, c.Text.Value); err != nil {
							return true, err
						}
						full.WriteString(c.Text.Value)
					}
				}
			}
		case strings.HasPrefix(event, "thread.run."):
			var r struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				LastError *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"last_error"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &r) == nil {
				runID, status = firstNonEmpty(r.ID, runID), firstNonEmpty(r.Status, status)
				if r.Usage != nil {
					usage = Usage{PromptTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens}
				}
				switch event {
				case "thread.run.failed", "thread.run.expired", "thread.run.cancelled":
					msg := status
					if r.LastError != nil {
						msg = r.LastError.Code + ": " + r.LastError.Message
					}
					runErr = errors.New("o run do agente terminou com " + msg)
					return true, nil
				case "thread.run.requires_action":
					runErr = errors.New("o agente pediu a execução de uma função do cliente (requires_action), que o doomctl não executa; use ferramentas do lado do Foundry")
					if runID != "" {
						f.json(context.Background(), http.MethodPost, "/threads/"+tid+"/runs/"+runID+"/cancel", map[string]any{}, nil)
					}
					return true, nil
				}
			}
		case event == "error":
			runErr = errors.New("erro no streaming do agente: " + data)
			return true, nil
		}
		return false, nil
	})
	if runErr != nil {
		return full.String(), usage, runErr
	}
	if err != nil {
		return full.String(), usage, err
	}
	if full.Len() == 0 && runID != "" {
		// sem deltas (ex.: proxy que não repassa streaming): espera o run e lê a última mensagem
		text, err := f.waitAndRead(ctx, tid, runID)
		if err != nil {
			return "", usage, err
		}
		io.WriteString(w, text)
		full.WriteString(text)
	}
	return full.String(), usage, nil
}

func (f *FoundryProvider) waitAndRead(ctx context.Context, tid, runID string) (string, error) {
	for i := 0; i < 600; i++ {
		var r struct {
			Status string `json:"status"`
		}
		if err := f.json(ctx, http.MethodGet, "/threads/"+tid+"/runs/"+runID, nil, &r); err != nil {
			return "", err
		}
		if r.Status == "completed" {
			break
		}
		if r.Status == "failed" || r.Status == "cancelled" || r.Status == "expired" || r.Status == "requires_action" {
			return "", errors.New("o run do agente terminou com status " + r.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	var ms struct {
		Data []struct {
			Role    string `json:"role"`
			Content []struct {
				Text struct {
					Value string `json:"value"`
				} `json:"text"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := f.json(ctx, http.MethodGet, "/threads/"+tid+"/messages?order=desc&limit=1", nil, &ms); err != nil {
		return "", err
	}
	for _, m := range ms.Data {
		if m.Role == "assistant" {
			var b strings.Builder
			for _, c := range m.Content {
				b.WriteString(c.Text.Value)
			}
			return b.String(), nil
		}
	}
	return "", errors.New("o agente não devolveu resposta")
}

func (f *FoundryProvider) ClassifyJSON(context.Context, []Message, GenOptions, map[string]any) (string, error) {
	return "", ErrUnsupported
}

// FoundryAgentInfo descreve um agente do projeto.
type FoundryAgentInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Model   string `json:"model"`
	Version string `json:"version,omitempty"`
}

// Agents lista os agentes do projeto (modos agent e classic).
func (f *FoundryProvider) Agents(ctx context.Context) ([]FoundryAgentInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out := []FoundryAgentInfo{}
	switch f.cfg.Mode {
	case "application":
		return out, nil
	case "classic":
		var r struct {
			Data []FoundryAgentInfo `json:"data"`
		}
		if err := f.json(ctx, http.MethodGet, "/assistants?limit=100&order=desc", nil, &r); err != nil {
			return nil, err
		}
		return append(out, r.Data...), nil
	}
	// agentes novos: GET /agents (api-version v1 ou preview)
	path := "/agents"
	if f.cfg.APIVersion == "v1" {
		path = "/agents?api-version=v1"
	}
	var r struct {
		Data []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Versions struct {
				Latest struct {
					Version    string `json:"version"`
					Definition struct {
						Model string `json:"model"`
					} `json:"definition"`
				} `json:"latest"`
			} `json:"versions"`
			Definition struct {
				Model string `json:"model"`
			} `json:"definition"`
		} `json:"data"`
	}
	if err := f.json(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, err
	}
	for _, a := range r.Data {
		name := firstNonEmpty(a.Name, a.ID)
		out = append(out, FoundryAgentInfo{ID: name, Name: name, Model: firstNonEmpty(a.Versions.Latest.Definition.Model, a.Definition.Model),
			Version: a.Versions.Latest.Version})
	}
	return out, nil
}

func (f *FoundryProvider) Models(ctx context.Context) ([]string, error) {
	ags, err := f.Agents(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, a := range ags {
		out = append(out, a.ID)
	}
	return out, nil
}

func (f *FoundryProvider) Status(ctx context.Context, _ string) Status {
	st := Status{Model: f.cfg.AgentID}
	if f.cfg.Mode == "application" {
		// sem endpoint de listagem: a validação é feita pelo Ping (uma resposta curta)
		st.Online, st.ModelFound = true, true
		return st
	}
	ags, err := f.Agents(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Online = true
	for _, a := range ags {
		label := a.ID
		if a.Name != "" && a.Name != a.ID {
			label += " (" + a.Name + ")"
		}
		if a.Model != "" {
			label += " · " + a.Model
		}
		st.Models = append(st.Models, label)
		if a.ID == f.cfg.AgentID || a.Name == f.cfg.AgentID || strings.HasPrefix(f.cfg.AgentID, a.Name+":") {
			st.ModelFound = true
		}
	}
	return st
}

// Ping valida credenciais e o agente com uma pergunta curta.
func (f *FoundryProvider) Ping(ctx context.Context, _ string) error {
	if f.cfg.Mode != "application" && f.cfg.AgentID == "" {
		return errors.New("informe o agente")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	_, _, err := f.Stream(ctx, []Message{{Role: "user", Content: "Responda apenas: ok"}}, GenOptions{ConversationKey: ""}, nil, nil, 0, io.Discard)
	return err
}

// ---------- API Responses (agentes novos e aplicações publicadas) ----------

func isNotFoundOrBad(err error) bool {
	m := err.Error()
	return strings.Contains(m, "HTTP 400") || strings.Contains(m, "HTTP 404")
}

func agentRef(id string) map[string]any {
	ref := map[string]any{"type": "agent_reference", "name": id}
	if name, ver, ok := strings.Cut(id, ":"); ok && name != "" && ver != "" {
		ref["name"], ref["version"] = name, ver
	}
	return ref
}

func (f *FoundryProvider) streamResponses(ctx context.Context, msgs []Message, opt GenOptions, w io.Writer) (string, Usage, error) {
	var usage Usage
	if f.cfg.Mode == "agent" && f.cfg.AgentID == "" {
		return "", usage, errors.New("informe o nome do agente")
	}
	key := opt.ConversationKey
	f.mu.Lock()
	prev := f.threads[key]
	f.mu.Unlock()
	if key == "" {
		prev = ""
	}
	build := func(prev string) map[string]any {
		var input []map[string]any
		extra := strings.TrimSpace(opt.ExtraInstructions)
		if f.cfg.Mode == "application" {
			// sem estado no servidor: envia o histórico (sem o system prompt do doomctl)
			if extra != "" {
				input = append(input, map[string]any{"role": "developer", "content": extra})
			}
			for _, m := range msgs {
				if m.Role == "user" || m.Role == "assistant" {
					input = append(input, map[string]any{"role": m.Role, "content": m.Content})
				}
			}
		} else {
			if prev == "" {
				// conversa nova no Foundry (ou contexto perdido após recarregar a configuração):
				// envia as instruções e o histórico local para não perder o contexto
				if extra != "" {
					input = append(input, map[string]any{"role": "developer", "content": extra})
				}
				for _, m := range msgs {
					if m.Role == "user" || m.Role == "assistant" {
						input = append(input, map[string]any{"role": m.Role, "content": m.Content})
					}
				}
			} else {
				for i := len(msgs) - 1; i >= 0; i-- {
					if msgs[i].Role == "user" {
						input = append(input, map[string]any{"role": "user", "content": msgs[i].Content})
						break
					}
				}
			}
		}
		b := map[string]any{"input": input, "stream": true}
		if f.cfg.Mode == "agent" {
			b["agent_reference"] = agentRef(f.cfg.AgentID)
			if prev != "" {
				b["previous_response_id"] = prev
			}
		}
		if opt.MaxTokens > 0 {
			b["max_output_tokens"] = opt.MaxTokens
		}
		return b
	}
	path := "/openai/responses"
	switch {
	case f.cfg.Mode == "application":
		path = "/responses"
	case f.cfg.APIVersion == "v1":
		path = "/openai/v1/responses"
	}
	resp, err := f.req(ctx, http.MethodPost, path, build(prev))
	if err != nil && prev != "" && isNotFoundOrBad(err) {
		// resposta anterior expirada/removida: recomeça a conversa (com o histórico local)
		f.ResetConversation(key)
		prev = ""
		resp, err = f.req(ctx, http.MethodPost, path, build(""))
	}
	if err != nil {
		return "", usage, err
	}
	defer resp.Body.Close()
	var full strings.Builder
	var respID string
	write := func(t string) error {
		if t == "" {
			return nil
		}
		if _, err := io.WriteString(w, t); err != nil {
			return err
		}
		full.WriteString(t)
		return nil
	}
	type respObj struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	finish := func(r respObj) error {
		respID = firstNonEmpty(r.ID, respID)
		if r.Usage != nil {
			usage = Usage{PromptTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
		}
		if r.Error != nil && r.Error.Message != "" {
			return errors.New("o agente falhou: " + strings.TrimPrefix(r.Error.Code+": ", ": ") + r.Error.Message)
		}
		for _, o := range r.Output {
			switch o.Type {
			case "function_call":
				return errors.New("o agente pediu a execução de uma função do cliente (" + o.Name + "), que o doomctl não executa; use ferramentas do lado do Foundry")
			case "mcp_approval_request":
				return errors.New("o agente pediu aprovação para usar uma ferramenta MCP; no Foundry, configure a ferramenta com require_approval=\"never\"")
			}
		}
		if full.Len() == 0 {
			for _, o := range r.Output {
				if o.Type == "message" {
					for _, c := range o.Content {
						if c.Type == "output_text" || c.Type == "text" {
							if err := write(c.Text); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		if r.Status == "incomplete" && r.IncompleteDetails != nil && full.Len() == 0 {
			return errors.New("resposta incompleta: " + r.IncompleteDetails.Reason)
		}
		return nil
	}
	var runErr error
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		// servidor sem streaming: resposta JSON completa
		var r respObj
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
			return "", usage, fmt.Errorf("resposta inválida do Foundry: %w", err)
		}
		runErr = finish(r)
	} else {
		err = sseLines(resp.Body, func(event, data string) (bool, error) {
			if strings.TrimSpace(data) == "[DONE]" {
				return true, nil
			}
			var ev struct {
				Type     string  `json:"type"`
				Delta    string  `json:"delta"`
				Message  string  `json:"message"`
				Code     string  `json:"code"`
				Response respObj `json:"response"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				return false, nil
			}
			typ := firstNonEmpty(ev.Type, event)
			switch typ {
			case "response.output_text.delta":
				if err := write(ev.Delta); err != nil {
					return true, err
				}
			case "response.created", "response.in_progress":
				respID = firstNonEmpty(ev.Response.ID, respID)
			case "response.completed", "response.incomplete", "response.failed":
				runErr = finish(ev.Response)
				if runErr == nil && typ == "response.failed" {
					runErr = errors.New("o agente falhou (response.failed)")
				}
				return true, nil
			case "error":
				runErr = errors.New("erro no streaming do agente: " + firstNonEmpty(ev.Message, ev.Code, data))
				return true, nil
			}
			return false, nil
		})
		if err != nil && runErr == nil {
			runErr = err
		}
	}
	if runErr != nil {
		return full.String(), usage, runErr
	}
	if f.cfg.Mode == "agent" && key != "" && respID != "" {
		f.mu.Lock()
		f.threads[key] = respID
		f.mu.Unlock()
	}
	return full.String(), usage, nil
}
