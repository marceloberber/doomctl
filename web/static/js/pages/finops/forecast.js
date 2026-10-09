import { get, post, put, del } from "../../api.js";
import { h, mount, card, btn, loading, table, input, select, toast, toastErr, emptyState, confirmDialog, modal, field, tabs } from "../../ui.js";
import { fmtMoney, fmtPct, forecastChart, filterBar, filterQuery, statTile, note, kpiMoney } from "./common.js";

export async function render(root, ctx) {
  let sub = ctx.query.get("sub") || "forecast";
  const body = h("div");
  mount(root, h("div", { class: "stack" }, tabs([
    { id: "forecast", label: "Previsão", icon: "activity" },
    { id: "whatif", label: "What-if do gasto atual", icon: "calc" },
    { id: "scenarios", label: "Cenários e comparação", icon: "layers" },
  ], sub, (id) => { sub = id; show(); }), body));
  function show() {
    mount(body, loading());
    (sub === "forecast" ? forecast(body, ctx) : sub === "whatif" ? whatif(body) : scenarios(body, ctx)).catch((e) => { mount(body, ""); toastErr(e); });
  }
  show();
}

async function forecast(body, ctx) {
  const f = { source_id: "", provider: "", account: "", service: "", region: "", project: "", environment: "", team: "" };
  const filters = h("div");
  const out = h("div", { class: "stack" });
  mount(body, h("div", { class: "stack" }, filters, out));
  async function load() {
    mount(filters, filterBar(ctx.shared.dims, f, load, { period: false }));
    mount(out, loading());
    const r = await get(`/api/finops/forecast?${filterQuery(f)}`);
    const fc = r.forecast, cur = r.currency;
    if (!fc.train_days) return mount(out, card({ body: emptyState("activity", "Sem histórico", "A previsão precisa de dados de custo dos últimos dias.") }));
    mount(out,
      h("div", { class: "stat-row" },
        statTile("Mês atual até ontem", kpiMoney(fc.mtd, cur), ""),
        statTile("Fechamento previsto", kpiMoney(fc.eom, cur), `faixa ${fmtMoney(fc.eom_low, cur, { compact: true })} – ${fmtMoney(fc.eom_high, cur, { compact: true })}`),
        statTile("Pela média de 7 dias", kpiMoney(fc.run_rate_eom, cur), "run-rate (referência)"),
        statTile("Próximo mês", kpiMoney(fc.next_month, cur), `tendência ${fc.slope_day >= 0 ? "+" : ""}${fmtMoney(fc.slope_day, cur)}/dia`)),
      card({ title: "Custo diário e previsão", subtitle: `Modelo: ${fc.method} · treino com ${fc.train_days} dias`, body: forecastChart(fc.history, fc.points, cur) }),
      note("A previsão projeta a tendência recente. Mudanças planejadas (novos ambientes, migrações, compromissos) não estão no histórico: use o what-if e os cenários para estimá-las."));
  }
  await load();
}

async function whatif(body) {
  const adj = {};
  let global = 0;
  const gl = input({ type: "number", value: "0", step: "1", "aria-label": "Variação global (%)" });
  const box = h("div", { class: "stack" });
  mount(body, card({ title: "What-if do gasto atual", subtitle: "Aplique variações percentuais por serviço ao gasto dos últimos 30 dias (ex.: +20% em EC2 com crescimento de tráfego, -30% em RDS após rightsizing).",
    body: h("div", { class: "stack" }, h("div", { class: "row" }, field("Variação global (%)", gl, "Vale para os serviços sem ajuste próprio", "fo-inline"),
      btn("Recalcular", { icon: "refresh", cls: "primary sm", onClick: () => load() })), box) }));
  async function load() {
    global = Number(gl.value || 0);
    const r = await post("/api/finops/whatif", { global, adjustments: adj });
    const cur = r.currency;
    if (!r.lines.length) return mount(box, emptyState("calc", "Sem custos nos últimos 30 dias", ""));
    mount(box,
      h("div", { class: "stat-row" }, statTile("Atual (30 dias)", kpiMoney(r.base, cur), ""), statTile("Projetado", kpiMoney(r.adjusted, cur), ""),
        statTile("Diferença", (r.delta >= 0 ? "+" : "") + fmtMoney(r.delta, cur), r.base ? fmtPct((r.delta / r.base) * 100) : "")),
      table([
        { label: "Serviço", key: "service" },
        { label: "Atual", cls: "right", render: (l) => fmtMoney(l.base, cur) },
        { label: "Variação (%)", render: (l) => { const i = input({ type: "number", step: "1", value: String(adj[l.service] ?? ""), placeholder: String(global), class: "fo-num", "aria-label": `Variação de ${l.service}` });
          i.addEventListener("change", () => { if (i.value === "") delete adj[l.service]; else adj[l.service] = Number(i.value); load(); }); return i; } },
        { label: "Projetado", cls: "right", render: (l) => h("b", { text: fmtMoney(l.adjusted, cur) }) },
      ], r.lines));
  }
  gl.addEventListener("change", load);
  await load();
}

