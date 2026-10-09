package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doomctl/doomctl/internal/agents"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

// ---------- Plug-ins de terceiros ----------

const portainerContainer = "doomctl-portainer"
const portainerVolume = "doomctl_portainer_data"

type plugin struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
	Status      string   `json:"status"` // not_installed | running | stopped | unavailable
	Port        int      `json:"port"`
	Scheme      string   `json:"scheme"`
	Notes       []string `json:"notes"`
	Error       string   `json:"error,omitempty"`
}

func (s *Server) portainerStatus(ctx context.Context) plugin {
	p := plugin{ID: "portainer", Name: "Portainer Server Community Edition",
		Description: "Gerenciamento visual de Docker (containers, imagens, volumes, redes e stacks). Roda como aplicação à parte, em outra porta.",
		Image:       s.cfg.PortainerImage, Port: s.cfg.PortainerPort, Scheme: "https", Status: "not_installed",
		Notes: []string{
			"Após instalar, crie o usuário administrador do Portainer em até 5 minutos (limite de segurança do próprio Portainer).",
			"O Portainer recebe acesso ao socket do Docker do host — trate-o como acesso root.",
			"Certificado autoassinado por padrão: o navegador exibirá um alerta no primeiro acesso.",
		}}
	if !sysinfo.Has(ctx, "docker") {
		p.Status, p.Error = "unavailable", "docker CLI não instalado no servidor"
		return p
	}
	so, se, code, err := sysinfo.Run(ctx, 10*time.Second, nil, s.toolEnv(), "docker", "inspect", "--format", "{{json .State}}", portainerContainer)
	if err != nil {
		p.Status, p.Error = "unavailable", err.Error()
		return p
	}
	if code != 0 {
		if strings.Contains(strings.ToLower(se), "no such") {
			return p
		}
		p.Status, p.Error = "unavailable", lastLines(se, 1)
		return p
	}
	var st struct{ Running bool }
	json.Unmarshal([]byte(so), &st)
	if st.Running {
		p.Status = "running"
	} else {
		p.Status = "stopped"
	}
	return p
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, []plugin{s.portainerStatus(r.Context())})
}

func (s *Server) pluginAction(w http.ResponseWriter, r *http.Request, u *User) {
	if r.PathValue("id") != "portainer" {
		fail(w, 404, "plug-in desconhecido")
		return
	}
	var in struct {
		EdgePort bool   `json:"edge_port"`
		Purge    bool   `json:"purge"`
		Confirm  string `json:"confirm"`
	}
	decode(r, &in)
	st := s.portainerStatus(r.Context())
	if st.Status == "unavailable" {
		fail(w, 400, "Docker indisponível: "+st.Error)
		return
	}
	d := func(a ...string) []string { return append([]string{"docker"}, a...) }
	var steps []jobs.Step
	action := r.PathValue("action")
	switch action {
	case "install":
		if st.Status != "not_installed" {
			fail(w, 409, "o Portainer já está instalado")
			return
		}
		run := d("run", "-d", "--name", portainerContainer, "--restart=always",
			"-p", strconv.Itoa(s.cfg.PortainerPort)+":9443")
		if in.EdgePort {
			run = append(run, "-p", "8000:8000")
		}
		run = append(run, "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", portainerVolume+":/data",
			"--label", "managed-by=doomctl", s.cfg.PortainerImage)
		steps = []jobs.Step{
			{Name: "volume", Args: d("volume", "create", portainerVolume)},
			{Name: "pull", Args: d("pull", s.cfg.PortainerImage)},
			{Name: "run", Args: run},
		}
	case "start":
		steps = []jobs.Step{{Args: d("start", portainerContainer)}}
	case "stop":
		steps = []jobs.Step{{Args: d("stop", portainerContainer)}}
	case "restart":
		steps = []jobs.Step{{Args: d("restart", portainerContainer)}}
	case "update":
		if st.Status == "not_installed" {
			fail(w, 400, "o Portainer não está instalado")
			return
		}
		run := d("run", "-d", "--name", portainerContainer, "--restart=always", "-p", strconv.Itoa(s.cfg.PortainerPort)+":9443",
			"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", portainerVolume+":/data", "--label", "managed-by=doomctl", s.cfg.PortainerImage)
		steps = []jobs.Step{
			{Name: "pull", Args: d("pull", s.cfg.PortainerImage)},
			{Name: "remove", Args: d("rm", "-f", portainerContainer)},
			{Name: "run", Args: run},
		}
	case "uninstall":
		steps = []jobs.Step{{Name: "remove container", Args: d("rm", "-f", portainerContainer)}}
		if in.Purge {
			if in.Confirm != "portainer" {
				fail(w, 400, "🛑 Destrutivo: digite 'portainer' para apagar também os dados (volume)")
				return
			}
			steps = append(steps, jobs.Step{Name: "remove volume", Args: d("volume", "rm", portainerVolume)})
		}
	default:
		fail(w, 400, "ação inválida")
		return
	}
	s.startJob(w, u, jobs.Spec{Module: "plugins", Action: "portainer_" + action, Target: "Portainer CE",
		Input: map[string]any{"image": s.cfg.PortainerImage, "port": s.cfg.PortainerPort, "edge_port": in.EdgePort, "purge": in.Purge},
		Steps: steps, Timeout: 15 * time.Minute})
}

