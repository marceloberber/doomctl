package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doomctl/doomctl/internal/agents"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

// ---------- Configuração dos agentes de IA pelo navegador (somente administradores) ----------
//
// O comportamento padrão (sem nada salvo) é exatamente o original: Ollama do .env,
// prompts do AGENTS.md, temperatura do ROUTER_TEMPERATURE. Qualquer falha ao carregar a
// configuração mantém o runtime padrão.

var aiNameRe = regexp.MustCompile(`^[\p{L}\p{N} ._()/+-]{1,60}$`)

// aiConnConfig é a parte não secreta de uma conexão (o segredo fica em secret_enc).
type aiConnConfig struct {
	BaseURL         string            `json:"base_url"`
	ChatPath        string            `json:"chat_path"`
	ModelsPath      string            `json:"models_path"`
	Query           string            `json:"query"`
	Headers         map[string]string `json:"headers"`
	TokenParam      string            `json:"token_param"`
	SendTemperature bool              `json:"send_temperature"`
	IncludeUsage    bool              `json:"include_usage"`
	Model           string            `json:"model"`
	Mode            string            `json:"mode"`
	APIVersion      string            `json:"api_version"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	Auth            agents.AuthConfig `json:"auth"`
}

type aiConn struct {
	ID         int64        `json:"id"`
	Name       string       `json:"name"`
	Kind       string       `json:"kind"`
	Preset     string       `json:"preset"`
	Config     aiConnConfig `json:"config"`
	HasSecret  bool         `json:"has_secret"`
	Enabled    bool         `json:"enabled"`
	External   bool         `json:"external"`
	LastTestAt *time.Time   `json:"last_test_at"`
	LastStatus string       `json:"last_status"`
	LastError  string       `json:"last_error"`
	CreatedBy  string       `json:"created_by"`
	UpdatedAt  time.Time    `json:"updated_at"`
	InUse      []string     `json:"in_use"`
	secret     []byte
}

func (c aiConn) def(secret string) agents.ConnectionDef {
	cf := c.Config
	auth := cf.Auth
	auth.Secret = secret
	return agents.ConnectionDef{ID: c.ID, Name: c.Name, Kind: c.Kind, Preset: c.Preset, BaseURL: cf.BaseURL, ChatPath: cf.ChatPath,
		ModelsPath: cf.ModelsPath, Query: cf.Query, Headers: cf.Headers, TokenParam: cf.TokenParam, SendTemperature: cf.SendTemperature,
		IncludeUsage: cf.IncludeUsage, Model: cf.Model, Mode: cf.Mode, APIVersion: cf.APIVersion, Auth: auth, TimeoutSeconds: cf.TimeoutSeconds}
}

type aiMCPConfig struct {
	Headers        map[string]string `json:"headers"`
	Auth           agents.AuthConfig `json:"auth"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	ToolsEnabled   map[string]bool   `json:"tools_enabled"`
}

type aiMCP struct {
	ID             int64                `json:"id"`
	Name           string               `json:"name"`
	URL            string               `json:"url"`
	Preset         string               `json:"preset"`
	Config         aiMCPConfig          `json:"config"`
	HasSecret      bool                 `json:"has_secret"`
	Enabled        bool                 `json:"enabled"`
	External       bool                 `json:"external"`
	Tools          []agents.MCPTool     `json:"tools"`
	ServerInfo     agents.MCPServerInfo `json:"server_info"`
	ToolsUpdatedAt *time.Time           `json:"tools_updated_at"`
	LastStatus     string               `json:"last_status"`
	LastError      string               `json:"last_error"`
	UpdatedAt      time.Time            `json:"updated_at"`
	InUse          []string             `json:"in_use"`
	secret         []byte
}

func (m aiMCP) def(secret string) agents.MCPServerDef {
	auth := m.Config.Auth
	auth.Secret = secret
	return agents.MCPServerDef{ID: m.ID, Name: m.Name, URL: m.URL, Auth: auth, Headers: m.Config.Headers, Enabled: m.Enabled,
		ToolsEnabled: m.Config.ToolsEnabled, Tools: m.Tools, TimeoutSeconds: m.Config.TimeoutSeconds}
}

func (s *Server) openSecret(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	p, err := s.box.Open(b)
	if err != nil {
		return "", errors.New("não foi possível ler o segredo (DOOMCTL_SECRET_KEY mudou?)")
	}
	return string(p), nil
}

// ---------- carga do banco ----------

func (s *Server) aiDefaultSettings() agents.Settings {
	st := agents.DefaultSettings()
	for _, a := range st.Agents {
		a.Temperature = s.cfg.RouterTemperature
	}
	return st
}

type aiSettingsMeta struct {
	Saved     bool      `json:"saved"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// errAIDB marca falhas de acesso ao banco (diferentes de configuração inválida).
var errAIDB = errors.New("banco indisponível")

func (s *Server) aiLoadSettings(ctx context.Context) (agents.Settings, aiSettingsMeta, error) {
	var raw []byte
	var meta aiSettingsMeta
	err := s.db.QueryRowContext(ctx, `SELECT value, updated_by, updated_at FROM ai_settings WHERE key='settings'`).Scan(&raw, &meta.UpdatedBy, &meta.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s.aiDefaultSettings(), meta, nil
	}
	if err != nil {
		return s.aiDefaultSettings(), meta, fmt.Errorf("%w: %v", errAIDB, err)
	}
	st := s.aiDefaultSettings()
	if err := json.Unmarshal(raw, &st); err != nil {
		return s.aiDefaultSettings(), meta, err
	}
	if err := st.Normalize(); err != nil {
		return s.aiDefaultSettings(), meta, err
	}
	meta.Saved = true
	return st, meta, nil
}

func (s *Server) aiLoadConns(ctx context.Context) ([]aiConn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, preset, config, secret_enc, enabled, last_test_at, last_status, last_error, created_by, updated_at
		FROM ai_connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []aiConn{}
	for rows.Next() {
		var c aiConn
		var cfg []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Preset, &cfg, &c.secret, &c.Enabled, &c.LastTestAt, &c.LastStatus, &c.LastError, &c.CreatedBy, &c.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(cfg, &c.Config)
		c.HasSecret = len(c.secret) > 0
		c.External = !agents.IsPrivateURL(c.Config.BaseURL)
		c.InUse = []string{}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Server) aiLoadMCP(ctx context.Context) ([]aiMCP, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, url, preset, config, secret_enc, enabled, tools, server_info, tools_updated_at, last_status, last_error, updated_at
		FROM ai_mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []aiMCP{}
	for rows.Next() {
		var m aiMCP
		var cfg, tools, info []byte
		if err := rows.Scan(&m.ID, &m.Name, &m.URL, &m.Preset, &cfg, &m.secret, &m.Enabled, &tools, &info, &m.ToolsUpdatedAt, &m.LastStatus, &m.LastError, &m.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(cfg, &m.Config)
		json.Unmarshal(tools, &m.Tools)
		json.Unmarshal(info, &m.ServerInfo)
		if m.Tools == nil {
			m.Tools = []agents.MCPTool{}
		}
		if m.Config.ToolsEnabled == nil {
			m.Config.ToolsEnabled = map[string]bool{}
		}
		m.HasSecret = len(m.secret) > 0
		m.External = !agents.IsPrivateURL(m.URL)
		m.InUse = []string{}
		out = append(out, m)
	}
	return out, rows.Err()
}

// aiRuntimeInput monta a entrada do runtime a partir do banco (conexões desativadas ficam de fora).
func (s *Server) aiRuntimeInput(ctx context.Context, st agents.Settings) (agents.RuntimeInput, []string) {
	in, warns, err := s.aiRuntimeInputErr(ctx, st)
	if err != nil {
		warns = append(warns, err.Error())
	}
	return in, warns
}

func (s *Server) aiRuntimeInputErr(ctx context.Context, st agents.Settings) (agents.RuntimeInput, []string, error) {
	in := agents.RuntimeInput{Settings: st, EnvThink: s.cfg.OllamaThink,
		DefaultOllama: agents.ConnectionDef{Name: "Ollama padrão", Kind: "ollama", BaseURL: s.cfg.OllamaHost, Model: s.cfg.OllamaModel}}
	var warns []string
	conns, err := s.aiLoadConns(ctx)
	if err != nil {
		return in, warns, fmt.Errorf("conexões: %w", err)
	}
	for _, c := range conns {
		if !c.Enabled {
			continue
		}
		sec, err := s.openSecret(c.secret)
		if err != nil {
			warns = append(warns, c.Name+": "+err.Error())
			continue
		}
		in.Connections = append(in.Connections, c.def(sec))
	}
	servers, err := s.aiLoadMCP(ctx)
	if err != nil {
		return in, warns, fmt.Errorf("MCP: %w", err)
	}
	for _, m := range servers {
		if !m.Enabled {
			continue
		}
		sec, err := s.openSecret(m.secret)
		if err != nil {
			warns = append(warns, m.Name+": "+err.Error())
			continue
		}
		in.MCP = append(in.MCP, m.def(sec))
	}
	return in, warns, nil
}

// reloadAI reconstrói o runtime a partir do banco. Falha de leitura do banco mantém o
// runtime atual (no início, o padrão do .env); configuração salva inválida volta ao padrão.
func (s *Server) reloadAI(parent context.Context) []string {
	s.aiReload.Lock()
	defer s.aiReload.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer cancel()
	st, _, err := s.aiLoadSettings(ctx)
	var warns []string
	if err != nil {
		if errors.Is(err, errAIDB) {
			slog.Error("assistente IA: configuração não recarregada", "erro", err)
			return []string{"não foi possível ler a configuração no banco; a configuração anterior continua ativa"}
		}
		warns = append(warns, "configuração salva inválida; usando o padrão: "+err.Error())
	}
	in, w, dbErr := s.aiRuntimeInputErr(ctx, st)
	if dbErr != nil {
		slog.Error("assistente IA: configuração não recarregada", "erro", dbErr)
		return []string{"não foi possível ler conexões/MCP no banco; a configuração anterior continua ativa"}
	}
	warns = append(warns, w...)
	rt := agents.BuildRuntime(in)
	warns = append(warns, rt.Errors...)
	s.ai.SetRuntime(rt)
	s.aiWarnings.Store(&warns)
	for _, x := range warns {
		slog.Warn("assistente IA", "aviso", x)
	}
	return warns
}

func (s *Server) aiCurrentWarnings() []string {
	if p := s.aiWarnings.Load(); p != nil {
		return *p
	}
	return []string{}
}

// ---------- presets ----------

type aiPreset struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	Kind            string   `json:"kind"`
	Group           string   `json:"group"`
	Description     string   `json:"description"`
	BaseURL         string   `json:"base_url"`
	BaseHint        string   `json:"base_hint"`
	ChatPath        string   `json:"chat_path,omitempty"`
	Query           string   `json:"query,omitempty"`
	TokenParam      string   `json:"token_param,omitempty"`
	SendTemperature bool     `json:"send_temperature"`
	IncludeUsage    bool     `json:"include_usage"`
	Mode            string   `json:"mode,omitempty"`
	APIVersion      string   `json:"api_version,omitempty"`
	Auth            []string `json:"auth"`
	AuthHeader      string   `json:"auth_header,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	ModelLabel      string   `json:"model_label"`
	ModelHint       string   `json:"model_hint"`
	Docs            string   `json:"docs,omitempty"`
}

