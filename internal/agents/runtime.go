package agents

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ConnectionDef descreve uma conexão com um provedor de modelos.
type ConnectionDef struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"` // ollama | openai | foundry_agent
	Preset          string            `json:"preset"`
	BaseURL         string            `json:"base_url"`
	ChatPath        string            `json:"chat_path"`
	ModelsPath      string            `json:"models_path"`
	Query           string            `json:"query"`
	Headers         map[string]string `json:"headers"`
	TokenParam      string            `json:"token_param"`
	SendTemperature bool              `json:"send_temperature"`
	IncludeUsage    bool              `json:"include_usage"`
	Model           string            `json:"model"` // modelo padrão (ou agente no Foundry)
	Mode            string            `json:"mode"`  // Foundry: agent | application | classic
	APIVersion      string            `json:"api_version"`
	Auth            AuthConfig        `json:"auth"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
}

// MCPServerDef descreve um servidor MCP (ferramentas) e o cache das ferramentas.
type MCPServerDef struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Auth    AuthConfig        `json:"auth"`
	Headers map[string]string `json:"headers"`
	Enabled bool              `json:"enabled"`
	// ToolsEnabled liga/desliga cada ferramenta. Sem entrada, só as marcadas como
	// somente leitura (readOnlyHint) ficam ativas — as que alteram algo exigem liberação.
	ToolsEnabled   map[string]bool `json:"tools_enabled"`
	Tools          []MCPTool       `json:"tools"`
	TimeoutSeconds int             `json:"timeout_seconds"`
}

// ToolEnabled informa se a ferramenta está liberada para os agentes.
func (m MCPServerDef) ToolEnabled(t MCPTool) bool {
	if v, ok := m.ToolsEnabled[t.Name]; ok {
		return v
	}
	return t.ReadOnly
}

// NewProvider cria o provedor de uma conexão.
func NewProvider(c ConnectionDef) (Provider, error) {
	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	switch c.Kind {
	case "ollama":
		if c.BaseURL == "" {
			return nil, fmt.Errorf("conexão %q: informe o endereço do Ollama", c.Name)
		}
		if c.ID == 0 {
			return NewOllamaProvider(c.BaseURL, c.Model, timeout), nil
		}
		return NewOllamaConnection(c.BaseURL, c.Model, timeout, c.Headers, c.Auth), nil
	case "openai":
		if c.BaseURL == "" {
			return nil, fmt.Errorf("conexão %q: informe a URL base da API", c.Name)
		}
		return NewOpenAIProvider(OpenAIConfig{BaseURL: c.BaseURL, ChatPath: c.ChatPath, ModelsPath: c.ModelsPath, Query: c.Query,
			Headers: c.Headers, TokenParam: c.TokenParam, SendTemperature: c.SendTemperature, IncludeUsage: c.IncludeUsage,
			Model: c.Model, Auth: c.Auth, Timeout: timeout}), nil
	case "foundry_agent":
		if c.BaseURL == "" {
			return nil, fmt.Errorf("conexão %q: informe o endpoint do projeto", c.Name)
		}
		return NewFoundryProvider(FoundryConfig{Endpoint: c.BaseURL, Mode: c.Mode, AgentID: c.Model, APIVersion: c.APIVersion, Auth: c.Auth, Timeout: timeout}), nil
	}
	return nil, fmt.Errorf("tipo de conexão desconhecido: %q", c.Kind)
}

// RuntimeInput reúne tudo o que define o comportamento dos agentes.
type RuntimeInput struct {
	Settings      Settings
	DefaultOllama ConnectionDef // ID 0: Ollama do .env
	EnvThink      *bool
	Connections   []ConnectionDef
	MCP           []MCPServerDef
}

type toolRef struct {
	client     *MCPClient
	serverName string
	tool       string
}

type agentRT struct {
	prov     Provider
	conn     ConnectionDef
	opt      GenOptions
	system   string
	tools    []ToolSpec
	toolMap  map[string]toolRef
	maxTools int
}

// Runtime é a configuração "compilada" (provedores, prompts e ferramentas) em uso.
type Runtime struct {
	Settings Settings
	router   struct {
		prov Provider
		opt  GenOptions
		conn ConnectionDef
	}
	agents map[Route]*agentRT
	provs  map[int64]Provider
	defs   map[int64]ConnectionDef
	Errors []string
}

var toolNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func toolName(server, tool string) string {
	n := toolNameRe.ReplaceAllString(strings.ToLower(server), "_") + "__" + toolNameRe.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// gpuLayers converte o modo de GPU em num_gpu (nil = automático, campo não enviado).
func gpuLayers(g GPUSettings) *int {
	switch g.Mode {
	case "cpu":
		n := 0
		return &n
	case "layers":
		n := g.Layers
		return &n
	}
	return nil
}

func parseThink(s string, env *bool) *bool {
	switch s {
	case "true":
		b := true
		return &b
	case "false":
		b := false
		return &b
	}
	return env
}

// BuildRuntime monta o runtime. Conexões inválidas não impedem o funcionamento:
// o agente afetado volta para o Ollama padrão e o problema fica em Errors.
func BuildRuntime(in RuntimeInput) *Runtime {
	rt := &Runtime{Settings: in.Settings, agents: map[Route]*agentRT{}, provs: map[int64]Provider{}, defs: map[int64]ConnectionDef{}}
	if err := rt.Settings.Normalize(); err != nil {
		rt.Errors = append(rt.Errors, "configurações inválidas, usando o padrão: "+err.Error())
		rt.Settings = DefaultSettings()
	}
	st := rt.Settings
	def := in.DefaultOllama
	def.ID, def.Kind = 0, "ollama"
	if def.Name == "" {
		def.Name = "Ollama padrão"
	}
	if p, err := NewProvider(def); err == nil {
		rt.provs[0], rt.defs[0] = p, def
	} else {
		rt.Errors = append(rt.Errors, err.Error())
	}
	for _, c := range in.Connections {
		p, err := NewProvider(c)
		if err != nil {
			rt.Errors = append(rt.Errors, err.Error())
			continue
		}
		rt.provs[c.ID], rt.defs[c.ID] = p, c
	}
	pick := func(id int64, who string) (Provider, ConnectionDef) {
		if p, ok := rt.provs[id]; ok {
			return p, rt.defs[id]
		}
		if id != 0 {
			rt.Errors = append(rt.Errors, fmt.Sprintf("%s: conexão #%d indisponível; usando o Ollama padrão", who, id))
		}
		return rt.provs[0], rt.defs[0]
	}
	// MCP
	clients := map[int64]*MCPClient{}
	servers := map[int64]MCPServerDef{}
	for _, m := range in.MCP {
		if !m.Enabled {
			continue
		}
		servers[m.ID] = m
		clients[m.ID] = NewMCPClient(MCPConfig{ID: m.ID, Name: m.Name, URL: m.URL, Auth: m.Auth, Headers: m.Headers,
			Timeout: time.Duration(m.TimeoutSeconds) * time.Second})
	}
	// classificador
	rt.router.prov, rt.router.conn = pick(st.Router.ConnectionID, "classificador")
	if rt.router.prov != nil && rt.router.prov.Kind() == "foundry_agent" {
		rt.Errors = append(rt.Errors, "classificador: agentes do Azure AI Foundry não classificam; usando o Ollama padrão")
		rt.router.prov, rt.router.conn = rt.provs[0], rt.defs[0]
	}
	rt.router.opt = GenOptions{Model: firstNonEmpty(st.Router.Model, rt.router.conn.Model), Think: in.EnvThink}
	if rt.router.conn.Kind == "ollama" {
		rt.router.opt.KeepAlive, rt.router.opt.NumGPU, rt.router.opt.NumThread = st.GPU.KeepAlive, gpuLayers(st.GPU), st.GPU.NumThread
	}
	for _, r := range []Route{RouteCloud, RouteNetSec} {
		as := st.Agents[r]
		p, c := pick(as.ConnectionID, PersonaName(r))
		a := &agentRT{prov: p, conn: c, maxTools: as.MaxToolCalls, toolMap: map[string]toolRef{}}
		a.opt = GenOptions{Model: firstNonEmpty(as.Model, c.Model), Temperature: as.Temperature, TopP: as.TopP, RepeatPenalty: as.RepeatPenalty,
			NumCtx: as.NumCtx, MaxTokens: as.MaxTokens, Think: parseThink(as.Think, in.EnvThink)}
		if a.opt.MaxTokens == 0 {
			a.opt.MaxTokens = st.Limits.MaxOutputTokens
		}
		if c.Kind == "ollama" {
			a.opt.NumGPU, a.opt.NumThread, a.opt.KeepAlive = gpuLayers(st.GPU), st.GPU.NumThread, st.GPU.KeepAlive
		}
		a.system = ComposeSystemPrompt(st, r)
		if c.Kind == "foundry_agent" && as.SendGuardrails {
			extra := ""
			if as.PromptMode != "default" && strings.TrimSpace(as.Prompt) != "" {
				extra = strings.TrimSpace(as.Prompt) + "\n\n"
			}
			a.opt.ExtraInstructions = extra + GuardrailsOnly(st)
		}
		if c.Kind != "foundry_agent" {
			for _, sid := range as.MCPServers {
				srv, ok := servers[sid]
				if !ok {
					continue
				}
				for _, t := range srv.Tools {
					if !srv.ToolEnabled(t) {
						continue
					}
					n := toolName(srv.Name, t.Name)
					if _, dup := a.toolMap[n]; dup {
						continue
					}
					desc := strings.TrimSpace("[" + srv.Name + "] " + firstNonEmpty(t.Title, "") + " " + t.Description)
					a.tools = append(a.tools, ToolSpec{Name: n, Description: truncateStr(desc, 1024), Parameters: t.InputSchema})
					a.toolMap[n] = toolRef{client: clients[sid], serverName: srv.Name, tool: t.Name}
				}
			}
		}
		rt.agents[r] = a
	}
	return rt
}

// AgentInfo descreve a configuração efetiva de uma persona.
type AgentInfo struct {
	Route      Route  `json:"route"`
	Persona    string `json:"persona"`
	Connection string `json:"connection"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	External   bool   `json:"external"`
	Tools      int    `json:"tools"`
}

