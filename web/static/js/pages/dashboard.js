import { get } from "../api.js";
import { h, mount, icon, card, table, statusBadge, fmtRel, fmtDur, fmtNum, tabs, toastErr } from "../ui.js";

const MODULE_NAMES = { ansible: "Ansible", docker: "Docker", kubernetes: "Kubernetes", opentofu: "OpenTofu", finops: "FinOps", netcalc: "Sub-redes",
  trivy: "Trivy", ai: "Assistente IA", plugins: "Plug-ins", logs: "Logs" };

function greeting() {
  const hr = new Date().getHours();
  return hr < 12 ? "Bom dia" : hr < 18 ? "Boa tarde" : "Boa noite";
}

function donut(pct, label, sub) {
  const r = 45, c = 2 * Math.PI * r;
  const v = Math.max(0, Math.min(100, pct));
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 100 100");
  const bg = document.createElementNS(ns, "circle");
  Object.entries({ cx: 50, cy: 50, r, fill: "none", stroke: "var(--surface-3)", "stroke-width": 9 }).forEach(([k, x]) => bg.setAttribute(k, x));
  const fg = document.createElementNS(ns, "circle");
  const color = v >= 95 ? "var(--ok)" : v >= 80 ? "var(--warn)" : "var(--err)";
  Object.entries({ cx: 50, cy: 50, r, fill: "none", stroke: color, "stroke-width": 9, "stroke-linecap": "round",
    "stroke-dasharray": `${(c * v) / 100} ${c}` }).forEach(([k, x]) => fg.setAttribute(k, x));
  svg.append(bg, fg);
  return h("div", { class: "donut", role: "img", "aria-label": `${label}: ${v.toFixed(1)}%` }, svg,
    h("div", { class: "center" }, h("b", { text: v.toFixed(v === 100 ? 0 : 1) + "%" }), h("span", { text: label }), sub ? h("small", { text: sub }) : null));
}

function kpi(label, value, deltaNode, sub) {
  return h("div", { class: "kpi" }, h("div", { class: "label", text: label }), h("div", { class: "value", text: value }),
    h("div", { class: "delta" }, deltaNode, sub ? h("span", { text: sub }) : null));
}

function delta(cur, prev, invert = false) {
  if (!prev) return h("b", { class: "neutral" }, "—");
  const d = ((cur - prev) / prev) * 100;
  const good = invert ? d <= 0 : d >= 0;
  return h("b", { class: good ? "up" : "down" }, icon(d >= 0 ? "bolt" : "down", "sm"), `${d >= 0 ? "+" : ""}${d.toFixed(1)}%`);
}

function bars(series) {
  const max = Math.max(1, ...series.map((d) => d.success + d.failed));
  const wrap = h("div", { class: "bars", role: "img", "aria-label": "Execuções por dia (sucesso e falha)" });
  const labels = h("div", { class: "bar-labels" });
  for (const d of series) {
    const day = new Date(d.day + "T12:00:00");
    const tip = `${day.toLocaleDateString("pt-BR")}: ${d.success} ok, ${d.failed} falha(s)`;
    wrap.appendChild(h("div", { class: "bar", dataset: { tip } },
      h("i", { class: "s", style: { height: (d.success / max) * 100 + "%" } }),
      h("i", { class: "f", style: { height: (d.failed / max) * 100 + "%" } })));
    labels.appendChild(h("span", { text: series.length > 14 ? (day.getDate() % 5 === 0 ? day.getDate() : "") : day.toLocaleDateString("pt-BR", { weekday: "short" }).replace(".", "") }));
  }
  return h("div", null, wrap, labels);
}

