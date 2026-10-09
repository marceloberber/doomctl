import { get, post } from "../../api.js";
import { h, mount, card, btn, loading, emptyState, toast, toastErr, icon, tabs } from "../../ui.js";
import { fmtMoney, fmtPct, deltaBadge, stackedBars, hbars, filterBar, periodQuery, filterQuery, DIM_LABELS, fmtDay, note, kpiMoney } from "./common.js";

export async function render(root, ctx) {
  const f = { period: "30", source_id: "", provider: "", account: "", service: "", region: "", project: "", environment: "", team: "" };
  for (const k of Object.keys(f)) if (ctx.query.get(k)) f[k] = ctx.query.get(k);
  const filters = h("div");
  const out = h("div", { class: "stack" }, loading());
  mount(root, h("div", { class: "stack" }, filters, out));
  let dim = "service";

  async function load() {
    mount(filters, filterBar(ctx.shared.dims, f, load));
    mount(out, loading());
    const { period, ...rest } = f;
    let o;
    try { o = await get(`/api/finops/overview?${periodQuery(period)}&${filterQuery(rest)}`); } catch (e) { mount(out, ""); return toastErr(e); }
    if (!o.has_data) return mount(out, emptyData(ctx, o));
    const cur = o.currency;
    const kpis = h("div", { class: "fo-kpis" },
      kpi("Custo no período", kpiMoney(o.total, cur), h("span", null, deltaBadge(o.delta_pct), " vs ", fmtMoney(o.prev_total, cur, { compact: true })),
        `${fmtDay(o.start)} a ${fmtDay(new Date(new Date(o.end + "T12:00:00") - 864e5).toISOString())} · ${o.days} dias`),
      kpi("Média diária", kpiMoney(o.daily_avg, cur), null, "no período"),
      kpi("Mês atual (até ontem)", kpiMoney(o.mtd, cur), null, `mês anterior: ${fmtMoney(o.last_month, cur, { compact: true })}`),
      kpi("Previsão do mês", kpiMoney(o.forecast_eom, cur), o.last_month ? deltaBadge(((o.forecast_eom - o.last_month) / o.last_month) * 100) : null, "fechamento estimado"));
    const notes = (o.notes || []).map((n) => note(n, n.includes("ignorados") ? "warn" : "info"));
    const dimTabs = Object.keys(DIM_LABELS).filter((d) => o.breakdowns[d] && o.breakdowns[d].length).map((d) => ({ id: d, label: DIM_LABELS[d] }));
    if (!dimTabs.find((t) => t.id === dim)) dim = dimTabs[0] ? dimTabs[0].id : "service";
    const bdBody = h("div");
    const drawBD = () => mount(bdBody, hbars(o.breakdowns[dim], cur, { limit: 15,
      onClick: ["service", "provider", "account", "region", "project", "environment", "team"].includes(dim) ? (k) => {
        if (k === "Outros" || k === "(vazio)") return;
        f[dim] = dim === "provider" ? k.toLowerCase() : k === "(sem equipe)" ? "" : k;
        load();
      } : null }));
    drawBD();
    const coverage = Object.entries(o.tag_coverage || {});
    mount(out,
      ...notes,
      kpis,
      card({ title: "Custo diário", subtitle: "Empilhado pelos 5 serviços de maior custo no período", body: stackedBars(o.series, o.series_keys, cur),
        actions: ctx.can("ai") ? btn("Analisar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops", filename: "custos.md",
          content: summary(o, f), prompt: "Analise este resumo de custos de nuvem: explique as principais variações e sugira ações de FinOps priorizadas pelo impacto.", send: true }) }) : null }),
      h("div", { class: "grid fo-g21" },
        card({ title: "Quebra de custos", subtitle: "Clique em um item para filtrar · variação vs período anterior", body: h("div", { class: "stack sm" },
          tabs(dimTabs, dim, (id) => { dim = id; drawBD(); }), bdBody) }),
        h("div", { class: "stack" },
          card({ title: "Cobertura de tags", subtitle: "Parcela do custo com tag de alocação", body: coverage.length ? h("div", { class: "stack sm" },
            coverage.map(([k, v]) => h("div", null, h("div", { class: "spread small" }, h("span", { text: DIM_LABELS[k] || k }), h("b", { text: v.toLocaleString("pt-BR", { maximumFractionDigits: 1 }) + "%" })),
              h("div", { class: "progress " + (v >= 90 ? "ok" : v >= 70 ? "warn" : "err") }, h("i", { style: { width: v + "%" } })))),
            h("p", { class: "small muted", text: "Custo sem tag não aparece no showback por projeto/ambiente. Defina as tags obrigatórias e políticas em IaC & políticas." }))
            : h("p", { class: "small muted", text: "As fontes ainda não trouxeram alocação por tags." }) }),
          card({ title: "Maiores variações", subtitle: "Serviços com maior aumento absoluto", body: increases(o.breakdowns.service, cur) }))));
  }
  load();
}