func (rt *Runtime) Agent(r Route) AgentInfo {
	a := rt.agents[r]
	if a == nil || a.prov == nil {
		return AgentInfo{Route: r, Persona: PersonaName(r)}
	}
	return AgentInfo{Route: r, Persona: PersonaName(r), Connection: a.conn.Name, Kind: a.conn.Kind, Model: a.opt.Model,
		External: a.prov.External(), Tools: len(a.tools)}
}

// RouterInfo descreve o classificador em uso.
func (rt *Runtime) RouterInfo() AgentInfo {
	if rt.router.prov == nil {
		return AgentInfo{}
	}
	return AgentInfo{Persona: "Classificador", Connection: rt.router.conn.Name, Kind: rt.router.conn.Kind, Model: rt.router.opt.Model,
		External: rt.router.prov.External()}
}

// Provider devolve o provedor de uma conexão (0 = Ollama padrão).
func (rt *Runtime) Provider(id int64) (Provider, ConnectionDef, bool) {
	p, ok := rt.provs[id]
	return p, rt.defs[id], ok
}

// SystemPrompt devolve o prompt efetivo de uma persona.
func (rt *Runtime) SystemPrompt(r Route) string {
	if a := rt.agents[r]; a != nil {
		return a.system
	}
	return ComposeSystemPrompt(rt.Settings, r)
}

