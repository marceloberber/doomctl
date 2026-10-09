// Aba "Laboratório & histórico": testa o rascunho (sem salvar), compara com a configuração
// salva, versões da configuração, uso por persona/conexão e exportação/importação.
import { get, post, streamNDJSON, download } from "../../api.js";
import { h, mount, icon, btn, badge, card, table, markdown, toast, toastErr, confirmDialog, fmtDate, fmtNum, filePicker } from "../../ui.js";
import { segmented, toggle, clone } from "./ai_common.js";

// add: como Element.append, mas ignora null/false e achata listas (igual ao h()).
const add = (el, ...k) => { el.append(...k.flat(Infinity).filter((x) => x !== null && x !== undefined && x !== false)); return el; };

const REASON = { heuristica: "palavras-chave", "classificador llm": "classificador LLM", "persona escolhida no teste": "persona escolhida" };
const PERSONA_NAME = { cloud_devops: "Atlas", redes_seguranca: "Sentinela", fora_de_escopo: "Fora de escopo" };

export async function render(root, ctx) {
  const ai = ctx.ai;
  const view = h("div", { class: "stack" });
  root.appendChild(view);
  view.append(playground(ai), h("div", { class: "grid g2" }, historyCard(ai), usageCard()), backupCard(ai));
}

function playground(ai) {
  let route = "auto";
  let compare = false;
  const ta = h("textarea", { rows: 3, placeholder: "Pergunta de teste — ex.: Como exponho um Deployment com Ingress e TLS?", "aria-label": "Pergunta de teste" });
  const out = h("div", { class: "grid g2 ai-compare" });
  let ctrls = [];
  const run = async () => {
    const message = ta.value.trim();
    if (!message) return toast("Escreva uma pergunta", "err");
    ctrls.forEach((c) => c.abort());
    ctrls = [];
    const panes = [{ title: ai.dirty ? "Rascunho (não salvo)" : "Configuração atual", settings: clone(ai.draft) }];
    if (compare) panes.push({ title: "Configuração salva", settings: clone(ai.cfg.settings) });
    out.classList.toggle("g2", panes.length > 1);
    mount(out, panes.map((p) => runPane(p, route, message, ctrls)));
  };
  const runBtn = btn("Executar", { icon: "play", cls: "primary", onClick: run });
  ta.addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); run(); } });
  return card({ title: "Laboratório", subtitle: "Teste temperatura, tom, prompts, conexões e ferramentas antes de salvar. Usa o rascunho das outras abas; nada é gravado e a conversa dos usuários não é afetada.",
    body: h("div", { class: "stack" },
      h("div", { class: "spread" },
        segmented([["auto", "Roteamento automático"], ["cloud_devops", "Atlas"], ["redes_seguranca", "Sentinela"]], route, (v) => { route = v; }, "Persona"),
        toggle("Comparar com a configuração salva", compare, (v) => { compare = v; })),
      h("div", { class: "chat-input ai-lab-input" }, h("div", { class: "grow" }, ta), runBtn),
      h("p", { class: "hint", text: "Ctrl+Enter executa. Cada teste conta no limite de perguntas e fica registrado em Logs (módulo ai)." }),
      out) });
}

