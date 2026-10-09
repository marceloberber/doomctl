import { get, post, saveText } from "../../api.js";
import { h, mount, card, btn, loading, table, badge, modal, toast, toastErr, select, tabs, emptyState, icon, input, runJob, terminal, checkbox, confirmDialog } from "../../ui.js";
import { fmtMoney, sevBadge, CATEGORIES, PROVIDER_LABELS, codeBlock, note, statTile, hbars, askText, kpiMoney } from "./common.js";

const SUB = [
  { id: "recs", label: "Recomendações", icon: "bolt" },
  { id: "commit", label: "Savings Plans & reservas", icon: "calendar" },
  { id: "usage", label: "Rede & armazenamento", icon: "network" },
  { id: "inventory", label: "Inventário", icon: "server" },
  { id: "policies", label: "Violações de políticas", icon: "shield" },
];

export async function render(root, ctx) {
  let sub = ctx.query.get("sub") || "recs";
  const body = h("div");
  mount(root, h("div", { class: "stack" }, tabs(SUB, sub, (id) => { sub = id; show(); }), body));
  const cache = {};
  async function show() {
    mount(body, loading());
    try {
      if (sub === "recs") await recs(body, ctx, cache);
      else if (sub === "commit") await commitments(body);
      else if (sub === "usage") await usage(body, ctx);
      else if (sub === "inventory") await inventory(body);
      else await policies(body, ctx, cache);
    } catch (e) { mount(body, ""); toastErr(e); }
  }
  show();
}