// AgentExternal informa se a persona usa um provedor externo.
func (rt *Runtime) AgentExternal(r Route) bool {
	a := rt.agents[r]
	return a != nil && a.prov != nil && a.prov.External()
}

// Classify usa o classificador configurado.
func (rt *Runtime) Classify(ctx context.Context, q string) (Route, error) {
	if rt.router.prov == nil {
		return "", fmt.Errorf("classificador indisponível")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rota": map[string]any{"type": "string", "enum": []string{string(RouteCloud), string(RouteNetSec), string(RouteOffTopic)}},
		},
		"required": []string{"rota"},
	}
	out, err := rt.router.prov.ClassifyJSON(ctx, []Message{{Role: "system", Content: classifierPrompt}, {Role: "user", Content: q}}, rt.router.opt, schema)
	if err != nil {
		return "", err
	}
	return parseClassifierOutput(out)
}

// Decide aplica guardrails de entrada (termos bloqueados) e o roteamento configurado.
func (rt *Runtime) Decide(ctx context.Context, q string) Decision {
	st := rt.Settings
	if t := BlockedTerm(q, st.Guardrails.BlockedTerms); t != "" {
		return Decision{Route: RouteOffTopic, Scores: heuristicScores(q), Reason: "termo bloqueado: " + t}
	}
	classify := rt.Classify
	if !st.Router.UseLLM {
		classify = func(context.Context, string) (Route, error) { return "", fmt.Errorf("classificador LLM desativado") }
	}
	d := decide(ctx, q, classify, st.Agents[RouteCloud].Keywords, st.Agents[RouteNetSec].Keywords)
	if !st.Guardrails.ScopeRefusal && d.Route == RouteOffTopic && strings.TrimSpace(q) != "" {
		if d.Scores.NetSec > d.Scores.Cloud {
			d.Route = RouteNetSec
		} else {
			d.Route = RouteCloud
		}
		d.Reason += "; escopo livre (recusa desativada)"
	}
	return d
}

