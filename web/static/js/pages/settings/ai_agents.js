// Aba "Agentes de IA": conexão/modelo, parâmetros de geração, persona (prompt em Markdown),
// palavras-chave e ferramentas MCP de cada persona, e o classificador de escopo.
import { get, post } from "../../api.js";
import { h, mount, btn, badge, card, toast, toastErr, confirmDialog } from "../../ui.js";
import {
  PERSONAS, KIND_LABEL, sliderField, numField, textField, selectField, toggle, segmented, mdEditor, chipsInput, fieldset,
  connectionOptions, connById, previewPrompts, warningsBox,
} from "./ai_common.js";

export async function render(root, ctx) {
  const ai = ctx.ai;
  const view = h("div");
  root.appendChild(view);
  const draw = () => {
    const cfg = ai.cfg;
    const d = ai.draft;
    const rt = cfg.runtime;
    mount(view, h("div", { class: "stack" },
      h("div", { class: "spread" },
        h("p", { class: "muted small ai-lead" },
          "Sem nada configurado, os agentes usam o Ollama do .env (", h("code", { text: cfg.env.ollama_host }), ", modelo ", h("code", { text: cfg.env.ollama_model }),
          ") com os prompts do AGENTS.md — exatamente como na instalação com ", h("code", { text: "--with-ai" }), ". O estilo do chat não muda: só o que você alterar aqui."),
        h("div", { class: "row" },
          btn("Ver prompt final", { icon: "eye", cls: "sm", onClick: () => previewPrompts(ai.draft) }),
          btn("Restaurar padrão", { icon: "refresh", cls: "sm ghost", onClick: async () => {
            if (!await confirmDialog({ title: "Restaurar padrão", message: "Volta personas, guardrails, estilo, limites e GPU para o padrão original (conexões e servidores MCP cadastrados são mantidos). Uma versão é guardada no histórico.", confirmText: "Restaurar" })) return;
            ai.apply(await post("/api/ai/config/reset", {}));
            toast("Padrão restaurado", "ok");
          } }))),
      warningsBox(rt.warnings),
      h("div", { class: "grid g2 ai-personas" }, PERSONAS.map((p) => personaCard(ai, p, d.agents[p.id], rt.agents[p.id]))),
      routerCard(ai, d.router, rt.router)));
  };
  draw();
  const off = ai.on((ev) => { if (!view.isConnected) return off(); if (ev !== "reload") draw(); });
}

