// Estado compartilhado das abas de IA: configuração carregada, rascunho editável e barra de salvar.
import { get, post, put } from "../../api.js";
import { h, mount, clear, icon, btn, badge, modal, markdown, toast, toastErr, select, checkbox } from "../../ui.js";

export const PERSONAS = [
  { id: "cloud_devops", name: "Atlas", area: "Cloud & DevOps", cls: "info" },
  { id: "redes_seguranca", name: "Sentinela", area: "Redes & Segurança", cls: "violet" },
];

export const KIND_LABEL = { ollama: "Ollama", openai: "API compatível com OpenAI", foundry_agent: "Agente do Microsoft Foundry" };

export const clone = (x) => JSON.parse(JSON.stringify(x));

export function createAIState() {
  const st = { cfg: null, draft: null, dirty: false, tab: null, subs: new Set() };
  const note = h("input", { type: "text", placeholder: "nota desta versão (opcional)", maxlength: 200, "aria-label": "Nota da versão" });
  const unload = (e) => { if (st.dirty) { e.preventDefault(); e.returnValue = ""; } };
  window.addEventListener("beforeunload", unload);

  st.saveBar = h("div", { class: "ai-savebar hidden", role: "region", "aria-label": "Alterações não salvas" },
    h("div", { class: "row nw" }, h("span", { class: "dot-pulse" }), h("strong", { text: "Alterações não salvas" }),
      h("span", { class: "muted small hide-sm", text: "Valem para todos os usuários assim que salvas." })),
    h("div", { class: "row" }, note,
      btn("Ver prompt final", { icon: "eye", cls: "sm", onClick: () => previewPrompts(st.draft) }),
      btn("Descartar", { icon: "x", cls: "sm ghost", onClick: () => st.discard() }),
      btn("Salvar e aplicar", { icon: "save", cls: "sm primary", onClick: async () => { await st.save(note.value); note.value = ""; } })));

  const update = () => st.saveBar.classList.toggle("hidden", !(st.dirty && st.tab && st.tab.ai));
  st.setTab = (t) => { st.tab = t; update(); };
  st.on = (fn) => { st.subs.add(fn); return () => st.subs.delete(fn); };
  const emit = (ev) => st.subs.forEach((fn) => { try { fn(ev); } catch (e) { console.error(e); } });

  st.ensure = async () => {
    if (!st.cfg) { st.cfg = await get("/api/ai/config"); st.draft = clone(st.cfg.settings); }
    return st.cfg;
  };
  // reload: recarrega do servidor (mantém o rascunho se houver alterações pendentes)
  st.reload = async () => {
    st.cfg = await get("/api/ai/config");
    if (!st.dirty) st.draft = clone(st.cfg.settings);
    emit("reload");
    return st.cfg;
  };
  st.apply = (cfg) => {
    st.cfg = cfg; st.draft = clone(cfg.settings); st.dirty = false; update();
    (cfg.warnings || []).forEach((w) => toast(w, ""));
    emit("saved");
  };
  st.change = () => { st.dirty = true; update(); };
  st.save = async (n) => {
    const r = await put("/api/ai/config", { settings: st.draft, note: n || "" });
    st.apply(r);
    toast("Configuração salva e aplicada", "ok");
  };
  st.discard = () => { st.draft = clone(st.cfg.settings); st.dirty = false; update(); emit("discard"); };
  st.destroy = () => { window.removeEventListener("beforeunload", unload); st.subs.clear(); };
  return st;
}

// ---------- controles ----------

export function sliderField(label, value, { min, max, step, hint, decimals = 2 }, onChange) {
  const num = h("input", { type: "number", min, max, step, value, class: "mono", "aria-label": label });
  const rng = h("input", { type: "range", min, max, step, value, "aria-label": label });
  const set = (v, from) => {
    let n = Number(v);
    if (Number.isNaN(n)) return;
    if (from !== num) num.value = n.toFixed(decimals);
    if (from !== rng) rng.value = n;
    onChange(Number(n.toFixed(decimals)));
  };
  rng.addEventListener("input", () => set(rng.value, rng));
  num.addEventListener("change", () => set(num.value, num));
  num.value = Number(value).toFixed(decimals);
  const el = h("label", { class: "field" }, h("span", { class: "lbl", text: label }), h("div", { class: "ai-range" }, rng, num), hint ? h("span", { class: "hint", text: hint }) : null);
  el.setBounds = (mn, mx) => { rng.min = num.min = mn; rng.max = num.max = mx; };
  return el;
}

