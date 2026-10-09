// doomctl — aplicação (roteamento, autenticação, layout).
import { get, post, setCSRF, onAuthLost } from "./api.js";
import { h, mount, clear, icon, logo, toast, toastErr, modal, field, input, btn, badge, fmtRel, copyText, debounce } from "./ui.js";
import { applyTheme, currentTheme } from "./theme.js";

const state = { me: null, page: null, jobsTimer: null };

// ---------- rotas ----------
export const ROUTES = [
  { path: "", title: "Visão Geral", icon: "home", module: "dashboard", section: null, page: "dashboard" },
  { path: "ansible", title: "Ansible", icon: "server", module: "ansible", section: "DevOps & Cloud", page: "soon", tag: "soon" },
  { path: "docker", title: "Docker", icon: "cube", module: "docker", section: "DevOps & Cloud", page: "docker" },
  { path: "kubernetes", title: "Kubernetes", icon: "helm", module: "kubernetes", section: "DevOps & Cloud", page: "kubernetes", tag: "beta" },
  { path: "opentofu", title: "OpenTofu", icon: "layers", module: "opentofu", section: "DevOps & Cloud", page: "opentofu" },
  { path: "aws", title: "AWS EC2 & S3", icon: "cloud", module: "aws", section: "DevOps & Cloud", page: "soon", tag: "soon" },
  { path: "finops", title: "FinOps", icon: "dollar", module: "finops", section: "DevOps & Cloud", page: "finops" },
  { path: "subnets", title: "Calculadora de sub-redes", icon: "calc", module: "netcalc", section: "Redes & Segurança", page: "netcalc" },
  { path: "masks", title: "Máscaras & Wildcard", icon: "hash", module: "netcalc", section: "Redes & Segurança", page: "masks" },
  { path: "trivy", title: "Trivy", icon: "shield", module: "trivy", section: "Redes & Segurança", page: "trivy" },
  { path: "falco", title: "Falco", icon: "activity", module: "falco", section: "Redes & Segurança", page: "soon", tag: "soon" },
  { path: "logs", title: "Logs", icon: "doc", module: "logs", section: "Operação", page: "logs" },
  { path: "ai", title: "Assistente IA", icon: "sparkles", module: "ai", section: "Operação", page: "ai" },
  { path: "plugins", title: "Plug-ins", icon: "puzzle", module: "plugins", section: "Operação", page: "plugins" },
  { path: "users", title: "Usuários", icon: "users", module: "users", section: "foot", page: "users", admin: true },
  { path: "settings", title: "Configurações", icon: "settings", module: "settings", section: "foot", page: "settings", admin: true },
  { path: "profile", title: "Meu perfil", icon: "user", module: null, section: "foot", page: "profile" },
];

export function can(module, action = "read") {
  if (!state.me) return false;
  if (!module) return true;
  const p = state.me.permissions[module];
  return !!(p && p[action]);
}