// ---------- Assistente de IA (Atlas / Sentinela) ----------

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request, _ *User) {
	st := s.ai.LLM().Status(r.Context())
	rt := s.ai.Runtime()
	set := rt.Settings
	agentsInfo := map[string]any{}
	temps := map[float64]bool{}
	custom := false
	for _, route := range []agents.Route{agents.RouteCloud, agents.RouteNetSec} {
		info := rt.Agent(route)
		a := set.Agents[route]
		temps[a.Temperature] = true
		agentsInfo[string(route)] = map[string]any{"persona": info.Persona, "connection": info.Connection, "kind": info.Kind, "model": info.Model,
			"external": info.External, "tools": info.Tools, "temperature": a.Temperature, "prompt_mode": a.PromptMode}
		if info.Kind != "ollama" || info.Connection != "Ollama padrão" || info.Model != s.cfg.OllamaModel || a.PromptMode != "default" {
			custom = true
		}
	}
	temperature := s.cfg.RouterTemperature
	if len(temps) == 1 {
		for t := range temps {
			temperature = t
		}
	}
	writeJSON(w, 200, map[string]any{"ollama": st, "temperature": temperature, "host": s.cfg.OllamaHost,
		"personas": []map[string]string{
			{"id": string(agents.RouteCloud), "name": "Atlas", "area": "Cloud & DevOps"},
			{"id": string(agents.RouteNetSec), "name": "Sentinela", "area": "Redes & Segurança"},
		},
		"agents": agentsInfo, "custom": custom, "uniform_temperature": len(temps) == 1,
		"scope_refusal": set.Guardrails.ScopeRefusal, "refusal": rt.RefusalMessage(), "router": rt.RouterInfo()})
}

func (s *Server) aiReset(w http.ResponseWriter, r *http.Request, u *User) {
	s.ai.Reset(strconv.FormatInt(u.ID, 10))
	ok(w)
}

// ndjsonWriter transforma o streaming do modelo em eventos NDJSON.
type ndjsonWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func (n *ndjsonWriter) send(v any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	b, _ := json.Marshal(v)
	n.w.Write(append(b, '\n'))
	if n.f != nil {
		n.f.Flush()
	}
}

func (n *ndjsonWriter) Write(p []byte) (int, error) {
	n.send(map[string]string{"type": "chunk", "text": string(p)})
	return len(p), nil
}

var langByExt = map[string]string{".yml": "yaml", ".yaml": "yaml", ".tf": "hcl", ".json": "json", ".sh": "bash",
	".py": "python", ".go": "go", ".conf": "nginx", ".ini": "ini"}

