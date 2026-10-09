// Componentes de interface do doomctl (DOM puro; textos de usuário sempre via textContent).
import { streamJob, post } from "./api.js";

// ---------- criação de elementos ----------
export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === "class") el.className = v;
      else if (k === "text") el.textContent = v;
      else if (k === "style" && typeof v === "object") Object.assign(el.style, v);
      else if (k === "dataset") Object.assign(el.dataset, v);
      else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2).toLowerCase(), v);
      else if (k === "value") el.value = v;
      else if (k === "checked" || k === "disabled" || k === "selected" || k === "readOnly" || k === "multiple") el[k] = !!v;
      else el.setAttribute(k, v === true ? "" : v);
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

export function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }
export function mount(el, ...children) { clear(el); append(el, children); return el; }

// ---------- ícones (traços estilo heroicons, 24x24) ----------
const ICONS = {
  home: "M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6",
  server: "M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01",
  cube: "M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4",
  cloud: "M3 15a4 4 0 004 4h9a5 5 0 10-.1-9.999 5.002 5.002 0 10-9.78 2.096A4.001 4.001 0 003 15z",
  layers: "M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10",
  helm: "M12 2.5l8.2 4.75v9.5L12 21.5l-8.2-4.75v-9.5L12 2.5z M12 9a3 3 0 100 6 3 3 0 000-6z M12 9V5.5 M12 15v3.5 M9.4 10.5L6.4 8.8 M14.6 13.5l3 1.7 M9.4 13.5l-3 1.7 M14.6 10.5l3-1.7",
  settings: "M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z",
  dollar: "M12 8c-1.657 0-3 .895-3 2s1.343 2 3 2 3 .895 3 2-1.343 2-3 2m0-8c1.11 0 2.08.402 2.599 1M12 8V7m0 1v8m0 0v1m0-1c-1.11 0-2.08-.402-2.599-1M21 12a9 9 0 11-18 0 9 9 0 0118 0z",
  shield: "M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z",
  chart: "M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z",
  bolt: "M13 10V3L4 14h7v7l9-11h-7z",
  doc: "M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z",
  file: "M7 21h10a2 2 0 002-2V9.414a1 1 0 00-.293-.707l-5.414-5.414A1 1 0 0012.586 3H7a2 2 0 00-2 2v14a2 2 0 002 2z",
  sparkles: "M5 3v4M3 5h4M6 17v4m-2-2h4m5-16l2.286 6.857L21 12l-5.714 2.143L13 21l-2.286-6.857L5 12l5.714-2.143L13 3z",
  chat: "M8 10h.01M12 10h.01M16 10h.01M9 16H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-5l-5 5v-5z",
  puzzle: "M11 4a2 2 0 114 0v1a1 1 0 001 1h3a1 1 0 011 1v3a1 1 0 01-1 1h-1a2 2 0 100 4h1a1 1 0 011 1v3a1 1 0 01-1 1h-3a1 1 0 01-1-1v-1a2 2 0 10-4 0v1a1 1 0 01-1 1H7a1 1 0 01-1-1v-3a1 1 0 00-1-1H4a2 2 0 110-4h1a1 1 0 001-1V7a1 1 0 011-1h3a1 1 0 001-1V4z",
  users: "M12 4.354a4 4 0 110 5.292M15 21H3v-1a6 6 0 0112 0v1zm0 0h6v-1a6 6 0 00-9-5.197M13 7a4 4 0 11-8 0 4 4 0 018 0z",
  user: "M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z",
  logout: "M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1",
  moon: "M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z",
  sun: "M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z",
  bell: "M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9",
  search: "M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z",
  plus: "M12 4v16m8-8H4",
  play: "M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z M21 12a9 9 0 11-18 0 9 9 0 0118 0z",
  stop: "M21 12a9 9 0 11-18 0 9 9 0 0118 0z M9 10a1 1 0 011-1h4a1 1 0 011 1v4a1 1 0 01-1 1h-4a1 1 0 01-1-1v-4z",
  check: "M5 13l4 4L19 7",
  checkCircle: "M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z",
  x: "M6 18L18 6M6 6l12 12",
  trash: "M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16",
  edit: "M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z",
  download: "M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4",
  upload: "M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12",
  copy: "M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z",
  refresh: "M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15",
  terminal: "M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z",
  lock: "M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z",
  key: "M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z",
  menu: "M4 6h16M4 12h16M4 18h16",
  down: "M19 9l-7 7-7-7",
  right: "M9 5l7 7-7 7",
  left: "M15 19l-7-7 7-7",
  alert: "M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z",
  info: "M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z",
  external: "M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14",
  save: "M8 7H5a2 2 0 00-2 2v9a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-3m-1 4l-3 3m0 0l-3-3m3 3V4",
  calendar: "M8 7V3m8 4V3m-9 8h10M5 21h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z",
  calc: "M9 7h6m0 10v-3m-3 3h.01M9 17h.01M9 14h.01M12 14h.01M15 11h.01M12 11h.01M9 11h.01M7 21h10a2 2 0 002-2V5a2 2 0 00-2-2H7a2 2 0 00-2 2v14a2 2 0 002 2z",
  table: "M3 10h18M3 14h18m-9-4v8m-7 0h14a2 2 0 002-2V8a2 2 0 00-2-2H5a2 2 0 00-2 2v8a2 2 0 002 2z",
  hash: "M7 20l4-16m2 16l4-16M6 9h14M4 15h14",
  eye: "M15 12a3 3 0 11-6 0 3 3 0 016 0z M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z",
  activity: "M22 12h-4l-3 9L9 3l-3 9H2",
  template: "M4 5a1 1 0 011-1h14a1 1 0 011 1v2a1 1 0 01-1 1H5a1 1 0 01-1-1V5zM4 13a1 1 0 011-1h6a1 1 0 011 1v6a1 1 0 01-1 1H5a1 1 0 01-1-1v-6zM16 13a1 1 0 011-1h2a1 1 0 011 1v6a1 1 0 01-1 1h-2a1 1 0 01-1-1v-6z",
  grid: "M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zM14 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zM14 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z",
  network: "M9 3h6v5H9z M3 16h5v5H3z M16 16h5v5h-5z M12 8v4 M5.5 16v-4h13v4",
  clock: "M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z",
  rocket: "M5 19c1-3 3-5 5-6m-1.5 7.5L12 17l2.5 3.5M14 4c3 0 6 3 6 6l-6 6-6-6c0-3 3-6 6-6z M15 9a1 1 0 100 2 1 1 0 000-2z",
  filter: "M3 4a1 1 0 011-1h16a1 1 0 011 1v2.586a1 1 0 01-.293.707l-6.414 6.414a1 1 0 00-.293.707V17l-4 4v-6.586a1 1 0 00-.293-.707L3.293 7.293A1 1 0 013 6.586V4z",
  folder: "M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z",
  cpu: "M9 3v2m6-2v2M9 19v2m6-2v2M5 9H3m2 6H3m18-6h-2m2 6h-2M7 19h10a2 2 0 002-2V7a2 2 0 00-2-2H7a2 2 0 00-2 2v10a2 2 0 002 2zM9 9h6v6H9V9z",
};

