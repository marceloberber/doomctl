package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// ---------- regressão: comportamento padrão idêntico ao original ----------

func TestDefaultPromptsMatchLegacy(t *testing.T) {
	for file, got := range map[string]string{
		"testdata/legacy_atlas.txt":      SystemPromptFor(RouteCloud),
		"testdata/legacy_sentinela.txt":  SystemPromptFor(RouteNetSec),
		"testdata/legacy_classifier.txt": classifierPrompt,
	} {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: prompt padrão diverge do original\n--- got ---\n%s\n--- want ---\n%s", file, got, want)
		}
	}
	// o runtime padrão usa exatamente o mesmo texto
	rt := BuildRuntime(RuntimeInput{Settings: DefaultSettings(), DefaultOllama: ConnectionDef{BaseURL: "http://x"}})
	if rt.SystemPrompt(RouteCloud) != SystemPromptFor(RouteCloud) || rt.SystemPrompt(RouteNetSec) != SystemPromptFor(RouteNetSec) {
		t.Fatal("runtime padrão com prompt diferente")
	}
	if len(rt.Errors) != 0 {
		t.Fatalf("erros inesperados: %v", rt.Errors)
	}
}

// capture grava os corpos das requisições /api/chat.
type capture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capture) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, b)
		c.mu.Unlock()
		var req map[string]any
		json.Unmarshal(b, &req)
		if req["stream"] == false {
			fmt.Fprint(w, `{"message":{"role":"assistant","content":"{\"rota\":\"cloud_devops\"}"},"done":true}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"**Resumo:** "},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"ok"},"done":true}`)
	}))
}

// As requisições ao Ollama com as configurações padrão são byte a byte iguais às do cliente original.
func TestDefaultOllamaRequestsAreIdentical(t *testing.T) {
	for _, think := range []*bool{nil, func() *bool { b := false; return &b }()} {
		c := &capture{}
		srv := c.server(t)
		cfg := Config{Host: srv.URL, Model: "qwen3.8", Temperature: 0.25, Think: think}
		legacy := NewOllama(cfg)
		msgs := []Message{{Role: "system", Content: SystemPromptFor(RouteCloud)}, {Role: "user", Content: "como versiono o tfstate?"}}
		if _, err := legacy.Chat(context.Background(), msgs, io.Discard); err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Classify(context.Background(), "pergunta ambígua"); err != nil {
			t.Fatal(err)
		}
		a := NewAssistant(legacy)
		var out bytes.Buffer
		if _, err := a.AskRouted(context.Background(), "k", Decision{Route: RouteCloud}, "como versiono o tfstate?", &out); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Runtime().Classify(context.Background(), "pergunta ambígua"); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if out.String() != "**Resumo:** ok" {
			t.Fatalf("stream = %q", out.String())
		}
		if len(c.bodies) != 4 {
			t.Fatalf("requisições = %d", len(c.bodies))
		}
		if !bytes.Equal(c.bodies[0], c.bodies[2]) {
			t.Errorf("chat diverge:\nlegacy: %s\nnovo:   %s", c.bodies[0], c.bodies[2])
		}
		if !bytes.Equal(c.bodies[1], c.bodies[3]) {
			t.Errorf("classificador diverge:\nlegacy: %s\nnovo:   %s", c.bodies[1], c.bodies[3])
		}
	}
}

// ---------- personalização ----------