export function numField(label, value, { min = 0, max, step = 1, hint, placeholder } = {}, onChange) {
  const inp = h("input", { type: "number", min, max, step, value, placeholder, class: "mono" });
  inp.addEventListener("change", () => onChange(inp.value === "" ? 0 : Number(inp.value)));
  return h("label", { class: "field" }, h("span", { class: "lbl", text: label }), inp, hint ? h("span", { class: "hint", text: hint }) : null);
}

export function textField(label, value, { hint, placeholder, list, mono, maxlength } = {}, onChange) {
  const inp = h("input", { type: "text", value: value || "", placeholder, list, class: mono ? "mono" : "", maxlength, autocomplete: "off" });
  inp.addEventListener("input", () => onChange(inp.value));
  const el = h("label", { class: "field" }, h("span", { class: "lbl", text: label }), inp, hint ? h("span", { class: "hint", text: hint }) : null);
  el.input = inp;
  return el;
}

export function selectField(label, options, value, onChange, hint) {
  const s = select(options, value);
  s.addEventListener("change", () => onChange(s.value));
  const el = h("label", { class: "field" }, h("span", { class: "lbl", text: label }), s, hint ? h("span", { class: "hint", text: hint }) : null);
  el.select = s;
  return el;
}

export function toggle(label, checked, onChange, hint) {
  const c = checkbox(label, checked);
  c.input.addEventListener("change", () => onChange(c.input.checked));
  return h("div", { class: "ai-toggle" }, c, hint ? h("span", { class: "hint", text: hint }) : null);
}

export function segmented(options, value, onChange, label) {
  const box = h("div", { class: "seg", role: "radiogroup", "aria-label": label || "" });
  for (const [v, l] of options) {
    const b = h("button", { type: "button", class: v === value ? "active" : "", role: "radio", "aria-checked": String(v === value) }, l);
    b.addEventListener("click", () => {
      box.querySelectorAll("button").forEach((x) => { x.classList.remove("active"); x.setAttribute("aria-checked", "false"); });
      b.classList.add("active"); b.setAttribute("aria-checked", "true");
      onChange(v);
    });
    box.appendChild(b);
  }
  return box;
}

// mdEditor: textarea com abas Editar / Pré-visualizar (Markdown).
export function mdEditor(value, onChange, { rows = 8, placeholder = "" } = {}) {
  const ta = h("textarea", { rows, placeholder, class: "mono", spellcheck: "false" });
  ta.value = value || "";
  const prev = h("div", { class: "md ai-md-preview hidden" });
  const tabsEl = segmented([["edit", "Editar"], ["preview", "Pré-visualizar"]], "edit", (v) => {
    const p = v === "preview";
    ta.classList.toggle("hidden", p); prev.classList.toggle("hidden", !p);
    if (p) mount(prev, ta.value.trim() ? markdown(ta.value) : h("p", { class: "muted", text: "Nada para pré-visualizar." }));
  }, "Modo do editor");
  ta.addEventListener("input", () => onChange(ta.value));
  const count = h("span", { class: "hint" });
  const upd = () => { count.textContent = `${ta.value.length.toLocaleString("pt-BR")} caracteres · Markdown`; };
  ta.addEventListener("input", upd); upd();
  const el = h("div", { class: "ai-md" }, h("div", { class: "spread" }, tabsEl, count), ta, prev);
  el.setValue = (v) => { ta.value = v || ""; upd(); };
  return el;
}