var aiPresets = []aiPreset{
	{ID: "ollama", Label: "Ollama (outro servidor)", Kind: "ollama", Group: "Local / rede interna",
		Description: "Outro servidor Ollama (ex.: máquina com GPU na rede). Os dados não saem do seu ambiente.",
		BaseURL:     "http://10.0.0.50:11434", BaseHint: "http://<host>:11434", Auth: []string{"none", "bearer"}, ModelLabel: "Modelo", ModelHint: "qwen3.8"},
	{ID: "openai_compatible", Label: "Servidor compatível com OpenAI (vLLM, LM Studio, LocalAI, llama.cpp)", Kind: "openai", Group: "Local / rede interna",
		Description: "Qualquer servidor que exponha /v1/chat/completions.", BaseURL: "http://10.0.0.50:8000/v1", BaseHint: "http://<host>:<porta>/v1",
		TokenParam: "max_tokens", SendTemperature: true, Auth: []string{"none", "bearer", "header"}, ModelLabel: "Modelo", ModelHint: "nome do modelo servido"},
	{ID: "foundry_agent", Label: "Microsoft Foundry — agente", Kind: "foundry_agent", Group: "Microsoft Azure", Mode: "agent", APIVersion: "v1",
		Description: "Agentes do Foundry Agent Service (nome e versão). A persona, as ferramentas e o conhecimento vêm do agente; o doomctl acrescenta os guardrails (opcional).",
		BaseURL:     "https://<recurso>.services.ai.azure.com/api/projects/<projeto>", BaseHint: "endpoint do projeto (Visão geral do projeto no portal do Foundry)",
		Auth: []string{"azure_entra", "bearer"}, Scope: "https://ai.azure.com/.default", ModelLabel: "Agente", ModelHint: "nome-do-agente ou nome-do-agente:versão",
		Docs: "https://learn.microsoft.com/azure/foundry/agents/concepts/runtime-components"},
	{ID: "foundry_app", Label: "Microsoft Foundry — aplicação publicada (agente)", Kind: "foundry_agent", Group: "Microsoft Azure", Mode: "application", APIVersion: "2025-11-15-preview",
		Description: "Agente publicado como aplicação (endpoint Responses sem estado: o doomctl envia o histórico).",
		BaseURL:     "https://<recurso>.services.ai.azure.com/api/projects/<projeto>/applications/<app>/protocols/openai", BaseHint: "URL da aplicação até /protocols/openai",
		Auth: []string{"azure_entra", "bearer"}, Scope: "https://ai.azure.com/.default", ModelLabel: "Agente", ModelHint: "(não usado)",
		Docs: "https://learn.microsoft.com/azure/foundry/agents/how-to/publish-responses"},
	{ID: "foundry_classic", Label: "Microsoft Foundry — agente clássico (asst_…)", Kind: "foundry_agent", Group: "Microsoft Azure", Mode: "classic", APIVersion: "2025-05-01",
		Description: "Agentes clássicos (threads/runs). A Microsoft recomenda migrar para os agentes novos.",
		BaseURL:     "https://<recurso>.services.ai.azure.com/api/projects/<projeto>", BaseHint: "endpoint do projeto",
		Auth: []string{"azure_entra", "bearer"}, Scope: "https://ai.azure.com/.default", ModelLabel: "ID do agente", ModelHint: "asst_..."},
	{ID: "foundry_models", Label: "Microsoft Foundry — modelos (deployments)", Kind: "openai", Group: "Microsoft Azure",
		Description: "Modelos implantados no recurso do Foundry (GPT, DeepSeek, Llama, Mistral...) pela API OpenAI v1.",
		BaseURL:     "https://<recurso>.services.ai.azure.com/openai/v1", BaseHint: "https://<recurso>.services.ai.azure.com/openai/v1",
		TokenParam: "max_completion_tokens", SendTemperature: true, IncludeUsage: true, Auth: []string{"api-key", "azure_entra"},
		Scope: "https://cognitiveservices.azure.com/.default", ModelLabel: "Deployment", ModelHint: "nome do deployment"},
	{ID: "azure_openai", Label: "Azure OpenAI", Kind: "openai", Group: "Microsoft Azure",
		Description: "Recurso Azure OpenAI pela API v1 (para a API antiga use /openai/deployments/<deployment> e api-version na query).",
		BaseURL:     "https://<recurso>.openai.azure.com/openai/v1", BaseHint: "https://<recurso>.openai.azure.com/openai/v1",
		TokenParam: "max_completion_tokens", SendTemperature: true, IncludeUsage: true, Auth: []string{"api-key", "azure_entra"},
		Scope: "https://cognitiveservices.azure.com/.default", ModelLabel: "Deployment", ModelHint: "nome do deployment"},
	{ID: "openai", Label: "OpenAI", Kind: "openai", Group: "Nuvem", BaseURL: "https://api.openai.com/v1", BaseHint: "https://api.openai.com/v1",
		Description: "API da OpenAI. Modelos de raciocínio não aceitam temperatura: desmarque \"Enviar temperatura\" se a API recusar.",
		TokenParam:  "max_completion_tokens", SendTemperature: true, IncludeUsage: true, Auth: []string{"bearer"}, ModelLabel: "Modelo", ModelHint: "gpt-4.1"},
	{ID: "gemini", Label: "Google Gemini (API compatível com OpenAI)", Kind: "openai", Group: "Nuvem",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", BaseHint: "https://generativelanguage.googleapis.com/v1beta/openai",
		Description: "Gemini via endpoint compatível com OpenAI (chave do Google AI Studio).",
		TokenParam:  "max_tokens", SendTemperature: true, IncludeUsage: true, Auth: []string{"bearer"}, ModelLabel: "Modelo", ModelHint: "gemini-2.5-flash"},
	{ID: "openrouter", Label: "OpenRouter", Kind: "openai", Group: "Nuvem", BaseURL: "https://openrouter.ai/api/v1", BaseHint: "https://openrouter.ai/api/v1",
		TokenParam: "max_tokens", SendTemperature: true, Auth: []string{"bearer"}, ModelLabel: "Modelo", ModelHint: "provedor/modelo"},
	{ID: "groq", Label: "Groq", Kind: "openai", Group: "Nuvem", BaseURL: "https://api.groq.com/openai/v1", BaseHint: "https://api.groq.com/openai/v1",
		TokenParam: "max_tokens", SendTemperature: true, Auth: []string{"bearer"}, ModelLabel: "Modelo", ModelHint: "nome do modelo"},
	{ID: "mistral", Label: "Mistral AI", Kind: "openai", Group: "Nuvem", BaseURL: "https://api.mistral.ai/v1", BaseHint: "https://api.mistral.ai/v1",
		TokenParam: "max_tokens", SendTemperature: true, Auth: []string{"bearer"}, ModelLabel: "Modelo", ModelHint: "mistral-large-latest"},
	{ID: "custom", Label: "Outra API compatível com OpenAI", Kind: "openai", Group: "Nuvem", BaseHint: "https://<api>/v1",
		TokenParam: "max_tokens", SendTemperature: true, Auth: []string{"none", "bearer", "api-key", "header", "azure_entra", "oauth2", "google_sa"},
		ModelLabel: "Modelo", ModelHint: "nome do modelo"},
}