// RefusalMessage devolve a mensagem de recusa configurada.
func (rt *Runtime) RefusalMessage() string {
	if m := strings.TrimSpace(rt.Settings.Guardrails.RefusalMessage); m != "" {
		return m
	}
	return RefusalMsg
}

// ToolEvent registra o uso de uma ferramenta MCP.
type ToolEvent struct {
	Name     string `json:"name"`
	Server   string `json:"server"`
	Tool     string `json:"tool"`
	Status   string `json:"status"` // start | ok | error
	Error    string `json:"error,omitempty"`
	Duration int64  `json:"duration_ms"`
}

type Hooks struct {
	OnTool func(ToolEvent)
}

// Result é o resultado de uma resposta.
type Result struct {
	Answer     string      `json:"answer"`
	Usage      Usage       `json:"usage"`
	Connection string      `json:"connection"`
	Kind       string      `json:"kind"`
	Model      string      `json:"model"`
	Tools      []ToolEvent `json:"tools"`
	Redacted   []string    `json:"redacted"`
}

// ---------- Assistente ----------

// Assistant mantém o runtime ativo e o histórico por chave (usuário) e por persona.
type Assistant struct {
	llm     *Ollama
	rt      atomic.Pointer[Runtime]
	mu      sync.Mutex
	history map[string]map[Route][]Message
}

func NewAssistant(llm *Ollama) *Assistant {
	a := &Assistant{llm: llm, history: map[string]map[Route][]Message{}}
	st := DefaultSettings()
	for _, r := range []Route{RouteCloud, RouteNetSec} {
		st.Agents[r].Temperature = llm.cfg.Temperature
	}
	a.rt.Store(BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{Name: "Ollama padrão", Kind: "ollama",
		BaseURL: llm.cfg.Host, Model: llm.cfg.Model}, EnvThink: llm.cfg.Think}))
	return a
}

func (a *Assistant) LLM() *Ollama { return a.llm }

// Runtime devolve o runtime ativo.
func (a *Assistant) Runtime() *Runtime { return a.rt.Load() }

// SetRuntime troca o runtime (configuração aplicada sem reiniciar o servidor).
func (a *Assistant) SetRuntime(rt *Runtime) {
	old := a.rt.Swap(rt)
	if old != nil {
		for _, p := range old.provs {
			if f, ok := p.(*FoundryProvider); ok {
				f.ResetConversation("")
			}
		}
	}
}

// Ask roteia a pergunta e faz streaming da resposta. onDecision é chamado antes
// do streaming (ex.: para imprimir "[Atlas]"). Perguntas fora de escopo recebem
// exatamente a mensagem de recusa sem chamar a persona.
func (a *Assistant) Ask(ctx context.Context, key, q string, w io.Writer, onDecision func(Decision)) (Decision, string, error) {
	d := a.Runtime().Decide(ctx, q)
	if onDecision != nil {
		onDecision(d)
	}
	ans, err := a.AskRouted(ctx, key, d, q, w)
	return d, ans, err
}

// AskRouted responde com a persona já decidida (o chamador fez o Decide).
func (a *Assistant) AskRouted(ctx context.Context, key string, d Decision, q string, w io.Writer) (string, error) {
	res, err := a.AskRoutedEx(ctx, key, d, q, w, nil)
	return res.Answer, err
}