func TestComposeToggles(t *testing.T) {
	st := DefaultSettings()
	st.Guardrails.ScopeRefusal = false
	st.Style.Emojis = false
	p := ComposeSystemPrompt(st, RouteCloud)
	if strings.Contains(p, RefusalMsg) {
		t.Error("regra de recusa deveria sair do prompt")
	}
	if strings.Contains(p, "🛑") || strings.Contains(p, "⚠️") || !strings.Contains(p, "DESTRUTIVO:") {
		t.Error("marcadores sem emoji não aplicados")
	}
	if !strings.HasPrefix(strings.Split(p, "REGRAS OBRIGATÓRIAS")[1][strings.Index(strings.Split(p, "REGRAS OBRIGATÓRIAS")[1], "\n\n")+2:], "1. ") {
		t.Error("numeração das regras deveria recomeçar em 1")
	}
	// limites ofensivos e integridade são sempre mantidos
	if !strings.Contains(p, "malware") || !strings.Contains(p, "INTEGRIDADE:") {
		t.Error("regras não desativáveis sumiram")
	}

	st = DefaultSettings()
	st.Guardrails.RefusalMessage = "Fora do meu escopo."
	st.Agents[RouteNetSec].PromptMode = "append"
	st.Agents[RouteNetSec].Prompt = "Sempre cite a RFC."
	st.GlobalInstructions = "Use a nomenclatura **ACME**."
	st.Style.Tone, st.Style.Verbosity, st.Style.Sections = "formal", "conciso", false
	p = ComposeSystemPrompt(st, RouteNetSec)
	for _, want := range []string{"Fora do meu escopo.", "Sempre cite a RFC.", "ACME", "Tom formal", "Seja conciso", "Sentinela"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt sem %q", want)
		}
	}
	if strings.Contains(p, "**Resumo:**") {
		t.Error("seções desativadas, mas o prompt ainda exige Resumo")
	}

	st = DefaultSettings()
	st.Agents[RouteCloud].PromptMode = "replace"
	st.Agents[RouteCloud].Prompt = "Você é Bob."
	p = ComposeSystemPrompt(st, RouteCloud)
	if !strings.HasPrefix(p, "Você é Bob.") || strings.Contains(p, "Atlas") || !strings.Contains(p, "REGRAS OBRIGATÓRIAS") {
		t.Errorf("modo substituir incorreto: %q", p[:80])
	}
}

func TestNormalizeValidation(t *testing.T) {
	st := DefaultSettings()
	st.Agents[RouteCloud].Temperature = 0.7
	if err := st.Normalize(); err == nil {
		t.Fatal("temperatura 0.7 deveria falhar com a trava ligada")
	}
	st.Guardrails.SafeTemperature = false
	if err := st.Normalize(); err != nil {
		t.Fatalf("trava desligada: %v", err)
	}
	st.GPU.KeepAlive = "abc"
	if err := st.Normalize(); err == nil {
		t.Fatal("keep_alive inválido aceito")
	}
	st.GPU.KeepAlive = "30m"
	st.Agents[RouteCloud].PromptMode = "replace"
	if err := st.Normalize(); err == nil {
		t.Fatal("substituir sem prompt aceito")
	}
}

func TestDecideBlockedAndKeywords(t *testing.T) {
	st := DefaultSettings()
	st.Guardrails.BlockedTerms = []string{"Projeto Fênix", "salário"}
	st.Agents[RouteNetSec].Keywords = []string{"wazuh"}
	st.Router.UseLLM = false
	rt := BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"}})
	ctx := context.Background()
	if d := rt.Decide(ctx, "Qual o salario do time de terraform?"); d.Route != RouteOffTopic {
		t.Errorf("termo bloqueado: rota %s", d.Route)
	}
	if d := rt.Decide(ctx, "status do projeto fenix no kubernetes"); d.Route != RouteOffTopic {
		t.Errorf("frase bloqueada: rota %s", d.Route)
	}
	if d := rt.Decide(ctx, "como configuro o wazuh?"); d.Route != RouteNetSec || d.UsedLLM {
		t.Errorf("palavra extra: %+v", d)
	}
	// sem classificador LLM e sem pontuação: falha fechada
	if d := rt.Decide(ctx, "bom dia"); d.Route != RouteOffTopic {
		t.Errorf("sem llm: rota %s", d.Route)
	}
	st.Guardrails.ScopeRefusal = false
	rt = BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"}})
	if d := rt.Decide(ctx, "bom dia"); d.Route != RouteCloud {
		t.Errorf("escopo livre: rota %s", d.Route)
	}
}