type mcpPreset struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Auth        []string `json:"auth"`
	AuthHeader  string   `json:"auth_header,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Docs        string   `json:"docs,omitempty"`
}

var mcpPresets = []mcpPreset{
	{ID: "custom", Label: "Servidor MCP (Streamable HTTP)", Description: "Qualquer servidor MCP remoto (transporte Streamable HTTP). Servidores somente stdio precisam de um proxy HTTP.",
		Auth: []string{"none", "bearer", "header", "api-key", "azure_entra", "oauth2", "google_sa"}},
	{ID: "google_stitch", Label: "Google Stitch", URL: "https://stitch.googleapis.com/mcp", AuthHeader: "X-Goog-Api-Key",
		Description: "Geração de telas e projetos de UI. Chave de API em Stitch → Settings → API Keys. As ferramentas criam conteúdo: libere-as na lista após conectar.",
		Auth:        []string{"header", "google_sa", "bearer"}, Scope: "https://www.googleapis.com/auth/cloud-platform"},
	{ID: "microsoft_learn", Label: "Microsoft Learn (documentação)", URL: "https://learn.microsoft.com/api/mcp",
		Description: "Busca e leitura da documentação oficial da Microsoft/Azure. Público, sem autenticação.", Auth: []string{"none"}},
	{ID: "aws_knowledge", Label: "AWS Knowledge (documentação)", URL: "https://knowledge-mcp.global.api.aws",
		Description: "Documentação e referências oficiais da AWS. Público, sem autenticação.", Auth: []string{"none"}},
	{ID: "github", Label: "GitHub", URL: "https://api.githubcopilot.com/mcp/",
		Description: "Repositórios, issues e PRs. Use um token (PAT) com o mínimo de permissões; prefira escopos somente leitura.", Auth: []string{"bearer"}},
}

// ---------- endpoints: configuração ----------

func (s *Server) aiConfigPayload(ctx context.Context) (map[string]any, error) {
	st, meta, err := s.aiLoadSettings(ctx)
	if err != nil {
		meta.Saved = false
	}
	conns, err := s.aiLoadConns(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.aiLoadMCP(ctx)
	if err != nil {
		return nil, err
	}
	markUse(st, conns, servers)
	rt := s.ai.Runtime()
	agentsInfo := map[string]any{}
	for _, r := range []agents.Route{agents.RouteCloud, agents.RouteNetSec} {
		agentsInfo[string(r)] = rt.Agent(r)
	}
	return map[string]any{
		"settings": st, "defaults": s.aiDefaultSettings(), "meta": meta,
		"env": map[string]any{"ollama_host": s.cfg.OllamaHost, "ollama_model": s.cfg.OllamaModel, "router_temperature": s.cfg.RouterTemperature,
			"think": s.cfg.OllamaThink, "gpu_install": s.cfg.AIGPU},
		"runtime":     map[string]any{"agents": agentsInfo, "router": rt.RouterInfo(), "warnings": s.aiCurrentWarnings()},
		"connections": conns, "mcp": servers, "auth_types": agents.AuthTypes, "presets": aiPresets, "mcp_presets": mcpPresets,
	}, nil
}

func markUse(st agents.Settings, conns []aiConn, servers []aiMCP) {
	for i := range conns {
		if st.Router.ConnectionID == conns[i].ID {
			conns[i].InUse = append(conns[i].InUse, "Classificador")
		}
		for _, r := range []agents.Route{agents.RouteCloud, agents.RouteNetSec} {
			if a := st.Agents[r]; a != nil && a.ConnectionID == conns[i].ID {
				conns[i].InUse = append(conns[i].InUse, agents.PersonaName(r))
			}
		}
	}
	for i := range servers {
		for _, r := range []agents.Route{agents.RouteCloud, agents.RouteNetSec} {
			if a := st.Agents[r]; a != nil {
				for _, id := range a.MCPServers {
					if id == servers[i].ID {
						servers[i].InUse = append(servers[i].InUse, agents.PersonaName(r))
					}
				}
			}
		}
	}
}

func (s *Server) aiGetConfig(w http.ResponseWriter, r *http.Request, _ *User) {
	p, err := s.aiConfigPayload(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

// aiValidate normaliza e confere as referências a conexões e servidores MCP.
func (s *Server) aiValidate(ctx context.Context, st *agents.Settings) ([]string, error) {
	if err := st.Normalize(); err != nil {
		return nil, err
	}
	conns, err := s.aiLoadConns(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.aiLoadMCP(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]aiConn{}
	for _, c := range conns {
		byID[c.ID] = c
	}
	mcpByID := map[int64]aiMCP{}
	for _, m := range servers {
		mcpByID[m.ID] = m
	}
	var warns []string
	check := func(id int64, who string) (aiConn, error) {
		if id == 0 {
			return aiConn{Kind: "ollama"}, nil
		}
		c, ok := byID[id]
		if !ok {
			return c, fmt.Errorf("%s: conexão #%d não existe", who, id)
		}
		if !c.Enabled {
			warns = append(warns, fmt.Sprintf("%s: a conexão %q está desativada — será usado o Ollama padrão até você ativá-la", who, c.Name))
		}
		return c, nil
	}
	rc, err := check(st.Router.ConnectionID, "Classificador")
	if err != nil {
		return nil, err
	}
	if rc.Kind == "foundry_agent" {
		return nil, errors.New("o classificador precisa de um modelo (Ollama ou API), não de um agente do Foundry")
	}
	for _, rt := range []agents.Route{agents.RouteCloud, agents.RouteNetSec} {
		a := st.Agents[rt]
		name := agents.PersonaName(rt)
		c, err := check(a.ConnectionID, name)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, id := range a.MCPServers {
			if _, ok := mcpByID[id]; !ok {
				return nil, fmt.Errorf("%s: servidor MCP #%d não existe", name, id)
			}
			ids = append(ids, id)
		}
		if ids == nil {
			ids = []int64{}
		}
		a.MCPServers = ids
		if c.Kind == "foundry_agent" && len(ids) > 0 {
			warns = append(warns, name+": agentes do Foundry usam as próprias ferramentas — os servidores MCP do doomctl não são repassados")
		}
		if c.Kind == "foundry_agent" && a.PromptMode != "default" && !a.SendGuardrails {
			warns = append(warns, name+": o prompt personalizado só chega ao agente do Foundry com \"Enviar guardrails\" ativado")
		}
	}
	return warns, nil
}

func (s *Server) aiSaveSettings(ctx context.Context, st agents.Settings, u *User, note string) error {
	raw, _ := json.Marshal(st)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_settings(key, value, updated_by) VALUES ('settings', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=now()`, string(raw), u.Username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_settings_history(value, note, created_by) VALUES ($1, $2, $3)`, string(raw), truncate(note, 200), u.Username); err != nil {
		return err
	}
	// mantém as 50 versões mais recentes
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_settings_history WHERE id NOT IN (SELECT id FROM ai_settings_history ORDER BY id DESC LIMIT 50)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) aiPutConfig(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Settings agents.Settings `json:"settings"`
		Note     string          `json:"note"`
	}
	in.Settings = s.aiDefaultSettings()
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	warns, err := s.aiValidate(r.Context(), &in.Settings)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := s.aiSaveSettings(r.Context(), in.Settings, u, firstNonBlank(in.Note, "configuração atualizada")); err != nil {
		failErr(w, err)
		return
	}
	rtWarns := s.reloadAI(r.Context())
	s.audit("ai", "config", "agentes", u, "info", "configuração dos agentes atualizada: "+truncate(in.Note, 200), in.Settings)
	p, err := s.aiConfigPayload(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	p["warnings"] = append(warns, rtWarns...)
	writeJSON(w, 200, p)
}

func firstNonBlank(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (s *Server) aiResetConfig(w http.ResponseWriter, r *http.Request, u *User) {
	if err := s.aiSaveSettings(r.Context(), s.aiDefaultSettings(), u, "restaurado o padrão"); err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(r.Context())
	s.audit("ai", "config_reset", "agentes", u, "info", "configuração dos agentes restaurada para o padrão", nil)
	s.aiGetConfig(w, r, u)
}

func (s *Server) aiConfigHistory(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, note, created_by, created_at FROM ai_settings_history ORDER BY id DESC LIMIT 50`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID        int64     `json:"id"`
		Note      string    `json:"note"`
		CreatedBy string    `json:"created_by"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Note, &it.CreatedBy, &it.CreatedAt); err != nil {
			failErr(w, err)
			return
		}
		out = append(out, it)
	}
	writeJSON(w, 200, out)
}

func (s *Server) aiRestoreConfig(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var raw []byte
	if err := s.db.QueryRowContext(r.Context(), `SELECT value FROM ai_settings_history WHERE id=$1`, id).Scan(&raw); err != nil {
		failErr(w, err)
		return
	}
	st := s.aiDefaultSettings()
	json.Unmarshal(raw, &st)
	if _, err := s.aiValidate(r.Context(), &st); err != nil {
		fail(w, 400, "a versão não pode ser restaurada: "+err.Error())
		return
	}
	if err := s.aiSaveSettings(r.Context(), st, u, fmt.Sprintf("restaurada a versão #%d", id)); err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(r.Context())
	s.audit("ai", "config_restore", "agentes", u, "info", fmt.Sprintf("configuração restaurada da versão #%d", id), nil)
	s.aiGetConfig(w, r, u)
}

func (s *Server) aiExportConfig(w http.ResponseWriter, r *http.Request, _ *User) {
	st, _, _ := s.aiLoadSettings(r.Context())
	conns, _ := s.aiLoadConns(r.Context())
	servers, _ := s.aiLoadMCP(r.Context())
	type ce struct {
		ID     int64        `json:"id"`
		Name   string       `json:"name"`
		Kind   string       `json:"kind"`
		Preset string       `json:"preset"`
		Config aiConnConfig `json:"config"`
	}
	type me struct {
		ID     int64       `json:"id"`
		Name   string      `json:"name"`
		URL    string      `json:"url"`
		Preset string      `json:"preset"`
		Config aiMCPConfig `json:"config"`
	}
	out := map[string]any{"doomctl_ai_export": 1, "exported_at": time.Now().UTC(), "settings": st, "connections": []ce{}, "mcp": []me{}}
	var cs []ce
	for _, c := range conns {
		cs = append(cs, ce{c.ID, c.Name, c.Kind, c.Preset, c.Config})
	}
	var ms []me
	for _, m := range servers {
		ms = append(ms, me{m.ID, m.Name, m.URL, m.Preset, m.Config})
	}
	if cs != nil {
		out["connections"] = cs
	}
	if ms != nil {
		out["mcp"] = ms
	}
	w.Header().Set("Content-Disposition", `attachment; filename="doomctl-ai-config.json"`)
	writeJSON(w, 200, out)
}

// aiImportConfig importa somente as configurações; referências a conexões/servidores
// inexistentes voltam para o Ollama padrão (os segredos nunca são exportados).
func (s *Server) aiImportConfig(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Settings *agents.Settings `json:"settings"`
	}
	if err := decode(r, &in); err != nil || in.Settings == nil {
		fail(w, 400, "arquivo inválido: esperado um JSON exportado pelo doomctl")
		return
	}
	st := s.aiDefaultSettings()
	b, _ := json.Marshal(in.Settings)
	json.Unmarshal(b, &st)
	conns, _ := s.aiLoadConns(r.Context())
	servers, _ := s.aiLoadMCP(r.Context())
	have := map[int64]bool{}
	for _, c := range conns {
		have[c.ID] = true
	}
	haveMCP := map[int64]bool{}
	for _, m := range servers {
		haveMCP[m.ID] = true
	}
	var notes []string
	if st.Router.ConnectionID != 0 && !have[st.Router.ConnectionID] {
		st.Router.ConnectionID = 0
		notes = append(notes, "classificador → Ollama padrão")
	}
	for rt, a := range st.Agents {
		if a == nil {
			continue
		}
		if a.ConnectionID != 0 && !have[a.ConnectionID] {
			a.ConnectionID = 0
			notes = append(notes, agents.PersonaName(rt)+" → Ollama padrão")
		}
		var ids []int64
		for _, id := range a.MCPServers {
			if haveMCP[id] {
				ids = append(ids, id)
			}
		}
		a.MCPServers = ids
	}
	warns, err := s.aiValidate(r.Context(), &st)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := s.aiSaveSettings(r.Context(), st, u, "importado de arquivo"); err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(r.Context())
	s.audit("ai", "config_import", "agentes", u, "info", "configuração dos agentes importada", nil)
	p, err := s.aiConfigPayload(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	if len(notes) > 0 {
		warns = append(warns, "referências ajustadas: "+strings.Join(notes, "; "))
	}
	p["warnings"] = warns
	writeJSON(w, 200, p)
}

// aiPreview devolve o system prompt efetivo de cada persona para as configurações enviadas.
func (s *Server) aiPreview(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Settings agents.Settings `json:"settings"`
	}
	in.Settings = s.aiDefaultSettings()
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := in.Settings.Normalize(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	out := map[string]any{}
	for _, rt := range []agents.Route{agents.RouteCloud, agents.RouteNetSec} {
		p := agents.ComposeSystemPrompt(in.Settings, rt)
		out[string(rt)] = map[string]any{"prompt": p, "chars": len([]rune(p)), "tokens_est": len([]rune(p)) / 4,
			"is_default": p == agents.SystemPromptFor(rt), "guardrails_only": agents.GuardrailsOnly(in.Settings)}
	}
	writeJSON(w, 200, out)
}

// ---------- playground (testa configurações sem salvar) ----------

func (s *Server) aiPlayground(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Settings agents.Settings `json:"settings"`
		Route    string          `json:"route"`
		Message  string          `json:"message"`
	}
	in.Settings = s.aiDefaultSettings()
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" || len(msg) > 32<<10 {
		fail(w, 400, "mensagem vazia ou grande demais")
		return
	}
	if _, err := s.aiValidate(r.Context(), &in.Settings); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !s.rl.allow(fmt.Sprintf("aipg:%d", u.ID), 30, 5*time.Minute) {
		fail(w, http.StatusTooManyRequests, "muitos testes seguidos; aguarde um pouco")
		return
	}
	rin, _ := s.aiRuntimeInput(r.Context(), in.Settings)
	rt := agents.BuildRuntime(rin)
	tmp := agents.NewAssistant(s.ai.LLM())
	tmp.SetRuntime(rt)

	nw := newNDJSON(w)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.Settings.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	start := time.Now()
	var d agents.Decision
	switch in.Route {
	case string(agents.RouteCloud), string(agents.RouteNetSec):
		d = agents.Decision{Route: agents.Route(in.Route), Reason: "persona escolhida no teste"}
	default:
		d = rt.Decide(ctx, msg)
	}
	nw.send(map[string]any{"type": "meta", "route": d.Route, "persona": personaFor(d.Route), "used_llm": d.UsedLLM, "reason": d.Reason,
		"scores": map[string]int{"cloud": d.Scores.Cloud, "netsec": d.Scores.NetSec}})
	for _, e := range rt.Errors {
		nw.send(map[string]any{"type": "notice", "text": e})
	}
	if d.Route == agents.RouteOffTopic {
		nw.send(map[string]any{"type": "refusal", "text": rt.RefusalMessage()})
		nw.send(map[string]any{"type": "stats", "duration_ms": time.Since(start).Milliseconds()})
		nw.send(map[string]any{"type": "done"})
		return
	}
	res, err := tmp.AskRoutedEx(ctx, "playground", d, msg, nw, &agents.Hooks{OnTool: func(e agents.ToolEvent) {
		nw.send(map[string]any{"type": "tool", "tool": e})
	}})
	if err != nil {
		nw.send(map[string]any{"type": "error", "error": "falha ao consultar o modelo: " + err.Error()})
	}
	nw.send(map[string]any{"type": "stats", "duration_ms": time.Since(start).Milliseconds(), "usage": res.Usage, "connection": res.Connection,
		"model": res.Model, "kind": res.Kind, "redacted": res.Redacted, "system_prompt": rt.SystemPrompt(d.Route)})
	nw.send(map[string]any{"type": "done"})
	s.audit("ai", "playground", personaFor(d.Route), u, "info", "teste de configuração: "+truncate(msg, 200),
		map[string]any{"connection": res.Connection, "model": res.Model})
}

func newNDJSON(w http.ResponseWriter) *ndjsonWriter {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	return &ndjsonWriter{w: w, f: fl}
}

// ---------- conexões ----------

type aiConnInput struct {
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Preset  string       `json:"preset"`
	Config  aiConnConfig `json:"config"`
	Secret  *string      `json:"secret"` // nil = mantém; "" = remove
	Enabled *bool        `json:"enabled"`
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// secretTargetChanged: mudou algo que define para onde (ou como) o segredo é enviado?
func secretTargetChanged(oldURL string, oldA agents.AuthConfig, newURL string, newA agents.AuthConfig) bool {
	host := func(raw string) string {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return raw
		}
		return strings.ToLower(u.Scheme + "://" + u.Host)
	}
	return host(oldURL) != host(newURL) || oldA.Type != newA.Type || oldA.TokenURL != newA.TokenURL ||
		!strings.EqualFold(oldA.HeaderName, newA.HeaderName) || oldA.TenantID != newA.TenantID
}

func validateAuth(a *agents.AuthConfig) error {
	if a.Type == "" {
		a.Type = "none"
	}
	if _, ok := agents.AuthTypes[a.Type]; !ok {
		return errors.New("tipo de autenticação inválido")
	}
	a.HeaderName = strings.TrimSpace(a.HeaderName)
	if a.Type == "header" && !headerNameRe.MatchString(a.HeaderName) {
		return errors.New("nome do cabeçalho de autenticação inválido")
	}
	if a.Type == "oauth2" {
		if u, err := url.Parse(a.TokenURL); err != nil || u.Scheme != "https" {
			return errors.New("a URL de token OAuth precisa ser https://")
		}
	}
	for _, p := range []*string{&a.TenantID, &a.ClientID, &a.Scope, &a.TokenURL, &a.Project} {
		*p = strings.TrimSpace(*p)
		if len(*p) > 300 {
			return errors.New("campo de autenticação muito longo")
		}
	}
	return nil
}

func validHeaders(h map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range h {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !headerNameRe.MatchString(k) || strings.ContainsAny(v, "\r\n") || len(v) > 2000 {
			return nil, fmt.Errorf("cabeçalho inválido: %q", k)
		}
		lk := strings.ToLower(k)
		switch lk {
		case "host", "content-length", "content-type", "transfer-encoding", "connection":
			return nil, fmt.Errorf("o cabeçalho %q é controlado pelo doomctl", k)
		}
		for _, w := range []string{"auth", "key", "token", "secret", "cookie", "password", "session"} {
			if strings.Contains(lk, w) {
				return nil, fmt.Errorf("o cabeçalho %q parece conter um segredo: use o campo de autenticação (guardado criptografado)", k)
			}
		}
		out[k] = v
	}
	if len(out) > 20 {
		return nil, errors.New("no máximo 20 cabeçalhos")
	}
	return out, nil
}

func validBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", errors.New("URL inválida (use http:// ou https://, sem usuário/senha na URL)")
	}
	if strings.Contains(raw, "<") || strings.Contains(raw, ">") {
		return "", errors.New("substitua os campos <...> da URL de exemplo")
	}
	return raw, nil
}

func (in *aiConnInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if !aiNameRe.MatchString(in.Name) {
		return errors.New("nome inválido (até 60 caracteres)")
	}
	switch in.Kind {
	case "ollama", "openai", "foundry_agent":
	default:
		return errors.New("tipo de conexão inválido")
	}
	c := &in.Config
	var err error
	if c.BaseURL, err = validBaseURL(c.BaseURL); err != nil {
		return err
	}
	if c.Headers, err = validHeaders(c.Headers); err != nil {
		return err
	}
	if err := validateAuth(&c.Auth); err != nil {
		return err
	}
	for _, p := range []*string{&c.ChatPath, &c.ModelsPath} {
		*p = strings.TrimSpace(*p)
		if *p != "" && (!strings.HasPrefix(*p, "/") || strings.Contains(*p, "..") || len(*p) > 200) {
			return errors.New("caminho inválido (deve começar com /)")
		}
	}
	c.Query = strings.TrimPrefix(strings.TrimSpace(c.Query), "?")
	if _, err := url.ParseQuery(c.Query); err != nil || len(c.Query) > 300 {
		return errors.New("query string inválida")
	}
	switch c.TokenParam {
	case "", "max_tokens", "max_completion_tokens":
	default:
		return errors.New("parâmetro de limite de tokens inválido")
	}
	c.Model = strings.TrimSpace(c.Model)
	if len(c.Model) > 200 {
		return errors.New("modelo muito longo")
	}
	if in.Kind == "foundry_agent" {
		switch c.Mode {
		case "", "agent", "application", "classic":
		default:
			return errors.New("modo do agente inválido")
		}
		if c.Mode != "application" && c.Model == "" {
			return errors.New("informe o agente (nome ou asst_…)")
		}
	} else {
		c.Mode = ""
	}
	c.APIVersion = strings.TrimSpace(c.APIVersion)
	if c.APIVersion != "" && !regexp.MustCompile(`^(v1|\d{4}-\d{2}-\d{2}(-preview)?)$`).MatchString(c.APIVersion) {
		return errors.New("api-version inválida (ex.: v1, 2025-05-01, 2025-11-15-preview)")
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 3600 {
		return errors.New("tempo limite entre 0 e 3600 s")
	}
	return nil
}

func (s *Server) aiListConns(w http.ResponseWriter, r *http.Request, _ *User) {
	conns, err := s.aiLoadConns(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	st, _, _ := s.aiLoadSettings(r.Context())
	markUse(st, conns, nil)
	writeJSON(w, 200, conns)
}

func (s *Server) aiSaveConn(w http.ResponseWriter, r *http.Request, u *User) {
	var id int64
	if r.PathValue("id") != "" {
		var err error
		if id, err = pathID(r); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	var in aiConnInput
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	cfg, _ := json.Marshal(in.Config)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx := r.Context()
	var secret []byte
	setSecret := in.Secret != nil
	if setSecret && strings.TrimSpace(*in.Secret) != "" {
		var err error
		if secret, err = s.box.Seal([]byte(strings.TrimSpace(*in.Secret))); err != nil {
			failErr(w, err)
			return
		}
	}
	var err error
	if id != 0 && !setSecret {
		// trocar destino ou forma de autenticação exige informar o segredo de novo
		// (impede redirecionar uma chave guardada para outro servidor)
		var oldKind string
		var oldCfg []byte
		var hasOld bool
		if err := s.db.QueryRowContext(ctx, `SELECT kind, config, secret_enc IS NOT NULL FROM ai_connections WHERE id=$1`, id).Scan(&oldKind, &oldCfg, &hasOld); err != nil {
			failErr(w, err)
			return
		}
		var oc aiConnConfig
		json.Unmarshal(oldCfg, &oc)
		if hasOld && secretTargetChanged(oc.BaseURL, oc.Auth, in.Config.BaseURL, in.Config.Auth) {
			if in.Config.Auth.Type != "none" {
				fail(w, 400, "ao mudar a URL ou a autenticação, informe o segredo novamente")
				return
			}
			setSecret = true // autenticação removida: apaga o segredo guardado
		}
	}
	if id == 0 {
		err = s.db.QueryRowContext(ctx, `INSERT INTO ai_connections(name, kind, preset, config, secret_enc, enabled, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.Name, in.Kind, in.Preset, string(cfg), secret, enabled, u.Username).Scan(&id)
	} else {
		var res sql.Result
		if setSecret {
			res, err = s.db.ExecContext(ctx, `UPDATE ai_connections SET name=$2, kind=$3, preset=$4, config=$5, secret_enc=$6, enabled=$7, updated_at=now() WHERE id=$1`,
				id, in.Name, in.Kind, in.Preset, string(cfg), secret, enabled)
		} else {
			res, err = s.db.ExecContext(ctx, `UPDATE ai_connections SET name=$2, kind=$3, preset=$4, config=$5, enabled=$6, updated_at=now() WHERE id=$1`,
				id, in.Name, in.Kind, in.Preset, string(cfg), enabled)
		}
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				err = sql.ErrNoRows
			}
		}
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(ctx)
	s.audit("ai", "connection_save", in.Name, u, "info", "conexão de IA salva ("+in.Kind+")",
		map[string]any{"kind": in.Kind, "preset": in.Preset, "base_url": in.Config.BaseURL, "model": in.Config.Model, "auth": in.Config.Auth.Type, "secret_changed": setSecret})
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) aiDeleteConn(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	st, _, _ := s.aiLoadSettings(r.Context())
	if st.Router.ConnectionID == id {
		fail(w, 409, "a conexão está em uso pelo classificador; troque-a em Agentes antes de excluir")
		return
	}
	for rt, a := range st.Agents {
		if a.ConnectionID == id {
			fail(w, 409, "a conexão está em uso por "+agents.PersonaName(rt)+"; troque-a em Agentes antes de excluir")
			return
		}
	}
	var name string
	if err := s.db.QueryRowContext(r.Context(), `DELETE FROM ai_connections WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(r.Context())
	s.audit("ai", "connection_delete", name, u, "info", "conexão de IA excluída", nil)
	ok(w)
}

func (s *Server) aiConnByID(ctx context.Context, id int64) (agents.ConnectionDef, *aiConn, error) {
	if id == 0 {
		return agents.ConnectionDef{ID: 0, Name: "Ollama padrão", Kind: "ollama", BaseURL: s.cfg.OllamaHost, Model: s.cfg.OllamaModel}, nil, nil
	}
	conns, err := s.aiLoadConns(ctx)
	if err != nil {
		return agents.ConnectionDef{}, nil, err
	}
	for _, c := range conns {
		if c.ID == id {
			sec, err := s.openSecret(c.secret)
			if err != nil {
				return agents.ConnectionDef{}, nil, err
			}
			cc := c
			return c.def(sec), &cc, nil
		}
	}
	return agents.ConnectionDef{}, nil, sql.ErrNoRows
}

func idParam(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 0 {
		return 0, errors.New("id inválido")
	}
	return id, nil
}

func (s *Server) aiTestConn(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := idParam(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	def, row, err := s.aiConnByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	res := map[string]any{"ok": false}
	var testErr error
	var note string
	p, err := agents.NewProvider(def)
	if err != nil {
		testErr = err
	} else {
		if pg, ok := p.(agents.ProviderPinger); ok {
			testErr = pg.Ping(ctx, def.Model)
		}
		// agentes novos do Foundry: tenta a api-version preview se a v1 não existir no recurso
		if testErr != nil && def.Kind == "foundry_agent" && def.Mode != "classic" && def.Mode != "application" && (def.APIVersion == "" || def.APIVersion == "v1") &&
			(strings.Contains(testErr.Error(), "HTTP 404") || strings.Contains(testErr.Error(), "HTTP 400")) {
			alt := def
			alt.APIVersion = "2025-11-15-preview"
			if p2, err2 := agents.NewProvider(alt); err2 == nil {
				if err2 = p2.(agents.ProviderPinger).Ping(ctx, alt.Model); err2 == nil && row != nil {
					row.Config.APIVersion = alt.APIVersion
					cfg, _ := json.Marshal(row.Config)
					s.db.ExecContext(r.Context(), `UPDATE ai_connections SET config=$2, updated_at=now() WHERE id=$1`, id, string(cfg))
					testErr, p = nil, p2
					note = "a API v1 não respondeu neste recurso; a conexão foi ajustada para api-version 2025-11-15-preview"
				}
			}
		}
		if testErr == nil {
			mctx, mcancel := context.WithTimeout(ctx, 20*time.Second)
			if ms, err := p.Models(mctx); err == nil {
				if len(ms) > 300 {
					ms = ms[:300]
				}
				res["models"] = ms
			}
			mcancel()
		}
	}
	res["latency_ms"] = time.Since(start).Milliseconds()
	status, msg := "ok", ""
	if testErr != nil {
		status, msg = "error", testErr.Error()
		res["error"] = msg
	} else {
		res["ok"] = true
	}
	if note != "" {
		res["note"] = note
	}
	if id != 0 {
		s.db.ExecContext(r.Context(), `UPDATE ai_connections SET last_test_at=now(), last_status=$2, last_error=$3 WHERE id=$1`, id, status, truncate(msg, 500))
		s.reloadAI(r.Context())
	}
	s.audit("ai", "connection_test", def.Name, u, "info", "teste de conexão: "+status+" "+truncate(msg, 300), nil)
	writeJSON(w, 200, res)
}

func (s *Server) aiConnModels(w http.ResponseWriter, r *http.Request, _ *User) {
	id, err := idParam(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	def, _, err := s.aiConnByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	p, err := agents.NewProvider(def)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if f, ok := p.(*agents.FoundryProvider); ok {
		ags, err := f.Agents(r.Context())
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"agents": ags})
		return
	}
	ms, err := p.Models(r.Context())
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": ms})
}

// ---------- servidores MCP ----------

type aiMCPInput struct {
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Preset  string      `json:"preset"`
	Config  aiMCPConfig `json:"config"`
	Secret  *string     `json:"secret"`
	Enabled *bool       `json:"enabled"`
}

func (s *Server) aiListMCP(w http.ResponseWriter, r *http.Request, _ *User) {
	servers, err := s.aiLoadMCP(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	st, _, _ := s.aiLoadSettings(r.Context())
	markUse(st, nil, servers)
	writeJSON(w, 200, servers)
}

func (s *Server) aiSaveMCP(w http.ResponseWriter, r *http.Request, u *User) {
	var id int64
	if r.PathValue("id") != "" {
		var err error
		if id, err = pathID(r); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	var in aiMCPInput
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !aiNameRe.MatchString(in.Name) {
		fail(w, 400, "nome inválido (até 60 caracteres)")
		return
	}
	var err error
	trailing := strings.HasSuffix(strings.TrimSpace(in.URL), "/")
	if in.URL, err = validBaseURL(in.URL); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if trailing {
		in.URL += "/" // alguns servidores (ex.: GitHub) exigem a barra final
	}
	if in.Config.Headers, err = validHeaders(in.Config.Headers); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := validateAuth(&in.Config.Auth); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Config.TimeoutSeconds < 0 || in.Config.TimeoutSeconds > 600 {
		fail(w, 400, "tempo limite entre 0 e 600 s")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx := r.Context()
	if id != 0 && in.Config.ToolsEnabled == nil {
		// preserva a política de ferramentas ao editar só a conexão
		var cfg []byte
		if err := s.db.QueryRowContext(ctx, `SELECT config FROM ai_mcp_servers WHERE id=$1`, id).Scan(&cfg); err != nil {
			failErr(w, err)
			return
		}
		var old aiMCPConfig
		json.Unmarshal(cfg, &old)
		in.Config.ToolsEnabled = old.ToolsEnabled
	}
	if in.Config.ToolsEnabled == nil {
		in.Config.ToolsEnabled = map[string]bool{}
	}
	cfg, _ := json.Marshal(in.Config)
	var secret []byte
	setSecret := in.Secret != nil
	if setSecret && strings.TrimSpace(*in.Secret) != "" {
		if secret, err = s.box.Seal([]byte(strings.TrimSpace(*in.Secret))); err != nil {
			failErr(w, err)
			return
		}
	}
	if id != 0 && !setSecret {
		var oldURL string
		var oldCfg []byte
		var hasOld bool
		if err := s.db.QueryRowContext(ctx, `SELECT url, config, secret_enc IS NOT NULL FROM ai_mcp_servers WHERE id=$1`, id).Scan(&oldURL, &oldCfg, &hasOld); err != nil {
			failErr(w, err)
			return
		}
		var oc aiMCPConfig
		json.Unmarshal(oldCfg, &oc)
		if hasOld && secretTargetChanged(oldURL, oc.Auth, in.URL, in.Config.Auth) {
			if in.Config.Auth.Type != "none" {
				fail(w, 400, "ao mudar a URL ou a autenticação, informe o segredo novamente")
				return
			}
			setSecret = true
		}
	}
	if id == 0 {
		err = s.db.QueryRowContext(ctx, `INSERT INTO ai_mcp_servers(name, url, preset, config, secret_enc, enabled, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.Name, in.URL, in.Preset, string(cfg), secret, enabled, u.Username).Scan(&id)
	} else {
		var res sql.Result
		if setSecret {
			res, err = s.db.ExecContext(ctx, `UPDATE ai_mcp_servers SET name=$2, url=$3, preset=$4, config=$5, secret_enc=$6, enabled=$7, updated_at=now() WHERE id=$1`,
				id, in.Name, in.URL, in.Preset, string(cfg), secret, enabled)
		} else {
			res, err = s.db.ExecContext(ctx, `UPDATE ai_mcp_servers SET name=$2, url=$3, preset=$4, config=$5, enabled=$6, updated_at=now() WHERE id=$1`,
				id, in.Name, in.URL, in.Preset, string(cfg), enabled)
		}
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				err = sql.ErrNoRows
			}
		}
	}
	if err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(ctx)
	s.audit("ai", "mcp_save", in.Name, u, "info", "servidor MCP salvo", map[string]any{"url": in.URL, "auth": in.Config.Auth.Type, "secret_changed": setSecret})
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) aiDeleteMCP(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var name string
	if err := s.db.QueryRowContext(r.Context(), `DELETE FROM ai_mcp_servers WHERE id=$1 RETURNING name`, id).Scan(&name); err != nil {
		failErr(w, err)
		return
	}
	// remove a referência das personas
	st, meta, _ := s.aiLoadSettings(r.Context())
	changed := false
	for _, a := range st.Agents {
		var ids []int64
		for _, x := range a.MCPServers {
			if x != id {
				ids = append(ids, x)
			} else {
				changed = true
			}
		}
		if ids == nil {
			ids = []int64{}
		}
		a.MCPServers = ids
	}
	if changed && meta.Saved {
		s.aiSaveSettings(r.Context(), st, u, "servidor MCP "+name+" removido")
	}
	s.reloadAI(r.Context())
	s.audit("ai", "mcp_delete", name, u, "info", "servidor MCP excluído", nil)
	ok(w)
}