export async function render(root, ctx) {
  let period = "week";
  let tab = "overview";
  let data = null;
  const dismissed = (() => { try { return sessionStorage.getItem("doomcli-alert") === "1"; } catch (_) { return false; } })();

  const alert = dismissed ? null : h("div", { class: "alert brand", role: "note" },
    h("div", { class: "alert-icon" }, icon("terminal")),
    h("div", { class: "grow" }, h("strong", null, "doomcli chegando em breve ", h("span", { class: "tag beta", text: "em breve" })),
      h("p", null, "A CLI oficial do DOOMCTL vai permitir executar playbooks, scans e geradores direto da linha de comando: ",
        h("span", { class: "cli-hint", text: "$ doomcli tofu plan lab-aws" }))),
    h("button", { class: "icon-btn close", type: "button", "aria-label": "Dispensar aviso", onclick: (e) => {
      try { sessionStorage.setItem("doomcli-alert", "1"); } catch (_) { /* ignore */ }
      e.currentTarget.closest(".alert").remove();
    } }, icon("x", "sm")));

  const seg = h("div", { class: "seg", role: "group", "aria-label": "Período" });
  const periods = [["day", "Diário"], ["week", "Semanal"], ["month", "Mensal"]];
  periods.forEach(([id, label]) => seg.appendChild(h("button", { type: "button", class: id === period ? "active" : "", onclick: (e) => {
    period = id; seg.querySelectorAll("button").forEach((b) => b.classList.remove("active")); e.currentTarget.classList.add("active"); load();
  } }, label)));

  const overviewBody = h("div", { class: "overview" });
  const tabBar = tabs([
    { id: "overview", label: "Overview geral", icon: "grid" },
    { id: "devops", label: "DevOps & Cloud", icon: "bolt" },
    { id: "netsec", label: "Redes & Segurança", icon: "shield" },
    { id: "ai", label: "Assistente IA", icon: "sparkles" },
  ], tab, (id) => { tab = id; renderOverview(); });

  const activity = h("div");
  const recent = h("div");
  const byModule = h("div");

  const name = (ctx.me.user.full_name || ctx.me.user.username).split(" ")[0];
  mount(root,
    alert,
    h("div", { class: "page-head" },
      h("div", null, h("h1", { text: `${greeting()}, ${name}` }), h("p", { text: "Status das automações, execuções e segurança do seu ambiente." })),
      h("div", { class: "row" }, h("span", { class: "badge outline" }, icon("calendar", "sm"), "Período"), seg)),
    h("div", { class: "stack" },
      h("section", { class: "card" },
        h("div", { class: "card-head" }, h("div", null, h("h2", { text: "Visão geral de operações" }), h("p", { text: "Execuções das ferramentas, taxa de sucesso e postura de segurança." }))),
        tabBar, overviewBody),
      h("div", { class: "grid g2" },
        card({ title: "Atividade", subtitle: "Execuções por dia — verde: sucesso · vermelho: falha", body: activity }),
        card({ title: "Por ferramenta", subtitle: "Execuções e registros no período", body: byModule })),
      card({ title: "Execuções recentes", subtitle: "Últimas ações registradas nas ferramentas",
        actions: ctx.can("logs") ? h("a", { href: "#/logs", class: "btn sm ghost" }, "Ver todos os logs", icon("right", "sm")) : null, body: recent, flush: true }),
      quickTiles(ctx)));

  function renderOverview() {
    if (!data) return;
    const d = data, c = d.counts;
    let kpis, gauge;
    if (tab === "overview") {
      kpis = [kpi("Execuções", fmtNum(d.executions), delta(d.executions, d.previous_executions), "vs período anterior"),
        kpi("Com sucesso", fmtNum(d.success), h("b", { class: "up" }, icon("check", "sm")), `${d.success_rate.toFixed(1)}% de sucesso`),
        kpi("Falhas", fmtNum(d.failed), h("b", { class: d.failed ? "down" : "up" }, icon(d.failed ? "alert" : "check", "sm")), d.failed ? "verifique os logs" : "nenhuma falha"),
        kpi("Em execução agora", fmtNum(d.running), h("b", { class: "neutral" }, icon("clock", "sm")), "jobs ativos")];
      gauge = [donut(d.success_rate, "Sucesso", "meta: ≥ 95%"), h("p", { class: "muted small", text: "Taxa de sucesso das execuções" })];
    } else if (tab === "devops") {
      kpis = [kpi("Hosts no inventário", fmtNum(c.hosts), null, `${fmtNum(c.vaults)} vault(s)`),
        kpi("Playbooks / roles", `${c.playbooks} / ${c.roles}`, null, "Ansible"),
        kpi("Projetos OpenTofu", fmtNum(c.tofu), null, "AWS e OCI"),
        kpi("Docker · K8s", `${c.docker} · ${c.k8s}`, null, `${c.clusters} cluster(s), ${c.registries} registry(ies)`)];
      const dev = ["ansible", "docker", "opentofu", "kubernetes"].reduce((a, m) => a + (d.by_module[m] || 0), 0);
      const total = Object.values(d.by_module).reduce((a, b) => a + b, 0) || 1;
      gauge = [donut((dev / total) * 100, "DevOps", `${dev} execuções`), h("p", { class: "muted small", text: "Participação de DevOps & Cloud nas execuções" })];
    } else if (tab === "netsec") {
      const mfaPct = c.users ? (c.mfa_users / c.users) * 100 : 0;
      kpis = [kpi("CVEs críticas (último scan)", fmtNum(d.trivy_critical), h("b", { class: d.trivy_critical ? "down" : "up" }, icon(d.trivy_critical ? "alert" : "check", "sm")), d.trivy_target || "nenhum scan ainda"),
        kpi("Scans Trivy", fmtNum(d.by_module.trivy || 0), null, "no período"),
        kpi("Cálculos de rede", fmtNum(d.by_module.netcalc || 0), null, "sub-redes e VLSM"),
        kpi("Usuários com MFA", `${c.mfa_users}/${c.users}`, null, "TOTP ativo")];
      gauge = [donut(mfaPct, "MFA", "meta: 100%"), h("p", { class: "muted small", text: "Cobertura de MFA entre usuários ativos" })];
    } else {
      kpis = [kpi("Perguntas ao assistente", fmtNum(d.ai_questions), null, "no período"),
        kpi("Personas", "2", null, "Atlas · Sentinela"),
        kpi("Modelo", "local", null, "Ollama (self-hosted)"),
        kpi("Execuções totais", fmtNum(d.executions), null, "todas as ferramentas")];
      const share = d.executions ? Math.min(100, (d.ai_questions / Math.max(1, d.executions + d.ai_questions)) * 100) : 0;
      gauge = [donut(share, "IA", "uso relativo"), h("p", { class: "muted small", text: "Consultas à IA vs. execuções" })];
    }
    mount(overviewBody, h("div", { class: "kpis" }, h("div", { class: "kpi-grid" }, kpis)), h("div", { class: "gauge" }, gauge));
  }

  async function load() {
    try {
      data = await get("/api/dashboard?period=" + period);
    } catch (e) { toastErr(e); return; }
    renderOverview();
    mount(activity, data.series.length ? bars(data.series) : h("p", { class: "muted", text: "Sem dados." }));
    const entries = Object.entries(data.by_module).sort((a, b) => b[1] - a[1]);
    const max = Math.max(1, ...entries.map((e) => e[1]));
    mount(byModule, entries.length ? h("div", { class: "stack sm" }, entries.map(([m, n]) =>
      h("div", null, h("div", { class: "spread small" }, h("span", { text: MODULE_NAMES[m] || m }), h("b", { text: fmtNum(n) })),
        h("div", { class: "progress" }, h("i", { style: { width: (n / max) * 100 + "%" } })))))
      : h("p", { class: "muted", text: "Nenhuma execução no período." }));
    mount(recent, table([
      { label: "Ferramenta", render: (r) => h("strong", { text: MODULE_NAMES[r.module] || r.module }) },
      { label: "Ação", render: (r) => h("span", { class: "mono small", text: r.action }) },
      { label: "Alvo", render: (r) => h("span", { text: r.target || "—" }) },
      { label: "Usuário", key: "username" },
      { label: "Status", render: (r) => statusBadge(r.status) },
      { label: "Quando", render: (r) => h("span", { class: "nowrap muted", text: fmtRel(r.started_at) }) },
      { label: "Duração", cls: "right", render: (r) => h("span", { class: "mono small", text: fmtDur(r.duration_ms) }) },
    ], data.recent, { onRowClick: ctx.can("logs") ? (r) => ctx.navigate("logs/" + r.id) : null,
      empty: h("div", { class: "empty" }, h("b", { text: "Nenhuma execução ainda" }), h("p", { text: "Comece pelo Docker, OpenTofu ou Kubernetes." })) }));
  }

  load();
  const timer = setInterval(() => { if (!document.hidden) load(); }, 30000);
  return { destroy: () => clearInterval(timer) };
}

function quickTiles(ctx) {
  const items = [
    ["docker", "cube", "Docker", "Dockerfile, compose e registries"],
    ["kubernetes", "helm", "Kubernetes", "Manifests, Helm, Kustomize (beta)"],
    ["opentofu", "layers", "OpenTofu", "AWS e OCI: plan, apply, destroy"],
    ["finops", "dollar", "FinOps", "Custos, anomalias, orçamentos e otimização"],
    ["subnets", "calc", "Sub-redes", "Calculadora, divisão e VLSM", "netcalc"],
    ["trivy", "shield", "Trivy", "Vulnerabilidades, misconfig e SBOM"],
  ].filter((i) => ctx.can(i[4] || i[0]));
  if (!items.length) return null;
  return h("section", null, h("div", { class: "eyebrow", text: "Acesso rápido" }),
    h("div", { class: "tiles" }, items.map(([p, ic, t, s]) => h("a", { class: "tile", href: "#/" + p },
      h("div", { class: "ic" }, icon(ic)), h("div", null, h("b", { text: t }), h("span", { text: s }))))));
}
