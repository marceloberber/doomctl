import { get, post, put, del, download } from "../../api.js";
import { h, mount, card, btn, loading, table, badge, input, select, field, toast, toastErr, emptyState, tabs, kvEditor, editor, markdown, modal, confirmDialog, checkbox, formBuilder } from "../../ui.js";
import { fmtMoney, codeBlock, note, statTile, fmtPct, kpiMoney } from "./common.js";

const SAMPLE = `provider "aws" {
  region = "us-east-1"
}

resource "aws_instance" "web" {
  count         = 3
  instance_type = "m5.large"
  root_block_device {
    volume_size = 50
    volume_type = "gp2"
  }
  tags = { Project = "portal", Environment = "prod", Owner = "web" }
}

resource "aws_nat_gateway" "main" {}
`;

export async function render(root, ctx) {
  let sub = ctx.query.get("sub") || "estimate";
  const body = h("div");
  mount(root, h("div", { class: "stack" }, tabs([
    { id: "estimate", label: "Estimativa de custo", icon: "calc" },
    { id: "ci", label: "Verificação CI/CD", icon: "checkCircle" },
    { id: "policies", label: "Políticas de custo", icon: "shield" },
  ], sub, (id) => { sub = id; show(); }), body));
  let projects = [];
  if (ctx.can("opentofu")) { try { projects = await get("/api/tofu/projects"); } catch (_) { projects = []; } }
  function show() {
    mount(body, loading());
    (sub === "estimate" ? estimate(body, ctx, projects) : sub === "ci" ? ci(body, ctx, projects) : policies(body, ctx)).catch((e) => { mount(body, ""); toastErr(e); });
  }
  show();
}

function sourcePicker(projects, { label = "Origem", initialCode = SAMPLE } = {}) {
  const sel = select([["", "Colar código .tf"], ...projects.map((p) => [String(p.id), `Projeto OpenTofu: ${p.name}`])], projects.length ? String(projects[0].id) : "", { "aria-label": label });
  const ed = editor({ value: initialCode, rows: 14 });
  const wrap = h("div", { class: "stack sm" }, sel, ed.el || ed);
  const sync = () => { (ed.el || ed).style.display = sel.value ? "none" : ""; };
  sel.addEventListener("change", sync);
  sync();
  wrap.payload = () => sel.value ? { id: Number(sel.value) } : { files: { "main.tf": (ed.textarea || ed.querySelector("textarea")).value } };
  wrap.projectId = () => Number(sel.value || 0);
  return wrap;
}

async function estimate(body, ctx, projects) {
  const src = sourcePicker(projects);
  const vars = kvEditor([], { keyPlaceholder: "variável", valuePlaceholder: "valor" });
  const region = input({ placeholder: "(do provider)", class: "mono", "aria-label": "Região" });
  const env = input({ placeholder: "ex.: prod", "aria-label": "Ambiente" });
  const out = h("div", { class: "stack" });
  const run = async () => {
    const p = src.payload();
    const req = { project_id: p.id || 0, files: p.files, region: region.value.trim(), env: env.value.trim(),
      vars: Object.fromEntries(vars.values().map((x) => [x.key, x.value])) };
    mount(out, loading("Estimando..."));
    const r = await post("/api/finops/iac/estimate", req);
    showEstimate(out, ctx, r, req);
  };
  mount(body, h("div", { class: "stack" },
    card({ title: "Estimar custo antes do apply", subtitle: "Leitura estática do código OpenTofu/Terraform (sem plan e sem credenciais), com o catálogo de preços e as políticas de custo.",
      body: h("div", { class: "grid fo-g21" }, src, h("div", { class: "stack sm" },
        field("Região (sobrescreve)", region), field("Ambiente para políticas", env, "Usado quando o recurso não tem a tag Environment"),
        h("div", null, h("span", { class: "lbl small muted", text: "Variáveis (sobrescrevem defaults/tfvars)" }), vars),
        btn("Estimar custo", { icon: "calc", cls: "primary", onClick: run }))) }),
    out));
}