const SVGNS = "http://www.w3.org/2000/svg";
export function icon(name, cls = "") {
  const svg = document.createElementNS(SVGNS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", ("i " + cls).trim());
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(SVGNS, "path");
  p.setAttribute("d", ICONS[name] || ICONS.info);
  svg.appendChild(p);
  return svg;
}

// Logo (paths gerados a partir da fonte VCR OSD) carregado uma vez e inline (herda currentColor).
let logoCache = null;
export async function logo(cls = "logo") {
  if (!logoCache) {
    const txt = await fetch("/img/logo.svg").then((r) => r.text());
    logoCache = new DOMParser().parseFromString(txt, "image/svg+xml").documentElement;
  }
  const el = document.importNode(logoCache, true);
  el.setAttribute("class", cls);
  return el;
}

// ---------- feedback ----------
let toastBox;
export function toast(msg, type = "") {
  if (!toastBox) { toastBox = h("div", { class: "toasts", role: "status", "aria-live": "polite" }); document.body.appendChild(toastBox); }
  const t = h("div", { class: "toast " + type }, icon(type === "err" ? "alert" : type === "ok" ? "checkCircle" : "info", "sm"), h("span", { text: msg }));
  toastBox.appendChild(t);
  setTimeout(() => t.remove(), type === "err" ? 7000 : 3800);
}
export const toastErr = (e) => toast(e && e.message ? e.message : String(e), "err");

export function modal({ title, body, actions = [], size = "", onClose }) {
  const back = h("div", { class: "modal-back" });
  const close = () => { back.remove(); document.removeEventListener("keydown", esc); onClose && onClose(); };
  const esc = (e) => { if (e.key === "Escape") close(); };
  document.addEventListener("keydown", esc);
  const foot = actions.length ? h("div", { class: "modal-foot" }) : null;
  for (const a of actions) {
    const b = h("button", { class: "btn " + (a.primary ? "primary" : a.danger ? "danger solid" : ""), type: "button" }, a.icon ? icon(a.icon, "sm") : null, a.label);
    b.addEventListener("click", async () => {
      if (!a.onClick) return close();
      b.disabled = true;
      try { const r = await a.onClick(); if (r !== false) close(); } catch (e) { toastErr(e); } finally { b.disabled = false; }
    });
    foot.appendChild(b);
  }
  const m = h("div", { class: "modal " + size, role: "dialog", "aria-modal": "true", "aria-label": title },
    h("div", { class: "modal-head" }, h("h3", { text: title }), h("button", { class: "icon-btn", type: "button", "aria-label": "Fechar", onclick: close }, icon("x"))),
    h("div", { class: "modal-body" }, body), foot);
  back.appendChild(m);
  back.addEventListener("mousedown", (e) => { if (e.target === back) close(); });
  document.body.appendChild(back);
  const first = m.querySelector("input, select, textarea");
  if (first) setTimeout(() => first.focus(), 30);
  return { close, el: m };
}

export function confirmDialog({ title = "Confirmar", message, danger = false, confirmText = "Confirmar", requireText }) {
  return new Promise((resolve) => {
    let done = false;
    const inp = requireText ? h("input", { type: "text", placeholder: requireText, autocomplete: "off" }) : null;
    const body = h("div", { class: "stack sm" },
      typeof message === "string" ? h("p", { text: message }) : message,
      requireText ? h("label", { class: "field" }, h("span", { class: "lbl" }, "Digite ", h("code", { text: requireText }), " para confirmar"), inp) : null);
    modal({
      title, body, onClose: () => { if (!done) resolve(false); },
      actions: [
        { label: "Cancelar" },
        { label: confirmText, danger, primary: !danger, onClick: () => {
          if (requireText && inp.value !== requireText) { toast("Texto de confirmação não confere", "err"); return false; }
          done = true; resolve(requireText ? inp.value : true);
        } },
      ],
    });
  });
}

export function promptPassword({ title = "Senha necessária", message, label = "Senha", confirmText = "Continuar" }) {
  return new Promise((resolve) => {
    let done = false;
    const inp = h("input", { type: "password", autocomplete: "off" });
    const submit = () => { done = true; resolve(inp.value); };
    const m = modal({
      title, onClose: () => { if (!done) resolve(null); },
      body: h("div", { class: "stack sm" }, message ? h("p", { class: "muted", text: message }) : null, field(label, inp)),
      actions: [{ label: "Cancelar" }, { label: confirmText, primary: true, icon: "key", onClick: () => { if (!inp.value) return false; submit(); } }],
    });
    inp.addEventListener("keydown", (e) => { if (e.key === "Enter" && inp.value) { submit(); m.close(); } });
  });
}

// ---------- formulários ----------
export function field(label, control, hint, cls = "") {
  return h("label", { class: "field " + cls }, h("span", { class: "lbl", text: label }), control, hint ? h("span", { class: "hint", text: hint }) : null);
}
export function input(props = {}) { return h("input", { type: "text", ...props }); }
export function select(options, value, props = {}) {
  const s = h("select", props);
  for (const o of options) {
    const [v, l] = Array.isArray(o) ? o : typeof o === "object" ? [o.value, o.label] : [o, o];
    s.appendChild(h("option", { value: v, selected: String(v) === String(value) }, l));
  }
  return s;
}
export function checkbox(label, checked, props = {}) {
  const cb = h("input", { type: "checkbox", checked, ...props });
  const el = h("label", { class: "check" }, cb, h("span", { text: label }));
  el.input = cb;
  return el;
}
export function formValues(root) {
  const out = {};
  root.querySelectorAll("[name]").forEach((el) => {
    if (el.type === "checkbox") out[el.name] = el.checked;
    else if (el.type === "number") out[el.name] = el.value === "" ? 0 : Number(el.value);
    else out[el.name] = el.value;
  });
  return out;
}

// ---------- tabelas, abas, badges ----------
export function badge(text, kind = "", dot = false) {
  return h("span", { class: "badge " + kind }, dot ? h("span", { class: "dot" }) : null, text);
}
const STATUS = {
  success: ["Sucesso", "ok"], failed: ["Falhou", "err"], running: ["Executando", "info"], queued: ["Na fila", "warn"],
  canceled: ["Cancelado", ""], interrupted: ["Interrompido", "warn"], info: ["Registro", "outline"], disconnected: ["Desconectado", "warn"],
};
export function statusBadge(s) { const [l, k] = STATUS[s] || [s, ""]; return badge(l, k, true); }

export function table(columns, rows, { onRowClick, empty } = {}) {
  if (!rows.length) return empty || emptyState("doc", "Nada por aqui ainda", "");
  const thead = h("thead", null, h("tr", null, columns.map((c) => h("th", { class: c.cls || "", text: c.label }))));
  const tbody = h("tbody");
  for (const r of rows) {
    const tr = h("tr", { class: onRowClick ? "click" : "" });
    for (const c of columns) {
      const v = c.render ? c.render(r) : r[c.key];
      tr.appendChild(h("td", { class: c.cls || "" }, v));
    }
    if (onRowClick) tr.addEventListener("click", (e) => { if (!e.target.closest("button, a, input, select")) onRowClick(r); });
    tbody.appendChild(tr);
  }
  return h("div", { class: "table-wrap" }, h("table", { class: "t" }, thead, tbody));
}

export function tabs(defs, active, onChange) {
  const bar = h("div", { class: "tabs", role: "tablist" });
  const buttons = {};
  for (const d of defs) {
    const b = h("button", { class: "tab" + (d.id === active ? " active" : ""), type: "button", role: "tab" }, d.icon ? icon(d.icon, "sm") : null, d.label,
      d.tag ? h("span", { class: "tag " + d.tag, text: d.tag === "beta" ? "beta" : d.tag }) : null);
    b.addEventListener("click", () => { Object.values(buttons).forEach((x) => x.classList.remove("active")); b.classList.add("active"); onChange(d.id); });
    buttons[d.id] = b;
    bar.appendChild(b);
  }
  return bar;
}

export function emptyState(ic, title, text, action) {
  return h("div", { class: "empty" }, h("div", { class: "ic" }, icon(ic, "lg")), h("b", { text: title }), text ? h("p", { text }) : null,
    action ? h("div", { style: { marginTop: "14px" } }, action) : null);
}
export function loading(text = "Carregando...") {
  return h("div", { class: "empty" }, h("div", { class: "ic" }, icon("refresh", "lg spin")), h("p", { text }));
}
export function card({ title, subtitle, actions, body, flush = false, foot, cls = "" }) {
  return h("section", { class: "card " + cls },
    title ? h("div", { class: "card-head" }, h("div", null, h("h2", { text: title }), subtitle ? h("p", { text: subtitle }) : null),
      actions ? h("div", { class: "row" }, actions) : null) : null,
    h("div", { class: "card-body" + (flush ? " flush" : "") }, body),
    foot ? h("div", { class: "card-foot" }, foot) : null);
}
export function pageHead(title, subtitle, actions, eyebrow) {
  return h("div", { class: "page-head" },
    h("div", null, eyebrow ? h("div", { class: "eyebrow" }, eyebrow) : null, h("h1", { text: title }), subtitle ? h("p", { text: subtitle }) : null),
    actions ? h("div", { class: "row" }, actions) : null);
}
export function btn(label, opts = {}) {
  const b = h("button", { class: "btn " + (opts.cls || ""), type: "button", title: opts.title, disabled: opts.disabled },
    opts.icon ? icon(opts.icon, "sm") : null, label ? h("span", { text: label }) : null);
  if (opts.onClick) {
    b.addEventListener("click", async (e) => {
      if (b.dataset.busy) return;
      b.dataset.busy = "1"; b.disabled = true;
      try { await opts.onClick(e); } catch (err) { toastErr(err); } finally { delete b.dataset.busy; b.disabled = !!opts.disabled; }
    });
  }
  return b;
}

// ---------- editor de código ----------
export function editor({ value = "", rows = 18, readOnly = false, placeholder = "", onInput } = {}) {
  const gutter = h("div", { class: "gutter", "aria-hidden": "true" });
  const ta = h("textarea", { spellcheck: "false", autocapitalize: "off", autocomplete: "off", rows, readOnly, placeholder });
  ta.value = value;
  const sync = () => {
    const n = ta.value.split("\n").length;
    let s = "";
    for (let i = 1; i <= n; i++) s += i + "\n";
    gutter.textContent = s;
    gutter.scrollTop = ta.scrollTop;
  };
  ta.addEventListener("input", () => { sync(); onInput && onInput(ta.value); });
  ta.addEventListener("scroll", () => { gutter.scrollTop = ta.scrollTop; });
  ta.addEventListener("keydown", (e) => {
    if (e.key === "Tab" && !readOnly) {
      e.preventDefault();
      const { selectionStart: a, selectionEnd: b } = ta;
      ta.setRangeText("  ", a, b, "end");
      sync();
    }
  });
  const el = h("div", { class: "editor", style: { height: rows * 19.4 + 24 + "px" } }, gutter, ta);
  sync();
  return {
    el, textarea: ta,
    get value() { return ta.value; },
    set value(v) { ta.value = v || ""; sync(); },
  };
}

// ---------- terminal / saída de jobs ----------
function classify(line) {
  if (/^━━/.test(line)) return "ln-head";
  if (/\b(fatal|FAILED|ERROR|error:|erro|unreachable=[1-9]|failed=[1-9]|CRITICAL)\b/i.test(line)) return "ln-err";
  if (/\b(WARNING|warn|changed:|HIGH)\b/.test(line)) return "ln-warn";
  if (/^(ok:|ok=|PLAY RECAP|Apply complete|Passed|No changes)/.test(line) || /\bsuccess\b/i.test(line)) return "ln-ok";
  if (/^\[doomctl\]/.test(line)) return "ln-dim";
  return "";
}

export function terminal({ title = "saída", prompt, placeholder = "", onCommand, height } = {}) {
  const pre = h("pre", { tabindex: "0" });
  if (height) pre.style.maxHeight = pre.style.minHeight = height;
  const status = h("span", { class: "small" });
  const head = h("div", { class: "term-head" }, h("span", { class: "lights" }, h("i"), h("i"), h("i")), h("span", { class: "grow", text: title }), status);
  const el = h("div", { class: "terminal" }, head, pre);
  let partial = "";
  const history = [];
  let hIdx = 0;
  const writeLine = (line) => {
    const cls = classify(line);
    pre.appendChild(cls ? h("span", { class: cls, text: line + "\n" }) : document.createTextNode(line + "\n"));
  };
  const api = {
    el,
    write(text) {
      const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
      const parts = (partial + text).split("\n");
      partial = parts.pop();
      parts.forEach(writeLine);
      if (atBottom) pre.scrollTop = pre.scrollHeight;
    },
    flush() { if (partial) { writeLine(partial); partial = ""; } pre.scrollTop = pre.scrollHeight; },
    clear() { clear(pre); partial = ""; },
    status(node) { mount(status, node); },
    text() { return pre.textContent + partial; },
  };
  if (onCommand) {
    const inp = h("input", { type: "text", placeholder, spellcheck: "false", autocomplete: "off", "aria-label": "comando" });
    inp.addEventListener("keydown", async (e) => {
      if (e.key === "Enter" && inp.value.trim()) {
        const cmd = inp.value.trim();
        history.push(cmd); hIdx = history.length;
        inp.value = "";
        api.write(`\n${prompt || "$"} ${cmd}\n`);
        try { await onCommand(cmd); } catch (err) { api.write(`[doomctl] ${err.message}\n`); }
      } else if (e.key === "ArrowUp" && hIdx > 0) { inp.value = history[--hIdx]; e.preventDefault(); }
      else if (e.key === "ArrowDown") { hIdx = Math.min(history.length, hIdx + 1); inp.value = history[hIdx] || ""; }
    });
    el.appendChild(h("div", { class: "term-input" }, h("span", { text: prompt || "$" }), inp));
    api.input = inp;
  }
  return api;
}

// runJob acompanha um job do backend no terminal. Resolve com o evento final.
export function runJob(res, term, { onEnd } = {}) {
  return new Promise((resolve) => {
    term.status(badge("executando", "info", true));
    let cancelBtn = null;
    if (res.job_id) {
      cancelBtn = h("button", { class: "btn xs ghost", type: "button", style: { color: "#fda4af" } }, "cancelar");
      cancelBtn.addEventListener("click", async () => {
        post(`/api/jobs/${res.job_id}/cancel`).catch(toastErr);
      });
      term.status([badge("executando", "info", true), " ", cancelBtn]);
    }
    streamJob(res.job_id, {
      onChunk: (t) => term.write(t),
      onEnd: (end) => {
        term.flush();
        term.status(statusBadge(end.status));
        if (end.status === "success") toast("Execução concluída", "ok");
        else if (end.status !== "disconnected") toast(`Execução terminou: ${STATUS[end.status] ? STATUS[end.status][0] : end.status}`, end.status === "canceled" ? "" : "err");
        onEnd && onEnd(end);
        resolve(end);
      },
    });
  });
}

// ---------- markdown seguro (respostas da IA) ----------
export function markdown(src, { onUseCode } = {}) {
  const root = h("div", { class: "md" });
  const lines = String(src || "").replace(/\r/g, "").split("\n");
  let i = 0;
  const inline = (text) => {
    const frag = document.createDocumentFragment();
    const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\((https?:\/\/[^)\s]+)\))/g;
    let last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) frag.appendChild(document.createTextNode(text.slice(last, m.index)));
      if (m[1]) frag.appendChild(h("code", { text: m[1].slice(1, -1) }));
      else if (m[2]) frag.appendChild(h("strong", { text: m[2].slice(2, -2) }));
      else if (m[3]) frag.appendChild(h("em", { text: m[3].slice(1, -1) }));
      else if (m[4]) frag.appendChild(h("a", { href: m[5], target: "_blank", rel: "noopener noreferrer", text: m[4].slice(1, m[4].indexOf("]")) }));
      last = re.lastIndex;
    }
    if (last < text.length) frag.appendChild(document.createTextNode(text.slice(last)));
    return frag;
  };
  while (i < lines.length) {
    const line = lines[i];
    const fence = /^```\s*([\w+-]*)/.exec(line);
    if (fence) {
      const lang = fence[1] || "";
      const buf = [];
      i++;
      while (i < lines.length && !/^```/.test(lines[i])) buf.push(lines[i++]);
      i++;
      const code = buf.join("\n");
      const actions = h("div", { class: "code-actions" },
        h("button", { type: "button", onclick: () => copyText(code) }, "copiar"),
        onUseCode ? h("button", { type: "button", onclick: () => onUseCode(code, lang) }, "usar no editor") : null);
      root.appendChild(h("pre", null, actions, h("code", { "data-lang": lang, text: code })));
      continue;
    }
    if (/^#{1,4}\s/.test(line)) { const lvl = line.match(/^#+/)[0].length; root.appendChild(h("h" + Math.min(lvl + 1, 4), null, inline(line.replace(/^#+\s/, "")))); i++; continue; }
    if (/^\s*[-*]\s+/.test(line)) {
      const ul = h("ul");
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) ul.appendChild(h("li", null, inline(lines[i++].replace(/^\s*[-*]\s+/, ""))));
      root.appendChild(ul); continue;
    }
    if (/^\s*\d+[.)]\s+/.test(line)) {
      const ol = h("ol");
      while (i < lines.length && /^\s*\d+[.)]\s+/.test(lines[i])) ol.appendChild(h("li", null, inline(lines[i++].replace(/^\s*\d+[.)]\s+/, ""))));
      root.appendChild(ol); continue;
    }
    if (/^\|.*\|\s*$/.test(line)) {
      const tbl = h("table");
      while (i < lines.length && /^\|.*\|\s*$/.test(lines[i])) {
        const cells = lines[i].trim().slice(1, -1).split("|").map((c) => c.trim());
        if (!cells.every((c) => /^:?-{2,}:?$/.test(c))) tbl.appendChild(h("tr", null, cells.map((c) => h("td", null, inline(c)))));
        i++;
      }
      root.appendChild(tbl); continue;
    }
    if (/^>\s?/.test(line)) { root.appendChild(h("blockquote", null, inline(line.replace(/^>\s?/, "")))); i++; continue; }
    if (!line.trim()) { i++; continue; }
    const para = [];
    while (i < lines.length && lines[i].trim() && !/^(```|#{1,4}\s|\s*[-*]\s+|\s*\d+[.)]\s+|\||>)/.test(lines[i])) para.push(lines[i++]);
    const p = h("p");
    para.forEach((l, idx) => { if (idx) p.appendChild(h("br")); p.appendChild(inline(l)); });
    root.appendChild(p);
  }
  return root;
}

// ---------- utilidades ----------
export async function copyText(t) {
  try { await navigator.clipboard.writeText(t); toast("Copiado", "ok"); }
  catch (_) {
    const ta = h("textarea", { style: { position: "fixed", opacity: "0" } }); ta.value = t;
    document.body.appendChild(ta); ta.select(); document.execCommand("copy"); ta.remove(); toast("Copiado", "ok");
  }
}
export function fmtDate(s) {
  if (!s) return "—";
  const d = new Date(s);
  return d.toLocaleString("pt-BR", { day: "2-digit", month: "2-digit", year: "2-digit", hour: "2-digit", minute: "2-digit" });
}
export function fmtRel(s) {
  if (!s) return "—";
  const diff = (Date.now() - new Date(s).getTime()) / 1000;
  if (diff < 60) return "agora";
  if (diff < 3600) return `há ${Math.floor(diff / 60)} min`;
  if (diff < 86400) return `há ${Math.floor(diff / 3600)} h`;
  return `há ${Math.floor(diff / 86400)} d`;
}
export function fmtDur(ms) {
  if (ms === null || ms === undefined) return "—";
  if (ms < 1000) return ms + " ms";
  const s = ms / 1000;
  if (s < 60) return s.toFixed(1) + " s";
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}
export function fmtNum(n) { return Number(n || 0).toLocaleString("pt-BR"); }
export function debounce(fn, ms = 250) { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; }
export function readFile(file) {
  return new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsText(file); });
}
export function filePicker(accept, onText) {
  const inp = h("input", { type: "file", accept, class: "hidden" });
  inp.addEventListener("change", async () => { if (inp.files[0]) onText(await readFile(inp.files[0]), inp.files[0].name); inp.value = ""; });
  document.body.appendChild(inp);
  inp.click();
  setTimeout(() => inp.remove(), 60000);
}
export function kvEditor(pairs = [], { keyPlaceholder = "CHAVE", valuePlaceholder = "valor", secret = false } = {}) {
  const box = h("div", { class: "stack sm" });
  const rows = h("div", { class: "stack sm" });
  const addRow = (k = "", v = "") => {
    const ki = input({ placeholder: keyPlaceholder, value: k, class: "mono" });
    const vi = input({ placeholder: valuePlaceholder, value: v, type: secret ? "password" : "text", autocomplete: "off" });
    const row = h("div", { class: "kv-row" }, ki, vi, h("button", { class: "btn sm ghost", type: "button", "aria-label": "remover", onclick: () => row.remove() }, icon("trash", "sm")));
    row._k = ki; row._v = vi;
    rows.appendChild(row);
  };
  pairs.forEach((p) => addRow(p.key ?? p[0], p.value ?? p[1]));
  box.append(rows, h("div", null, btn("Adicionar variável", { icon: "plus", cls: "sm", onClick: () => addRow() })));
  box.values = () => [...rows.children].map((r) => ({ key: r._k.value.trim(), value: r._v.value })).filter((p) => p.key);
  return box;
}

// jobModal: abre um modal com terminal e acompanha a execução devolvida pela API.
export async function jobModal(title, requestFn, { onEnd } = {}) {
  const term = terminal({ title, height: "380px" });
  const m = modal({ title, body: term.el, size: "wide", actions: [{ label: "Fechar" }] });
  try {
    const res = await requestFn();
    term.write(`[doomctl] execução #${res.log_id} iniciada\n`);
    const end = await runJob(res, term);
    onEnd && onEnd(end);
    return end;
  } catch (e) {
    term.write(`[doomctl] ${e.message}\n`);
    term.flush();
    term.status(badge("erro", "err", true));
    return null;
  } finally { void m; }
}

