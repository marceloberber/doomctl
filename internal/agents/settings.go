package agents

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Settings é a configuração dos agentes editável pelo navegador (persistida pelo
// servidor). Os valores padrão reproduzem exatamente o comportamento original
// descrito no AGENTS.md.
type Settings struct {
	Router     RouterSettings           `json:"router"`
	Agents     map[Route]*AgentSettings `json:"agents"`
	Guardrails Guardrails               `json:"guardrails"`
	Style      Style                    `json:"style"`
	Limits     Limits                   `json:"limits"`
	GPU        GPUSettings              `json:"gpu"`
	// Instruções globais em Markdown, acrescentadas a todas as personas.
	GlobalInstructions string `json:"global_instructions"`
}

type RouterSettings struct {
	UseLLM       bool   `json:"use_llm"`       // usa o classificador LLM em empates/zero
	ConnectionID int64  `json:"connection_id"` // 0 = Ollama padrão (.env)
	Model        string `json:"model"`         // vazio = modelo da conexão/ambiente
}

type AgentSettings struct {
	ConnectionID  int64    `json:"connection_id"`
	Model         string   `json:"model"`
	Temperature   float64  `json:"temperature"`
	TopP          float64  `json:"top_p"`
	RepeatPenalty float64  `json:"repeat_penalty"`
	NumCtx        int      `json:"num_ctx"`
	MaxTokens     int      `json:"max_tokens"`  // 0 = usa o limite global
	Think         string   `json:"think"`       // "" (não envia) | "true" | "false"
	PromptMode    string   `json:"prompt_mode"` // default | append | replace
	Prompt        string   `json:"prompt"`      // Markdown
	Keywords      []string `json:"keywords"`    // palavras extras para o roteamento (peso 3)
	MCPServers    []int64  `json:"mcp_servers"`
	MaxToolCalls  int      `json:"max_tool_calls"`
	// Somente para conexões "Agente do Azure AI Foundry": envia os guardrails/estilo
	// do doomctl como instruções adicionais do run.
	SendGuardrails bool `json:"send_guardrails"`
}

type Guardrails struct {
	SafeTemperature   bool     `json:"safe_temperature"` // faixa 0.2–0.3 do AGENTS.md
	ScopeRefusal      bool     `json:"scope_refusal"`    // recusa fora de escopo
	RefusalMessage    string   `json:"refusal_message"`
	AntiHallucination bool     `json:"anti_hallucination"`
	OperationalSafety bool     `json:"operational_safety"`
	BlockedTerms      []string `json:"blocked_terms"`
	Redact            string   `json:"redact"` // off | external | all
	// Conteúdo de anexos (arquivos do editor) pode ir para provedores externos?
	AttachmentsExternal bool `json:"attachments_external"`
	// Ferramentas MCP com efeitos colaterais (sem readOnlyHint) exigem liberação explícita no servidor MCP.
}

type Style struct {
	Tone      string `json:"tone"`      // cauteloso | neutro | descontraido | formal
	Verbosity string `json:"verbosity"` // equilibrado | conciso | detalhado
	Language  string `json:"language"`  // pt-BR | en | es
	Sections  bool   `json:"sections"`  // Resumo / Validação / Riscos
	Emojis    bool   `json:"emojis"`    // marcadores 🛑 e ⚠️
}

type Limits struct {
	MaxOutputTokens int `json:"max_output_tokens"` // 0 = sem limite
	MaxInputChars   int `json:"max_input_chars"`
	MaxAttachChars  int `json:"max_attach_chars"`
	RatePer5Min     int `json:"rate_per_5min"`
	TimeoutSeconds  int `json:"timeout_seconds"`
	HistoryMessages int `json:"history_messages"` // mensagens (pergunta+resposta) mantidas por persona
	ToolResultChars int `json:"tool_result_chars"`
}

type GPUSettings struct {
	Mode      string `json:"mode"`       // auto | cpu | layers
	Layers    int    `json:"layers"`     // num_gpu quando mode=layers
	KeepAlive string `json:"keep_alive"` // ex.: 5m, 30m, -1 (sempre carregado); vazio = padrão do Ollama
	NumThread int    `json:"num_thread"` // 0 = automático
}

// DefaultSettings reproduz o comportamento original.
func DefaultSettings() Settings {
	agent := func() *AgentSettings {
		return &AgentSettings{Temperature: 0.25, TopP: 0.9, RepeatPenalty: 1.1, NumCtx: 8192, PromptMode: "default",
			Keywords: []string{}, MCPServers: []int64{}, MaxToolCalls: 4, SendGuardrails: true}
	}
	return Settings{
		Router: RouterSettings{UseLLM: true},
		Agents: map[Route]*AgentSettings{RouteCloud: agent(), RouteNetSec: agent()},
		Guardrails: Guardrails{SafeTemperature: true, ScopeRefusal: true, RefusalMessage: RefusalMsg, AntiHallucination: true,
			OperationalSafety: true, BlockedTerms: []string{}, Redact: "external", AttachmentsExternal: true},
		Style:  Style{Tone: "cauteloso", Verbosity: "equilibrado", Language: "pt-BR", Sections: true, Emojis: true},
		Limits: Limits{MaxInputChars: 32 << 10, MaxAttachChars: 256 << 10, RatePer5Min: 30, TimeoutSeconds: 600, HistoryMessages: 10, ToolResultChars: 8000},
		GPU:    GPUSettings{Mode: "auto"},
	}
}

var (
	tones      = map[string]bool{"cauteloso": true, "neutro": true, "descontraido": true, "formal": true}
	verbosity  = map[string]bool{"equilibrado": true, "conciso": true, "detalhado": true}
	languages  = map[string]bool{"pt-BR": true, "en": true, "es": true}
	promptMode = map[string]bool{"default": true, "append": true, "replace": true}
)

// Normalize completa campos ausentes e valida faixas. Erros são devolvidos para o
// usuário corrigir (nada é ajustado silenciosamente além de padrões vazios).
func (s *Settings) Normalize() error {
	d := DefaultSettings()
	if s.Agents == nil {
		s.Agents = map[Route]*AgentSettings{}
	}
	for _, r := range []Route{RouteCloud, RouteNetSec} {
		if s.Agents[r] == nil {
			s.Agents[r] = d.Agents[r]
		}
	}
	for r := range s.Agents {
		if r != RouteCloud && r != RouteNetSec {
			delete(s.Agents, r)
		}
	}
	if s.Guardrails.RefusalMessage = strings.TrimSpace(s.Guardrails.RefusalMessage); s.Guardrails.RefusalMessage == "" {
		s.Guardrails.RefusalMessage = RefusalMsg
	}
	if len(s.Guardrails.RefusalMessage) > 300 {
		return errors.New("mensagem de recusa muito longa (máx. 300)")
	}
	switch s.Guardrails.Redact {
	case "off", "external", "all":
	case "":
		s.Guardrails.Redact = d.Guardrails.Redact
	default:
		return errors.New("redação de segredos inválida")
	}
	var terms []string
	for _, t := range s.Guardrails.BlockedTerms {
		if t = strings.TrimSpace(t); t != "" && len(terms) < 200 {
			terms = append(terms, t)
		}
	}
	if terms == nil {
		terms = []string{}
	}
	s.Guardrails.BlockedTerms = terms
	if !tones[s.Style.Tone] {
		s.Style.Tone = d.Style.Tone
	}
	if !verbosity[s.Style.Verbosity] {
		s.Style.Verbosity = d.Style.Verbosity
	}
	if !languages[s.Style.Language] {
		s.Style.Language = d.Style.Language
	}
	l := &s.Limits
	if l.MaxInputChars <= 0 {
		l.MaxInputChars = d.Limits.MaxInputChars
	}
	if l.MaxAttachChars <= 0 {
		l.MaxAttachChars = d.Limits.MaxAttachChars
	}
	if l.RatePer5Min <= 0 {
		l.RatePer5Min = d.Limits.RatePer5Min
	}
	if l.TimeoutSeconds <= 0 {
		l.TimeoutSeconds = d.Limits.TimeoutSeconds
	}
	if l.HistoryMessages < 0 {
		l.HistoryMessages = d.Limits.HistoryMessages
	}
	if l.ToolResultChars <= 0 {
		l.ToolResultChars = d.Limits.ToolResultChars
	}
	switch {
	case l.MaxInputChars > 1<<20, l.MaxAttachChars > 4<<20:
		return errors.New("limite de entrada acima do máximo (1 MiB de mensagem, 4 MiB de anexo)")
	case l.MaxOutputTokens < 0 || l.MaxOutputTokens > 200000:
		return errors.New("limite de tokens de saída inválido")
	case l.RatePer5Min > 1000:
		return errors.New("limite de perguntas por 5 minutos acima de 1000")
	case l.TimeoutSeconds > 3600:
		return errors.New("tempo limite máximo de 3600 s")
	case l.HistoryMessages > 100:
		return errors.New("histórico máximo de 100 mensagens")
	}
	switch s.GPU.Mode {
	case "auto", "cpu", "layers":
	case "":
		s.GPU.Mode = "auto"
	default:
		return errors.New("modo de GPU inválido")
	}
	if s.GPU.Layers < 0 || s.GPU.Layers > 999 || s.GPU.NumThread < 0 || s.GPU.NumThread > 512 {
		return errors.New("camadas na GPU (0–999) ou threads (0–512) fora da faixa")
	}
	if ka := strings.TrimSpace(s.GPU.KeepAlive); ka != "" && !validKeepAlive(ka) {
		return errors.New("keep_alive inválido (ex.: 5m, 1h, 0, -1)")
	}
	if len(s.GlobalInstructions) > 32<<10 {
		return errors.New("instruções globais acima de 32 KiB")
	}
	for r, a := range s.Agents {
		name := PersonaName(r)
		if !promptMode[a.PromptMode] {
			a.PromptMode = "default"
		}
		if len(a.Prompt) > 64<<10 {
			return fmt.Errorf("%s: prompt acima de 64 KiB", name)
		}
		if a.PromptMode == "replace" && strings.TrimSpace(a.Prompt) == "" {
			return fmt.Errorf("%s: o modo \"substituir\" exige um prompt", name)
		}
		if s.Guardrails.SafeTemperature && (a.Temperature < 0.2 || a.Temperature > 0.3) {
			return fmt.Errorf("%s: temperatura %.2f fora da faixa segura 0.2–0.3 (desative a trava em Guardrails para usar outros valores)", name, a.Temperature)
		}
		if a.Temperature < 0 || a.Temperature > 2 {
			return fmt.Errorf("%s: temperatura deve ficar entre 0 e 2", name)
		}
		if a.TopP <= 0 || a.TopP > 1 {
			a.TopP = 0.9
		}
		if a.RepeatPenalty <= 0 || a.RepeatPenalty > 3 {
			a.RepeatPenalty = 1.1
		}
		if a.NumCtx < 0 || a.NumCtx > 1<<20 {
			return fmt.Errorf("%s: contexto (num_ctx) fora da faixa", name)
		}
		if a.NumCtx == 0 {
			a.NumCtx = 8192
		}
		if a.MaxTokens < 0 || a.MaxTokens > 200000 {
			return fmt.Errorf("%s: limite de tokens inválido", name)
		}
		switch a.Think {
		case "", "true", "false":
		default:
			return fmt.Errorf("%s: valor de think inválido", name)
		}
		if a.MaxToolCalls <= 0 || a.MaxToolCalls > 20 {
			a.MaxToolCalls = 4
		}
		var kws []string
		for _, k := range a.Keywords {
			if k = normalize(strings.TrimSpace(k)); k != "" && len(kws) < 200 {
				kws = append(kws, k)
			}
		}
		if kws == nil {
			kws = []string{}
		}
		a.Keywords = kws
		if a.MCPServers == nil {
			a.MCPServers = []int64{}
		}
		a.Model = strings.TrimSpace(a.Model)
	}
	s.Router.Model = strings.TrimSpace(s.Router.Model)
	return nil
}

func validKeepAlive(s string) bool {
	if s == "0" || s == "-1" {
		return true
	}
	if n, err := strconv.Atoi(s); err == nil && n >= -1 {
		return true
	}
	if len(s) < 2 {
		return false
	}
	unit := s[len(s)-1]
	if unit != 's' && unit != 'm' && unit != 'h' {
		return false
	}
	_, err := strconv.Atoi(s[:len(s)-1])
	return err == nil
}

// ---------- composição do system prompt ----------

func (s Settings) markers() (destrutivo, suposicao string) {
	if s.Style.Emojis {
		return "🛑 Destrutivo:", "⚠️ Suposição:"
	}
	return "DESTRUTIVO:", "Suposição:"
}

func (st Style) isDefault() bool {
	d := DefaultSettings().Style
	return st == d
}

func (st Style) formatText() string {
	if st.isDefault() {
		return formatDefault
	}
	var b strings.Builder
	lang := map[string]string{"pt-BR": "pt-BR", "en": "inglês (en)", "es": "espanhol (es)"}[st.Language]
	b.WriteString("FORMATO: responda em " + lang + ". ")
	if st.Sections {
		b.WriteString("Comece com \"**Resumo:**\" (1–2 frases). Depois passos/código em blocos com linguagem,\n" +
			"\"**Validação:**\" (como testar) e \"**Riscos / Rollback:**\" quando houver risco real.\n")
	} else {
		b.WriteString("Use Markdown e blocos de código com linguagem; organize a resposta como achar melhor.\n")
	}
	b.WriteString("Sem introduções ou elogios à pergunta. ")
	switch st.Verbosity {
	case "conciso":
		b.WriteString("Seja conciso: respostas curtas, só o essencial e o comando/código necessário.\n")
	case "detalhado":
		b.WriteString("Seja detalhado: explique o porquê de cada passo, alternativas e armadilhas comuns.\n")
	default:
		b.WriteString("Estilo técnico, pragmático e direto.\n")
	}
	switch st.Tone {
	case "neutro":
		b.WriteString("Tom neutro e objetivo, sem humor.")
	case "descontraido":
		b.WriteString("Tom descontraído e próximo, sem perder a precisão técnica.")
	case "formal":
		b.WriteString("Tom formal e corporativo.")
	default:
		b.WriteString("Tom cauteloso: se não sabe, diz que não sabe.")
	}
	if !st.Emojis {
		b.WriteString(" Não use emojis.")
	}
	b.WriteString("\n")
	return b.String()
}

// ComposeSystemPrompt monta o system prompt da persona conforme as configurações.
// Com DefaultSettings o texto é idêntico ao prompt original do AGENTS.md.
func ComposeSystemPrompt(s Settings, r Route) string {
	a := s.Agents[r]
	if a == nil {
		a = DefaultSettings().Agents[r]
	}
	var b strings.Builder
	switch a.PromptMode {
	case "replace":
		b.WriteString(strings.TrimSpace(a.Prompt))
		b.WriteString("\n")
	case "append":
		b.WriteString(DefaultPersonaPrompt(r))
		if p := strings.TrimSpace(a.Prompt); p != "" {
			b.WriteString("\nINSTRUÇÕES ADICIONAIS DA PERSONA:\n" + p + "\n")
		}
	default:
		b.WriteString(DefaultPersonaPrompt(r))
	}
	if g := strings.TrimSpace(s.GlobalInstructions); g != "" {
		b.WriteString("\nINSTRUÇÕES DA ORGANIZAÇÃO:\n" + g + "\n")
	}
	b.WriteString(guardrailsText(s))
	return b.String()
}

// guardrailsText devolve o bloco "REGRAS OBRIGATÓRIAS" + FORMATO.
func guardrailsText(s Settings) string {
	destr, sup := s.markers()
	var rules []string
	if s.Guardrails.ScopeRefusal {
		rules = append(rules, strings.ReplaceAll(ruleScope, "{{RECUSA}}", s.Guardrails.RefusalMessage))
	}
	if s.Guardrails.AntiHallucination {
		rules = append(rules, strings.ReplaceAll(ruleAntiHalluc, "{{SUPOSICAO}}", sup))
	}
	if s.Guardrails.OperationalSafety {
		rules = append(rules, strings.ReplaceAll(ruleOperational, "{{DESTRUTIVO}}", destr))
	}
	// Limites de segurança ofensiva e integridade do prompt não são desativáveis.
	rules = append(rules, ruleOffensive, ruleIntegrity)
	var b strings.Builder
	b.WriteString(guardrailsHeader + "\n\n")
	for i, r := range rules {
		fmt.Fprintf(&b, "%d. %s\n\n", i+1, r)
	}
	b.WriteString(s.Style.formatText())
	return b.String()
}

// GuardrailsOnly devolve só as regras e o formato (para agentes externos que já têm persona).
func GuardrailsOnly(s Settings) string {
	var b strings.Builder
	if g := strings.TrimSpace(s.GlobalInstructions); g != "" {
		b.WriteString("INSTRUÇÕES DA ORGANIZAÇÃO:\n" + g + "\n")
	}
	b.WriteString(guardrailsText(s))
	return strings.TrimSpace(b.String())
}