function showEstimate(out, ctx, r, req) {
  const e = r.estimate, cur = e.currency;
  const sel = new Set(e.fixes.map((f) => f.id));
  const fixSavings = e.fixes.reduce((s, f) => s + (f.savings || 0), 0);
  const canFix = req.project_id && ctx.canManage && ctx.can("opentofu", "manage");
  mount(out,
    e.notes.map((n) => note(n, n.includes("parcial") ? "warn" : "info")),
    h("div", { class: "stat-row" },
      statTile("Custo mensal estimado", kpiMoney(e.total, cur), `região ${e.region || "?"}${e.env ? " · ambiente " + e.env : ""}`),
      statTile("Por ano", kpiMoney(e.total * 12, cur), ""),
      statTile("Recursos com custo", String(e.items.length), `${e.free.length} sem custo fixo · ${e.unsupported.length} sem modelo`),
      statTile("Violações de política", String(e.violations.length), e.violations.some((v) => v.action === "block") ? "há bloqueios" : "")),
    card({ title: "Recursos", flush: true, actions: ctx.can("ai") ? btn("Revisar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops", filename: "estimativa-iac.txt",
      content: e.items.map((i) => `${i.address} x${i.count}: ${i.description} → ${i.monthly != null ? i.monthly : "sem preço"} ${cur}/mês ${i.note || ""}`).join("\n"),
      prompt: "Sugira uma arquitetura mais econômica (tipos/tamanhos, Graviton, Spot, armazenamento, NAT) mantendo desempenho e disponibilidade para estes recursos.", send: true }) }) : null,
    body: table([
      { label: "Recurso", render: (i) => h("div", null, h("strong", { class: "mono", text: i.address }), h("div", { class: "small muted", text: `${i.file}:${i.line}` })) },
      { label: "Configuração", render: (i) => h("div", { class: "small" }, h("div", { text: i.description }), i.note ? h("div", { class: "muted", text: i.note }) : null) },
      { label: "Qtd.", key: "count", cls: "right" },
      { label: "Componentes", render: (i) => h("div", { class: "small" }, i.components.map((c) => h("div", null, `${c.label}: `, c.monthly != null ? h("span", { text: fmtMoney(c.monthly, cur) }) : h("span", { style: { color: "var(--warn)" }, text: "sem preço (" + c.sku + ")" })))) },
      { label: "Mensal", cls: "right", render: (i) => i.monthly != null ? h("b", { text: fmtMoney(i.monthly, cur) }) : h("span", { class: "faint", text: i.description === "custo por uso" ? "por uso" : "—" }) },
    ], e.items, { empty: emptyState("layers", "Nenhum recurso com custo", "") }) }),
    e.violations.length ? card({ title: "Políticas de custo", flush: true, body: table([
      { label: "Política", render: (v) => h("strong", { text: v.policy }) },
      { label: "Ação", render: (v) => v.action === "block" ? badge("bloquear", "err") : badge("avisar", "warn") },
      { label: "Recurso", render: (v) => h("span", { class: "mono small", text: v.resource_id }) },
      { label: "Motivo", render: (v) => h("span", { class: "small", text: v.message }) },
    ], e.violations) }) : null,
    e.fixes.length ? card({ title: "Otimizações aplicáveis ao código", subtitle: fixSavings ? `Economia estimada: ${fmtMoney(fixSavings, cur)}/mês` : "gp2 → gp3 e gerações atuais de instância",
      actions: canFix ? btn("Aplicar ao projeto", { icon: "check", cls: "primary sm", onClick: async () => {
        if (!sel.size) return toast("Selecione ao menos uma otimização", "err");
        if (!(await confirmDialog({ title: "Aplicar otimizações", message: `Alterar ${sel.size} linha(s) no projeto OpenTofu? O plano atual será invalidado; rode plan antes do apply.` }))) return;
        const x = await post("/api/finops/iac/fix", { project_id: req.project_id, fixes: [...sel], vars: req.vars, region: req.region, env: req.env });
        toast(`${x.applied} otimização(ões) aplicada(s)`, "ok");
        const r2 = await post("/api/finops/iac/estimate", req);
        showEstimate(out, ctx, r2, req);
      } }) : null,
      flush: true, body: h("div", null, table([
        { label: "", render: (f) => { const c = checkbox("", true); c.input.addEventListener("change", () => { c.input.checked ? sel.add(f.id) : sel.delete(f.id); }); return c; } },
        { label: "Recurso", render: (f) => h("span", { class: "mono small", text: f.address }) },
        { label: "Linha", render: (f) => h("span", { class: "mono small", text: `${f.file}:${f.line}` }) },
        { label: "Mudança", render: (f) => h("span", { class: "mono small" }, h("del", { text: f.old }), " → ", h("b", { text: f.new })) },
        { label: "Motivo", render: (f) => h("span", { class: "small", text: f.reason }) },
        { label: "Economia", cls: "right", render: (f) => f.savings != null ? h("b", { class: "up", text: fmtMoney(f.savings, cur) }) : "—" },
      ], e.fixes), !canFix ? h("p", { class: "small muted", style: { padding: "10px 16px" }, text: req.project_id ? "Aplicar exige permissão de gerenciar FinOps e OpenTofu." : "Selecione um projeto OpenTofu salvo para aplicar as correções automaticamente." }) : null) }) : null,
    e.unsupported.length ? h("p", { class: "small muted", text: "Sem modelo de custo: " + e.unsupported.join(", ") }) : null);
}