function runPane(p, route, message, ctrls) {
  const body = h("div", { class: "md ai-lab-out" }, h("div", { class: "typing" }, h("i"), h("i"), h("i")));
  const head = h("div", { class: "row" }, h("strong", { text: p.title }));
  const meta = h("div", { class: "row small muted" });
  const extra = h("div", { class: "stack sm" });
  const pane = h("div", { class: "ai-pane" }, head, meta, body, extra);
  const ctrl = new AbortController();
  ctrls.push(ctrl);
  let acc = "";
  let raf = 0;
  const paint = () => { raf = 0; mount(body, markdown(acc)); };
  const tools = [];
  (async () => {
    try {
      await streamNDJSON("/api/ai/playground", { settings: p.settings, route, message }, (ev) => {
        if (ev.type === "meta") {
          add(head, badge(PERSONA_NAME[ev.route] || ev.route, ev.route === "cloud_devops" ? "info" : ev.route === "redes_seguranca" ? "violet" : "outline"),
            ev.used_llm ? badge("classificador LLM", "outline") : null);
          add(meta, h("span", { text: REASON[ev.reason] || ev.reason }));
        } else if (ev.type === "chunk") { acc += ev.text; if (!raf) raf = requestAnimationFrame(paint); }
        else if (ev.type === "refusal") { acc = ev.text; paint(); }
        else if (ev.type === "notice") { extra.append(h("div", { class: "small", style: { color: "var(--warn)" } }, icon("alert", "sm"), " ", ev.text)); }
        else if (ev.type === "tool" && ev.tool.status !== "start") { tools.push(ev.tool); }
        else if (ev.type === "error") { mount(body, h("div", { class: "alert err", style: { margin: 0 } }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: ev.error })))); acc = ""; }
        else if (ev.type === "stats") {
          const u = ev.usage || {};
          add(meta, h("span", { text: "·" }), h("span", { text: `${(ev.duration_ms / 1000).toFixed(1)} s` }),
            ev.connection ? [h("span", { text: "·" }), h("span", null, ev.connection, ev.model ? [" · ", h("code", { text: ev.model })] : null)] : null,
            u.output_tokens ? [h("span", { text: "·" }), h("span", { text: `${fmtNum(u.prompt_tokens)} → ${fmtNum(u.output_tokens)} tokens` })] : null);
          if (ev.redacted && ev.redacted.length) extra.append(h("div", { class: "small muted" }, icon("lock", "sm"), " Redigido antes do envio: ", ev.redacted.join(", ")));
          if (tools.length) extra.append(h("div", { class: "row small" }, icon("bolt", "sm"), tools.map((t) => badge(`${t.server}/${t.tool} · ${t.duration_ms} ms`, t.status === "ok" ? "ok" : "err"))));
          if (ev.system_prompt) extra.append(h("details", { class: "ai-details" }, h("summary", { text: "System prompt usado" }), h("pre", { class: "mono small ai-out", text: ev.system_prompt })));
        }
      }, ctrl.signal);
      if (acc) paint();
    } catch (e) {
      if (e.name !== "AbortError") mount(body, h("div", { class: "alert err", style: { margin: 0 } }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: e.message }))));
    }
  })();
  return pane;
}

function historyCard(ai) {
  const box = h("div");
  const load = async () => {
    let rows;
    try { rows = await get("/api/ai/config/history"); } catch (e) { return toastErr(e); }
    mount(box, table([
      { label: "Versão", render: (r) => h("span", { class: "mono", text: "#" + r.id }) },
      { label: "Nota", render: (r) => h("span", { text: r.note || "—" }) },
      { label: "Por", render: (r) => h("span", { class: "small", text: r.created_by }) },
      { label: "Quando", render: (r) => h("span", { class: "small muted", text: fmtDate(r.created_at) }) },
      { label: "", cls: "right", render: (r, i) => btn("Restaurar", { icon: "refresh", cls: "xs", onClick: async () => {
        if (ai.dirty && !await confirmDialog({ title: "Descartar rascunho?", message: "Há alterações não salvas que serão descartadas.", confirmText: "Continuar" })) return;
        if (!await confirmDialog({ title: `Restaurar a versão #${r.id}`, message: "A configuração atual é substituída (e guardada como nova versão no histórico).", confirmText: "Restaurar" })) return;
        ai.apply(await post(`/api/ai/config/history/${r.id}/restore`, {}));
        toast(`Versão #${r.id} restaurada`, "ok"); load();
      } }) },
    ], rows, { empty: h("p", { class: "muted small ai-pad", text: "Nenhuma versão salva ainda. Cada \"Salvar e aplicar\" cria uma versão." }) }));
  };
  load();
  const off = ai.on((ev) => { if (!box.isConnected) return off(); if (ev === "saved") load(); });
  return card({ title: "Versões da configuração", subtitle: "As 50 mais recentes. Restaurar é instantâneo.", flush: true, body: box });
}