function parseHash() {
  const raw = location.hash.replace(/^#\/?/, "");
  const [pathPart, query = ""] = raw.split("?");
  const segs = pathPart.split("/").filter(Boolean);
  return { path: segs[0] || "", rest: segs.slice(1), query: new URLSearchParams(query) };
}

export function navigate(path) { location.hash = "#/" + path.replace(/^\/+/, ""); }

// ---------- boot ----------
async function boot() {
  applyTheme(currentTheme());
  onAuthLost(() => { if (state.me) { state.me = null; toast("Sessão expirada. Faça login novamente.", "err"); showAuth(); } });
  let me;
  try { me = await get("/api/auth/me", { noRedirect: true }); } catch (e) { me = { authenticated: false }; }
  if (me.csrf) setCSRF(me.csrf);
  if (me.authenticated) {
    state.me = me;
    if (me.user.must_change_password) return showForcedPassword();
    return startApp();
  }
  if (me.stage === "mfa") return showMFA();
  if (me.stage === "enroll") return showEnroll();
  showAuth();
}

async function reloadMe() {
  const me = await get("/api/auth/me");
  if (me.csrf) setCSRF(me.csrf);
  state.me = me;
  return me;
}

// ---------- telas de autenticação ----------
async function authShell(content) {
  const root = document.getElementById("app");
  const art = h("div", { class: "auth-art" },
    h("div", { class: "osd" }, h("span", { id: "osd-clock", style: { marginLeft: "auto" } })),
    h("div", null, await logo("logo"), h("div", { class: "tagline", text: "DevOps & Cloud · Redes & Segurança — self-hosted" }),
      h("ul", null,
        h("li", { text: "Dockerfile, docker-compose, OpenTofu e Kubernetes" }),
        h("li", { text: "Subnetting, Trivy e SBOM" }),
        h("li", { text: "Assistentes de IA locais (Ollama): Atlas e Sentinela" }))),
    h("div", { class: "osd bottom" }, h("span", { text: "SP 0:00:00" }), h("span", { text: "CH 03" })));
  mount(root, h("div", { class: "auth" }, art, h("div", { class: "auth-form" }, h("div", { class: "auth-box" }, content))));
  const clock = document.getElementById("osd-clock");
  const tick = () => { if (clock.isConnected) { clock.textContent = new Date().toLocaleTimeString("pt-BR") + "  " + new Date().toLocaleDateString("pt-BR"); setTimeout(tick, 1000); } };
  tick();
}

function showAuth(msg) {
  stopPolling();
  const user = input({ name: "username", autocomplete: "username", autocapitalize: "off", required: true });
  const pass = input({ name: "password", type: "password", autocomplete: "current-password", required: true });
  const err = h("div", { class: "alert err hidden", role: "alert" });
  const submit = h("button", { class: "btn primary block", type: "submit" }, icon("lock", "sm"), "Entrar");
  const form = h("form", { class: "stack" },
    field("Usuário", user), field("Senha", pass), err, submit);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true; err.classList.add("hidden");
    try {
      const r = await post("/api/auth/login", { username: user.value, password: pass.value }, { noRedirect: true });
      if (r.status === "mfa") return showMFA();
      if (r.status === "mfa_enroll") return showEnroll();
      await reloadMe();
      if (r.must_change_password) return showForcedPassword();
      startApp();
    } catch (ex) {
      mount(err, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: ex.message })));
      err.classList.remove("hidden");
    } finally { submit.disabled = false; }
  });
  authShell([h("h1", { text: "Entrar no doomctl" }), h("p", { class: "sub", text: msg || "Use suas credenciais. O MFA será solicitado se estiver ativo." }), form])
    .then(() => user.focus());
}

function showMFA() {
  const code = input({ class: "otp", inputmode: "numeric", autocomplete: "one-time-code", maxlength: "9", placeholder: "000000" });
  const submit = h("button", { class: "btn primary block", type: "submit" }, icon("key", "sm"), "Verificar");
  const form = h("form", { class: "stack" }, field("Código do aplicativo autenticador", code, "Ou um código de recuperação (xxxx-xxxx)."), submit,
    h("button", { class: "btn ghost block", type: "button", onclick: () => logoutTo() }, "Voltar ao login"));
  form.addEventListener("submit", async (e) => {
    e.preventDefault(); submit.disabled = true;
    try {
      const r = await post("/api/auth/mfa", { code: code.value }, { noRedirect: true });
      await reloadMe();
      if (r.must_change_password) return showForcedPassword();
      startApp();
    } catch (ex) { toastErr(ex); code.select(); if (ex.status === 429) showAuth(ex.message); } finally { submit.disabled = false; }
  });
  authShell([h("h1", { text: "Verificação em duas etapas" }), h("p", { class: "sub", text: "Abra o Microsoft Authenticator, Google Authenticator ou outro app TOTP e informe o código de 6 dígitos." }), form])
    .then(() => code.focus());
}

