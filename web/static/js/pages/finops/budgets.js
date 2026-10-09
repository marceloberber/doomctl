import { get, post, put, del } from "../../api.js";
import { h, mount, card, btn, loading, badge, modal, confirmDialog, formBuilder, toast, toastErr, emptyState, icon } from "../../ui.js";
import { fmtMoney, budgetMeter, sparkline, note } from "./common.js";

const SCOPE_LABELS = { all: "Todo o custo", provider: "Provedor", source: "Fonte", account: "Conta", service: "Serviço", region: "Região",
  project: "Projeto (tag)", environment: "Ambiente (tag)", team: "Equipe" };
const STATUS = { ok: ["Dentro do orçamento", "ok"], warning: ["Limite de alerta atingido", "warn"], exceeded: ["Excedido", "err"],
  forecast_exceeds: ["Previsão acima do limite", "warn"], no_rate: ["Sem taxa de câmbio", ""] };

export async function render(root, ctx) {
  const out = h("div", { class: "stack" }, loading());
  mount(root, out);
  async function load() {
    let r;
    try { r = await get("/api/finops/budgets"); } catch (e) { mount(out, ""); return toastErr(e); }
    const cur = r.currency;
    const totalBudget = r.budgets.filter((b) => b.scope_type === "all").reduce((s, b) => s + b.amount_base, 0);
    mount(out,
      card({ title: "Orçamentos mensais", subtitle: "Gasto do mês até ontem e previsão de fechamento (regressão + sazonalidade semanal). Alertas por limite e por previsão.",
        actions: ctx.canManage ? btn("Novo orçamento", { icon: "plus", cls: "primary sm", onClick: () => edit(ctx, null, cur, load) }) : null,
        body: r.budgets.length ? h("div", { class: "fo-budgets" }, r.budgets.map((b) => budgetRow(ctx, b, cur, load)))
          : emptyState("calendar", "Nenhum orçamento", "Crie orçamentos para o custo total, um provedor, serviço, projeto, ambiente ou equipe.",
            ctx.canManage ? btn("Criar orçamento", { icon: "plus", cls: "primary", onClick: () => edit(ctx, null, cur, load) }) : null) }),
      totalBudget ? null : note("Dica: crie um orçamento com escopo \"Todo o custo\" para acompanhar o fechamento do mês e receber alerta de previsão."));
  }
  load();
}

function budgetRow(ctx, b, cur, reload) {
  const [label, kind] = STATUS[b.status] || [b.status, ""];
  const scope = b.scope_type === "all" ? SCOPE_LABELS.all : `${SCOPE_LABELS[b.scope_type] || b.scope_type}: ${b.scope_value}`;
  return h("div", { class: "fo-budget" },
    h("div", { class: "spread" },
      h("div", null, h("strong", { text: b.name }), h("div", { class: "small muted", text: scope + (b.currency !== cur ? ` · definido em ${fmtMoney(b.amount, b.currency)}` : "") })),
      h("div", { class: "row" }, badge(label, kind, true),
        b.notify ? h("span", { class: "faint", title: "Alertas ativados" }, icon("bell", "sm")) : null,
        ctx.canManage ? btn("", { icon: "edit", cls: "sm ghost", title: "Editar", onClick: () => edit(ctx, b, cur, reload) }) : null,
        ctx.canManage ? btn("", { icon: "trash", cls: "sm ghost", title: "Excluir", onClick: async () => {
          if (!(await confirmDialog({ title: "Excluir orçamento", message: `Excluir "${b.name}"?`, danger: true, confirmText: "Excluir" }))) return;
          await del(`/api/finops/budgets/${b.id}`); toast("Orçamento excluído", "ok"); reload();
        } }) : null)),
    b.status === "no_rate" ? note(`Cadastre a taxa de ${b.currency} → ${cur} em Fontes & configurações.`, "warn") : h("div", { class: "fo-budget-body" },
      h("div", { class: "grow" },
        budgetMeter(b.pct_actual, b.pct_forecast),
        h("div", { class: "spread small", style: { marginTop: "6px" } },
          h("span", null, h("b", { text: fmtMoney(b.actual, cur) }), h("span", { class: "muted", text: ` gastos (${b.pct_actual.toFixed(0)}%)` })),
          h("span", { class: "muted" }, "previsão ", h("b", { text: fmtMoney(b.forecast, cur) }), ` (${b.pct_forecast.toFixed(0)}%)`),
          h("span", { class: "muted" }, "limite ", h("b", { text: fmtMoney(b.amount_base, cur) })))),
      h("div", { class: "fo-budget-spark", title: "Gasto acumulado no mês" }, sparkline(b.cumulative, { label: "Gasto acumulado no mês" }),
        h("span", { class: "faint small", text: "limites: " + (b.thresholds || []).map((t) => t + "%").join(", ") }))));
}

function edit(ctx, b, cur, reload) {
  const d = ctx.shared.dims || {};
  const opts = { provider: d.providers, account: d.accounts, service: d.services, region: d.regions, project: d.projects, environment: d.environments, team: d.teams,
    source: (d.sources || []).map((s) => [String(s.id), s.name]) };
  const form = formBuilder([
    { name: "name", label: "Nome", placeholder: "Produção — mensal" },
    { name: "scope_type", label: "Escopo", type: "select", options: Object.entries(SCOPE_LABELS) },
    { name: "scope_value", label: "Valor do escopo", placeholder: "ex.: Amazon Relational Database Service", hint: "Para escopos diferentes de \"Todo o custo\"." },
    { name: "amount", label: "Limite mensal", type: "number" },
    { name: "currency", label: "Moeda", mono: true, value: cur },
    { name: "thresholds", label: "Alertar em (% do limite)", value: "80,100", mono: true },
    { name: "notify", label: "Enviar alertas (painel e webhook)", type: "bool", value: true, span: true },
  ], b ? { ...b, thresholds: (b.thresholds || []).join(",") } : {});
  const dl = h("datalist", { id: "fo-scope-values" });
  const sv = form.ctrl("scope_value");
  sv.setAttribute("list", "fo-scope-values");
  const fill = () => {
    const t = form.ctrl("scope_type").value;
    mount(dl, (opts[t] || []).map((o) => Array.isArray(o) ? h("option", { value: o[0] }, o[1]) : h("option", { value: o })));
    sv.disabled = t === "all";
  };
  form.ctrl("scope_type").addEventListener("change", fill);
  fill();
  modal({ title: b ? "Editar orçamento" : "Novo orçamento", body: h("div", null, form, dl), actions: [
    { label: "Cancelar" },
    { label: "Salvar", primary: true, onClick: async () => {
      const v = form.values();
      const body = { ...v, amount: Number(v.amount), thresholds: String(v.thresholds).split(/[,; ]+/).map(Number).filter((n) => n > 0) };
      try {
        if (b) await put(`/api/finops/budgets/${b.id}`, body); else await post("/api/finops/budgets", body);
        toast("Orçamento salvo", "ok"); reload(); return true;
      } catch (e) { toastErr(e); return false; }
    } },
  ] });
}