async function scenarios(body, ctx) {
  const [r, prices] = await Promise.all([get("/api/finops/scenarios"), get("/api/finops/prices")]);
  const cur = r.currency;
  const dl = h("datalist", { id: "fo-skus" }, [...new Set(prices.prices.map((p) => p.sku))].map((s) => h("option", { value: s })));
  mount(body, h("div", { class: "stack" }, dl,
    card({ title: "Cenários", subtitle: "Compare configurações (A × B): \"3 → 10 nós\", troca de tipo de instância, AWS × OCI, regiões. Preços do catálogo.",
      actions: btn(ctx.canManage ? "Novo cenário" : "Simular cenário", { icon: "plus", cls: "primary sm", onClick: () => editor(ctx, null, cur, () => scenarios(body, ctx)) }),
      flush: true, body: table([
        { label: "Cenário", render: (s) => h("div", null, h("strong", { text: s.name }), h("div", { class: "small muted", text: s.description })) },
        { label: "A (atual)", cls: "right", render: (s) => fmtMoney(s.result.a.total, cur) },
        { label: "B (proposta)", cls: "right", render: (s) => fmtMoney(s.result.b.total, cur) },
        { label: "Diferença/mês", cls: "right", render: (s) => h("b", { class: s.result.delta > 0 ? "down" : "up", text: (s.result.delta > 0 ? "+" : "") + fmtMoney(s.result.delta, cur) }) },
        { label: "%", render: (s) => h("span", { class: "small", text: fmtPct(s.result.delta_pct) }) },
        { label: "", render: (s) => s.result.a.missing + s.result.b.missing ? h("span", { class: "small", style: { color: "var(--warn)" }, text: `${s.result.a.missing + s.result.b.missing} sem preço` }) : null },
      ], r.scenarios, { onRowClick: (s) => editor(ctx, s, cur, () => scenarios(body, ctx)),
        empty: emptyState("layers", "Nenhum cenário salvo", "Monte um cenário para estimar mudanças de arquitetura antes de aplicá-las.") }) })));
}

function itemsEditor(items) {
  const rows = h("div", { class: "stack sm" });
  const add = (it = { label: "", provider: "aws", region: "", sku: "", qty: 1, usage: 0 }) => {
    const label = input({ value: it.label, placeholder: "Descrição", "aria-label": "Descrição" });
    const prov = select([["aws", "AWS"], ["oci", "OCI"], ["k8s", "Kubernetes"], ["custom", "Outro"]], it.provider, { "aria-label": "Provedor" });
    const region = input({ value: it.region, placeholder: "região (vazio = *)", class: "mono", "aria-label": "Região" });
    const sku = input({ value: it.sku, placeholder: "instance:m5.large", class: "mono", list: "fo-skus", "aria-label": "SKU" });
    const qty = input({ type: "number", value: String(it.qty ?? 1), min: "0", step: "any", class: "fo-num", "aria-label": "Quantidade" });
    const usage = input({ type: "number", value: it.usage ? String(it.usage) : "", min: "0", step: "any", class: "fo-num", placeholder: "730 h / GB", "aria-label": "Uso (horas/mês ou GB)" });
    const row = h("div", { class: "fo-item-row" }, label, prov, region, sku, qty, usage, h("button", { class: "btn sm ghost", type: "button", "aria-label": "remover", onclick: () => row.remove() }, "×"));
    row.val = () => ({ label: label.value.trim(), provider: prov.value, region: region.value.trim(), sku: sku.value.trim(), qty: Number(qty.value || 0), usage: Number(usage.value || 0) });
    rows.appendChild(row);
  };
  (items && items.length ? items : [undefined]).forEach((i) => add(i));
  const el = h("div", { class: "stack sm" }, h("div", { class: "fo-item-row head small muted" }, ["Descrição", "Provedor", "Região", "SKU do catálogo", "Qtd.", "Uso", ""].map((t) => h("span", { text: t }))),
    rows, h("div", null, btn("Adicionar item", { icon: "plus", cls: "sm", onClick: () => add() })));
  el.values = () => [...rows.children].map((r) => r.val()).filter((x) => x.sku);
  return el;
}