// ---------- recomendações ----------
async function recs(body, ctx, cache) {
  const r = await get("/api/finops/findings");
  cache.findings = r;
  const cur = r.currency;
  const f = { category: "", severity: "", provider: "", dismissed: false, q: "" };
  const selected = new Set();
  const listBox = h("div");
  const catSel = select([["", "Todas as categorias"], ...Object.entries(CATEGORIES).filter(([k]) => r.findings.some((x) => x.category === k))], "", { "aria-label": "Categoria" });
  const sevSel = select([["", "Todas as severidades"], ["high", "Alta"], ["medium", "Média"], ["low", "Baixa"], ["info", "Info"]], "", { "aria-label": "Severidade" });
  const provSel = select([["", "Todos os provedores"], ...[...new Set(r.findings.map((x) => x.provider).filter(Boolean))].map((p) => [p, PROVIDER_LABELS[p] || p])], "", { "aria-label": "Provedor" });
  const search = input({ placeholder: "Buscar recurso ou regra", type: "search", "aria-label": "Buscar" });
  const showDis = checkbox("Mostrar descartadas", false);
  [catSel, sevSel, provSel].forEach((s) => s.addEventListener("change", () => { f.category = catSel.value; f.severity = sevSel.value; f.provider = provSel.value; draw(); }));
  search.addEventListener("input", () => { f.q = search.value.toLowerCase(); draw(); });
  showDis.input.addEventListener("change", () => { f.dismissed = showDis.input.checked; draw(); });

  const scriptBtn = btn("Gerar script (dry-run)", { icon: "terminal", cls: "sm", onClick: async () => {
    if (!selected.size) return toast("Selecione recomendações com comando de remediação", "err");
    const x = await post("/api/finops/remediation/script", { keys: [...selected] });
    modal({ title: `Script de remediação (${x.count})`, size: "wide", body: h("div", { class: "stack sm" },
      note("Revise antes de executar. Comandos destrutivos estão com --dry-run: remova a opção somente após validar."), codeBlock(x.script, { lang: "bash" })),
      actions: [{ label: "Fechar" }, { label: "Baixar .sh", primary: true, icon: "download", onClick: () => { saveText(x.script, "doomctl-finops-remediacao.sh", "text/x-shellscript"); } }] });
  } });

  const cats = Object.entries(r.summary).sort((a, b) => b[1].savings - a[1].savings);
  mount(body, h("div", { class: "stack" },
    h("div", { class: "stat-row" },
      statTile("Economia potencial", kpiMoney(r.total_savings, cur), "por mês, recomendações ativas"),
      statTile("Por ano", kpiMoney(r.total_savings * 12, cur), "se todas forem aplicadas"),
      statTile("Recomendações", String(r.findings.filter((x) => !x.dismissed).length), `${r.resources} recursos no inventário`),
      statTile("Violações de políticas", String(r.violations.length), r.violations.some((v) => v.action === "block") ? "há bloqueios" : "somente avisos")),
    cats.length ? h("div", { class: "fo-chips" }, cats.map(([k, v]) => h("button", { type: "button", class: "fo-chip", onclick: () => { catSel.value = k; f.category = k; draw(); } },
      h("b", { text: CATEGORIES[k] || k }), h("span", { text: `${v.count} · ${fmtMoney(v.savings, cur, { compact: true })}/mês` })))) : null,
    card({ title: "Recomendações", subtitle: "Ordenadas pela economia mensal estimada. Valores do catálogo de preços (estimativas) ou do AWS Cost Explorer.",
      actions: [catSel, sevSel, provSel, search, showDis, scriptBtn], flush: true, body: listBox })));

  function draw() {
    const rows = r.findings.filter((x) => (f.dismissed || !x.dismissed) && (!f.category || x.category === f.category) && (!f.severity || x.severity === f.severity)
      && (!f.provider || x.provider === f.provider) && (!f.q || (x.name + " " + x.title + " " + x.resource_id + " " + x.rule).toLowerCase().includes(f.q)));
    mount(listBox, table([
      { label: "", render: (x) => x.remediation ? h("input", { type: "checkbox", "aria-label": "Selecionar", checked: selected.has(x.key), onchange: (e) => { e.target.checked ? selected.add(x.key) : selected.delete(x.key); } }) : null },
      { label: "Recomendação", render: (x) => h("div", null, h("strong", { text: x.title }), h("div", { class: "small muted", text: x.name })) },
      { label: "Categoria", render: (x) => badge(CATEGORIES[x.category] || x.category, "outline") },
      { label: "Local", render: (x) => h("span", { class: "small nowrap" }, h("b", { text: PROVIDER_LABELS[x.provider] || x.provider }), " ", h("span", { class: "muted", text: x.region || "" })) },
      { label: "Severidade", render: (x) => sevBadge(x.severity) },
      { label: "Economia/mês", cls: "right", render: (x) => x.monthly_savings != null ? h("b", { class: "up", title: x.savings_note || "", text: fmtMoney(x.monthly_savings, cur) + (x.savings_note ? " *" : "") }) : h("span", { class: "faint", text: "—" }) },
      { label: "", render: (x) => h("div", { class: "row nw" }, x.dismissed ? badge("descartada", "") : null, x.action ? h("span", { title: "Ação automatizada disponível" }, icon("bolt", "sm")) : null) },
    ], rows, { onRowClick: (x) => detail(ctx, x, cur, () => recs(body, ctx, cache)),
      empty: emptyState("checkCircle", "Nenhuma recomendação", r.resources ? "Nada a otimizar com os filtros atuais." : "Sincronize o inventário (AWS) ou importe um CSV de recursos em Fontes & configurações.") }));
  }
  draw();
}

