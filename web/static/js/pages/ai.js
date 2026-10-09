import { get, post, streamNDJSON } from "../api.js";
import { h, mount, clear, icon, btn, badge, card, markdown, toast, toastErr, pageHead } from "../ui.js";

const PERSONAS = {
  cloud_devops: { name: "Atlas", cls: "atlas", initials: "AT", area: "Cloud & DevOps" },
  redes_seguranca: { name: "Sentinela", cls: "sentinela", initials: "SE", area: "Redes & Segurança" },
  fora_de_escopo: { name: "Fora de escopo", cls: "refusal", initials: "—", area: "" },
};

// Configuração efetiva (carregada uma vez): só altera textos quando o administrador
// personalizou os agentes; no padrão, o chat fica exatamente como sempre foi.
let statusPromise = null;
export function aiStatus(refresh) {
  if (!statusPromise || refresh) statusPromise = get("/api/ai/status").catch(() => null);
  return statusPromise;
}

function providerSentence(s) {
  if (!s || !s.agents) return "O modelo roda localmente no Ollama.";
  const a = s.agents.cloud_devops;
  const b = s.agents.redes_seguranca;
  const local = (x) => x.kind === "ollama" && !x.external;
  if (local(a) && local(b)) return "O modelo roda localmente no Ollama.";
  const desc = (x) => (x.kind === "foundry_agent" ? `o agente do Microsoft Foundry "${x.connection}"` : `"${x.connection}"`);
  if (a.connection === b.connection) return `Os agentes usam ${desc(a)}.`;
  return `Atlas usa ${desc(a)} e Sentinela usa ${desc(b)}.`;
}

export function chatPanel({ compact = false } = {}) {
  const log = h("div", { class: "chat-log", "aria-live": "polite" });
  const ta = h("textarea", { rows: 2, placeholder: "Pergunte sobre Docker, OpenTofu, Kubernetes, AWS/OCI, sub-redes, firewall, Trivy... (Enter envia, Shift+Enter quebra linha)", "aria-label": "Mensagem" });
  const attach = h("div", { class: "row hidden", style: { padding: "0 14px 0" } });
  let context = null;
  let ctrl = null;
  const sendBtn = h("button", { class: "btn primary", type: "button", "aria-label": "Enviar" }, icon("right", "sm"));
  const stopBtn = h("button", { class: "btn hidden", type: "button", "aria-label": "Parar" }, icon("stop", "sm"));
  const welcomeText = (s) => {
    const refusal = s && s.scope_refusal === false ? "" : `Perguntas fora desses domínios recebem: \`${(s && s.refusal) || "Não posso responder a este tipo de pergunta"}\`. `;
    return "Olá! Sou o assistente do **doomctl**. As perguntas são roteadas automaticamente:\n\n" +
      "- **Atlas** — Cloud & DevOps (Docker, OpenTofu/Terraform, Ansible, Kubernetes, AWS, OCI, FinOps)\n" +
      "- **Sentinela** — Redes & Segurança (sub-redes, firewall, NAT, roteamento, container security)\n\n" +
      refusal + providerSentence(s);
  };
  let welcomeEl = null;
  const welcome = () => {
    clear(log);
    welcomeEl = msg("system", "doomctl", markdown(welcomeText(null)));
    log.appendChild(welcomeEl);
    aiStatus(true).then((s) => {
      if (!s || !welcomeEl || !welcomeEl.isConnected) return;
      const t = welcomeText(s);
      if (t !== welcomeText(null)) mount(welcomeEl.querySelector(".bubble"), h("div", { class: "persona" }, "doomctl"), markdown(t));
    });
  };
  const setAttach = () => {
    clear(attach);
    if (!context || !context.content) { attach.classList.add("hidden"); return; }
    attach.classList.remove("hidden");
    attach.append(badge(`anexo: ${context.filename || "conteúdo"} (${context.content.length} caracteres)`, "info"),
      h("button", { class: "btn xs ghost", type: "button", onclick: () => { context = null; setAttach(); } }, icon("x", "sm"), "remover"));
  };
  const send = async () => {
    const text = ta.value.trim();
    if (!text || ctrl) return;
    ta.value = "";
    log.appendChild(msg("user", "Você", h("div", { style: { whiteSpace: "pre-wrap" }, text })));
    const body = h("div", { class: "typing" }, h("i"), h("i"), h("i"));
    const label = h("span", { text: "roteando..." });
    const m = msg("system", label, body);
    log.appendChild(m);
    log.scrollTop = log.scrollHeight;
    ctrl = new AbortController();
    sendBtn.classList.add("hidden"); stopBtn.classList.remove("hidden");
    let acc = "";
    let raf = 0;
    const paint = () => { raf = 0; mount(body, markdown(acc)); const near = log.scrollHeight - log.scrollTop - log.clientHeight < 120; if (near) log.scrollTop = log.scrollHeight; };
    const payload = { message: text, context: context ? { module: context.module || "", filename: context.filename || "", content: context.content || "" } : {} };
    context = null; setAttach();
    try {
      await streamNDJSON("/api/ai/chat", payload, (ev) => {
        if (ev.type === "meta") {
          const p = PERSONAS[ev.route] || PERSONAS.fora_de_escopo;
          m.className = "msg " + p.cls;
          m.querySelector(".who").textContent = p.initials;
          mount(label, p.name, p.area ? badge(p.area, p.cls === "atlas" ? "info" : "violet") : null, ev.used_llm ? badge("classificador LLM", "outline") : null);
        } else if (ev.type === "chunk") {
          acc += ev.text;
          if (!raf) raf = requestAnimationFrame(paint);
        } else if (ev.type === "refusal") {
          acc = ev.text; paint();
        } else if (ev.type === "tool" && ev.tool && ev.tool.status !== "start") {
          label.appendChild(badge(`ferramenta: ${ev.tool.tool}`, ev.tool.status === "ok" ? "outline" : "err"));
        } else if (ev.type === "notice") {
          toast(ev.text);
        } else if (ev.type === "error") {
          mount(body, h("div", { class: "alert err", style: { margin: 0 } }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: ev.error }),
            h("p", { text: ev.hint || "Verifique se o Ollama está no ar e se o modelo configurado foi baixado (ollama pull)." }))));
        }
      }, ctrl.signal);
      if (acc) paint();
    } catch (e) {
      if (e.name !== "AbortError") { mount(body, h("p", { class: "muted", text: "Erro: " + e.message })); toastErr(e); }
      else { acc += "\n\n_(interrompido)_"; paint(); }
    } finally {
      ctrl = null;
      sendBtn.classList.remove("hidden"); stopBtn.classList.add("hidden");
      ta.focus();
    }
  };
  sendBtn.addEventListener("click", send);
  stopBtn.addEventListener("click", () => ctrl && ctrl.abort());
  ta.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } });
  ta.addEventListener("input", () => { ta.style.height = "auto"; ta.style.height = Math.min(200, ta.scrollHeight) + "px"; });
  welcome();
  const el = h("div", { class: "chat" + (compact ? " compact" : "") }, log, attach,
    h("div", { class: "chat-input" }, h("div", { class: "grow" }, ta), sendBtn, stopBtn));
  return {
    el,
    focus: () => ta.focus(),
    reset: async () => { await post("/api/ai/reset", {}); welcome(); toast("Conversa reiniciada", "ok"); },
    setContext: (c) => {
      context = c && c.content ? c : null;
      setAttach();
      if (c && c.prompt) ta.value = c.prompt;
      if (c && c.send) send();
    },
  };
}

