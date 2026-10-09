// FinOps — componentes compartilhados (formatação, gráficos e filtros).
import { h, icon, badge, select, btn, copyText, toast, modal, field, input } from "../../ui.js";

export const SERIES_COLORS = ["var(--c1)", "var(--c2)", "var(--c3)", "var(--c4)", "var(--c5)", "var(--c6)"];
export const OTHER_COLOR = "var(--c-other)";

const nfCache = {};
export function fmtMoney(v, cur = "USD", { compact = false, digits } = {}) {
  const key = cur + compact + digits;
  if (!nfCache[key]) {
    try {
      nfCache[key] = new Intl.NumberFormat("pt-BR", { style: "currency", currency: cur, notation: compact ? "compact" : "standard",
        maximumFractionDigits: digits ?? (compact ? 1 : 2), minimumFractionDigits: compact ? 0 : digits ?? 2 });
    } catch (_) {
      nfCache[key] = new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 2 });
    }
  }
  return nfCache[key].format(Number(v || 0));
}

export function fmtPct(p, digits = 1) {
  if (p === null || p === undefined || Number.isNaN(p)) return "—";
  return `${p > 0 ? "+" : ""}${Number(p).toLocaleString("pt-BR", { maximumFractionDigits: digits, minimumFractionDigits: 0 })}%`;
}

export function fmtDay(s, opts = { day: "2-digit", month: "2-digit" }) {
  if (!s) return "—";
  return new Date(String(s).slice(0, 10) + "T12:00:00").toLocaleDateString("pt-BR", opts);
}

// Para custo, aumento é ruim (vermelho) e redução é boa (verde).
export function deltaBadge(p, { invert = false } = {}) {
  if (p === null || p === undefined) return h("span", { class: "faint small", text: "—" });
  const bad = invert ? p < 0 : p > 0;
  const cls = Math.abs(p) < 0.5 ? "neutral" : bad ? "down" : "up";
  return h("b", { class: "fo-delta " + cls }, h("span", { "aria-hidden": "true", text: p > 0 ? "▲" : p < 0 ? "▼" : "•" }), fmtPct(p));
}

export const SEVERITY = { high: ["Alta", "err"], medium: ["Média", "warn"], low: ["Baixa", "info"], info: ["Info", ""] };
export function sevBadge(s) { const [l, k] = SEVERITY[s] || [s, ""]; return badge(l, k, true); }

export const CATEGORIES = {
  rightsizing: "Rightsizing", idle: "Ociosos", waste: "Desperdício", schedule: "Agendamento", modernize: "Modernização",
  tagging: "Tags", spot: "Spot", database: "Bancos de dados", storage: "Armazenamento", network: "Rede", kubernetes: "Kubernetes", commitment: "Compromissos",
};

export const DIM_LABELS = { service: "Serviço", provider: "Provedor", account: "Conta", region: "Região", project: "Projeto", environment: "Ambiente", team: "Equipe" };

export const PROVIDER_LABELS = { aws: "AWS", oci: "OCI", csv: "CSV", demo: "Exemplo", custom: "Outro", k8s: "Kubernetes" };

// ---------- gráficos ----------

