// Aba "Comportamento": guardrails, estilo/tom, instruções da organização (Markdown) e limites.
import { h, mount, btn, badge, card } from "../../ui.js";
import { numField, textField, selectField, toggle, segmented, mdEditor, chipsInput, previewPrompts } from "./ai_common.js";

export async function render(root, ctx) {
  const ai = ctx.ai;
  const view = h("div");
  root.appendChild(view);
  const draw = () => {
    const d = ai.draft;
    const g = d.guardrails;
    const st = d.style;
    const l = d.limits;
    const setG = (k, v) => { g[k] = v; ai.change(); };
    const setS = (k, v) => { st[k] = v; ai.change(); };
    const setL = (k, v) => { l[k] = v; ai.change(); };
    const refusal = textField("Mensagem de recusa", g.refusal_message, { maxlength: 300, hint: "Resposta exata para perguntas fora de escopo (sem chamar o modelo)." }, (v) => setG("refusal_message", v));
    refusal.classList.toggle("hidden", !g.scope_refusal);
    const def = (cond) => cond ? badge("padrão", "ok") : badge("alterado", "warn");
    mount(view, h("div", { class: "stack" },
      h("div", { class: "spread" },
        h("p", { class: "muted small ai-lead", text: "Com tudo no padrão, o system prompt é idêntico ao do AGENTS.md. Os limites de segurança ofensiva e a proteção contra injeção de prompt não podem ser desligados." }),
        btn("Ver prompt final", { icon: "eye", cls: "sm", onClick: () => previewPrompts(ai.draft) })),
      h("div", { class: "grid g2" },
        card({ title: "Guardrails", subtitle: "Regras aplicadas às duas personas.", body: h("div", { class: "stack" },
          toggle("Trava de temperatura segura (0.2–0.3)", g.safe_temperature, (v) => setG("safe_temperature", v),
            "Recomendado: reduz criatividade e alucinação. Desligada, aceita 0–2."),
          toggle("Recusar perguntas fora de escopo", g.scope_refusal, (v) => { setG("scope_refusal", v); refusal.classList.toggle("hidden", !v); },
            "Desligado, o assistente responde qualquer tema (a persona ainda é escolhida pelo roteador)."),
          refusal,
          toggle("Anti-alucinação", g.anti_hallucination, (v) => setG("anti_hallucination", v),
            "Não inventar flags, APIs, preços; marcar suposições; perguntar o que falta."),
          toggle("Segurança operacional", g.operational_safety, (v) => setG("operational_safety", v),
            "Avisos em comandos destrutivos, simulação antes, sem segredos reais, menor privilégio."),
          h("div", { class: "field" }, h("span", { class: "lbl", text: "Termos bloqueados" }),
            chipsInput(g.blocked_terms, (v) => setG("blocked_terms", v), "ex.: nome de projeto sigiloso"),
            h("span", { class: "hint", text: "Perguntas com estes termos recebem a mensagem de recusa sem chegar ao modelo (sem acento/caixa)." })),
          h("div", { class: "field" }, h("span", { class: "lbl", text: "Redação de segredos na pergunta" }),
            segmented([["off", "Desligada"], ["external", "Só provedores externos"], ["all", "Sempre"]], g.redact, (v) => setG("redact", v), "Redação de segredos"),
            h("span", { class: "hint", text: "Mascara chaves AWS, tokens GitHub/GitLab/Slack, JWT, chaves privadas, senhas em URLs e pares senha=… antes de enviar." })),
          toggle("Permitir anexos (arquivos do editor) para provedores externos", g.attachments_external, (v) => setG("attachments_external", v),
            "Desligado, o anexo só é enviado a modelos no seu ambiente (Ollama/rede interna).")) }),
        h("div", { class: "stack" },
          card({ title: "Estilo e tom", actions: [def(st.tone === "cauteloso" && st.verbosity === "equilibrado" && st.language === "pt-BR" && st.sections && st.emojis)],
            body: h("div", { class: "stack" },
              h("div", { class: "form-grid" },
                selectField("Tom", [["cauteloso", "Cauteloso (padrão)"], ["neutro", "Neutro"], ["descontraido", "Descontraído"], ["formal", "Formal"]], st.tone, (v) => setS("tone", v)),
                selectField("Detalhamento", [["equilibrado", "Equilibrado (padrão)"], ["conciso", "Conciso"], ["detalhado", "Detalhado"]], st.verbosity, (v) => setS("verbosity", v)),
                selectField("Idioma das respostas", [["pt-BR", "Português (Brasil)"], ["en", "Inglês"], ["es", "Espanhol"]], st.language, (v) => setS("language", v))),
              toggle("Estrutura Resumo → Passos → Validação → Riscos", st.sections, (v) => setS("sections", v)),
              toggle("Marcadores 🛑 Destrutivo e ⚠️ Suposição", st.emojis, (v) => setS("emojis", v), "Desligado: DESTRUTIVO: / Suposição: sem emoji.")) }),
          card({ title: "Instruções da organização", subtitle: "Markdown acrescentado às duas personas (padrões internos, nomenclatura, ferramentas aprovadas...).",
            body: mdEditor(d.global_instructions, (v) => { d.global_instructions = v; ai.change(); }, { rows: 8,
              placeholder: "## Padrões da ACME\n- Use OpenTofu (não Terraform)\n- Região padrão: sa-east-1\n- Tags obrigatórias: `owner`, `cost-center`" }) }))),
      card({ title: "Limites", subtitle: "Proteções de uso e custo. Valem para o chat, a gaveta do assistente e o laboratório.", body: h("div", { class: "form-grid three" },
        numField("Máx. tokens de saída (global)", l.max_output_tokens, { min: 0, max: 200000, hint: "0 = sem limite (padrão). Cada persona pode ter o seu." }, (v) => setL("max_output_tokens", v)),
        numField("Tamanho máx. da pergunta (caracteres)", l.max_input_chars, { min: 100, max: 1048576, hint: "Padrão 32768." }, (v) => setL("max_input_chars", v)),
        numField("Tamanho máx. do anexo (caracteres)", l.max_attach_chars, { min: 0, max: 4194304, hint: "Padrão 262144." }, (v) => setL("max_attach_chars", v)),
        numField("Perguntas por usuário a cada 5 min", l.rate_per_5min, { min: 1, max: 1000, hint: "Padrão 30." }, (v) => setL("rate_per_5min", v)),
        numField("Tempo limite da resposta (s)", l.timeout_seconds, { min: 10, max: 3600, hint: "Padrão 600." }, (v) => setL("timeout_seconds", v)),
        numField("Memória da conversa (mensagens)", l.history_messages, { min: 0, max: 100, hint: "Pergunta+resposta guardadas por persona. Padrão 10; 0 = sem memória." }, (v) => setL("history_messages", v)),
        numField("Tamanho máx. do resultado de ferramenta", l.tool_result_chars, { min: 500, max: 200000, hint: "Caracteres repassados ao modelo por chamada MCP. Padrão 8000." }, (v) => setL("tool_result_chars", v))) })));
  };
  draw();
  const off = ai.on((ev) => { if (!view.isConnected) return off(); if (ev !== "reload") draw(); });
}