function msg(kind, who, body) {
  const p = kind === "user" ? { initials: "EU" } : { initials: "AI" };
  return h("div", { class: "msg " + kind }, h("div", { class: "who", text: p.initials }),
    h("div", { class: "bubble" }, h("div", { class: "persona" }, who), body));
}

export async function render(root, ctx) {
  const panel = chatPanel();
  const status = h("div", { class: "row" }, badge("verificando Ollama...", "outline"));
  const isAdmin = ctx.me && ctx.me.user && ctx.me.user.role === "admin";
  mount(root,
    pageHead("Assistente IA", "Consultores locais (Ollama): Atlas para Cloud & DevOps e Sentinela para Redes & Segurança.",
      [isAdmin ? btn("Configurar", { icon: "settings", onClick: () => ctx.navigate("settings/agentes") }) : null,
        btn("Nova conversa", { icon: "refresh", onClick: () => panel.reset() })], ["Operação", status]),
    h("section", { class: "card" }, panel.el));
  panel.focus();
  const s = await aiStatus();
  if (!s) return;
  const o = s.ollama;
  if (!s.custom) {
    mount(status, o.online ? badge("Ollama online", "ok", true) : badge("Ollama offline", "err", true),
      badge(`modelo: ${o.model}`, o.model_found ? "info" : "warn"), badge(`temperatura ${s.temperature}`, "outline"));
    if (o.online && !o.model_found) toast(`O modelo "${o.model}" não foi encontrado no Ollama. Rode: ollama pull ${o.model}`, "err");
    return;
  }
  // agentes personalizados: mostra o que cada persona usa
  const a = s.agents.cloud_devops;
  const b = s.agents.redes_seguranca;
  const sub = root.querySelector(".page-head p");
  if (sub && [a, b].some((x) => x.kind !== "ollama" || x.external)) sub.textContent = "Consultores de IA: Atlas para Cloud & DevOps e Sentinela para Redes & Segurança.";
  const usesDefault = [a, b].some((x) => x.connection === "Ollama padrão");
  const who = (name, x) => badge(`${name}: ${x.kind === "foundry_agent" ? x.connection : (x.model || x.connection)}`, x.external ? "warn" : "info");
  mount(status, usesDefault ? (o.online ? badge("Ollama online", "ok", true) : badge("Ollama offline", "err", true)) : null,
    who("Atlas", a), who("Sentinela", b),
    s.uniform_temperature ? badge(`temperatura ${s.temperature}`, "outline") : badge(`temperatura ${a.temperature} / ${b.temperature}`, "outline"));
}