function editor(ctx, s, cur, reload) {
  const name = input({ value: s ? s.name : "", placeholder: "Nome do cenário" });
  const desc = input({ value: s ? s.description : "", placeholder: "Descrição (opcional)" });
  const a = itemsEditor(s ? s.a : []);
  const b = itemsEditor(s ? s.b : []);
  const regions = input({ placeholder: "us-east-1, sa-east-1, eu-west-1", class: "mono" });
  const result = h("div");
  const calc = async () => {
    const x = await post("/api/finops/scenarios/price", { a: a.values(), b: b.values(), regions: regions.value.split(/[ ,;]+/).filter(Boolean) });
    const r = x.result;
    const lines = (p) => table([
      { label: "Item", render: (l) => h("span", { class: "small", text: l.label || l.sku }) },
      { label: "Preço unitário", cls: "right", render: (l) => l.unit_price != null ? h("span", { class: "small", text: `${fmtMoney(l.unit_price, cur, { digits: 4 })}/${l.unit}` }) : h("span", { class: "small", style: { color: "var(--warn)" }, text: l.note }) },
      { label: "Mensal", cls: "right", render: (l) => l.monthly != null ? fmtMoney(l.monthly, cur) : "—" },
    ], p.lines);
    mount(result, h("div", { class: "stack sm" },
      h("div", { class: "stat-row" }, h("div", { class: "stat" }, h("div", { class: "l", text: "A" }), h("div", { class: "n", text: fmtMoney(r.a.total, cur) })),
        h("div", { class: "stat" }, h("div", { class: "l", text: "B" }), h("div", { class: "n", text: fmtMoney(r.b.total, cur) })),
        h("div", { class: "stat" }, h("div", { class: "l", text: "B − A" }), h("div", { class: "n " + (r.delta > 0 ? "down" : "up"), text: (r.delta > 0 ? "+" : "") + fmtMoney(r.delta, cur) }), h("div", { class: "small muted", text: fmtPct(r.delta_pct) }))),
      h("div", { class: "grid g2" }, h("div", null, h("b", { text: "A" }), lines(r.a)), h("div", null, h("b", { text: "B" }), lines(r.b))),
      x.regions ? h("div", null, h("b", { text: "Configuração A em outras regiões" }), table([
        { label: "Região", key: "region" }, { label: "Total", cls: "right", render: (x2) => fmtMoney(x2.total, cur) },
        { label: "Sem preço", render: (x2) => x2.missing ? String(x2.missing) : "—" }],
        Object.entries(x.regions).map(([region, p]) => ({ region, ...p })))) : null));
  };
  const actions = [{ label: "Fechar" }, { label: "Calcular", icon: "calc", onClick: async () => { await calc(); return false; } }];
  if (ctx.canManage) {
    actions.push({ label: "Salvar", primary: true, onClick: async () => {
      const bodyX = { name: name.value.trim(), description: desc.value.trim(), a: a.values(), b: b.values() };
      if (s) await put(`/api/finops/scenarios/${s.id}`, bodyX); else await post("/api/finops/scenarios", bodyX);
      toast("Cenário salvo", "ok"); reload();
    } });
    if (s) actions.splice(1, 0, { label: "Excluir", danger: true, onClick: async () => {
      if (!(await confirmDialog({ title: "Excluir cenário", message: `Excluir "${s.name}"?`, danger: true }))) return false;
      await del(`/api/finops/scenarios/${s.id}`); toast("Cenário excluído", "ok"); reload();
    } });
  }
  modal({ title: s ? s.name : "Novo cenário", size: "wide", actions, body: h("div", { class: "stack" },
    h("div", { class: "grid g2" }, field("Nome", name), field("Descrição", desc)),
    h("div", null, h("h3", { class: "fo-h3", text: "A — configuração atual" }), a),
    h("div", null, h("h3", { class: "fo-h3", text: "B — proposta" }), b),
    field("Comparar A em regiões (opcional)", regions, "Usa os preços do catálogo de cada região"),
    h("p", { class: "small muted", text: "Uso: horas/mês para preços por hora (vazio = 730 h, ligado o mês todo) ou GB para preços por GB-mês. SKUs: instance:<tipo>, volume:<tipo>, db:<classe>, nat_gateway, public_ip, eks_cluster, ocpu:<shape>, memory:<shape>, k8s:vcpu..." }),
    result) });
  if (s) calc().catch(toastErr);
}