func TestRefusalCustomMessage(t *testing.T) {
	st := DefaultSettings()
	st.Guardrails.RefusalMessage = "Só falo de infra."
	a := NewAssistant(NewOllama(Config{Host: "http://127.0.0.1:1", Model: "m", Temperature: 0.25}))
	a.SetRuntime(BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"}}))
	var out bytes.Buffer
	ans, err := a.AskRouted(context.Background(), "u", Decision{Route: RouteOffTopic}, "x", &out)
	if err != nil || ans != "Só falo de infra." || out.String() != ans {
		t.Fatalf("recusa = %q, %v", ans, err)
	}
}

func TestOllamaGPUOptionsAndLimits(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	defer srv.Close()
	st := DefaultSettings()
	st.GPU = GPUSettings{Mode: "cpu", KeepAlive: "30m", NumThread: 8}
	st.Limits.MaxOutputTokens = 512
	a := NewAssistant(NewOllama(Config{Host: srv.URL, Model: "qwen3.8", Temperature: 0.25}))
	a.SetRuntime(BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: srv.URL, Model: "qwen3.8"}}))
	if _, err := a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, "oi", io.Discard); err != nil {
		t.Fatal(err)
	}
	var req struct {
		KeepAlive string         `json:"keep_alive"`
		Options   map[string]any `json:"options"`
	}
	json.Unmarshal(c.bodies[0], &req)
	if req.KeepAlive != "30m" || req.Options["num_gpu"] != 0.0 || req.Options["num_thread"] != 8.0 || req.Options["num_predict"] != 512.0 {
		t.Fatalf("opções = %s", c.bodies[0])
	}
}

func TestHistoryLimit(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	defer srv.Close()
	st := DefaultSettings()
	st.Limits.HistoryMessages = 2
	a := NewAssistant(NewOllama(Config{Host: srv.URL, Model: "m", Temperature: 0.25}))
	a.SetRuntime(BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: srv.URL, Model: "m"}}))
	for i := 0; i < 3; i++ {
		a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, fmt.Sprintf("p%d", i), io.Discard)
	}
	if n := len(a.history["u"][RouteCloud]); n != 2 {
		t.Fatalf("histórico = %d", n)
	}
	st.Limits.HistoryMessages = 0
	a.SetRuntime(BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: srv.URL, Model: "m"}}))
	a.AskRouted(context.Background(), "v", Decision{Route: RouteCloud}, "x", io.Discard)
	if n := len(a.history["v"][RouteCloud]); n != 0 {
		t.Fatalf("histórico desativado = %d", n)
	}
}

func TestInvalidConnectionFallsBackToDefault(t *testing.T) {
	st := DefaultSettings()
	st.Agents[RouteCloud].ConnectionID = 99
	rt := BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://ollama:11434", Model: "qwen3.8"}})
	if info := rt.Agent(RouteCloud); info.Kind != "ollama" || info.Model != "qwen3.8" {
		t.Fatalf("fallback = %+v", info)
	}
	if len(rt.Errors) == 0 {
		t.Fatal("deveria registrar o erro da conexão")
	}
}

// ---------- provedores externos ----------

