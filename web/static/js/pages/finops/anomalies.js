import { get, post } from "../../api.js";
import { h, mount, card, btn, loading, table, badge, toast, toastErr, select, fmtRel, emptyState } from "../../ui.js";
import { fmtMoney, fmtPct, sevBadge, fmtDay, note } from "./common.js";

const SCOPES = { total: "Total", service: "Serviço", region: "Região", account: "Conta", project: "Projeto", environment: "Ambiente" };

export async function render(root, ctx) {
  let days = "30";
  let scope = "";
  const out = h("div", { class: "stack" }, loading());
  mount(root, out);

  async function load() {
    mount(out, loading());
    let r;
    try { r = await get(`/api/finops/anomalies?days=${days}`); } catch (e) { mount(out, ""); return toastErr(e); }
    const cur = r.settings.currency;
    const list = r.anomalies.filter((a) => !scope || a.scope === scope);
    const daysSel = select([["7", "7 dias"], ["30", "30 dias"], ["60", "60 dias"], ["90", "90 dias"]], days, { "aria-label": "Janela" });
    daysSel.addEventListener("change", () => { days = daysSel.value; load(); });
    const scopeSel = select([["", "Todos os escopos"], ...Object.entries(SCOPES)], scope, { "aria-label": "Escopo" });
    scopeSel.addEventListener("change", () => { scope = scopeSel.value; load(); });
    const counts = { high: 0, medium: 0, low: 0 };
    r.anomalies.forEach((a) => { counts[a.severity] = (counts[a.severity] || 0) + 1; });
    const impact = r.anomalies.filter((a) => a.scope === "total" || a.scope === "service").reduce((s, a) => s + (a.scope === "service" ? a.delta : 0), 0);
    mount(out,
      h("div", { class: "stat-row" },
        stat("Anomalias", String(r.anomalies.length), `últimos ${days} dias`),
        stat("Severidade alta", String(counts.high || 0), "aumento ≥ 100% ou de alto valor"),
        stat("Impacto (serviços)", fmtMoney(impact, cur), "acima do esperado, somado"),
        stat("Alertas enviados", String(r.alerts.length), r.settings.webhook ? "webhook configurado" : "sem webhook")),
      card({ title: "Anomalias de custo", subtitle: `Dia comparado com a mediana dos ${r.settings.window} dias anteriores · alerta quando +${r.settings.pct}%, +${fmtMoney(r.settings.min_abs, cur)} e z ≥ ${r.settings.z}`,
        actions: [daysSel, scopeSel, ctx.can("ai") && list.length ? btn("Investigar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops", filename: "anomalias.txt",
          content: list.slice(0, 25).map((a) => `${a.day} ${a.scope}=${a.name} real=${a.actual} esperado=${a.expected} delta=${a.delta} (${fmtPct(a.delta_pct)}) z=${a.z} ${a.drivers ? "drivers: " + a.drivers.map((d) => d.name + " +" + d.delta).join(", ") : ""}`).join("\n"),
          prompt: "Estas são anomalias de custo detectadas. Quais as causas mais prováveis de cada uma e como investigar (Cost Explorer / Usage API, CloudTrail/Audit)?", send: true }) }) : null],
        flush: true,
        body: table([
          { label: "Dia", render: (a) => h("span", { class: "nowrap", text: fmtDay(a.day, { weekday: "short", day: "2-digit", month: "2-digit" }) }) },
          { label: "Escopo", render: (a) => h("div", null, h("span", { class: "faint small", text: SCOPES[a.scope] || a.scope }), h("div", null, h("strong", { text: a.name }))) },
          { label: "Tipo", render: (a) => a.kind === "new" ? badge("novo custo", "info") : badge("pico", "warn") },
          { label: "Real", cls: "right", render: (a) => fmtMoney(a.actual, cur) },
          { label: "Esperado", cls: "right", render: (a) => h("span", { class: "muted", text: a.kind === "new" ? "—" : fmtMoney(a.expected, cur) }) },
          { label: "Diferença", cls: "right", render: (a) => h("b", { class: "down" }, "+" + fmtMoney(a.delta, cur), a.delta_pct != null ? h("span", { class: "faint small", text: " " + fmtPct(a.delta_pct, 0) }) : null) },
          { label: "Severidade", render: (a) => sevBadge(a.severity) },
          { label: "Principais causas", render: (a) => a.drivers && a.drivers.length ? h("span", { class: "small", text: a.drivers.map((d) => `${d.name} (+${fmtMoney(d.delta, cur)})`).join(" · ") }) : h("span", { class: "faint", text: "—" }) },
        ], list, { empty: emptyState("checkCircle", "Nenhuma anomalia", "O gasto ficou dentro do padrão no período. Ajuste a sensibilidade em Fontes & configurações.") }) }),
      card({ title: "Alertas", subtitle: "Anomalias (média/alta) e orçamentos são avaliados a cada 10 minutos; cada alerta é enviado uma única vez.",
        actions: ctx.canManage ? [
          btn("Avaliar agora", { icon: "refresh", cls: "sm", onClick: async () => { const x = await post("/api/finops/alerts/evaluate"); toast(`${x.new} alerta(s) novo(s)`, "ok"); load(); } }),
          btn("Testar webhook", { icon: "bell", cls: "sm", onClick: async () => { const x = await post("/api/finops/alerts/test"); toast(x.message, "ok"); } }),
        ] : null,
        flush: true,
        body: h("div", null, !r.settings.webhook ? h("div", { style: { padding: "12px 16px 0" } }, note("Configure um webhook (Slack, Teams, Google Chat, Mattermost ou Discord) em Fontes & configurações para receber os alertas.")) : null,
          table([
            { label: "Quando", render: (a) => h("span", { class: "muted nowrap", text: fmtRel(a.created_at) }) },
            { label: "Tipo", render: (a) => badge({ anomaly: "anomalia", budget: "orçamento", forecast: "previsão" }[a.kind] || a.kind, a.kind === "anomaly" ? "warn" : "info") },
            { label: "Mensagem", render: (a) => h("span", { class: "small", text: a.message }) },
            { label: "Entrega", render: (a) => a.delivered ? badge("enviado", "ok") : a.error ? h("span", { class: "small", title: a.error }, badge("falhou", "err")) : badge("só no painel", "") },
          ], r.alerts, { empty: emptyState("bell", "Nenhum alerta gerado", "") })) }));
  }
  load();
}

function stat(l, n, sub) {
  return h("div", { class: "stat" }, h("div", { class: "l", text: l }), h("div", { class: "n", text: n }), h("div", { class: "small muted", text: sub }));
}