// formBuilder: monta campos a partir de definições e lê/escreve valores.
// def: { name, label, type: text|textarea|select|bool|number|password, options, placeholder, hint, span, value }
export function formBuilder(defs, values = {}, { cols = 2 } = {}) {
  const grid = h("div", { class: "form-grid" + (cols === 3 ? " three" : "") });
  const ctrls = {};
  for (const d of defs) {
    const v = values[d.name] !== undefined ? values[d.name] : d.value !== undefined ? d.value : "";
    let c, el;
    if (d.type === "bool") {
      el = checkbox(d.label, v === true || v === "true");
      c = el.input;
      el = h("div", { class: "field" + (d.span ? " span-all" : ""), style: { justifyContent: "flex-end" } }, el, d.hint ? h("span", { class: "hint", text: d.hint }) : null);
    } else {
      if (d.type === "textarea") { c = h("textarea", { rows: d.rows || 3, placeholder: d.placeholder || "" }); c.value = v; }
      else if (d.type === "select") c = select(d.options, v);
      else c = input({ type: d.type === "number" ? "number" : d.type === "password" ? "password" : "text", placeholder: d.placeholder || "", value: v, autocomplete: "off", class: d.mono ? "mono" : "" });
      el = field(d.label, c, d.hint, d.span ? "span-all" : "");
    }
    ctrls[d.name] = { c, d };
    grid.appendChild(el);
  }
  grid.values = () => {
    const out = {};
    for (const [k, { c, d }] of Object.entries(ctrls)) {
      if (d.type === "bool") out[k] = c.checked;
      else if (d.type === "number") out[k] = c.value === "" ? 0 : Number(c.value);
      else out[k] = c.value;
    }
    return out;
  };
  grid.set = (vals) => {
    for (const [k, v] of Object.entries(vals)) {
      const x = ctrls[k];
      if (!x) continue;
      if (x.d.type === "bool") x.c.checked = !!v; else x.c.value = v ?? "";
    }
  };
  grid.ctrl = (k) => ctrls[k] && ctrls[k].c;
  return grid;
}

// streamJobInto reconecta um terminal a um job em andamento.
export function streamJobInto(jobId, term) { return runJob({ job_id: jobId }, term); }