async function ci(body, ctx, projects) {
  const base = sourcePicker(projects, { label: "Base" });
  const head = sourcePicker([], { label: "Proposta", initialCode: SAMPLE.replace("count         = 3", "count         = 6") });
  const out = h("div", { class: "stack" });
  const st = await get("/api/finops/settings");
  const run = async () => {
    const b = base.payload(), hd = head.payload();
    const x = await post("/api/finops/iac/check", { base_project_id: b.id || 0, base_files: b.files, files: hd.files });
    const ck = x.check;
    mount(out,
      h("div", { class: "alert " + (ck.pass ? "info" : "err") }, h("div", { class: "alert-icon" }, ck.pass ? "✓" : "✕"),
        h("div", null, h("strong", { text: ck.pass ? "Aprovado: dentro dos limites de custo" : "Bloqueado" }),
          h("p", { text: `${fmtMoney(ck.before, ck.currency)} → ${fmtMoney(ck.after, ck.currency)} (${ck.delta >= 0 ? "+" : ""}${fmtMoney(ck.delta, ck.currency)}/mês, ${fmtPct(ck.delta_pct)})` }))),
      card({ title: "Comentário para o pull/merge request", body: markdown(x.markdown), actions: btn("Copiar Markdown", { icon: "copy", cls: "sm", onClick: async () => { await navigator.clipboard.writeText(x.markdown); toast("Copiado", "ok"); } }) }));
  };
  const s = st.settings;
  mount(body, h("div", { class: "stack" },
    card({ title: "Simular a verificação", subtitle: `Bloqueia quando o aumento passa de ${s.iac_max_increase_pct}% e de ${fmtMoney(s.iac_max_increase_abs, s.base_currency)}/mês, ou viola política com ação "bloquear".`,
      body: h("div", { class: "stack" }, h("div", { class: "grid g2" }, h("div", null, h("h3", { class: "fo-h3", text: "Versão atual (base)" }), base), h("div", null, h("h3", { class: "fo-h3", text: "Versão proposta" }), head)),
        h("div", null, btn("Verificar", { icon: "checkCircle", cls: "primary", onClick: run }))) }),
    out,
    card({ title: "Integrar ao pipeline", subtitle: "O binário doomctl tem o subcomando cost-check, que roda offline com o catálogo exportado (preços, políticas e limites).",
      actions: btn("Exportar catálogo", { icon: "download", cls: "sm", onClick: () => download("/api/finops/prices/export", undefined, "doomctl-finops-catalog.json") }),
      body: h("div", { class: "stack sm" },
        h("p", { class: "small", text: "1. Exporte o catálogo e versione em .finops/doomctl-finops-catalog.json no repositório de infraestrutura. 2. Adicione o job abaixo. O job falha (exit 1) quando a mudança é bloqueada — exija o check na proteção da branch." }),
        h("b", { class: "small", text: "GitHub Actions" }),
        codeBlock(GHA, { lang: "yaml" }),
        h("b", { class: "small", text: "GitLab CI" }),
        codeBlock(GITLAB, { lang: "yaml" }),
        h("p", { class: "small muted", text: "Sem Docker: compile com \"go build ./cmd/doomctl\" e execute \"doomctl cost-check --help\"." })) })));
}

const GHA = `name: finops-cost-check
on: pull_request
permissions:
  contents: read
  pull-requests: write
jobs:
  cost:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { path: head }
      - uses: actions/checkout@v4
        with: { ref: \${{ github.base_ref }}, path: base }
      - name: Estimar custo da mudança
        run: |
          docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/w" -w /w \\
            registry.exemplo.com/doomctl:0.1.0 cost-check \\
            --catalog head/.finops/doomctl-finops-catalog.json \\
            --base base/infra --head head/infra --markdown cost.md
      - name: Comentar no PR
        if: always()
        env: { GH_TOKEN: \${{ github.token }} }
        run: gh pr comment \${{ github.event.pull_request.number }} --repo \${{ github.repository }} --body-file cost.md`;

const GITLAB = `finops-cost-check:
  stage: test
  image: { name: registry.exemplo.com/doomctl:0.1.0, entrypoint: [""] }
  rules: [{ if: $CI_PIPELINE_SOURCE == "merge_request_event" }]
  script:
    - git fetch origin "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"
    - mkdir -p /tmp/base && git --work-tree=/tmp/base checkout "origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME" -- infra
    - doomctl cost-check --catalog .finops/doomctl-finops-catalog.json --base /tmp/base/infra --head infra --markdown cost.md
  artifacts: { when: always, paths: [cost.md] }`;