function detail(ctx, x, cur, reload) {
  const actions = [{ label: "Fechar" }];
  if (ctx.canManage) {
    actions.unshift({ label: x.dismissed ? "Restaurar" : "Descartar", icon: x.dismissed ? "refresh" : "x", onClick: async () => {
      let reason = "";
      if (!x.dismissed) { reason = await askText("Descartar recomendação", "Motivo (opcional)", "ex.: necessário para DR"); if (reason === null) return false; }
      await post("/api/finops/findings/dismiss", { key: x.key, reason, restore: !!x.dismissed });
      toast(x.dismissed ? "Recomendação restaurada" : "Recomendação descartada", "ok"); reload();
    } });
  }
  const remediate = x.action && ctx.canManage ? h("div", { class: "fo-remediate" },
    h("div", { class: "spread" }, h("div", null, h("strong", null, icon("bolt", "sm"), " Remediação automatizada"),
      h("p", { class: "small muted", text: "Executa a ação pela API da AWS com as credenciais da fonte. Sempre simule antes; a execução exige confirmar o ID do recurso." })),
    h("div", { class: "row" },
      btn("Simular (dry-run)", { icon: "eye", cls: "sm", onClick: () => runAction(x, true) }),
      btn("Executar", { icon: "play", cls: "sm danger", onClick: () => runAction(x, false) })))) : null;
  modal({ title: x.title, size: "wide", actions, body: h("div", { class: "stack sm" },
    h("div", { class: "row" }, sevBadge(x.severity), badge(CATEGORIES[x.category] || x.category, "outline"), badge(`${PROVIDER_LABELS[x.provider] || x.provider} ${x.region || ""}`, "")),
    h("dl", { class: "kv" },
      h("dt", { text: "Recurso" }), h("dd", { text: x.name }),
      h("dt", { text: "Tipo" }), h("dd", { text: x.resource_type || "—" }),
      h("dt", { text: "Economia estimada" }), h("dd", { text: x.monthly_savings != null ? `${fmtMoney(x.monthly_savings, cur)}/mês · ${fmtMoney(x.monthly_savings * 12, cur)}/ano` : "não estimada" }),
      x.savings_note ? [h("dt", { text: "Premissa" }), h("dd", { text: x.savings_note })] : null),
    x.detail ? h("p", { text: x.detail }) : null,
    h("div", { class: "alert info" }, h("div", { class: "alert-icon" }, icon("checkCircle")), h("div", null, h("strong", { text: "Recomendação" }), h("p", { text: x.recommendation }))),
    x.remediation ? h("div", null, h("div", { class: "small muted", style: { margin: "6px 0" }, text: "Comandos sugeridos (revise antes de executar):" }), codeBlock(x.remediation, { lang: "bash" })) : null,
    remediate,
    ctx.can("ai") ? h("div", null, btn("Perguntar ao Atlas", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops", filename: "recomendacao.txt",
      content: `${x.title}\nRecurso: ${x.name} (${x.provider} ${x.region})\n${x.detail}\nRecomendação: ${x.recommendation}\n${x.remediation || ""}`,
      prompt: "Avalie os riscos desta otimização de custo e descreva um plano seguro de execução e rollback.", send: true }) })) : null) });
}

async function runAction(x, dry) {
  let confirm = "";
  if (!dry) {
    const okc = await confirmDialog({ title: "Executar remediação", danger: true, confirmText: "Executar", requireText: x.resource_id,
      message: `${x.title}. A ação será executada na AWS com as credenciais da fonte e pode ser irreversível.` });
    if (!okc) return;
    confirm = x.resource_id;
  }
  const term = terminal({ title: dry ? "simulação" : "remediação", height: "220px" });
  modal({ title: (dry ? "Simulação: " : "Execução: ") + x.title, size: "wide", body: term.el, actions: [{ label: "Fechar" }] });
  try {
    const res = await post("/api/finops/remediation/execute", { key: x.key, dry_run: dry, confirm });
    await runJob(res, term);
  } catch (e) { term.write(`[doomctl] ${e.message}\n`); term.flush(); }
}