func (s *Server) aiChat(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Message string `json:"message"`
		Context struct {
			Module   string `json:"module"`
			Filename string `json:"filename"`
			Content  string `json:"content"`
		} `json:"context"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	rt := s.ai.Runtime()
	lim := rt.Settings.Limits
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		fail(w, 400, "mensagem vazia")
		return
	}
	if len(msg) > lim.MaxInputChars || len(in.Context.Content) > lim.MaxAttachChars {
		fail(w, 400, "mensagem ou anexo grande demais")
		return
	}
	if !s.rl.allow(fmt.Sprintf("ai:%d", u.ID), lim.RatePer5Min, 5*time.Minute) {
		fail(w, http.StatusTooManyRequests, "muitas perguntas seguidas; aguarde um pouco")
		return
	}

	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	nw := &ndjsonWriter{w: w, f: fl}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(lim.TimeoutSeconds)*time.Second)
	defer cancel()
	// classificação de escopo e roteamento usam só a pergunta (o anexo pode conter instruções hostis)
	d := rt.Decide(ctx, msg)
	nw.send(map[string]any{"type": "meta", "route": d.Route, "persona": personaFor(d.Route), "used_llm": d.UsedLLM,
		"reason": d.Reason, "scores": map[string]int{"cloud": d.Scores.Cloud, "netsec": d.Scores.NetSec}})

	question := msg
	attachment := in.Context.Filename
	if c := strings.TrimSpace(in.Context.Content); c != "" {
		if d.Route != agents.RouteOffTopic && rt.AgentExternal(d.Route) && !rt.Settings.Guardrails.AttachmentsExternal {
			nw.send(map[string]any{"type": "notice", "text": "O anexo não foi enviado: a política da organização não permite anexos para provedores externos."})
			attachment = ""
		} else {
			lang := ""
			for ext, l := range langByExt {
				if strings.HasSuffix(strings.ToLower(in.Context.Filename), ext) {
					lang = l
				}
			}
			if strings.EqualFold(in.Context.Filename, "Dockerfile") {
				lang = "dockerfile"
			}
			name := in.Context.Filename
			if name == "" {
				name = "anexo"
			}
			question = fmt.Sprintf("%s\n\nArquivo anexado (%s):\n```%s\n%s\n```", msg, name, lang, c)
		}
	}

	var answer string
	var res agents.Result
	var err error
	if d.Route == agents.RouteOffTopic {
		answer = rt.RefusalMessage()
		nw.send(map[string]any{"type": "refusal", "text": answer})
	} else {
		res, err = s.ai.AskRoutedEx(ctx, strconv.FormatInt(u.ID, 10), d, question, nw, &agents.Hooks{OnTool: func(e agents.ToolEvent) {
			nw.send(map[string]any{"type": "tool", "tool": e})
		}})
		answer = res.Answer
	}
	status := "success"
	if err != nil {
		status = "failed"
		slog.Warn("assistente IA", "erro", err, "conexao", res.Connection)
		hint := ""
		if res.Kind != "" && res.Kind != "ollama" || res.Connection != "" && res.Connection != "Ollama padrão" {
			hint = "Verifique a conexão \"" + res.Connection + "\" em Configurações → Assistente IA → Conexões."
		}
		nw.send(map[string]any{"type": "error", "error": "falha ao consultar o modelo: " + err.Error(), "hint": hint})
	}
	nw.send(map[string]any{"type": "done"})

	persona := personaFor(d.Route)
	var tools []string
	for _, t := range res.Tools {
		if t.Status != "start" {
			tools = append(tools, t.Server+"/"+t.Tool+":"+t.Status)
		}
	}
	_, dbErr := s.db.Exec(`INSERT INTO tool_logs(module, action, target, user_id, username, status, input, output, finished_at, duration_ms)
		VALUES ('ai', $1, $2, $3, $4, $5, $6, $7, now(), $8)`, "chat_"+string(d.Route), truncate(msg, 200), u.ID, u.Username,
		status, mustJSON(map[string]any{"route": d.Route, "used_llm": d.UsedLLM, "module": in.Context.Module,
			"attachment": attachment, "connection": res.Connection, "kind": res.Kind, "model": res.Model,
			"tokens_in": res.Usage.PromptTokens, "tokens_out": res.Usage.OutputTokens, "tools": tools, "redacted": res.Redacted}),
		"PERGUNTA:\n"+truncate(question, 64<<10)+"\n\nRESPOSTA ("+persona+"):\n"+answer, time.Since(start).Milliseconds())
	if dbErr != nil {
		slog.Error("log IA", "erro", dbErr)
	}
}

func personaFor(r agents.Route) string {
	switch r {
	case agents.RouteCloud:
		return "Atlas"
	case agents.RouteNetSec:
		return "Sentinela"
	}
	return "Fora de escopo"
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