async function policies(body, ctx) {
  const r = await get("/api/finops/policies");
  const kinds = r.kinds;
  mount(body, card({ title: "Políticas de custo", subtitle: "Avaliadas no inventário (Otimização), na estimativa de IaC e no cost-check de CI/CD. \"Bloquear\" reprova o pipeline; \"avisar\" só sinaliza.",
    actions: ctx.canManage ? btn("Nova política", { icon: "plus", cls: "primary sm", onClick: () => editPolicy(null, kinds, () => policies(body, ctx)) }) : null,
    flush: true, body: table([
      { label: "Política", render: (p) => h("div", null, h("strong", { text: p.name }), h("div", { class: "small muted", text: kinds[p.kind] || p.kind })) },
      { label: "Ambientes", render: (p) => h("span", { class: "mono small", text: p.match }) },
      { label: "Regra", render: (p) => h("span", { class: "small", text: describe(p) }) },
      { label: "Ação", render: (p) => p.action === "block" ? badge("bloquear", "err") : badge("avisar", "warn") },
      { label: "Status", render: (p) => p.enabled ? badge("ativa", "ok", true) : badge("inativa", "") },
    ], r.policies, { onRowClick: ctx.canManage ? (p) => editPolicy(p, kinds, () => policies(body, ctx)) : null,
      empty: emptyState("shield", "Nenhuma política", "Exemplos: \"produção não pode criar recursos acima de US$ 500/mês\", \"dev sem instâncias *.metal\", \"tags Project/Owner obrigatórias\".") }) }));
}

function describe(p) {
  const v = p.value || {};
  const list = (x) => (Array.isArray(x) ? x : String(x || "").split(/[,\s]+/)).filter(Boolean).join(", ");
  switch (p.kind) {
    case "max_monthly_cost": return `até ${v.amount}/mês por recurso`;
    case "deny_instance_types": return `proíbe ${list(v.patterns)}`;
    case "allowed_regions": return `somente ${list(v.regions)}`;
    case "require_tags": return `exige ${list(v.tags)}`;
    case "max_volume_gb": return `volumes até ${v.gb} GB`;
  }
  return JSON.stringify(v);
}

function editPolicy(p, kinds, reload) {
  const v = (p && p.value) || {};
  const list = (x) => (Array.isArray(x) ? x.join(", ") : x || "");
  const form = formBuilder([
    { name: "name", label: "Nome", placeholder: "Produção: até US$ 500/mês por recurso" },
    { name: "kind", label: "Tipo", type: "select", options: Object.entries(kinds) },
    { name: "match", label: "Ambientes (tag Environment, glob)", value: "*", mono: true, hint: "\"*\" = todos · ex.: prod*,production" },
    { name: "action", label: "Ação", type: "select", options: [["warn", "Avisar"], ["block", "Bloquear (CI/CD)"]] },
    { name: "param", label: "Parâmetro", mono: true, span: true, hint: "Custo: valor mensal · Tipos: padrões (*.metal, p*, x2*) · Regiões: lista · Tags: lista · Volume: GB" },
    { name: "enabled", label: "Ativa", type: "bool", value: true },
  ], p ? { ...p, param: p.kind === "max_monthly_cost" ? v.amount : p.kind === "max_volume_gb" ? v.gb : list(v.patterns || v.regions || v.tags) } : {});
  const actions = [{ label: "Cancelar" }];
  if (p) actions.push({ label: "Excluir", danger: true, onClick: async () => {
    if (!(await confirmDialog({ title: "Excluir política", message: `Excluir "${p.name}"?`, danger: true }))) return false;
    await del(`/api/finops/policies/${p.id}`); toast("Política excluída", "ok"); reload();
  } });
  actions.push({ label: "Salvar", primary: true, onClick: async () => {
    const x = form.values();
    const items = String(x.param).split(/[,;\s]+/).filter(Boolean);
    const value = { max_monthly_cost: { amount: Number(String(x.param).replace(",", ".")) }, max_volume_gb: { gb: Number(x.param) }, deny_instance_types: { patterns: items },
      allowed_regions: { regions: items }, require_tags: { tags: items } }[x.kind];
    const bodyX = { name: x.name, kind: x.kind, match: x.match, action: x.action, enabled: x.enabled, value };
    if (p) await put(`/api/finops/policies/${p.id}`, bodyX); else await post("/api/finops/policies", bodyX);
    toast("Política salva", "ok"); reload();
  } });
  modal({ title: p ? "Editar política" : "Nova política", body: form, actions });
}