function kpi(label, value, deltaNode, sub) {
  return h("div", { class: "kpi fo-kpi" + (value.length > 11 ? " long" : "") }, h("div", { class: "label", text: label }), h("div", { class: "value", text: value, title: value }),
    h("div", { class: "delta" }, deltaNode, sub ? h("span", { text: sub }) : null));
}

function increases(svc, cur) {
  const rows = (svc || []).map((b) => ({ ...b, d: b.cost - b.prev })).filter((b) => Math.abs(b.d) >= 0.01).sort((a, b) => b.d - a.d);
  const top = rows.slice(0, 4).concat(rows.length > 4 ? rows.slice(-2).filter((x) => x.d < 0) : []);
  if (!top.length) return h("p", { class: "small muted", text: "Sem variações relevantes." });
  return h("div", { class: "fo-movers" }, top.map((b) => h("div", { class: "fo-mover" },
    h("span", { class: "k", text: b.key }), h("b", { class: b.d > 0 ? "down" : "up", text: (b.d > 0 ? "+" : "") + fmtMoney(b.d, cur) }),
    h("span", { class: "faint small", text: fmtPct(b.delta_pct) }))));
}

function summary(o, f) {
  const cur = o.currency;
  const lines = [`Período: ${o.start} a ${o.end} (exclusivo), moeda ${cur}`,
    `Filtros: ${Object.entries(f).filter(([k, v]) => v && k !== "period").map(([k, v]) => `${k}=${v}`).join(", ") || "nenhum"}`,
    `Total: ${o.total.toFixed(2)} (anterior ${o.prev_total.toFixed(2)}, variação ${fmtPct(o.delta_pct)})`,
    `Mês atual até ontem: ${o.mtd.toFixed(2)}; previsão de fechamento: ${o.forecast_eom.toFixed(2)}; mês anterior: ${o.last_month.toFixed(2)}`];
  for (const [d, list] of Object.entries(o.breakdowns)) {
    lines.push(`\n${DIM_LABELS[d] || d}:`);
    for (const b of list.slice(0, 8)) lines.push(`- ${b.key}: ${b.cost.toFixed(2)} (${b.share}% · anterior ${b.prev.toFixed(2)} · ${fmtPct(b.delta_pct)})`);
  }
  if (Object.keys(o.tag_coverage || {}).length) lines.push(`\nCobertura de tags: ${JSON.stringify(o.tag_coverage)}`);
  return lines.join("\n");
}

function emptyData(ctx, o) {
  const manage = ctx.canManage;
  return h("div", { class: "stack" },
    (o.notes || []).map((n) => note(n, "warn")),
    card({ body: emptyState("dollar", "Nenhum custo no período", manage
      ? "Conecte uma conta AWS (Cost Explorer) ou OCI (Usage API), importe um CSV de custos ou carregue dados de exemplo para conhecer o módulo."
      : "Peça a quem gerencia o FinOps para conectar uma fonte de custos.",
    manage ? h("div", { class: "row", style: { justifyContent: "center" } },
      btn("Conectar fonte", { icon: "plus", cls: "primary", onClick: () => ctx.goTab("fontes") }),
      btn("Carregar dados de exemplo", { icon: "play", onClick: async () => {
        try { const r = await post("/api/finops/demo"); toast(`Exemplo carregado: ${r.rows} linhas de custo e ${r.resources} recursos`, "ok"); await ctx.shared.reloadDims(); ctx.goTab("custos"); }
        catch (e) { toastErr(e); }
      } })) : null) }),
    h("div", { class: "fo-howto small muted" }, icon("info", "sm"), h("span", { text: "Os dados de custo chegam das APIs dos provedores com até 24 h de atraso; o dia de hoje não é exibido por estar incompleto." })));
}