// chipsInput: lista de termos (Enter ou vírgula adiciona).
export function chipsInput(values, onChange, placeholder = "digite e tecle Enter") {
  const list = [...(values || [])];
  const chips = h("div", { class: "ai-chips" });
  const inp = h("input", { type: "text", placeholder, "aria-label": placeholder });
  const draw = () => {
    clear(chips);
    list.forEach((v, i) => chips.appendChild(h("span", { class: "ai-chip" }, v,
      h("button", { type: "button", "aria-label": "remover " + v, onclick: () => { list.splice(i, 1); draw(); onChange([...list]); } }, icon("x", "sm")))));
    chips.appendChild(inp);
  };
  const add = () => {
    const parts = inp.value.split(",").map((x) => x.trim()).filter(Boolean);
    let changed = false;
    for (const p of parts) if (!list.includes(p)) { list.push(p); changed = true; }
    inp.value = "";
    if (changed) { draw(); onChange([...list]); inp.focus(); }
  };
  inp.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === ",") { e.preventDefault(); add(); }
    else if (e.key === "Backspace" && !inp.value && list.length) { list.pop(); draw(); onChange([...list]); inp.focus(); }
  });
  inp.addEventListener("blur", add);
  chips.addEventListener("click", (e) => { if (e.target === chips) inp.focus(); });
  draw();
  return chips;
}

export function fieldset(title, ...children) {
  return h("div", { class: "fieldset" }, h("div", { class: "legend" }, title), ...children);
}

// connectionOptions: [0 = Ollama padrão] + conexões salvas.
export function connectionOptions(cfg, { noFoundry = false } = {}) {
  const opts = [[0, `Ollama padrão (.env) — ${cfg.env.ollama_model}`]];
  for (const c of cfg.connections) {
    if (noFoundry && c.kind === "foundry_agent") continue;
    opts.push([c.id, `${c.name} — ${KIND_LABEL[c.kind] || c.kind}${c.enabled ? "" : " (desativada)"}`]);
  }
  return opts;
}

export function connById(cfg, id) {
  if (!id) return { id: 0, name: "Ollama padrão", kind: "ollama", config: { model: cfg.env.ollama_model, base_url: cfg.env.ollama_host }, enabled: true };
  return cfg.connections.find((c) => c.id === id) || null;
}

export async function previewPrompts(settings) {
  let r;
  try { r = await post("/api/ai/config/preview", { settings }); } catch (e) { return toastErr(e); }
  const body = h("div", { class: "stack" },
    h("p", { class: "muted small", text: "System prompt efetivo de cada persona com as configurações atuais (incluindo alterações ainda não salvas). Agentes do Microsoft Foundry recebem apenas as regras e o estilo, como instruções adicionais." }),
    ...PERSONAS.map((p) => {
      const x = r[p.id];
      const pre = h("pre", { class: "mono", text: x.prompt });
      return h("div", { class: "stack sm" },
        h("div", { class: "spread" }, h("div", { class: "row" }, h("strong", { text: p.name }), badge(p.area, p.cls),
          x.is_default ? badge("idêntico ao original (AGENTS.md)", "ok", true) : badge("personalizado", "warn", true)),
          h("span", { class: "small muted", text: `${x.chars.toLocaleString("pt-BR")} caracteres · ~${x.tokens_est.toLocaleString("pt-BR")} tokens` })),
        h("div", { class: "codebox" }, pre,
          h("button", { class: "btn xs", type: "button", onclick: () => { navigator.clipboard.writeText(x.prompt); toast("Copiado", "ok"); } }, icon("copy", "sm"), "copiar")));
    }));
  modal({ title: "Prompt final das personas", body, size: "xwide", actions: [{ label: "Fechar" }] });
}

export function fmtBytes(n) {
  if (!n) return "—";
  const u = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${u[i]}`;
}

export function statusPill(status, err) {
  if (status === "ok") return badge("ok", "ok", true);
  if (status === "error") { const b = badge("erro", "err", true); if (err) b.title = err; return b; }
  return badge("não testado", "outline");
}

export function warningsBox(list) {
  if (!list || !list.length) return null;
  return h("div", { class: "alert warn" }, h("div", { class: "alert-icon" }, icon("alert")),
    h("div", null, h("strong", { text: "Atenção na configuração atual" }), h("ul", { class: "ai-ul" }, list.map((w) => h("li", { text: w })))));
}

export { mount, clear };