// ---------- compromissos ----------
async function commitments(body) {
  const c = await get("/api/finops/commitments");
  const cur = c.currency;
  mount(body, h("div", { class: "stack" },
    c.notes.map((n) => note(n)),
    c.groups.length ? c.groups.map((g) => card({ title: g.label, subtitle: `Serviços: ${g.services.join(", ")}`,
      body: h("div", { class: "stack" },
        h("div", { class: "stat-row" },
          statTile("Média diária", kpiMoney(g.daily_avg, cur), "últimos 30 dias"),
          statTile("Base estável (P10)", kpiMoney(g.daily_p10, cur), "por dia"),
          statTile("Base por hora", fmtMoney(g.baseline_hourly, cur, { digits: 3 }), "sob demanda")),
        h("div", { class: "fo-mini-bars", role: "img", "aria-label": "Gasto diário dos últimos 30 dias com a linha da base P10" },
          (() => { const max = Math.max(1e-9, ...g.daily); return g.daily.map((v) => h("i", { style: { height: (v / max) * 100 + "%" }, title: fmtMoney(v, cur) })); })(),
          h("b", { class: "p10", style: { bottom: (g.daily_p10 / Math.max(1e-9, ...g.daily)) * 100 + "%" } })),
        table([
          { label: "Prazo", key: "term" },
          { label: "Desconto (premissa)", render: (o) => Math.round(o.discount * 100) + "%" },
          { label: "Compromisso por hora", cls: "right", render: (o) => fmtMoney(o.hourly_commit, cur, { digits: 3 }) },
          { label: "Economia/mês", cls: "right", render: (o) => h("b", { class: "up", text: fmtMoney(o.monthly_savings, cur) }) },
          { label: "Economia/ano", cls: "right", render: (o) => fmtMoney(o.monthly_savings * 12, cur) },
        ], g.options),
        h("p", { class: "small", text: g.recommendation })) }))
      : card({ body: emptyState("calendar", "Sem consumo elegível", "Não há gasto de computação/bancos sob demanda nos últimos 30 dias para analisar compromissos.") })));
}

// ---------- rede & armazenamento ----------
async function usage(body, ctx) {
  const u = await get("/api/finops/usage?days=30");
  const cur = u.currency;
  if (!u.has_data) {
    return mount(body, card({ body: emptyState("network", "Sem detalhamento de uso", "Ative \"Detalhar rede e armazenamento\" na fonte AWS/OCI e sincronize, ou importe um CSV com a coluna usage_type.",
      ctx.canManage ? btn("Abrir fontes", { icon: "settings", onClick: () => ctx.goTab("fontes") }) : null) }));
  }
  mount(body, h("div", { class: "stack" },
    h("div", { class: "grid g2" },
      card({ title: "Rede", subtitle: "Últimos 30 dias por categoria · variação vs 30 dias anteriores", body: hbars(u.network, cur) }),
      card({ title: "Armazenamento", subtitle: "Últimos 30 dias por categoria", body: hbars(u.storage, cur) })),
    card({ title: "Recomendações de rede e armazenamento", flush: true, body: table([
      { label: "Recomendação", render: (x) => h("div", null, h("strong", { text: x.title }), h("div", { class: "small muted", text: x.detail })) },
      { label: "O que fazer", render: (x) => h("span", { class: "small", text: x.recommendation }) },
      { label: "Economia", cls: "right", render: (x) => x.monthly_savings != null ? h("b", { class: "up", title: x.savings_note, text: fmtMoney(x.monthly_savings, cur) + " *" }) : h("span", { class: "faint small", text: x.savings_note || "—" }) },
    ], u.findings, { empty: emptyState("checkCircle", "Nada a destacar", "Custos de rede e armazenamento abaixo dos limites de alerta.") }) }),
    h("div", { class: "grid g2" },
      card({ title: "Principais tipos de uso — rede", body: hbars(u.top_network, cur, { limit: 10 }) }),
      card({ title: "Principais tipos de uso — armazenamento", body: hbars(u.top_storage, cur, { limit: 10 }) }))));
}