// Barras diárias empilhadas (até 6 séries + "Outros").
export function stackedBars(series, keys, cur) {
  if (!series.length) return h("p", { class: "muted small", text: "Sem dados no período." });
  const max = Math.max(1e-9, ...series.map((d) => d.total));
  const colorOf = (k, i) => (k === "Outros" ? OTHER_COLOR : SERIES_COLORS[i % SERIES_COLORS.length]);
  const wrap = h("div", { class: "fo-bars", role: "img", "aria-label": `Custo diário de ${fmtDay(series[0].day)} a ${fmtDay(series[series.length - 1].day)}` });
  const labels = h("div", { class: "fo-bar-labels", "aria-hidden": "true" });
  const step = Math.ceil(series.length / 10);
  series.forEach((d, idx) => {
    const tip = [`${fmtDay(d.day, { weekday: "short", day: "2-digit", month: "2-digit" })} · ${fmtMoney(d.total, cur)}`]
      .concat(keys.filter((k) => d.parts[k]).map((k) => `${k}: ${fmtMoney(d.parts[k], cur)}`)).join("\n");
    const col = h("div", { class: "fo-bar", dataset: { tip }, tabindex: "0", "aria-label": tip.replace(/\n/g, "; ") });
    keys.forEach((k, i) => {
      const v = d.parts[k] || 0;
      if (v > 0) col.appendChild(h("i", { style: { height: (v / max) * 100 + "%", background: colorOf(k, i) } }));
    });
    wrap.appendChild(col);
    labels.appendChild(h("span", { text: idx % step === 0 ? fmtDay(d.day) : "" }));
  });
  const legend = h("div", { class: "fo-legend" }, keys.map((k, i) => h("span", null, h("i", { style: { background: colorOf(k, i) } }), k)));
  return h("div", { class: "fo-chart" }, h("div", { class: "fo-axis-max small faint", text: fmtMoney(max, cur, { compact: true }) }), wrap, labels, legend);
}

// Lista de barras horizontais (quebra por dimensão).
export function hbars(buckets, cur, { onClick, limit = 12, showDelta = true, emptyText = "Sem dados." } = {}) {
  if (!buckets || !buckets.length) return h("p", { class: "muted small", text: emptyText });
  const rows = buckets.slice(0, limit);
  const max = Math.max(1e-9, ...rows.map((b) => b.cost || b.prev || 0));
  const list = h("div", { class: "fo-hbars" });
  for (const b of rows) {
    const el = h(onClick ? "button" : "div", { class: "fo-hbar" + (onClick ? " click" : ""), type: onClick ? "button" : undefined,
      title: onClick ? `Filtrar por ${b.key}` : undefined, onclick: onClick ? () => onClick(b.key) : undefined },
      h("span", { class: "k", text: b.key }),
      h("span", { class: "track", "aria-hidden": "true" }, h("i", { style: { width: Math.max(0.5, ((b.cost || 0) / max) * 100) + "%" } })),
      h("span", { class: "v", text: fmtMoney(b.cost, cur) }),
      h("span", { class: "s faint", text: b.share ? b.share.toLocaleString("pt-BR", { maximumFractionDigits: 1 }) + "%" : "" }),
      showDelta ? h("span", { class: "d" }, deltaBadge(b.delta_pct)) : null);
    list.appendChild(el);
  }
  if (buckets.length > limit) list.appendChild(h("p", { class: "faint small", text: `+ ${buckets.length - limit} itens` }));
  return list;
}