export function mfaEnrollForm({ onDone, intro }) {
  const box = h("div", { class: "stack" });
  const code = input({ class: "otp", inputmode: "numeric", autocomplete: "one-time-code", maxlength: "6", placeholder: "000000" });
  (async () => {
    try {
      const s = await get("/api/auth/mfa/setup", { noRedirect: true });
      const secret = h("code", { text: s.secret.replace(/(.{4})/g, "$1 ").trim(), class: "mono", style: { fontSize: "13px", wordBreak: "break-all" } });
      const submit = btn("Ativar MFA", { cls: "primary block", icon: "check", onClick: async () => {
        const r = await post("/api/auth/mfa/enable", { code: code.value }, { noRedirect: true });
        showRecovery(r.recovery_codes, () => onDone(r));
      } });
      code.addEventListener("keydown", (e) => { if (e.key === "Enter") submit.click(); });
      mount(box,
        intro ? h("p", { class: "muted", text: intro }) : null,
        h("div", { class: "row", style: { alignItems: "flex-start", gap: "18px" } },
          h("div", { class: "qr" }, h("img", { src: s.qr, alt: "QR code para o aplicativo autenticador" })),
          h("div", { class: "stack sm grow", style: { minWidth: "180px" } },
            h("b", { text: "1. Escaneie o QR code" }),
            h("span", { class: "muted small", text: "Compatível com Microsoft Authenticator, Google Authenticator, Authy, FreeOTP, Aegis, 2FAS, Bitwarden etc." }),
            h("b", { text: "Ou digite a chave manualmente:" }), secret,
            h("span", { class: "muted small", text: `Tipo: TOTP · ${s.algorithm} · ${s.digits} dígitos · ${s.period}s` }))),
        field("2. Informe o código gerado", code), submit);
      code.focus();
    } catch (e) { toastErr(e); }
  })();
  return box;
}

function showRecovery(codes, then) {
  const text = codes.join("\n");
  modal({
    title: "Códigos de recuperação",
    body: h("div", { class: "stack" },
      h("div", { class: "alert warn" }, h("div", { class: "alert-icon" }, icon("alert")),
        h("div", null, h("strong", { text: "Guarde estes códigos em local seguro" }), h("p", { text: "Cada código pode ser usado uma única vez caso você perca o acesso ao aplicativo. Eles não serão exibidos novamente." }))),
      h("div", { class: "codes" }, codes.map((c) => h("span", { text: c }))),
      h("div", { class: "row" }, btn("Copiar", { icon: "copy", cls: "sm", onClick: () => copyText(text) }),
        btn("Baixar .txt", { icon: "download", cls: "sm", onClick: () => { import("./api.js").then((m) => m.saveText("doomctl — códigos de recuperação\n\n" + text + "\n", "doomctl-recovery-codes.txt")); } }))),
    actions: [{ label: "Já guardei os códigos", primary: true }],
    onClose: then,
  });
}

function showEnroll() {
  authShell([h("h1", { text: "Configure o MFA" }), h("p", { class: "sub", text: "O administrador exige verificação em duas etapas para a sua conta." }),
    mfaEnrollForm({ onDone: async (r) => { await reloadMe(); if (r.must_change_password) return showForcedPassword(); startApp(); } }),
    h("button", { class: "btn ghost block", type: "button", style: { marginTop: "12px" }, onclick: () => logoutTo() }, "Cancelar")]);
}

function showForcedPassword() {
  const cur = input({ type: "password", autocomplete: "current-password" });
  const nw = input({ type: "password", autocomplete: "new-password" });
  const nw2 = input({ type: "password", autocomplete: "new-password" });
  const submit = h("button", { class: "btn primary block", type: "submit" }, "Salvar nova senha");
  const form = h("form", { class: "stack" }, field("Senha atual (temporária)", cur), field("Nova senha", nw, "Mínimo 10 caracteres com 3 destes: minúsculas, maiúsculas, números, símbolos."),
    field("Confirme a nova senha", nw2), submit, h("button", { class: "btn ghost block", type: "button", onclick: () => logoutTo() }, "Sair"));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (nw.value !== nw2.value) return toast("As senhas não conferem", "err");
    submit.disabled = true;
    try { await post("/api/auth/password", { current: cur.value, new: nw.value }); toast("Senha alterada", "ok"); await reloadMe(); startApp(); }
    catch (ex) { toastErr(ex); } finally { submit.disabled = false; }
  });
  authShell([h("h1", { text: "Defina uma nova senha" }), h("p", { class: "sub", text: "Por segurança, troque a senha temporária antes de continuar." }), form]).then(() => cur.focus());
}