function usageCard() {
  const box = h("div", { class: "stack" });
  let days = 30;
  const load = async () => {
    let u;
    try { u = await get(`/api/ai/usage?days=${days}`); } catch (e) { return toastErr(e); }
    const total = u.by_route.reduce((s, x) => s + x.count, 0);
    const tok = u.by_route.reduce((s, x) => s + x.tokens_in + x.tokens_out, 0);
    const failed = u.by_route.reduce((s, x) => s + x.failed, 0);
    const cols = (label) => [
      { label, render: (x) => h("span", { text: label === "Persona" ? (PERSONA_NAME[x.key] || x.key || "—") : (x.key || "—") }) },
      { label: "Perguntas", cls: "right", render: (x) => h("span", { class: "mono", text: fmtNum(x.count) }) },
      { label: "Falhas", cls: "right", render: (x) => x.failed ? badge(fmtNum(x.failed), "err") : h("span", { class: "faint", text: "0" }) },
      { label: "Tempo médio", cls: "right", render: (x) => h("span", { class: "mono small", text: `${(x.avg_ms / 1000).toFixed(1)} s` }) },
      { label: "Tokens", cls: "right", render: (x) => h("span", { class: "mono small", text: x.tokens_in + x.tokens_out ? fmtNum(x.tokens_in + x.tokens_out) : "—" }) },
    ];
    const max = Math.max(1, ...u.daily.map((d) => d.count));
    mount(box,
      h("div", { class: "stat-row" }, stat("Perguntas", fmtNum(total)), stat("Falhas", fmtNum(failed)), stat("Tokens", tok ? fmtNum(tok) : "—")),
      u.daily.length ? h("div", { class: "ai-spark", title: "Perguntas por dia" }, u.daily.map((d) => h("i", { title: `${d.day}: ${d.count}`, style: { height: Math.max(4, (d.count / max) * 100) + "%" } }))) : null,
      table(cols("Persona"), u.by_route, { empty: h("p", { class: "muted small", text: "Sem perguntas no período." }) }),
      u.by_connection.length ? table(cols("Conexão"), u.by_connection) : null,
      u.by_user.length ? table(cols("Usuário"), u.by_user.slice(0, 10)) : null);
  };
  load();
  return card({ title: "Uso do assistente", subtitle: "Perguntas do chat e da gaveta (o laboratório não entra).",
    actions: [segmented([[7, "7 dias"], [30, "30 dias"], [90, "90 dias"]], days, (v) => { days = v; load(); }, "Período")], body: box });
}

function stat(l, n) { return h("div", { class: "stat" }, h("div", { class: "n", text: n }), h("div", { class: "l", text: l })); }

function backupCard(ai) {
  return card({ title: "Exportar / importar", subtitle: "JSON com personas, guardrails, estilo, limites e GPU. Conexões e servidores MCP vão sem segredos (para referência).",
    body: h("div", { class: "row" },
      btn("Exportar configuração", { icon: "download", onClick: () => download("/api/ai/config/export", undefined, "doomctl-ai-config.json") }),
      btn("Importar configuração", { icon: "upload", onClick: () => filePicker(".json,application/json", async (text) => {
        let data;
        try { data = JSON.parse(text); } catch (_) { return toast("JSON inválido", "err"); }
        if (!await confirmDialog({ title: "Importar configuração", message: "Substitui as configurações dos agentes (conexões e MCP inexistentes neste servidor voltam para o Ollama padrão). A versão atual fica no histórico.", confirmText: "Importar" })) return;
        try { ai.apply(await post("/api/ai/config/import", data)); toast("Configuração importada", "ok"); } catch (e) { toastErr(e); }
      }) })) });
}