const SVGNS = "http://www.w3.org/2000/svg";
function svgEl(tag, attrs) {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

// Linha de custo real + previsão com faixa de incerteza (~80%).
export function forecastChart(history, points, cur) {
  const all = [...history.map((p) => ({ day: p.day, a: p.actual })), ...points.map((p) => ({ day: p.day, v: p.value, lo: p.low, hi: p.high }))];
  if (!all.length) return h("p", { class: "muted small", text: "Sem histórico suficiente." });
  const W = 1000, H = 260, pad = 6;
  const max = Math.max(1e-9, ...all.map((p) => Math.max(p.a || 0, p.hi || 0, p.v || 0))) * 1.06;
  const lo = Math.min(...all.map((p) => (p.a != null ? p.a : p.lo != null ? p.lo : 0)));
  // eixo a partir de ~90% do mínimo quando a série é estável (mostra a variação); senão a partir de zero
  const min = lo > max * 0.45 ? lo * 0.9 : 0;
  const x = (i) => pad + (i / Math.max(1, all.length - 1)) * (W - 2 * pad);
  const y = (v) => H - ((Math.max(v, min) - min) / (max - min)) * (H - 10);
  const svg = svgEl("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", class: "fo-line", role: "img",
    "aria-label": "Custo diário real e previsto" });
  for (let g = 1; g <= 3; g++) svg.appendChild(svgEl("line", { x1: 0, x2: W, y1: (H / 4) * g, y2: (H / 4) * g, class: "grid" }));
  const n0 = history.length;
  if (points.length) {
    let band = "";
    points.forEach((p, i) => { band += `${i ? "L" : "M"}${x(n0 + i)},${y(p.high)} `; });
    for (let i = points.length - 1; i >= 0; i--) band += `L${x(n0 + i)},${y(points[i].low)} `;
    svg.appendChild(svgEl("path", { d: band + "Z", class: "band" }));
    let fl = n0 > 0 ? `M${x(n0 - 1)},${y(history[n0 - 1].actual)} ` : "";
    points.forEach((p, i) => { fl += `${fl ? "L" : "M"}${x(n0 + i)},${y(p.value)} `; });
    svg.appendChild(svgEl("path", { d: fl, class: "fc" }));
    svg.appendChild(svgEl("line", { x1: x(n0), x2: x(n0), y1: 0, y2: H, class: "today" }));
  }
  if (n0) {
    let d = "";
    history.forEach((p, i) => { d += `${i ? "L" : "M"}${x(i)},${y(p.actual)} `; });
    svg.appendChild(svgEl("path", { d, class: "act" }));
  }
  const first = all[0].day, last = all[all.length - 1].day, today = points[0] ? points[0].day : null;
  return h("div", { class: "fo-chart" },
    h("div", { class: "fo-axis-max small faint", text: fmtMoney(max, cur, { compact: true }) }),
    h("div", { class: "fo-line-wrap" }, svg),
    min > 0 ? h("div", { class: "small faint", text: "eixo a partir de " + fmtMoney(min, cur, { compact: true }) }) : null,
    h("div", { class: "spread small faint" }, h("span", { text: fmtDay(first) }), today ? h("span", { text: "hoje · " + fmtDay(today) }) : null, h("span", { text: fmtDay(last) })),
    h("div", { class: "fo-legend" }, h("span", null, h("i", { class: "line act" }), "Real"), h("span", null, h("i", { class: "line fc" }), "Previsto"),
      h("span", null, h("i", { class: "band" }), "Faixa provável (~80%)")));
}

// Sparkline simples (valores diários).
export function sparkline(values, { label = "tendência" } = {}) {
  const W = 120, H = 32;
  if (!values || values.length < 2) return h("span", { class: "faint", text: "—" });
  const max = Math.max(1e-9, ...values);
  const svg = svgEl("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", class: "fo-spark", role: "img", "aria-label": label });
  let d = "";
  values.forEach((v, i) => { d += `${i ? "L" : "M"}${(i / (values.length - 1)) * W},${H - (v / max) * (H - 2) - 1} `; });
  svg.appendChild(svgEl("path", { d }));
  return svg;
}

// Barra de progresso com marcador de previsão.
export function budgetMeter(pctActual, pctForecast) {
  const a = Math.min(100, Math.max(0, pctActual));
  const f = Math.min(100, Math.max(0, pctForecast));
  const cls = pctActual >= 100 ? "err" : pctActual >= 80 ? "warn" : pctForecast >= 100 ? "warn" : "ok";
  return h("div", { class: "fo-meter " + cls, role: "img", "aria-label": `Gasto ${pctActual.toFixed(0)}% do orçamento; previsão ${pctForecast.toFixed(0)}%` },
    h("i", { class: "fill", style: { width: a + "%" } }), pctForecast > pctActual ? h("i", { class: "fc", style: { left: a + "%", width: Math.max(0, f - a) + "%" } }) : null,
    h("i", { class: "limit" }));
}

// ---------- filtros ----------

export const PERIODS = [["7", "7 dias"], ["30", "30 dias"], ["90", "90 dias"], ["180", "180 dias"], ["365", "12 meses"], ["mtd", "Mês atual"], ["last", "Mês anterior"]];