// API compatível com OpenAI + ferramenta MCP (servidor falso), com redação de segredos.
func TestOpenAIProviderWithMCPTool(t *testing.T) {
	var mcpCalls []string
	var mu sync.Mutex
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		mcpCalls = append(mcpCalls, req.Method)
		mu.Unlock()
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "s1" {
			t.Errorf("%s sem sessão", req.Method)
		}
		if r.Header.Get("Authorization") != "Bearer mcp-token" {
			t.Errorf("MCP sem auth")
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/call":
			// resposta em SSE
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"3 instâncias\"}]}}\n\n", req.ID)
		}
	}))
	defer mcp.Close()

	var bodies []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "k123" || r.URL.Query().Get("api-version") != "2024-10-21" {
			t.Errorf("auth/query ausentes: %v %v", r.Header, r.URL)
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"inv__list_vms\",\"arguments\":\"\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"env\\\":\\\"prod\\\"}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"**Resumo:** \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"3 VMs.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer api.Close()

	st := DefaultSettings()
	st.Agents[RouteCloud].ConnectionID = 1
	st.Agents[RouteCloud].MCPServers = []int64{7}
	st.Guardrails.Redact = "all"
	in := RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"},
		Connections: []ConnectionDef{{ID: 1, Name: "Azure OpenAI", Kind: "openai", BaseURL: api.URL + "/openai/deployments/gpt", Query: "api-version=2024-10-21",
			Model: "gpt-4o", SendTemperature: true, IncludeUsage: true, Auth: AuthConfig{Type: "api-key", Secret: "k123"}}},
		MCP: []MCPServerDef{{ID: 7, Name: "Inv", URL: mcp.URL, Enabled: true, Auth: AuthConfig{Type: "bearer", Secret: "mcp-token"},
			Tools: []MCPTool{{Name: "list_vms", Description: "lista VMs", ReadOnly: true, InputSchema: map[string]any{"type": "object"}},
				{Name: "delete_vm", Description: "apaga VM", Destructive: true}}}},
	}
	rt := BuildRuntime(in)
	if len(rt.Errors) > 0 {
		t.Fatal(rt.Errors)
	}
	if info := rt.Agent(RouteCloud); info.Tools != 1 {
		t.Fatalf("ferramentas = %d (a destrutiva não pode ser oferecida)", info.Tools)
	}
	a := NewAssistant(NewOllama(Config{Host: "http://127.0.0.1:1", Model: "m", Temperature: 0.25}))
	a.SetRuntime(rt)
	var out bytes.Buffer
	var events []ToolEvent
	res, err := a.AskRoutedEx(context.Background(), "u", Decision{Route: RouteCloud}, "quantas VMs? password=SuperSecreta123", &out,
		&Hooks{OnTool: func(e ToolEvent) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "**Resumo:** 3 VMs." || res.Usage.OutputTokens != 4 {
		t.Fatalf("saída = %q usage=%+v", out.String(), res.Usage)
	}
	if len(res.Redacted) == 0 {
		t.Fatal("senha não redigida")
	}
	first, _ := json.Marshal(bodies[0])
	if strings.Contains(string(first), "SuperSecreta123") {
		t.Fatal("segredo vazou para o provedor")
	}
	if bodies[0]["temperature"] != 0.25 || bodies[0]["tools"] == nil {
		t.Fatalf("corpo inicial = %s", first)
	}
	second, _ := json.Marshal(bodies[1])
	if !strings.Contains(string(second), `"tool_call_id":"c1"`) || !strings.Contains(string(second), "3 instâncias") {
		t.Fatalf("resultado da ferramenta não enviado: %s", second)
	}
	if len(events) != 2 || events[1].Status != "ok" || events[1].Tool != "list_vms" {
		t.Fatalf("eventos = %+v", events)
	}
	if strings.Join(mcpCalls, ",") != "initialize,notifications/initialized,tools/call" {
		t.Fatalf("chamadas MCP = %v", mcpCalls)
	}
}

func TestMCPListToolsAndSessionExpiry(t *testing.T) {
	inits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			inits++
			w.Header().Set("Mcp-Session-Id", fmt.Sprint("s", inits))
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-03-26","serverInfo":{"name":"x","version":"2"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") == "s1" && inits == 1 {
				w.WriteHeader(404) // sessão expirou
				return
			}
			if r.Header.Get("MCP-Protocol-Version") != "2025-03-26" {
				t.Errorf("versão do protocolo negociada não enviada")
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[{"name":"get","description":"lê","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}},{"name":"put","description":"grava"}]}}`, req.ID)
		}
	}))
	defer srv.Close()
	c := NewMCPClient(MCPConfig{URL: srv.URL})
	info, err := c.Initialize(context.Background())
	if err != nil || info.Name != "x" {
		t.Fatal(info, err)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inits != 2 || len(tools) != 2 || !tools[0].ReadOnly || tools[1].ReadOnly || !tools[1].Destructive {
		t.Fatalf("inits=%d tools=%+v", inits, tools)
	}
}