func (s *Server) aiMCPByID(ctx context.Context, id int64) (*aiMCP, agents.MCPServerDef, error) {
	servers, err := s.aiLoadMCP(ctx)
	if err != nil {
		return nil, agents.MCPServerDef{}, err
	}
	for _, m := range servers {
		if m.ID == id {
			sec, err := s.openSecret(m.secret)
			if err != nil {
				return nil, agents.MCPServerDef{}, err
			}
			mm := m
			return &mm, m.def(sec), nil
		}
	}
	return nil, agents.MCPServerDef{}, sql.ErrNoRows
}

// aiRefreshMCP conecta, lista as ferramentas e guarda o cache.
func (s *Server) aiRefreshMCP(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	row, def, err := s.aiMCPByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	c := agents.NewMCPClient(agents.MCPConfig{ID: def.ID, Name: def.Name, URL: def.URL, Auth: def.Auth, Headers: def.Headers,
		Timeout: time.Duration(def.TimeoutSeconds) * time.Second})
	start := time.Now()
	info, err := c.Initialize(ctx)
	var tools []agents.MCPTool
	if err == nil {
		tools, err = c.ListTools(ctx)
	}
	if err != nil {
		s.db.ExecContext(r.Context(), `UPDATE ai_mcp_servers SET last_status='error', last_error=$2, updated_at=now() WHERE id=$1`, id, truncate(err.Error(), 500))
		s.audit("ai", "mcp_refresh", row.Name, u, "info", "falha ao conectar: "+truncate(err.Error(), 300), nil)
		fail(w, 502, "falha ao conectar ao servidor MCP: "+err.Error())
		return
	}
	tj, _ := json.Marshal(tools)
	ij, _ := json.Marshal(info)
	if _, err := s.db.ExecContext(r.Context(), `UPDATE ai_mcp_servers SET tools=$2, server_info=$3, tools_updated_at=now(), last_status='ok', last_error='', updated_at=now() WHERE id=$1`,
		id, string(tj), string(ij)); err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(r.Context())
	s.audit("ai", "mcp_refresh", row.Name, u, "info", fmt.Sprintf("%d ferramenta(s) listada(s)", len(tools)), nil)
	writeJSON(w, 200, map[string]any{"server_info": info, "tools": tools, "latency_ms": time.Since(start).Milliseconds()})
}