// ---------- inventário ----------
async function inventory(body) {
  const r = await get("/api/finops/resources");
  const cur = r.currency;
  let q = "", type = "";
  const types = [...new Set(r.resources.map((x) => x.type))];
  const tSel = select([["", "Todos os tipos"], ...types.map((t) => [t, t])], "", { "aria-label": "Tipo" });
  const search = input({ type: "search", placeholder: "Buscar por ID, nome, tag", "aria-label": "Buscar" });
  const box = h("div");
  tSel.addEventListener("change", () => { type = tSel.value; draw(); });
  search.addEventListener("input", () => { q = search.value.toLowerCase(); draw(); });
  const total = r.resources.reduce((s, x) => s + (x.estimated || 0), 0);
  mount(body, card({ title: `Inventário (${r.resources.length})`, subtitle: `Custo mensal estimado: ${fmtMoney(total, cur)} · tags obrigatórias: ${r.required_tags.join(", ") || "nenhuma"}`,
    actions: [tSel, search], flush: true, body: box }));
  function draw() {
    const rows = r.resources.filter((x) => (!type || x.type === type) && (!q || JSON.stringify([x.resource_id, x.name, x.tags, x.sku]).toLowerCase().includes(q)));
    mount(box, table([
      { label: "Recurso", render: (x) => h("div", null, h("strong", { text: x.name || x.resource_id }), h("div", { class: "small muted mono", text: x.resource_id })) },
      { label: "Tipo", render: (x) => h("span", { class: "small" }, x.type, x.sku ? h("span", { class: "muted", text: " · " + x.sku }) : null, x.size_gb ? h("span", { class: "muted", text: ` · ${x.size_gb} GB` }) : null) },
      { label: "Local", render: (x) => h("span", { class: "small nowrap", text: `${PROVIDER_LABELS[x.provider] || x.provider} ${x.region}` }) },
      { label: "Estado", render: (x) => badge(x.state || "—", x.state === "running" || x.state === "in-use" || x.state === "available" && x.type === "database" ? "ok" : x.attached === false ? "warn" : "") },
      { label: "CPU média/máx.", render: (x) => x.cpu_avg != null ? h("span", { class: "small", text: `${x.cpu_avg.toFixed(1)}% / ${x.cpu_max != null ? x.cpu_max.toFixed(1) + "%" : "—"}` }) : h("span", { class: "faint", text: "—" }) },
      { label: "Tags faltando", render: (x) => x.missing_tags.length ? h("span", { class: "small", style: { color: "var(--warn)" }, text: x.missing_tags.join(", ") }) : h("span", { class: "faint", text: "—" }) },
      { label: "Custo/mês", cls: "right", render: (x) => x.estimated != null ? fmtMoney(x.estimated, cur) : h("span", { class: "faint", title: "Sem preço no catálogo", text: "—" }) },
    ], rows, { empty: emptyState("server", "Inventário vazio", "Use \"Inventário\" em uma fonte AWS ou importe um CSV de recursos.") }));
  }
  draw();
}

// ---------- políticas ----------
async function policies(body, ctx, cache) {
  const r = cache.findings || await get("/api/finops/findings");
  mount(body, card({ title: "Violações de políticas no inventário", subtitle: "As mesmas políticas são aplicadas à estimativa de IaC e ao cost-check de CI/CD.",
    actions: btn("Gerenciar políticas", { icon: "shield", cls: "sm", onClick: () => ctx.goTab("iac", "?sub=policies") }), flush: true,
    body: table([
      { label: "Política", render: (v) => h("strong", { text: v.policy }) },
      { label: "Ação", render: (v) => v.action === "block" ? badge("bloquear", "err") : badge("avisar", "warn") },
      { label: "Recurso", render: (v) => h("span", { class: "small", text: v.name }) },
      { label: "Motivo", render: (v) => h("span", { class: "small", text: v.message }) },
    ], r.violations, { empty: emptyState("checkCircle", "Nenhuma violação", "O inventário atende às políticas ativas.") }) }));
}