export async function logoutTo() {
  try { await post("/api/auth/logout", {}, { noRedirect: true }); } catch (_) { /* ignore */ }
  state.me = null;
  setCSRF("");
  showAuth("Você saiu do doomctl.");
}

// ---------- layout principal ----------
let shell = null;

async function startApp() {
  const root = document.getElementById("app");
  const me = state.me;
  const initials = (me.user.full_name || me.user.username).split(/\s+/).map((p) => p[0]).slice(0, 2).join("");
  const nav = h("nav", { class: "nav", "aria-label": "Menu principal" });
  const foot = h("div", { class: "sidebar-foot nav" });
  const visible = ROUTES.filter((r) => (r.admin ? me.user.role === "admin" : r.module ? can(r.module) : true));
  let lastSection = null;
  for (const r of visible) {
    const a = h("a", { href: "#/" + r.path, dataset: { path: r.path } }, icon(r.icon), h("span", { text: r.title }),
      r.tag ? h("span", { class: "tag " + r.tag, text: r.tag === "soon" ? "em breve" : r.tag }) : null);
    if (r.section === "foot") { foot.appendChild(a); continue; }
    if (r.section && r.section !== lastSection) { nav.appendChild(h("div", { class: "nav-section", text: r.section })); lastSection = r.section; }
    nav.appendChild(a);
  }
  const quick = quickActions();
  const sidebar = h("aside", { class: "sidebar", id: "sidebar" },
    h("div", { class: "sidebar-brand" }, h("a", { href: "#/", "aria-label": "doomctl — início" }, await logo("logo"), h("span", { class: "cursor" }))),
    quick.length ? h("div", { class: "sidebar-cta" }, h("button", { class: "btn primary block upper", type: "button", onclick: () => openQuick(quick) }, icon("plus", "sm"), "Nova automação")) : null,
    nav, foot);

  const searchBox = searchWidget(visible);
  const jobsBtn = h("button", { class: "icon-btn", type: "button", title: "Execuções em andamento", "aria-label": "Execuções em andamento" }, icon("bell"));
  jobsBtn.addEventListener("click", () => showJobs());
  const themeBtn = h("button", { class: "theme-switch", type: "button", title: "Alternar tema claro/escuro", "aria-label": "Alternar tema claro/escuro", role: "switch" },
    h("span", { class: "sun" }, icon("sun", "sm")), h("span", { class: "moon" }, icon("moon", "sm")));
  const syncThemeAria = () => themeBtn.setAttribute("aria-checked", currentTheme() === "dark" ? "true" : "false");
  themeBtn.addEventListener("click", () => { applyTheme(currentTheme() === "dark" ? "light" : "dark", true); syncThemeAria(); });
  syncThemeAria();
  const topbar = h("header", { class: "topbar" },
    h("button", { class: "icon-btn menu-btn", type: "button", "aria-label": "Abrir menu", onclick: () => appEl.classList.toggle("nav-open") }, icon("menu")),
    searchBox,
    h("div", { class: "topbar-actions" },
      can("ai") ? h("button", { class: "btn primary upper", type: "button", onclick: () => openAI() }, icon("sparkles", "sm"), h("span", { text: "Assistente" })) : null,
      themeBtn, jobsBtn, h("div", { class: "vsep" }),
      h("a", { href: "#/profile", class: "avatar", title: `${me.user.full_name || me.user.username} (${roleName(me.user.role)})`, "aria-label": "Meu perfil" }, initials),
      h("button", { class: "btn ghost sm hide-sm", type: "button", onclick: () => logoutTo() }, icon("logout", "sm"), "Sair")));
  const content = h("main", { class: "content", id: "content", tabindex: "-1" });
  const appEl = h("div", { class: "app" }, sidebar, h("div", { class: "scrim", onclick: () => appEl.classList.remove("nav-open") }),
    h("div", { class: "main" }, topbar, content));
  mount(root, appEl);
  shell = { appEl, content, sidebar, jobsBtn };
  window.removeEventListener("hashchange", route);
  window.addEventListener("hashchange", route);
  startPolling();
  route();
}