func (s *Server) aiMCPTools(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		ToolsEnabled map[string]bool `json:"tools_enabled"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	row, _, err := s.aiMCPByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	known := map[string]agents.MCPTool{}
	for _, t := range row.Tools {
		known[t.Name] = t
	}
	pol := map[string]bool{}
	var writes []string
	for k, v := range in.ToolsEnabled {
		t, ok := known[k]
		if !ok {
			continue
		}
		pol[k] = v
		if v && !t.ReadOnly {
			writes = append(writes, k)
		}
	}
	row.Config.ToolsEnabled = pol
	cfg, _ := json.Marshal(row.Config)
	if _, err := s.db.ExecContext(r.Context(), `UPDATE ai_mcp_servers SET config=$2, updated_at=now() WHERE id=$1`, id, string(cfg)); err != nil {
		failErr(w, err)
		return
	}
	s.reloadAI(r.Context())
	sort.Strings(writes)
	s.audit("ai", "mcp_tools", row.Name, u, "info", "política de ferramentas atualizada", map[string]any{"write_tools_enabled": writes})
	ok(w)
}

// aiMCPCall executa uma ferramenta manualmente (teste). Ferramentas que alteram algo exigem confirmação.
func (s *Server) aiMCPCall(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct {
		Tool    string         `json:"tool"`
		Args    map[string]any `json:"args"`
		Confirm bool           `json:"confirm"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	row, def, err := s.aiMCPByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	var tool *agents.MCPTool
	for i := range row.Tools {
		if row.Tools[i].Name == in.Tool {
			tool = &row.Tools[i]
		}
	}
	if tool == nil {
		fail(w, 404, "ferramenta não encontrada (atualize a lista)")
		return
	}
	if !tool.ReadOnly && !in.Confirm {
		fail(w, 409, "esta ferramenta pode alterar dados no serviço remoto; confirme a execução")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	c := agents.NewMCPClient(agents.MCPConfig{ID: def.ID, Name: def.Name, URL: def.URL, Auth: def.Auth, Headers: def.Headers,
		Timeout: time.Duration(def.TimeoutSeconds) * time.Second})
	start := time.Now()
	out, isErr, err := c.CallTool(ctx, in.Tool, in.Args)
	status := "ok"
	if err != nil {
		out, isErr, status = err.Error(), true, "error"
	} else if isErr {
		status = "error"
	}
	if len(out) > 64<<10 {
		out = out[:64<<10] + "\n…(truncado)"
	}
	s.audit("ai", "mcp_call", row.Name+"/"+in.Tool, u, "info", "execução manual de ferramenta MCP: "+status, map[string]any{"tool": in.Tool, "read_only": tool.ReadOnly})
	writeJSON(w, 200, map[string]any{"output": out, "is_error": isErr, "duration_ms": time.Since(start).Milliseconds()})
}

// ---------- Ollama: modelos, memória e GPU ----------

func (s *Server) ollamaFor(ctx context.Context, id int64) (*agents.OllamaProvider, agents.ConnectionDef, error) {
	def, _, err := s.aiConnByID(ctx, id)
	if err != nil {
		return nil, def, err
	}
	if def.Kind != "ollama" {
		return nil, def, errors.New("a conexão escolhida não é um servidor Ollama")
	}
	p, err := agents.NewProvider(def)
	if err != nil {
		return nil, def, err
	}
	return p.(*agents.OllamaProvider), def, nil
}

type ollamaContainer struct {
	Found  bool   `json:"found"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Image  string `json:"image,omitempty"`
	State  string `json:"state,omitempty"`
	GPU    string `json:"gpu"` // nvidia | amd | none | unknown
	Error  string `json:"error,omitempty"`
	Docker bool   `json:"docker"`
}

type gpuDevice struct {
	Name     string `json:"name"`
	MemTotal int    `json:"mem_total_mb"`
	MemUsed  int    `json:"mem_used_mb"`
	Util     int    `json:"util_pct"`
}

func (s *Server) findOllamaContainer(ctx context.Context) ollamaContainer {
	oc := ollamaContainer{GPU: "unknown"}
	if !sysinfo.Has(ctx, "docker") {
		oc.Error = "docker CLI indisponível no servidor"
		return oc
	}
	oc.Docker = true
	ref := s.cfg.OllamaContainer
	if ref == "" {
		for _, filters := range [][]string{
			{"--filter", "label=com.docker.compose.service=ollama", "--filter", "label=com.docker.compose.project=doomctl"},
			{"--filter", "label=com.docker.compose.service=ollama"},
		} {
			args := append([]string{"docker", "ps", "-a", "--format", "{{.ID}}"}, filters...)
			so, se, code, err := sysinfo.Run(ctx, 10*time.Second, nil, s.toolEnv(), args...)
			if err != nil || code != 0 {
				oc.Error = dockerErr(se, err)
				return oc
			}
			if f := strings.Fields(so); len(f) > 0 {
				ref = f[0]
				break
			}
		}
	}
	if ref == "" {
		return oc
	}
	so, se, code, err := sysinfo.Run(ctx, 10*time.Second, nil, s.toolEnv(), "docker", "inspect", "--format",
		"{{.Id}}|{{.Name}}|{{.Config.Image}}|{{.State.Status}}|{{json .HostConfig.DeviceRequests}}|{{json .HostConfig.Devices}}", ref)
	if err != nil || code != 0 {
		oc.Error = dockerErr(se, err)
		return oc
	}
	parts := strings.SplitN(strings.TrimSpace(so), "|", 6)
	if len(parts) < 6 {
		return oc
	}
	oc.Found = true
	oc.ID, oc.Name, oc.Image, oc.State = parts[0][:12], strings.TrimPrefix(parts[1], "/"), parts[2], parts[3]
	oc.GPU = "none"
	if strings.Contains(parts[4], "nvidia") || strings.Contains(parts[4], `"gpu"`) {
		oc.GPU = "nvidia"
	} else if strings.Contains(parts[5], "/dev/kfd") {
		oc.GPU = "amd"
	}
	return oc
}

// dockerErr resume falhas comuns do docker CLI.
func dockerErr(stderr string, err error) string {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "docker.sock") || strings.Contains(low, "cannot connect to the docker daemon"):
		return "sem acesso ao Docker (socket não montado no container do doomctl)"
	case strings.Contains(low, "permission denied"):
		return "sem permissão no socket do Docker (confira DOCKER_GID no .env)"
	}
	return truncate(firstNonBlank(lastLines(stderr, 1), fmt.Sprint(err)), 200)
}

func (s *Server) nvidiaSMI(ctx context.Context, id string) ([]gpuDevice, string) {
	so, se, code, err := sysinfo.Run(ctx, 10*time.Second, nil, s.toolEnv(), "docker", "exec", id, "nvidia-smi",
		"--query-gpu=name,memory.total,memory.used,utilization.gpu", "--format=csv,noheader,nounits")
	if err != nil || code != 0 {
		return nil, firstNonBlank(lastLines(se, 1), lastLines(so, 1), fmt.Sprint(err))
	}
	var out []gpuDevice
	for _, line := range strings.Split(strings.TrimSpace(so), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 4 {
			continue
		}
		atoi := func(x string) int { n, _ := strconv.Atoi(strings.TrimSpace(x)); return n }
		out = append(out, gpuDevice{Name: strings.TrimSpace(f[0]), MemTotal: atoi(f[1]), MemUsed: atoi(f[2]), Util: atoi(f[3])})
	}
	return out, ""
}

func (s *Server) aiOllamaInfo(w http.ResponseWriter, r *http.Request, _ *User) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("conn"), 10, 64)
	o, def, err := s.ollamaFor(r.Context(), id)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	out := map[string]any{"conn": id, "host": def.BaseURL, "default_model": def.Model, "gpu_install": s.cfg.AIGPU}
	models, err := o.ListModels(ctx)
	if err != nil {
		out["online"], out["error"] = false, err.Error()
		models = []agents.OllamaModel{}
	} else {
		out["online"] = true
		out["version"] = o.Version(ctx)
	}
	out["models"] = models
	running, _ := o.PS(ctx)
	if running == nil {
		running = []agents.RunningModel{}
	}
	out["running"] = running
	if id == 0 {
		oc := s.findOllamaContainer(ctx)
		out["container"] = oc
		if oc.Found && oc.GPU == "nvidia" && oc.State == "running" {
			devs, e := s.nvidiaSMI(ctx, oc.ID)
			out["gpus"], out["gpus_error"] = devs, e
		}
	}
	writeJSON(w, 200, out)
}

var ollamaModelRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,199}$`)

func (s *Server) aiOllamaPull(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Conn  int64  `json:"conn"`
		Model string `json:"model"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Model = strings.TrimSpace(in.Model)
	if !ollamaModelRe.MatchString(in.Model) {
		fail(w, 400, "nome de modelo inválido")
		return
	}
	o, def, err := s.ollamaFor(r.Context(), in.Conn)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.startJob(w, u, jobs.Spec{Module: "ai", Action: "ollama_pull", Target: in.Model + " @ " + def.Name, Input: map[string]any{"model": in.Model, "host": def.BaseURL},
		Timeout: 6 * time.Hour, Steps: []jobs.Step{{Name: "ollama pull " + in.Model, Func: func(ctx context.Context, w io.Writer) error {
			last, lastAt := "", time.Time{}
			err := o.Pull(ctx, in.Model, func(status string, done, total int64) {
				line := status
				if total > 0 {
					line = fmt.Sprintf("%s  %.0f%%  (%s / %s)", status, float64(done)/float64(total)*100, humanBytes(done), humanBytes(total))
				}
				if status != last || time.Since(lastAt) > 3*time.Second {
					fmt.Fprintln(w, line)
					last, lastAt = status, time.Now()
				}
			})
			if err == nil {
				fmt.Fprintln(w, "modelo pronto")
			}
			return err
		}}}})
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (s *Server) aiOllamaDelete(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Conn  int64  `json:"conn"`
		Model string `json:"model"`
	}
	if err := decode(r, &in); err != nil || !ollamaModelRe.MatchString(in.Model) {
		fail(w, 400, "modelo inválido")
		return
	}
	o, def, err := s.ollamaFor(r.Context(), in.Conn)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := o.Delete(r.Context(), in.Model); err != nil {
		fail(w, 502, err.Error())
		return
	}
	s.audit("ai", "ollama_delete", in.Model+" @ "+def.Name, u, "info", "modelo removido do Ollama", nil)
	ok(w)
}

// aiOllamaLoad carrega (com as opções de GPU atuais) ou descarrega um modelo da memória.
func (s *Server) aiOllamaLoad(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Conn   int64  `json:"conn"`
		Model  string `json:"model"`
		Action string `json:"action"` // load | unload
	}
	if err := decode(r, &in); err != nil || !ollamaModelRe.MatchString(in.Model) {
		fail(w, 400, "modelo inválido")
		return
	}
	o, def, err := s.ollamaFor(r.Context(), in.Conn)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	st := s.ai.Runtime().Settings
	opt := agents.GenOptions{NumThread: st.GPU.NumThread, NumCtx: 8192}
	if a := st.Agents[agents.RouteCloud]; a != nil {
		opt.NumCtx = a.NumCtx
	}
	switch st.GPU.Mode {
	case "cpu":
		n := 0
		opt.NumGPU = &n
	case "layers":
		n := st.GPU.Layers
		opt.NumGPU = &n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	keep := st.GPU.KeepAlive
	if in.Action == "unload" {
		keep = "0"
	} else if in.Action != "load" {
		fail(w, 400, "ação inválida")
		return
	}
	start := time.Now()
	if err := o.Load(ctx, in.Model, keep, opt); err != nil {
		fail(w, 502, err.Error())
		return
	}
	s.audit("ai", "ollama_"+in.Action, in.Model+" @ "+def.Name, u, "info", "modelo "+map[string]string{"load": "carregado", "unload": "descarregado"}[in.Action], nil)
	running, _ := o.PS(r.Context())
	writeJSON(w, 200, map[string]any{"ok": true, "duration_ms": time.Since(start).Milliseconds(), "running": running})
}

// aiOllamaRestart reinicia o container do Ollama (compose, perfil ai).
func (s *Server) aiOllamaRestart(w http.ResponseWriter, r *http.Request, u *User) {
	oc := s.findOllamaContainer(r.Context())
	if !oc.Found {
		fail(w, 404, firstNonBlank(oc.Error, "container do Ollama não encontrado (ele só existe quando instalado com --with-ai)"))
		return
	}
	s.startJob(w, u, jobs.Spec{Module: "ai", Action: "ollama_restart", Target: oc.Name, Input: map[string]any{"container": oc.Name},
		Timeout: 5 * time.Minute, Steps: []jobs.Step{{Name: "docker restart " + oc.Name, Args: []string{"docker", "restart", oc.ID}}}})
}

// ---------- uso ----------

func (s *Server) aiUsage(w http.ResponseWriter, r *http.Request, _ *User) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 30
	}
	ctx := r.Context()
	since := time.Now().AddDate(0, 0, -days)
	type row struct {
		Key      string  `json:"key"`
		Count    int64   `json:"count"`
		Failed   int64   `json:"failed"`
		AvgMs    float64 `json:"avg_ms"`
		TokensIn int64   `json:"tokens_in"`
		TokensOu int64   `json:"tokens_out"`
	}
	group := func(expr string) []row {
		rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(`+expr+`, ''), count(*), count(*) FILTER (WHERE status='failed'),
			COALESCE(avg(duration_ms),0), COALESCE(sum((input->>'tokens_in')::bigint),0), COALESCE(sum((input->>'tokens_out')::bigint),0)
			FROM tool_logs WHERE module='ai' AND action LIKE 'chat_%' AND started_at >= $1 GROUP BY 1 ORDER BY 2 DESC LIMIT 50`, since)
		if err != nil {
			slog.Error("uso IA", "erro", err)
			return []row{}
		}
		defer rows.Close()
		out := []row{}
		for rows.Next() {
			var x row
			if rows.Scan(&x.Key, &x.Count, &x.Failed, &x.AvgMs, &x.TokensIn, &x.TokensOu) == nil {
				out = append(out, x)
			}
		}
		return out
	}
	daily := []map[string]any{}
	if rows, err := s.db.QueryContext(ctx, `SELECT to_char(date_trunc('day', started_at), 'YYYY-MM-DD'), count(*) FROM tool_logs
		WHERE module='ai' AND action LIKE 'chat_%' AND started_at >= $1 GROUP BY 1 ORDER BY 1`, since); err == nil {
		defer rows.Close()
		for rows.Next() {
			var d string
			var n int64
			if rows.Scan(&d, &n) == nil {
				daily = append(daily, map[string]any{"day": d, "count": n})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"days": days,
		"by_route": group(`input->>'route'`), "by_connection": group(`input->>'connection'`), "by_user": group(`username`), "daily": daily})
}