export function periodQuery(p) {
  const now = new Date();
  const iso = (d) => d.toISOString().slice(0, 10);
  if (p === "mtd") {
    const s = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
    const e = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
    if (e <= s) e.setUTCDate(e.getUTCDate() + 1);
    return `start=${iso(s)}&end=${iso(e)}`;
  }
  if (p === "last") {
    const s = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - 1, 1));
    const e = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
    return `start=${iso(s)}&end=${iso(e)}`;
  }
  return `days=${p}`;
}

export function filterQuery(f) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(f)) if (v) q.set(k, v);
  return q.toString();
}

// Barra de filtros: período + dimensões existentes.
export function filterBar(dims, f, onChange, { period = true, withAlloc = true } = {}) {
  const bar = h("div", { class: "fo-filters" });
  const add = (label, key, options) => {
    if (!options || !options.length) return;
    const s = select([["", label + ": todos"], ...options], f[key] || "", { "aria-label": label });
    s.addEventListener("change", () => { f[key] = s.value; onChange(); });
    bar.appendChild(s);
  };
  if (period) {
    const s = select(PERIODS, f.period || "30", { "aria-label": "Período" });
    s.addEventListener("change", () => { f.period = s.value; onChange(); });
    bar.appendChild(s);
  }
  add("Fonte", "source_id", (dims.sources || []).map((x) => [String(x.id), x.name]));
  add("Provedor", "provider", (dims.providers || []).map((x) => [x, PROVIDER_LABELS[x] || x]));
  add("Conta", "account", dims.accounts);
  add("Serviço", "service", dims.services);
  add("Região", "region", dims.regions);
  if (withAlloc) {
    add("Projeto", "project", dims.projects ? ["(sem tag)", ...dims.projects] : null);
    add("Ambiente", "environment", dims.environments ? ["(sem tag)", ...dims.environments] : null);
    add("Equipe", "team", dims.teams);
  }
  const active = Object.entries(f).some(([k, v]) => k !== "period" && v);
  if (active) bar.appendChild(btn("Limpar filtros", { icon: "x", cls: "sm ghost", onClick: () => { for (const k of Object.keys(f)) if (k !== "period") f[k] = ""; onChange(); } }));
  return bar;
}

export function codeBlock(text, { lang = "" } = {}) {
  return h("div", { class: "fo-code" }, h("pre", { class: "mono", "data-lang": lang, text }),
    h("button", { class: "btn sm ghost", type: "button", onclick: async () => { await copyText(text); toast("Copiado", "ok"); } }, icon("copy", "sm"), "Copiar"));
}

export function note(text, kind = "info") {
  return h("div", { class: "alert " + kind }, h("div", { class: "alert-icon" }, icon(kind === "warn" ? "alert" : "info")), h("div", { class: "grow" }, h("p", { text, style: { color: "var(--text-2)", marginTop: 0 } })));
}

// Valores grandes sem centavos (e compactos acima de 1 milhão) para caber nos cartões.
export function kpiMoney(v, cur) {
  const a = Math.abs(Number(v || 0));
  if (a >= 1e6) return fmtMoney(v, cur, { compact: true });
  if (a >= 1000) return fmtMoney(v, cur, { digits: 0 });
  return fmtMoney(v, cur);
}

export function statTile(label, value, sub) {
  return h("div", { class: "stat" + (String(value).length > 11 ? " long" : ""), title: typeof value === "string" ? value : undefined }, h("div", { class: "l", text: label }), h("div", { class: "n", text: value }), sub ? h("div", { class: "small muted" }, sub) : null);
}

// Pergunta um texto em um modal; devolve null se cancelado.
export function askText(title, label, placeholder = "") {
  return new Promise((resolve) => {
    let done = false;
    const inp = input({ placeholder, maxlength: 300 });
    modal({ title, body: field(label, inp), onClose: () => { if (!done) resolve(null); },
      actions: [{ label: "Cancelar" }, { label: "Confirmar", primary: true, onClick: () => { done = true; resolve(inp.value.trim()); } }] });
  });
}