export function roleName(r) { return { admin: "Administrador", operator: "Operador", viewer: "Visualizador" }[r] || r; }

function quickActions() {
  const all = [
    { label: "Dockerfile", icon: "cube", module: "docker", to: "docker?tab=dockerfile" },
    { label: "docker-compose.yaml", icon: "layers", module: "docker", to: "docker?tab=compose" },
    { label: "Projeto OpenTofu", icon: "cloud", module: "opentofu", to: "opentofu?new=1" },
    { label: "Manifest Kubernetes", icon: "helm", module: "kubernetes", to: "kubernetes?tab=generator" },
    { label: "Scan de imagem (Trivy)", icon: "shield", module: "trivy", to: "trivy" },
  ];
  return all.filter((a) => can(a.module, "manage"));
}

function openQuick(items) {
  const m = modal({
    title: "Nova automação",
    body: h("div", { class: "tiles" }, items.map((it) => h("a", { class: "tile", href: "#/" + it.to, onclick: () => setTimeout(() => m.close(), 0) },
      h("div", { class: "ic" }, icon(it.icon)), h("div", null, h("b", { text: it.label }))))),
    size: "wide",
  });
}

function searchWidget(routes) {
  const inp = h("input", { type: "search", placeholder: "Buscar ferramentas e logs...", "aria-label": "Buscar" });
  const results = h("div", { class: "search-results hidden" });
  let items = [];
  let sel = 0;
  const render = () => {
    clear(results);
    if (!items.length) { results.classList.add("hidden"); return; }
    items.forEach((it, i) => results.appendChild(h("a", { href: "#/" + it.to, class: i === sel ? "sel" : "", onclick: () => close() }, icon(it.icon, "sm"),
      h("span", { class: "grow", text: it.label }), it.hint ? h("span", { class: "small faint", text: it.hint }) : null)));
    results.classList.remove("hidden");
  };
  const close = () => { results.classList.add("hidden"); inp.value = ""; items = []; };
  const search = debounce(async () => {
    const q = inp.value.trim().toLowerCase();
    if (!q) { items = []; render(); return; }
    items = routes.filter((r) => r.title.toLowerCase().includes(q)).map((r) => ({ label: r.title, icon: r.icon, to: r.path, hint: "ferramenta" }));
    const extra = [];
    try {
      if (can("logs")) {
        const lg = await get("/api/logs?limit=5&q=" + encodeURIComponent(q));
        lg.items.forEach((l) => extra.push({ label: `${l.module} · ${l.action} · ${l.target}`, icon: "doc", to: `logs/${l.id}`, hint: fmtRel(l.started_at) }));
      }
    } catch (_) { /* ignore */ }
    items = items.concat(extra).slice(0, 12);
    sel = 0;
    render();
  }, 200);
  inp.addEventListener("input", search);
  inp.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { sel = Math.min(items.length - 1, sel + 1); render(); e.preventDefault(); }
    else if (e.key === "ArrowUp") { sel = Math.max(0, sel - 1); render(); e.preventDefault(); }
    else if (e.key === "Enter" && items[sel]) { navigate(items[sel].to); close(); }
    else if (e.key === "Escape") close();
  });
  inp.addEventListener("blur", () => setTimeout(() => results.classList.add("hidden"), 200));
  document.addEventListener("keydown", (e) => { if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); inp.focus(); } });
  return h("div", { class: "search" }, icon("search", "sm"), inp, h("kbd", { class: "badge outline", text: "Ctrl K" }), results);
}