func TestFoundryAgent(t *testing.T) {
	threads := 0
	var lastRun map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api-version") != "2025-05-01" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("requisição sem versão/token: %s %v", r.URL, r.Header)
		}
		switch {
		case r.URL.Path == "/api/projects/p/threads" && r.Method == "POST":
			threads++
			fmt.Fprintf(w, `{"id":"thread_%d"}`, threads)
		case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == "POST":
			fmt.Fprint(w, `{"id":"msg"}`)
		case strings.HasSuffix(r.URL.Path, "/runs"):
			json.NewDecoder(r.Body).Decode(&lastRun)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: thread.run.created\ndata: {\"id\":\"run_1\",\"status\":\"queued\"}\n\n")
			fmt.Fprint(w, "event: thread.message.delta\ndata: {\"delta\":{\"content\":[{\"type\":\"text\",\"text\":{\"value\":\"Olá \"}}]}}\n\n")
			fmt.Fprint(w, "event: thread.message.delta\ndata: {\"delta\":{\"content\":[{\"type\":\"text\",\"text\":{\"value\":\"do Foundry\"}}]}}\n\n")
			fmt.Fprint(w, "event: thread.run.completed\ndata: {\"id\":\"run_1\",\"status\":\"completed\",\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n")
			fmt.Fprint(w, "event: done\ndata: [DONE]\n\n")
		default:
			t.Errorf("rota inesperada %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	st := DefaultSettings()
	st.Agents[RouteCloud].ConnectionID = 3
	rt := BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"},
		Connections: []ConnectionDef{{ID: 3, Name: "Foundry", Kind: "foundry_agent", Mode: "classic", BaseURL: srv.URL + "/api/projects/p", Model: "asst_1",
			Auth: AuthConfig{Type: "bearer", Secret: "tok"}}}})
	a := NewAssistant(NewOllama(Config{Host: "http://127.0.0.1:1", Model: "m", Temperature: 0.25}))
	a.SetRuntime(rt)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if _, err := a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, "oi", &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != "Olá do Foundry" {
			t.Fatalf("saída = %q", out.String())
		}
	}
	if threads != 1 {
		t.Fatalf("a conversa deveria reutilizar a thread (threads=%d)", threads)
	}
	if lastRun["assistant_id"] != "asst_1" || !strings.Contains(fmt.Sprint(lastRun["additional_instructions"]), "REGRAS OBRIGATÓRIAS") {
		t.Fatalf("run = %v", lastRun)
	}
	a.Reset("u")
	a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, "oi", io.Discard)
	if threads != 2 {
		t.Fatal("limpar a conversa deveria criar nova thread")
	}
}

// Agentes novos do Foundry: API Responses com agent_reference e previous_response_id.
func TestFoundryResponsesAgent(t *testing.T) {
	var bodies []map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/p/openai/v1/responses" || r.URL.RawQuery != "" {
			t.Errorf("rota inesperada %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		n++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_%d\"}}\n\n", n)
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Oi \"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Marcelo\"}\n\n")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n", n)
	}))
	defer srv.Close()
	st := DefaultSettings()
	st.Agents[RouteNetSec].ConnectionID = 4
	rt := BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"},
		Connections: []ConnectionDef{{ID: 4, Name: "Foundry novo", Kind: "foundry_agent", BaseURL: srv.URL + "/api/projects/p", Model: "sec-agent:3",
			Auth: AuthConfig{Type: "bearer", Secret: "tok"}}}})
	a := NewAssistant(NewOllama(Config{Host: "http://127.0.0.1:1", Model: "m", Temperature: 0.25}))
	a.SetRuntime(rt)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		res, err := a.AskRoutedEx(context.Background(), "u", Decision{Route: RouteNetSec}, "oi", &out, nil)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != "Oi Marcelo" || res.Usage.OutputTokens != 2 {
			t.Fatalf("saída = %q %+v", out.String(), res.Usage)
		}
	}
	ref, _ := bodies[0]["agent_reference"].(map[string]any)
	if ref["name"] != "sec-agent" || ref["version"] != "3" || ref["type"] != "agent_reference" {
		t.Fatalf("agent_reference = %v", bodies[0]["agent_reference"])
	}
	if bodies[0]["previous_response_id"] != nil || bodies[1]["previous_response_id"] != "resp_1" {
		t.Fatalf("previous_response_id: %v / %v", bodies[0]["previous_response_id"], bodies[1]["previous_response_id"])
	}
	first, _ := json.Marshal(bodies[0]["input"])
	second, _ := json.Marshal(bodies[1]["input"])
	if !strings.Contains(string(first), "developer") || strings.Contains(string(second), "developer") {
		t.Fatalf("guardrails deveriam ir só no 1º turno: %s | %s", first, second)
	}
}