// AskRoutedEx é AskRouted com ganchos (ferramentas) e metadados da resposta.
func (a *Assistant) AskRoutedEx(ctx context.Context, key string, d Decision, q string, w io.Writer, hooks *Hooks) (Result, error) {
	rt := a.Runtime()
	if d.Route == RouteOffTopic {
		msg := rt.RefusalMessage()
		_, err := io.WriteString(w, msg)
		return Result{Answer: msg}, err
	}
	ag := rt.agents[d.Route]
	if ag == nil || ag.prov == nil {
		return Result{}, fmt.Errorf("persona sem provedor configurado")
	}
	res := Result{Connection: ag.conn.Name, Kind: ag.conn.Kind, Model: ag.opt.Model}
	st := rt.Settings
	if st.Guardrails.Redact == "all" || (st.Guardrails.Redact == "external" && ag.prov.External()) {
		q, res.Redacted = Redact(q)
	}

	a.mu.Lock()
	h := append([]Message(nil), a.history[key][d.Route]...)
	a.mu.Unlock()

	msgs := append([]Message{{Role: "system", Content: ag.system}}, h...)
	msgs = append(msgs, Message{Role: "user", Content: q})

	opt := ag.opt
	opt.ConversationKey = key + "|" + string(d.Route)
	var run ToolRunner
	if len(ag.tools) > 0 {
		run = func(ctx context.Context, call ToolCall) (string, error) {
			ref, ok := ag.toolMap[call.Name]
			ev := ToolEvent{Name: call.Name, Server: ref.serverName, Tool: ref.tool, Status: "start"}
			if !ok {
				ev.Status, ev.Error = "error", "ferramenta desconhecida"
				res.Tools = append(res.Tools, ev)
				return "", fmt.Errorf("ferramenta %q não existe", call.Name)
			}
			if hooks != nil && hooks.OnTool != nil {
				hooks.OnTool(ev)
			}
			start := time.Now()
			tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			out, isErr, err := ref.client.CallTool(tctx, ref.tool, call.Args)
			cancel()
			ev.Duration = time.Since(start).Milliseconds()
			ev.Status = "ok"
			if err != nil || isErr {
				ev.Status = "error"
				if err != nil {
					ev.Error = err.Error()
				} else {
					ev.Error = truncateStr(out, 300)
				}
			}
			res.Tools = append(res.Tools, ev)
			if hooks != nil && hooks.OnTool != nil {
				hooks.OnTool(ev)
			}
			if err != nil {
				return "", err
			}
			return clip(out, st.Limits.ToolResultChars), nil
		}
	}
	answer, usage, err := ag.prov.Stream(ctx, msgs, opt, ag.tools, run, ag.maxTools, w)
	res.Answer, res.Usage = answer, usage
	if err != nil {
		return res, err
	}
	h = append(h, Message{Role: "user", Content: q}, Message{Role: "assistant", Content: answer})
	if max := st.Limits.HistoryMessages; len(h) > max {
		h = h[len(h)-max:]
	}
	a.mu.Lock()
	if a.history[key] == nil {
		a.history[key] = map[Route][]Message{}
	}
	a.history[key][d.Route] = h
	a.mu.Unlock()
	return res, nil
}

// Reset apaga o histórico (e threads em agentes externos) de uma chave.
func (a *Assistant) Reset(key string) {
	a.mu.Lock()
	delete(a.history, key)
	a.mu.Unlock()
	for _, p := range a.Runtime().provs {
		if f, ok := p.(*FoundryProvider); ok {
			f.ResetConversation(key + "|")
		}
	}
}

// ProviderPinger é implementado pelos provedores que validam credenciais com uma chamada real.
type ProviderPinger interface {
	Ping(ctx context.Context, model string) error
}

// Ping do Ollama: servidor no ar e modelo presente.
func (o *OllamaProvider) Ping(ctx context.Context, model string) error {
	st := o.Status(ctx, model)
	if st.Error != "" {
		return fmt.Errorf("%s", st.Error)
	}
	if !st.ModelFound {
		return fmt.Errorf("modelo %q não encontrado no Ollama (baixe em GPU & modelos)", st.Model)
	}
	return nil
}

// SortedConnections devolve as conexões do runtime em ordem de ID.
func (rt *Runtime) SortedConnections() []ConnectionDef {
	var out []ConnectionDef
	for _, d := range rt.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