// ---------- execuções em andamento ----------
function startPolling() {
  stopPolling();
  const tick = async () => {
    if (!state.me || document.hidden) return;
    try {
      const jobs = await get("/api/jobs");
      const dot = shell.jobsBtn.querySelector(".dot");
      if (jobs.length && !dot) shell.jobsBtn.appendChild(h("span", { class: "dot" }));
      if (!jobs.length && dot) dot.remove();
      state.jobs = jobs;
    } catch (_) { /* ignore */ }
  };
  tick();
  state.jobsTimer = setInterval(tick, 8000);
}
function stopPolling() { if (state.jobsTimer) clearInterval(state.jobsTimer); state.jobsTimer = null; }

async function showJobs() {
  let jobs = [];
  try { jobs = await get("/api/jobs"); } catch (e) { return toastErr(e); }
  modal({
    title: "Execuções em andamento",
    body: jobs.length ? h("div", { class: "list" }, jobs.map((j) => h("a", { class: "list-item", href: `#/logs/${j.log_id}`, onclick: (e) => e.target.closest(".modal-back").remove() },
      h("span", { class: "status-dot run" }), h("div", { class: "grow" }, h("b", { text: `${j.module} · ${j.action}` }), h("div", { class: "meta", text: `${j.target} — ${j.username} — ${fmtRel(j.started_at)}` })))))
      : h("p", { class: "muted", text: "Nenhuma execução em andamento. O histórico completo está em Logs." }),
    actions: [{ label: "Abrir Logs", primary: true, onClick: () => navigate("logs") }],
  });
}

// ---------- roteador ----------
const pageCache = {};

async function route() {
  if (!state.me || !shell) return;
  const { path, rest, query } = parseHash();
  const r = ROUTES.find((x) => x.path === path) || ROUTES[0];
  shell.appEl.classList.remove("nav-open");
  if (drawer && window.innerWidth < 1024) drawer.el.classList.remove("open");
  shell.sidebar.querySelectorAll("a[data-path]").forEach((a) => a.classList.toggle("active", a.dataset.path === r.path));
  document.title = `${r.title} · doomctl`;
  const allowed = r.admin ? state.me.user.role === "admin" : r.module ? can(r.module) : true;
  const content = shell.content;
  if (state.page && state.page.destroy) { try { state.page.destroy(); } catch (_) { /* ignore */ } }
  state.page = null;
  if (!allowed) {
    mount(content, h("div", { class: "card" }, h("div", { class: "empty" }, h("div", { class: "ic" }, icon("lock", "lg")),
      h("b", { text: "Acesso restrito" }), h("p", { text: "Seu perfil não tem permissão para este módulo. Fale com um administrador." }))));
    return;
  }
  mount(content, h("div", { class: "empty" }, h("div", { class: "ic" }, icon("refresh", "lg spin")), h("p", { text: "Carregando..." })));
  try {
    const mod = pageCache[r.page] || (pageCache[r.page] = await import(`./pages/${r.page}.js`));
    const ctx = {
      me: state.me, route: r, rest, query, can, navigate, openAI, reloadMe,
      canManage: can(r.module, "manage"),
    };
    clear(content);
    state.page = (await mod.render(content, ctx)) || null;
    content.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  } catch (e) {
    console.error(e);
    mount(content, h("div", { class: "alert err" }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: "Erro ao carregar a página" }), h("p", { text: e.message }))));
  }
}

// ---------- assistente (drawer global) ----------
let drawer = null;
export async function openAI(context) {
  if (!can("ai")) return toast("Seu perfil não tem acesso ao assistente de IA", "err");
  const { chatPanel } = await import("./pages/ai.js");
  if (!drawer) {
    const panel = chatPanel({ compact: true });
    drawer = { panel, el: h("aside", { class: "drawer", "aria-label": "Assistente de IA" },
      h("div", { class: "card-head" }, h("div", null, h("h2", null, "Assistente doomctl"), h("p", { text: "Atlas (Cloud & DevOps) · Sentinela (Redes & Segurança)" })),
        h("button", { class: "icon-btn", type: "button", "aria-label": "Fechar assistente", onclick: () => drawer.el.classList.remove("open") }, icon("x"))),
      panel.el) };
    document.body.appendChild(drawer.el);
  }
  drawer.el.classList.add("open");
  if (context) drawer.panel.setContext(context);
  drawer.panel.focus();
}

boot();