// Aplicação publicada: sem estado, histórico enviado pelo doomctl; resposta JSON (sem streaming).
func TestFoundryApplicationStateless(t *testing.T) {
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/p/applications/app/protocols/openai/responses" || r.URL.Query().Get("api-version") != "2025-11-15-preview" {
			t.Errorf("rota inesperada %s", r.URL)
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		in, _ := json.Marshal(b["input"])
		inputs = append(inputs, string(in))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"resposta"}]}]}`)
	}))
	defer srv.Close()
	st := DefaultSettings()
	st.Agents[RouteCloud].ConnectionID = 5
	st.Agents[RouteCloud].SendGuardrails = false
	rt := BuildRuntime(RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"},
		Connections: []ConnectionDef{{ID: 5, Name: "App", Kind: "foundry_agent", Mode: "application", BaseURL: srv.URL + "/api/projects/p/applications/app/protocols/openai"}}})
	a := NewAssistant(NewOllama(Config{Host: "http://127.0.0.1:1", Model: "m", Temperature: 0.25}))
	a.SetRuntime(rt)
	for _, q := range []string{"p1", "p2"} {
		var out bytes.Buffer
		if _, err := a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, q, &out); err != nil || out.String() != "resposta" {
			t.Fatal(out.String(), err)
		}
	}
	if strings.Contains(inputs[0], "system") || !strings.Contains(inputs[1], "p1") || !strings.Contains(inputs[1], "resposta") || !strings.Contains(inputs[1], "p2") {
		t.Fatalf("histórico não enviado corretamente: %v", inputs)
	}
}

func TestEntraClientCredentials(t *testing.T) {
	tokenCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			tokenCalls++
			r.ParseForm()
			if r.Form.Get("client_secret") != "sec" || r.Form.Get("scope") != "https://ai.azure.com/.default" {
				t.Errorf("form = %v", r.Form)
			}
			fmt.Fprint(w, `{"access_token":"AT","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer AT" {
			t.Errorf("sem token Entra")
		}
		fmt.Fprint(w, `{"data":[{"id":"asst_9","name":"Ops","model":"gpt-4o"}]}`)
	}))
	defer srv.Close()
	old := entraAuthority
	entraAuthority = srv.URL + "/"
	defer func() { entraAuthority = old }()
	f := NewFoundryProvider(FoundryConfig{Endpoint: srv.URL, Mode: "classic", AgentID: "asst_9", Auth: AuthConfig{Type: "azure_entra", TenantID: "t", ClientID: "c", Secret: "sec"}})
	for i := 0; i < 2; i++ {
		ags, err := f.Agents(context.Background())
		if err != nil || len(ags) != 1 || ags[0].Name != "Ops" {
			t.Fatal(ags, err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("token deveria ficar em cache (chamadas=%d)", tokenCalls)
	}
}

func TestRedact(t *testing.T) {
	in := "chave AKIAABCDEFGHIJKLMNOP e token ghp_abcdefghijklmnopqrstuvwxyz0123 em postgres://app:s3nh4@db:5432/x; password: hunter2hunter"
	out, found := Redact(in)
	for _, leak := range []string{"AKIAABCDEFGHIJKLMNOP", "ghp_abcdefghij", "s3nh4", "hunter2hunter"} {
		if strings.Contains(out, leak) {
			t.Errorf("vazou %q: %s", leak, out)
		}
	}
	if len(found) < 4 {
		t.Errorf("encontrados = %v", found)
	}
	if o, f := Redact("kubectl get pods -n prod"); o != "kubectl get pods -n prod" || len(f) != 0 {
		t.Errorf("texto comum alterado: %q %v", o, f)
	}
}

func TestIsPrivateURL(t *testing.T) {
	for u, want := range map[string]bool{"http://ollama:11434": true, "http://127.0.0.1:1": true, "http://10.1.2.3": true,
		"https://x.openai.azure.com": false, "https://api.openai.com/v1": false, "http://gpu.lan:8000": true} {
		if got := isPrivateURL(u); got != want {
			t.Errorf("%s = %v", u, got)
		}
	}
}

func TestBlockedTermsWithPunctuation(t *testing.T) {
	terms := []string{"acme.corp", "proj_x", "C#", "Fênix"}
	for q, want := range map[string]string{"deploy no ACME.corp?": "acme.corp", "status do proj_x": "proj_x", "app em c# no k8s": "C#",
		"projeto fenix": "Fênix", "kubectl get pods": ""} {
		if got := BlockedTerm(q, terms); got != want {
			t.Errorf("%q → %q, want %q", q, got, want)
		}
	}
	if extraScore("monitorar com node_exporter", []string{"node_exporter"}) != 3 {
		t.Error("palavra-chave extra com _ não casou")
	}
}

// Conexão Ollama cadastrada aplica autenticação e é externa quando pública; a padrão não.
func TestOllamaConnectionAuth(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"models":[]}`)
	}))
	defer srv.Close()
	p, _ := NewProvider(ConnectionDef{ID: 5, Kind: "ollama", BaseURL: srv.URL, Auth: AuthConfig{Type: "bearer", Secret: "tk"}})
	p.Models(context.Background())
	if auth != "Bearer tk" {
		t.Fatalf("auth = %q", auth)
	}
	pub, _ := NewProvider(ConnectionDef{ID: 6, Kind: "ollama", BaseURL: "https://ollama.example.com"})
	def, _ := NewProvider(ConnectionDef{ID: 0, Kind: "ollama", BaseURL: "https://ollama.example.com"})
	if !pub.External() || def.External() {
		t.Fatal("External incorreto")
	}
}

// Após recarregar a configuração (runtime novo), o agente do Foundry recebe o histórico local.
func TestFoundryKeepsContextAcrossReload(t *testing.T) {
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		in, _ := json.Marshal(b["input"])
		inputs = append(inputs, string(in))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"r%d","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"resp%d"}]}]}`, len(inputs), len(inputs))
	}))
	defer srv.Close()
	st := DefaultSettings()
	st.Agents[RouteCloud].ConnectionID = 9
	in := RuntimeInput{Settings: st, DefaultOllama: ConnectionDef{BaseURL: "http://127.0.0.1:1"},
		Connections: []ConnectionDef{{ID: 9, Name: "F", Kind: "foundry_agent", BaseURL: srv.URL + "/api/projects/p", Model: "ag"}}}
	a := NewAssistant(NewOllama(Config{Host: "http://127.0.0.1:1", Model: "m", Temperature: 0.25}))
	a.SetRuntime(BuildRuntime(in))
	a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, "pergunta1", io.Discard)
	a.SetRuntime(BuildRuntime(in)) // ex.: alguém salvou a configuração
	a.AskRouted(context.Background(), "u", Decision{Route: RouteCloud}, "pergunta2", io.Discard)
	if !strings.Contains(inputs[1], "pergunta1") || !strings.Contains(inputs[1], "resp1") {
		t.Fatalf("contexto perdido após recarregar: %s", inputs[1])
	}
}

// Chamadas paralelas sem índice (todas 0) com ids diferentes não podem ser concatenadas.
func TestOpenAIParallelToolCallsSameIndex(t *testing.T) {
	n := 0
	var second map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"type\":\"function\",\"function\":{\"name\":\"s__t1\",\"arguments\":\"{}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"b\",\"type\":\"function\",\"function\":{\"name\":\"s__t2\",\"arguments\":\"{}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		second = b
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	p := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, Model: "m"})
	var called []string
	run := func(_ context.Context, c ToolCall) (string, error) { called = append(called, c.Name); return "x", nil }
	if _, _, err := p.Stream(context.Background(), []Message{{Role: "user", Content: "q"}}, GenOptions{}, []ToolSpec{{Name: "s__t1"}, {Name: "s__t2"}}, run, 4, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Join(called, ",") != "s__t1,s__t2" || second == nil {
		t.Fatalf("chamadas = %v", called)
	}
}