function personaCard(ai, p, a, live) {
  const cfg = ai.cfg;
  const set = (k, v) => { a[k] = v; ai.change(); };
  const conn = () => connById(cfg, a.connection_id) || connById(cfg, 0);
  const listId = "models-" + p.id;
  const datalist = h("datalist", { id: listId });
  const modelSection = h("div", { class: "stack sm" });
  const genSection = h("div");
  const promptSection = h("div", { class: "stack sm" });
  const toolsSection = h("div", { class: "stack sm" });

  const drawModel = () => {
    const c = conn();
    const isFoundry = c.kind === "foundry_agent";
    const model = textField(isFoundry ? "Agente (opcional)" : "Modelo", a.model, {
      list: listId, mono: true,
      placeholder: (c.config && c.config.model) || (isFoundry ? "agente definido na conexão" : "modelo da conexão"),
      hint: isFoundry ? "Vazio = agente configurado na conexão. Use nome ou nome:versão." : "Vazio = modelo padrão da conexão.",
    }, (v) => set("model", v.trim()));
    const listBtn = btn(isFoundry ? "Listar agentes" : "Listar modelos", { icon: "search", cls: "sm", onClick: async () => {
      try {
        const r = await get(`/api/ai/connections/${a.connection_id || 0}/models`);
        const items = r.agents ? r.agents.map((x) => x.version ? `${x.name}:${x.version}` : x.id) : r.models;
        mount(datalist, items.map((m) => h("option", { value: m })));
        toast(`${items.length} ${r.agents ? "agente(s)" : "modelo(s)"} disponível(is) — comece a digitar no campo`, "ok");
        model.input.focus();
      } catch (e) { toastErr(e); }
    } });
    const think = c.kind === "ollama" ? selectField("Raciocínio (think)", [["", "Padrão do ambiente"], ["false", "Desligado"], ["true", "Ligado"]], a.think,
      (v) => set("think", v), "Modelos com raciocínio: o texto do raciocínio nunca é exibido.") : null;
    mount(modelSection,
      h("div", { class: "form-grid" },
        selectField("Conexão", connectionOptions(cfg), a.connection_id, (v) => { a.connection_id = Number(v); a.model = ""; ai.change(); drawModel(); drawGen(); drawTools(); drawPrompt(); },
          c.kind === "ollama" ? "Ollama: os dados ficam no seu ambiente." : (c.external ? "Provedor externo: perguntas saem do seu ambiente (veja a redação de segredos em Comportamento)." : "Servidor na rede interna.")),
        h("div", { class: "field" }, model, h("div", { class: "row" }, listBtn)), think),
      datalist);
  };

  const drawGen = () => {
    const c = conn();
    if (c.kind === "foundry_agent") {
      mount(genSection, h("p", { class: "muted small", text: "Temperatura, top_p e modelo são definidos no próprio agente do Foundry. O limite de tokens de saída abaixo é repassado quando informado." }),
        h("div", { class: "form-grid" }, numField("Máx. tokens de saída", a.max_tokens, { min: 0, max: 200000, hint: "0 = limite global (Comportamento → Limites)" }, (v) => set("max_tokens", v))));
      return;
    }
    const safe = ai.draft.guardrails.safe_temperature;
    const isOllama = c.kind === "ollama";
    const sendsTemp = isOllama || (c.config && c.config.send_temperature);
    mount(genSection, h("div", { class: "form-grid" },
      sliderField("Temperatura", a.temperature, { min: safe ? 0.2 : 0, max: safe ? 0.3 : 2, step: 0.01,
        hint: safe ? "Trava segura 0.2–0.3 (AGENTS.md). Desative em Comportamento → Guardrails." : (sendsTemp ? "Valores altos aumentam a criatividade e o risco de alucinação." : "Esta conexão não envia temperatura (ver conexão).") },
      (v) => set("temperature", v)),
      sliderField("top_p", a.top_p, { min: 0.05, max: 1, step: 0.05, hint: "Corta a cauda de tokens improváveis (padrão 0.9)." }, (v) => set("top_p", v)),
      isOllama ? sliderField("repeat_penalty", a.repeat_penalty, { min: 1, max: 2, step: 0.05, hint: "Evita repetições (padrão 1.1)." }, (v) => set("repeat_penalty", v)) : null,
      isOllama ? numField("Contexto (num_ctx)", a.num_ctx, { min: 512, max: 1048576, step: 512, hint: "Padrão 8192. Mais contexto usa mais RAM/VRAM." }, (v) => set("num_ctx", v)) : null,
      numField("Máx. tokens de saída", a.max_tokens, { min: 0, max: 200000, hint: "0 = limite global (Comportamento → Limites)" }, (v) => set("max_tokens", v))));
  };

  const drawPrompt = () => {
    const c = conn();
    const isFoundry = c.kind === "foundry_agent";
    const editor = mdEditor(a.prompt, (v) => set("prompt", v), { rows: 9,
      placeholder: a.prompt_mode === "replace" ? `Você é ${p.name}, ...\n\nDescreva identidade, especialidades e boas práticas em Markdown.` : "- Sempre cite a documentação oficial\n- Prefira exemplos com OpenTofu" });
    editor.classList.toggle("hidden", a.prompt_mode === "default");
    const modeHint = {
      default: "Persona original do AGENTS.md.",
      append: "Mantém a persona original e acrescenta suas instruções.",
      replace: "Substitui identidade e especialidades. Guardrails, regras de segurança e formato continuam valendo.",
    };
    const hint = h("span", { class: "hint", text: modeHint[a.prompt_mode] });
    mount(promptSection,
      h("div", { class: "spread" }, segmented([["default", "Padrão"], ["append", "Acrescentar"], ["replace", "Substituir"]], a.prompt_mode, (v) => {
        set("prompt_mode", v); editor.classList.toggle("hidden", v === "default"); hint.textContent = modeHint[v];
      }, "Modo do prompt da persona"), hint),
      editor,
      isFoundry ? toggle("Enviar guardrails, estilo e instruções do doomctl ao agente", a.send_guardrails, (v) => set("send_guardrails", v),
        "Vão como instruções adicionais (mensagem developer no 1º turno). A persona e as ferramentas continuam sendo as do agente.") : null);
  };

  const drawTools = () => {
    const c = conn();
    const servers = cfg.mcp;
    const mcpList = h("div", { class: "ai-checklist" });
    if (c.kind === "foundry_agent") {
      mcpList.appendChild(h("p", { class: "muted small", text: "Agentes do Foundry usam as ferramentas configuradas no próprio Foundry (inclusive MCP)." }));
    } else if (!servers.length) {
      mcpList.appendChild(h("p", { class: "muted small" }, "Nenhum servidor MCP cadastrado. ", h("a", { href: "#/settings/conexoes", text: "Cadastrar em Conexões & MCP" }), "."));
    } else {
      for (const s of servers) {
        const enabledTools = s.tools.filter((t) => (t.name in s.config.tools_enabled ? s.config.tools_enabled[t.name] : t.read_only)).length;
        const cb = h("input", { type: "checkbox", checked: a.mcp_servers.includes(s.id) });
        cb.addEventListener("change", () => {
          a.mcp_servers = cb.checked ? [...new Set([...a.mcp_servers, s.id])] : a.mcp_servers.filter((x) => x !== s.id);
          ai.change();
        });
        mcpList.appendChild(h("label", { class: "check ai-check-row" }, cb, h("span", null, h("b", { text: s.name }), " ",
          h("span", { class: "muted small", text: `${enabledTools}/${s.tools.length} ferramenta(s) liberada(s)` }),
          !s.enabled ? badge("desativado", "warn") : null, s.last_status === "error" ? badge("erro", "err") : null)));
      }
    }
    mount(toolsSection,
      h("div", { class: "field" }, h("span", { class: "lbl", text: "Palavras-chave extras para o roteamento" }),
        chipsInput(a.keywords, (v) => set("keywords", v), "ex.: wazuh, zabbix"),
        h("span", { class: "hint", text: "Termos que direcionam a pergunta para esta persona (peso forte). Somam-se às palavras-chave do AGENTS.md." })),
      h("div", { class: "field" }, h("span", { class: "lbl", text: "Ferramentas (servidores MCP)" }), mcpList),
      c.kind !== "foundry_agent" && servers.length ? h("div", { class: "form-grid" }, numField("Máx. chamadas de ferramenta por resposta", a.max_tool_calls, { min: 1, max: 20 }, (v) => set("max_tool_calls", v)),
        h("p", { class: "hint ai-pad", text: "As ferramentas exigem um modelo com suporte a function calling (ex.: Qwen 3, Llama 3.1+, GPT-4.1)." })) : null);
  };

  drawModel(); drawGen(); drawPrompt(); drawTools();
  const liveLine = live && live.connection ? h("span", null, "Em uso: ", h("b", { text: live.connection }), live.model ? [" · ", h("code", { text: live.model })] : null,
    live.tools ? ` · ${live.tools} ferramenta(s)` : null) : null;
  return card({
    title: `${p.name} · ${p.area}`,
    subtitle: null,
    actions: [live && live.external ? badge("provedor externo", "warn", true) : badge(KIND_LABEL[live && live.kind] || "Ollama", "outline")],
    body: h("div", { class: "stack" },
      liveLine ? h("p", { class: "small muted ai-live" }, liveLine) : null,
      fieldset("Modelo", modelSection),
      fieldset("Geração", genSection),
      fieldset("Persona (system prompt em Markdown)", promptSection),
      fieldset("Roteamento e ferramentas", toolsSection)),
  });
}

function routerCard(ai, r, live) {
  const cfg = ai.cfg;
  const set = (k, v) => { r[k] = v; ai.change(); };
  return card({
    title: "Classificador de escopo",
    subtitle: "Decide entre Atlas, Sentinela e fora de escopo quando as palavras-chave não bastam (temperatura 0, saída JSON).",
    actions: [live && live.connection ? h("span", { class: "small muted" }, "Em uso: ", h("b", { text: live.connection }), live.model ? [" · ", h("code", { text: live.model })] : null) : null],
    body: h("div", { class: "form-grid three" },
      toggle("Usar o classificador LLM", r.use_llm, (v) => set("use_llm", v), "Desligado: só palavras-chave; sem pontuação, a pergunta é recusada (falha fechada)."),
      selectField("Conexão", connectionOptions(cfg, { noFoundry: true }), r.connection_id, (v) => set("connection_id", Number(v)), "Agentes do Foundry não classificam."),
      textField("Modelo", r.model, { mono: true, placeholder: "modelo da conexão", hint: "Um modelo pequeno e rápido basta." }, (v) => set("model", v.trim()))),
  });
}

