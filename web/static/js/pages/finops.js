// FinOps — custos de nuvem (AWS e OCI): análise, anomalias, orçamentos, otimização,
// Kubernetes, previsão, cenários, IaC, relatórios e configurações.
import { get } from "../api.js";
import { h, mount, clear, tabs, pageHead, loading, btn, toastErr } from "../ui.js";

const TABS = [
  { id: "custos", label: "Custos", icon: "chart", mod: "overview" },
  { id: "anomalias", label: "Anomalias", icon: "bell", mod: "anomalies" },
  { id: "orcamentos", label: "Orçamentos", icon: "calendar", mod: "budgets" },
  { id: "otimizacao", label: "Otimização", icon: "bolt", mod: "optimize" },
  { id: "kubernetes", label: "Kubernetes", icon: "helm", mod: "k8s" },
  { id: "previsao", label: "Previsão", icon: "activity", mod: "forecast" },
  { id: "iac", label: "IaC", icon: "layers", mod: "iac" },
  { id: "relatorios", label: "Relatórios", icon: "doc", mod: "reports" },
  { id: "fontes", label: "Fontes", icon: "settings", mod: "sources" },
];

export async function render(root, ctx) {
  const active = TABS.find((t) => t.id === ctx.rest[0]) ? ctx.rest[0] : "custos";
  const body = h("div", { class: "fo-body" });
  const shared = { dims: null, settings: null };
  shared.reloadDims = async () => {
    try { shared.dims = await get("/api/finops/dimensions"); } catch (e) { shared.dims = { sources: [], currency: "USD" }; toastErr(e); }
    return shared.dims;
  };
  const head = pageHead("FinOps", "Custos de nuvem AWS e OCI: análise, anomalias, orçamentos, otimização, Kubernetes, previsão, IaC e relatórios.",
    [ctx.can("ai") ? btn("Perguntar ao Atlas", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops",
      prompt: "Quais práticas de FinOps devo priorizar para reduzir custos na AWS e na OCI sem afetar disponibilidade?" }) }) : null],
    ["DevOps & Cloud"]);
  const bar = tabs(TABS, active, (id) => {
    history.replaceState(null, "", "#/finops/" + id);
    show(id);
  });
  bar.classList.add("fo-tabs");
  mount(root, head, bar, body);

  let seq = 0;
  async function show(id) {
    const t = TABS.find((x) => x.id === id) || TABS[0];
    const my = ++seq;
    mount(body, loading());
    try {
      if (!shared.dims) await shared.reloadDims();
      const mod = await import(`./finops/${t.mod}.js`);
      if (my !== seq) return;
      clear(body);
      await mod.render(body, { ...ctx, shared, goTab: (x, q = "") => { history.replaceState(null, "", "#/finops/" + x + q); render(root, { ...ctx, rest: [x], query: new URLSearchParams(q.replace(/^\?/, "")) }); } });
    } catch (e) {
      if (my === seq) { clear(body); toastErr(e); }
    }
  }
  show(active);
}
