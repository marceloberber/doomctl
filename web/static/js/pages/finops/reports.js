import { post, saveText } from "../../api.js";
import { h, mount, card, btn, loading, table, markdown, toastErr, formBuilder, emptyState } from "../../ui.js";
import { fmtMoney, fmtPct, DIM_LABELS, stackedBars } from "./common.js";

function months() {
  const out = [];
  const d = new Date();
  for (let i = 0; i < 13; i++) {
    const m = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - i, 1));
    const v = m.toISOString().slice(0, 7);
    out.push([v, m.toLocaleDateString("pt-BR", { month: "long", year: "numeric", timeZone: "UTC" }) + (i === 0 ? " (parcial)" : "")]);
  }
  return out;
}

export async function render(root, ctx) {
  const form = formBuilder([
    { name: "month", label: "Mês", type: "select", options: [...months(), ["custom", "Período personalizado"]], value: months()[1][0] },
    { name: "group_by", label: "Agrupar por", type: "select", options: Object.entries(DIM_LABELS).map(([k, v]) => [k, v]), value: "team" },
    { name: "mode", label: "Modelo", type: "select", options: [["showback", "Showback (informativo)"], ["chargeback", "Chargeback (cobrança interna)"]] },
    { name: "distribute", label: "Ratear custo sem tag proporcionalmente (chargeback)", type: "bool", value: true },
    { name: "start", label: "Início (personalizado)", placeholder: "AAAA-MM-DD", mono: true },
    { name: "end", label: "Fim exclusivo (personalizado)", placeholder: "AAAA-MM-DD", mono: true },
    { name: "title", label: "Título (opcional)", span: true },
  ], {}, { cols: 3 });
  const out = h("div", { class: "stack" });
  const gen = async () => {
    const v = form.values();
    const req = { title: v.title, group_by: v.group_by, mode: v.mode, distribute: v.distribute };
    if (v.month === "custom") { req.start = v.start; req.end = v.end; } else req.month = v.month;
    mount(out, loading("Gerando relatório..."));
    try { show(await post("/api/finops/report", req)); } catch (e) { mount(out, ""); toastErr(e); }
  };
  mount(root, h("div", { class: "stack" },
    card({ title: "Relatórios de custo", subtitle: "Showback mostra o custo por equipe/projeto; chargeback atribui o valor a cobrar, com rateio do custo sem alocação. Exporte em Markdown ou CSV.",
      body: h("div", { class: "stack" }, form, h("div", null, btn("Gerar relatório", { icon: "doc", cls: "primary", onClick: gen }))) }),
    out));

  function show(r) {
    const cur = r.currency;
    const slug = `finops-${r.mode}-${r.group_by}-${r.start}`;
    const charge = r.mode === "chargeback";
    mount(out,
      card({ title: r.title, subtitle: `${r.lines.length} linhas · total ${fmtMoney(r.total, cur)} · ${fmtPct(r.delta_pct)} vs período anterior`,
        actions: [btn("Markdown", { icon: "download", cls: "sm", onClick: () => saveText(r.markdown, slug + ".md", "text/markdown") }),
          btn("CSV", { icon: "download", cls: "sm", onClick: () => saveText(r.csv, slug + ".csv", "text/csv") }),
          ctx.can("ai") ? btn("Resumo executivo com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops", filename: slug + ".md", content: r.markdown,
            prompt: "Escreva um resumo executivo (até 8 linhas) deste relatório de custos para a diretoria, com destaques, riscos e 3 ações recomendadas.", send: true }) }) : null],
        flush: true,
        body: table(charge ? [
          { label: DIM_LABELS[r.group_by], render: (l) => h("strong", { text: l.key }) },
          { label: "Custo direto", cls: "right", render: (l) => fmtMoney(l.direct, cur) },
          { label: "Rateio", cls: "right", render: (l) => fmtMoney(l.shared, cur) },
          { label: "Total a cobrar", cls: "right", render: (l) => h("b", { text: fmtMoney(l.total, cur) }) },
          { label: "%", cls: "right", render: (l) => l.share.toLocaleString("pt-BR", { maximumFractionDigits: 1 }) + "%" },
          { label: "Anterior", cls: "right", render: (l) => h("span", { class: "muted", text: fmtMoney(l.prev, cur) }) },
          { label: "Variação", cls: "right", render: (l) => h("span", { class: "small", text: fmtPct(l.delta_pct) }) },
        ] : [
          { label: DIM_LABELS[r.group_by], render: (l) => h("strong", { text: l.key }) },
          { label: "Custo", cls: "right", render: (l) => h("b", { text: fmtMoney(l.total, cur) }) },
          { label: "%", cls: "right", render: (l) => l.share.toLocaleString("pt-BR", { maximumFractionDigits: 1 }) + "%" },
          { label: "Anterior", cls: "right", render: (l) => h("span", { class: "muted", text: fmtMoney(l.prev, cur) }) },
          { label: "Variação", cls: "right", render: (l) => h("span", { class: "small", text: fmtPct(l.delta_pct) }) },
        ], r.lines, { empty: emptyState("doc", "Sem custos no período", "") }) }),
      r.series && r.series.length ? card({ title: "Custo diário no período", body: stackedBars(r.series, r.series_keys || [], cur) }) : null,
      card({ title: "Pré-visualização (Markdown)", body: markdown(r.markdown) }));
  }
}
