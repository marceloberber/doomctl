import { get, post, put, del, download } from "../../api.js";
import {
  h, mount, card, btn, loading, table, badge, modal, confirmDialog, formBuilder, field, input, select, toast, toastErr, emptyState,
  fmtRel, filePicker, jobModal, kvEditor, tabs, checkbox,
} from "../../ui.js";
import { fmtMoney, codeBlock, note, PROVIDER_LABELS } from "./common.js";

const IAM_POLICY = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "DoomctlFinOpsSomenteLeitura",
      "Effect": "Allow",
      "Action": [
        "ce:GetCostAndUsage",
        "ce:GetRightsizingRecommendation",
        "ec2:DescribeInstances",
        "ec2:DescribeVolumes",
        "ec2:DescribeAddresses",
        "ec2:DescribeSnapshots",
        "rds:DescribeDBInstances",
        "cloudwatch:GetMetricStatistics",
        "pricing:GetProducts"
      ],
      "Resource": "*"
    }
  ]
}`;

const IAM_REMEDIATION = `{
  "Sid": "DoomctlFinOpsRemediacao",
  "Effect": "Allow",
  "Action": [
    "ec2:StopInstances",
    "ec2:ModifyVolume",
    "ec2:CreateSnapshot",
    "ec2:DeleteSnapshot",
    "ec2:DeleteVolume",
    "ec2:ReleaseAddress"
  ],
  "Resource": "*"
}`;

const OCI_POLICY = "Allow group doomctl-finops to read usage-report in tenancy";

const COST_CSV = `date,provider,account,service,region,project,environment,team,cost,currency,usage_type
2026-09-01,aws,prod,Amazon EC2,us-east-1,portal,prod,Produto Web,123.45,USD,
2026-09-01,oci,tenancy,Compute,sa-saopaulo-1,erp,prod,SRE,"45,10",USD,`;

const INV_CSV = `provider,region,resource_id,type,name,sku,size_gb,state,monthly_cost,currency,cpu_avg,cpu_max,mem_avg,attached,age_days,tags
aws,us-east-1,i-0abc,instance,web-1,m5.xlarge,,running,,USD,4.2,18,35,,200,Project=portal;Environment=prod;Owner=web
aws,us-east-1,vol-0abc,volume,,gp2,500,available,,USD,,,,false,90,Project=data`;

const PRICE_CSV = `provider,region,sku,unit,price,currency
aws,us-east-1,instance:m5.large,hour,0.096,USD
aws,us-east-1,volume:gp3,gb-month,0.08,USD
oci,*,ocpu:VM.Standard.E5.Flex,hour,0.03,USD`;

export async function render(root, ctx) {
  let sub = ctx.query.get("sub") || "sources";
  const body = h("div");
  mount(root, h("div", { class: "stack" }, tabs([
    { id: "sources", label: "Fontes de custo", icon: "cloud" },
    { id: "prices", label: "Catálogo de preços", icon: "table" },
    { id: "settings", label: "Configurações", icon: "settings" },
  ], sub, (id) => { sub = id; show(); }), body));
  function show() {
    mount(body, loading());
    (sub === "sources" ? sources(body, ctx) : sub === "prices" ? prices(body, ctx) : settings(body, ctx)).catch((e) => { mount(body, ""); toastErr(e); });
  }
  show();
}

// ---------- fontes ----------
async function sources(body, ctx) {
  const r = await get("/api/finops/sources");
  const reload = async () => { await ctx.shared.reloadDims(); return sources(body, ctx); };
  const hasDemo = r.sources.some((s) => s.provider === "demo");
  const m = ctx.canManage;
  mount(body, h("div", { class: "stack" },
    card({ title: "Fontes de custo", subtitle: "AWS: Cost Explorer (custos e tags), EC2/RDS/CloudWatch (inventário). OCI: Usage API. CSV: qualquer nuvem ou fatura. Sincronização automática conforme as configurações.",
      actions: m ? [btn(hasDemo ? "Remover exemplo" : "Carregar exemplo", { icon: hasDemo ? "trash" : "play", cls: "sm", onClick: async () => {
        if (hasDemo) {
          if (!(await confirmDialog({ title: "Remover dados de exemplo", message: "Remove a fonte de demonstração, preços, orçamentos, políticas e cenários marcados como exemplo.", danger: true, confirmText: "Remover" }))) return;
          await del("/api/finops/demo"); toast("Dados de exemplo removidos", "ok");
        } else { const x = await post("/api/finops/demo"); toast(`Exemplo carregado (${x.rows} linhas de custo)`, "ok"); }
        reload();
      } }), btn("Nova fonte", { icon: "plus", cls: "primary sm", onClick: () => editSource(ctx, null, reload) })] : null,
      flush: true,
      body: table([
        { label: "Fonte", render: (s) => h("div", null, h("strong", { text: s.name }), h("div", { class: "small muted", text: s.account_label + (s.config.linked_account ? ` · conta ${s.config.linked_account}` : "") })) },
        { label: "Tipo", render: (s) => badge(PROVIDER_LABELS[s.provider] || s.provider, { aws: "warn", oci: "err", csv: "info", demo: "" }[s.provider] || "") },
        { label: "Credenciais", render: (s) => s.provider === "csv" || s.provider === "demo" ? h("span", { class: "faint", text: "—" })
          : s.has_credentials ? h("span", { class: "small" }, badge("configuradas", "ok"), " ", h("span", { class: "muted mono", text: s.credential_hint })) : badge("pendentes", "warn") },
        { label: "Última sincronização", render: (s) => h("div", { class: "small" }, s.last_sync_at ? h("span", { text: fmtRel(s.last_sync_at) }) : h("span", { class: "faint", text: "nunca" }),
          s.last_status === "failed" ? h("div", null, badge("falhou", "err")) : s.last_status === "warning" ? h("div", null, badge("com avisos", "warn")) : null,
          s.last_error ? h("div", { class: "muted", title: s.last_error, text: s.last_error.slice(0, 80) + (s.last_error.length > 80 ? "…" : "") }) : null) },
        { label: "Dados", render: (s) => h("span", { class: "small muted", text: `${s.rows.toLocaleString("pt-BR")} linhas · ${s.resources} recursos` }) },
        { label: "", render: (s) => m && s.provider !== "demo" ? h("div", { class: "row nw" },
          s.provider !== "csv" ? btn("Sincronizar", { icon: "refresh", cls: "sm", onClick: () => syncJob(s, reload) }) : null,
          btn("", { icon: "settings", cls: "sm ghost", title: "Ações", onClick: () => actions(ctx, s, reload) })) : null },
      ], r.sources, { empty: emptyState("cloud", "Nenhuma fonte", "Conecte AWS ou OCI, ou importe CSV. Para conhecer o módulo, carregue os dados de exemplo.",
        m ? btn("Nova fonte", { icon: "plus", cls: "primary", onClick: () => editSource(ctx, null, reload) }) : null) }) }),
    card({ title: "Permissões necessárias", subtitle: "Use credenciais dedicadas, de menor privilégio.", body: h("div", { class: "stack sm" },
      h("b", { class: "small", text: "AWS — política IAM somente leitura (usuário/role do doomctl)" }), codeBlock(IAM_POLICY, { lang: "json" }),
      h("p", { class: "small muted", text: "Cada requisição à API do Cost Explorer custa US$ 0,01; uma sincronização faz de 3 a 6 requisições (mais páginas em contas grandes). Ative as tags de alocação (Project, Environment) em Billing → Cost allocation tags e, para recomendações de rightsizing, habilite-as nas preferências do Cost Explorer." }),
      h("b", { class: "small", text: "AWS — remediação automatizada (opcional; habilitada por administrador na fonte)" }), codeBlock(IAM_REMEDIATION, { lang: "json" }),
      h("b", { class: "small", text: "OCI — política do grupo do usuário de API" }), codeBlock(OCI_POLICY),
      h("p", { class: "small muted", text: "Na OCI informe a home region e uma API key RSA sem passphrase. Tags definidas: Namespace.Chave; tags livres: só a chave." })) })));
}

function syncJob(s, reload) {
  return jobModal(`Sincronizar ${s.name}`, () => post(`/api/finops/sources/${s.id}/sync`, {}), { onEnd: () => reload() });
}

function actions(ctx, s, reload) {
  const isAWS = s.provider === "aws", isCloud = s.provider === "aws" || s.provider === "oci";
  const item = (label, desc, fn) => h("button", { type: "button", class: "list-item", onclick: async () => { mm.close(); try { await fn(); } catch (e) { toastErr(e); } } },
    h("div", { class: "grow" }, h("b", { text: label }), h("div", { class: "meta", text: desc })));
  const mm = modal({ title: s.name, body: h("div", { class: "fo-actions" },
    isCloud ? item("Credenciais", isAWS ? "Access key ID e secret (ou credenciais temporárias)" : "Tenancy, usuário, fingerprint, região e chave privada", () => creds(s, reload)) : null,
    isCloud ? item("Testar conexão", "Consulta 2 dias de custo para validar credenciais e permissões", async () => { const x = await post(`/api/finops/sources/${s.id}/test`); toast(x.message, "ok"); }) : null,
    isCloud ? item("Sincronizar período maior", "Reprocessa N dias (ex.: 180) de histórico", async () => {
      const d = input({ type: "number", value: "90", min: "1", max: "395" });
      modal({ title: "Sincronizar histórico", body: field("Dias", d, "Até 395 dias (limite do Cost Explorer: 13–14 meses)"), actions: [{ label: "Cancelar" },
        { label: "Sincronizar", primary: true, onClick: () => { jobModal(`Sincronizar ${s.name}`, () => post(`/api/finops/sources/${s.id}/sync`, { days: Number(d.value) }), { onEnd: () => reload() }); } }] });
    }) : null,
    isAWS ? item("Atualizar inventário", `EC2, EBS, IPs, snapshots e RDS em ${s.config.regions.join(", ") || "(configure as regiões)"}`, () =>
      jobModal(`Inventário ${s.name}`, () => post(`/api/finops/sources/${s.id}/scan`), { onEnd: () => reload() })) : null,
    item("Importar custos (CSV)", "Substitui os dados do período coberto pelo arquivo", () => importCSV(s, "costs", reload)),
    item("Importar inventário (CSV)", "Recursos com estado, CPU, tamanho, tags e custo", () => importCSV(s, "resources", reload)),
    item("Editar", "Nome, regiões, tags de alocação e opções", () => editSource(ctx, s, reload)),
    item("Excluir", "Remove a fonte e todos os seus dados", async () => {
      if (!(await confirmDialog({ title: "Excluir fonte", message: `Excluir "${s.name}" e todos os custos/recursos importados?`, danger: true, confirmText: "Excluir", requireText: s.name }))) return;
      await del(`/api/finops/sources/${s.id}`); toast("Fonte excluída", "ok"); reload();
    })), actions: [{ label: "Fechar" }] });
}

function importCSV(s, kind, reload) {
  const example = kind === "costs" ? COST_CSV : INV_CSV;
  const replace = checkbox("Substituir todo o inventário desta fonte", true);
  modal({ title: kind === "costs" ? "Importar custos (CSV)" : "Importar inventário (CSV)", size: "wide",
    body: h("div", { class: "stack sm" },
      h("p", { class: "small", text: kind === "costs"
        ? "Colunas obrigatórias: date, service, cost. Opcionais: provider, account, region, project, environment, team, currency, usage_type, usage_qty, usage_unit. Aceita vírgula ou ponto e vírgula e valores no formato brasileiro."
        : "Colunas obrigatórias: resource_id, type (instance, volume, snapshot, public_ip, database, load_balancer, nat_gateway, bucket, other). Tags no formato chave=valor;chave2=valor2." }),
      codeBlock(example, { lang: "csv" }), kind === "resources" ? replace : null),
    actions: [{ label: "Cancelar" }, { label: "Escolher arquivo", primary: true, icon: "upload", onClick: () => {
      filePicker(".csv,text/csv", async (text, name) => {
        try {
          const x = await post(`/api/finops/sources/${s.id}/import-${kind}`, { csv: text, replace: replace.input.checked });
          toast(kind === "costs" ? `${name}: ${x.message}` : `${name}: ${x.resources} recursos importados`, "ok");
          if (x.errors && x.errors.length) modal({ title: "Linhas ignoradas", body: h("ul", { class: "small" }, x.errors.map((e) => h("li", { text: e }))), actions: [{ label: "OK" }] });
          reload();
        } catch (e) { toastErr(e); }
      });
    } }] });
}

function creds(s, reload) {
  const isAWS = s.provider === "aws";
  const form = formBuilder(isAWS ? [
    { name: "access_key_id", label: "Access key ID", mono: true, placeholder: "AKIA..." },
    { name: "secret_access_key", label: "Secret access key", type: "password" },
    { name: "session_token", label: "Session token (opcional, credenciais temporárias)", type: "password", span: true },
  ] : [
    { name: "tenancy_ocid", label: "Tenancy OCID", mono: true, placeholder: "ocid1.tenancy.oc1..", span: true },
    { name: "user_ocid", label: "User OCID", mono: true, placeholder: "ocid1.user.oc1..", span: true },
    { name: "fingerprint", label: "Fingerprint", mono: true, placeholder: "aa:bb:cc:..." },
    { name: "region", label: "Home region", mono: true, placeholder: "sa-saopaulo-1" },
    { name: "private_key", label: "Chave privada (PEM, sem passphrase)", type: "textarea", rows: 6, span: true, placeholder: "-----BEGIN PRIVATE KEY-----" },
  ]);
  modal({ title: "Credenciais — " + s.name, size: "wide", body: h("div", { class: "stack sm" }, form,
    note("As credenciais são criptografadas (AES-256-GCM) no banco e nunca são devolvidas pela API. Salvar substitui as atuais.")),
    actions: [{ label: "Cancelar" }, { label: "Salvar", primary: true, icon: "key", onClick: async () => {
      await put(`/api/finops/sources/${s.id}/credentials`, form.values()); toast("Credenciais salvas", "ok"); reload();
    } }] });
}

function editSource(ctx, s, reload) {
  const c = s ? s.config : { regions: [], tag_project: "Project", tag_environment: "Environment", usage: true, rightsizing: true, metrics: true, history_days: 90 };
  const admin = ctx.me.user.role === "admin";
  const form = formBuilder([
    { name: "provider", label: "Tipo", type: "select", options: [["aws", "AWS (Cost Explorer)"], ["oci", "Oracle Cloud (Usage API)"], ["csv", "CSV (importação manual)"]], value: s ? s.provider : "aws" },
    { name: "name", label: "Nome", placeholder: "AWS produção" },
    { name: "account_label", label: "Rótulo da conta", placeholder: "aws-prod (123456789012)" },
    { name: "currency", label: "Moeda padrão (CSV)", value: "USD", mono: true },
    { name: "regions", label: "Regiões do inventário (AWS)", placeholder: "us-east-1, sa-east-1", mono: true, value: (c.regions || []).join(", ") },
    { name: "linked_account", label: "Conta vinculada (AWS Organizations, opcional)", placeholder: "123456789012", mono: true, value: c.linked_account || "" },
    { name: "tag_project", label: "Tag de projeto", value: c.tag_project, mono: true },
    { name: "tag_environment", label: "Tag de ambiente", value: c.tag_environment, mono: true },
    { name: "tag_team", label: "Tag de equipe (opcional)", value: c.tag_team || "", mono: true },
    { name: "history_days", label: "Histórico na 1ª sincronização (dias)", type: "number", value: c.history_days || 90 },
    { name: "usage", label: "Detalhar rede e armazenamento (tipos de uso)", type: "bool", value: !!c.usage },
    { name: "rightsizing", label: "Recomendações de rightsizing do Cost Explorer", type: "bool", value: !!c.rightsizing },
    { name: "metrics", label: "CPU via CloudWatch no inventário", type: "bool", value: !!c.metrics },
    { name: "auto_sync", label: "Sincronizar automaticamente", type: "bool", value: s ? s.auto_sync : true },
    { name: "remediation", label: "Permitir remediação automatizada (somente administrador)", type: "bool", value: !!c.remediation },
  ], s || {});
  if (s) form.ctrl("provider").disabled = true;
  if (!admin) form.ctrl("remediation").disabled = true;
  const eps = kvEditor(Object.entries(c.endpoints || {}), { keyPlaceholder: "ce | ec2 | rds | cloudwatch | pricing | oci_usage", valuePlaceholder: "https://..." });
  modal({ title: s ? "Editar fonte" : "Nova fonte de custos", size: "wide", body: h("div", { class: "stack" }, form,
    admin ? h("details", null, h("summary", { class: "small", text: "Endpoints alternativos (LocalStack, proxies) — somente administradores" }), eps) : null,
    note("Depois de salvar, cadastre as credenciais em Ações → Credenciais e sincronize.")),
  actions: [{ label: "Cancelar" }, { label: "Salvar", primary: true, onClick: async () => {
    const v = form.values();
    const config = { regions: v.regions.split(/[\s,;]+/).filter(Boolean), linked_account: v.linked_account.trim(), tag_project: v.tag_project.trim(),
      tag_environment: v.tag_environment.trim(), tag_team: v.tag_team.trim(), history_days: Number(v.history_days), usage: v.usage, rightsizing: v.rightsizing,
      metrics: v.metrics, remediation: v.remediation, endpoints: admin ? Object.fromEntries(eps.values().map((x) => [x.key, x.value.trim()])) : undefined };
    const bodyX = { name: v.name, provider: v.provider, account_label: v.account_label, currency: v.currency, auto_sync: v.auto_sync, config };
    const x = s ? await put(`/api/finops/sources/${s.id}`, bodyX) : await post("/api/finops/sources", bodyX);
    toast("Fonte salva", "ok");
    reload();
    if (!s && v.provider !== "csv") creds({ ...bodyX, id: x.id, provider: v.provider, name: v.name }, reload);
  } }] });
}

// ---------- catálogo de preços ----------
async function prices(body, ctx) {
  const r = await get("/api/finops/prices");
  const m = ctx.canManage;
  let q = "";
  const search = input({ type: "search", placeholder: "Buscar SKU, provedor, região", "aria-label": "Buscar" });
  const box = h("div");
  search.addEventListener("input", () => { q = search.value.toLowerCase(); draw(); });
  const reload = () => prices(body, ctx);
  mount(body, h("div", { class: "stack" },
    note("Os preços alimentam o custo estimado do inventário, IaC, Kubernetes e cenários. São estimativas sob demanda (sem descontos): confirme na fatura. Preços com origem \"demo\" são ilustrativos."),
    card({ title: `Catálogo de preços (${r.prices.length})`, subtitle: "SKUs: instance:<tipo>, volume:<tipo>, snapshot, public_ip, nat_gateway, load_balancer:<tipo>, db:<classe>, db-storage:<tipo>, eks_cluster, cache:<tipo>, ocpu:<shape>, memory:<shape>, volume:storage, volume:vpu, k8s:vcpu, k8s:memory_gb",
      actions: [search,
        m ? btn("Adicionar", { icon: "plus", cls: "sm primary", onClick: () => editPrice(null, reload) }) : null,
        m ? btn("Buscar na AWS", { icon: "cloud", cls: "sm", onClick: () => awsLookup(ctx, reload) }) : null,
        m ? btn("Importar CSV", { icon: "upload", cls: "sm", onClick: () => modal({ title: "Importar preços (CSV)", body: h("div", { class: "stack sm" }, h("p", { class: "small", text: "Colunas: provider, region (vazio = *), sku, unit (hour, gb-month, month, gb-hour, unit), price, currency." }), codeBlock(PRICE_CSV)),
          actions: [{ label: "Cancelar" }, { label: "Escolher arquivo", primary: true, icon: "upload", onClick: () => filePicker(".csv,text/csv", async (text) => {
            try { const x = await post("/api/finops/prices/import", { csv: text }); toast(`${x.imported} preço(s) importado(s)`, "ok"); reload(); } catch (e) { toastErr(e); }
          }) }] }) }) : null,
        btn("Exportar para CI", { icon: "download", cls: "sm", onClick: () => download("/api/finops/prices/export", undefined, "doomctl-finops-catalog.json") })],
      flush: true, body: box })));
  function draw() {
    const rows = r.prices.filter((p) => !q || `${p.provider} ${p.region} ${p.sku} ${p.source}`.toLowerCase().includes(q));
    mount(box, table([
      { label: "Provedor", render: (p) => badge(PROVIDER_LABELS[p.provider] || p.provider, "outline") },
      { label: "Região", render: (p) => h("span", { class: "mono small", text: p.region }) },
      { label: "SKU", render: (p) => h("strong", { class: "mono", text: p.sku }) },
      { label: "Preço", cls: "right", render: (p) => h("span", null, fmtMoney(p.price, p.currency, { digits: p.price < 1 ? 4 : 2 }), h("span", { class: "muted small", text: " /" + p.unit })) },
      { label: "Origem", render: (p) => badge(p.source, p.source === "demo" ? "warn" : p.source === "aws-pricing" ? "ok" : "") },
      { label: "Atualizado", render: (p) => h("span", { class: "muted small", text: fmtRel(p.updated_at) }) },
      { label: "", render: (p) => m ? h("div", { class: "row nw" }, btn("", { icon: "edit", cls: "sm ghost", title: "Editar", onClick: () => editPrice(p, reload) }),
        btn("", { icon: "trash", cls: "sm ghost", title: "Excluir", onClick: async () => { await del(`/api/finops/prices/${p.id}`); toast("Preço excluído", "ok"); reload(); } })) : null },
    ], rows, { empty: emptyState("table", "Catálogo vazio", "Adicione preços, importe CSV, busque no AWS Price List ou carregue os dados de exemplo.") }));
  }
  draw();
}

function editPrice(p, reload) {
  const form = formBuilder([
    { name: "provider", label: "Provedor", type: "select", options: [["aws", "AWS"], ["oci", "OCI"], ["k8s", "Kubernetes"], ["custom", "Outro"]] },
    { name: "region", label: "Região (* = qualquer)", value: "*", mono: true },
    { name: "sku", label: "SKU", mono: true, placeholder: "instance:m5.large", span: true },
    { name: "unit", label: "Unidade", type: "select", options: [["hour", "por hora"], ["gb-month", "por GB-mês"], ["month", "por mês"], ["gb-hour", "por GB-hora"], ["unit", "por unidade"]] },
    { name: "price", label: "Preço", type: "number" },
    { name: "currency", label: "Moeda", value: "USD", mono: true },
  ], p || {});
  modal({ title: p ? "Editar preço" : "Novo preço", body: form, actions: [{ label: "Cancelar" }, { label: "Salvar", primary: true, onClick: async () => {
    const v = form.values();
    await post("/api/finops/prices", { ...v, price: Number(v.price) }); toast("Preço salvo", "ok"); reload();
  } }] });
}

function awsLookup(ctx, reload) {
  const srcs = (ctx.shared.dims.sources || []);
  const form = formBuilder([
    { name: "source_id", label: "Fonte AWS (credenciais)", type: "select", options: srcs.map((s) => [String(s.id), s.name]) },
    { name: "region", label: "Região", value: "us-east-1", mono: true },
    { name: "kind", label: "Tipo", type: "select", options: [["ec2", "Instância EC2 (Linux, sob demanda)"], ["ebs", "Volume EBS"]] },
    { name: "values", label: "Tipos (separados por vírgula)", placeholder: "m5.large, m5.xlarge, t3.medium · gp3, gp2", mono: true, span: true },
  ]);
  modal({ title: "Buscar preços no AWS Price List", body: h("div", { class: "stack sm" }, form, note("Requer pricing:GetProducts na política da fonte. Os valores são preços públicos sob demanda, em USD.")),
    actions: [{ label: "Cancelar" }, { label: "Buscar", primary: true, icon: "search", onClick: async () => {
      const v = form.values();
      const x = await post("/api/finops/prices/aws-lookup", { source_id: Number(v.source_id), region: v.region.trim(), kind: v.kind, values: v.values.split(/[\s,;]+/).filter(Boolean) });
      toast(`${x.prices.length} preço(s) atualizado(s)`, "ok");
      if (x.errors && x.errors.length) modal({ title: "Não encontrados", body: h("ul", { class: "small" }, x.errors.map((e) => h("li", { text: e }))), actions: [{ label: "OK" }] });
      reload();
    } }] });
}

// ---------- configurações ----------
async function settings(body, ctx) {
  const r = await get("/api/finops/settings");
  const s = r.settings;
  const m = ctx.canManage;
  const pct = (x) => Math.round((x || 0) * 100);
  const general = formBuilder([
    { name: "base_currency", label: "Moeda base", mono: true },
    { name: "auto_sync_hours", label: "Sincronização automática (horas, 0 = desligada)", type: "number" },
    { name: "required_tags", label: "Tags obrigatórias", mono: true, hint: "Separadas por vírgula" },
    { name: "webhook_url", label: "Webhook de alertas", mono: true, placeholder: "https://hooks.slack.com/services/...", hint: "Slack, Teams, Google Chat, Mattermost ou Discord (campo text/content)" },
  ], { ...s, required_tags: (s.required_tags || []).join(", ") });
  const fx = kvEditor(Object.entries(s.fx_rates || {}), { keyPlaceholder: "USD", valuePlaceholder: "5.40 (valor de 1 unidade na moeda base)" });
  const anomaly = formBuilder([
    { name: "anomaly_window", label: "Janela da linha de base (dias)", type: "number" },
    { name: "anomaly_pct", label: "Aumento mínimo (%)", type: "number" },
    { name: "anomaly_min_abs", label: "Aumento mínimo absoluto (por dia)", type: "number" },
    { name: "anomaly_z", label: "z robusto mínimo", type: "number" },
  ], s, { cols: 2 });
  const opt = formBuilder([
    { name: "idle_cpu", label: "Ocioso: CPU média abaixo de (%)", type: "number" },
    { name: "rightsize_cpu", label: "Rightsizing: CPU máxima abaixo de (%)", type: "number" },
    { name: "rightsize_mem", label: "Rightsizing: memória média abaixo de (%)", type: "number" },
    { name: "snapshot_days", label: "Snapshots antigos a partir de (dias)", type: "number" },
    { name: "off_hours_envs", label: "Ambientes para desligar fora do horário", mono: true, span: true },
    { name: "off_hours_hours", label: "Horas ligadas por semana no agendamento", type: "number" },
    { name: "commit_1y", label: "Premissa de desconto — compromisso 1 ano (%)", type: "number" },
    { name: "commit_3y", label: "Premissa de desconto — compromisso 3 anos (%)", type: "number" },
    { name: "spot", label: "Premissa de desconto — Spot/Preemptible (%)", type: "number" },
    { name: "iac_max_increase_pct", label: "CI/CD: aumento máximo (%)", type: "number" },
    { name: "iac_max_increase_abs", label: "CI/CD: aumento máximo mensal (valor)", type: "number" },
  ], { ...s, off_hours_envs: (s.off_hours_envs || []).join(", "), commit_1y: pct(s.commit_discount_1y), commit_3y: pct(s.commit_discount_3y), spot: pct(s.spot_discount) });
  const rules = h("div", { class: "stack sm" });
  const addRule = (rl = { dim: "project", pattern: "", team: "" }) => {
    const dim = select([["project", "Projeto"], ["service", "Serviço"]], rl.dim, { "aria-label": "Dimensão" });
    const pat = input({ value: rl.pattern, placeholder: "portal* ou *SageMaker*", class: "mono", "aria-label": "Padrão" });
    const team = input({ value: rl.team, placeholder: "Equipe / centro de custo", "aria-label": "Equipe" });
    const row = h("div", { class: "kv-row fo-rule" }, dim, pat, team, h("button", { class: "btn sm ghost", type: "button", "aria-label": "remover", onclick: () => row.remove() }, "×"));
    row.val = () => ({ dim: dim.value, pattern: pat.value.trim(), team: team.value.trim() });
    rules.appendChild(row);
  };
  (s.team_rules || []).forEach(addRule);
  const save = async () => {
    const g = general.values(), a = anomaly.values(), o = opt.values();
    const payload = { ...s, ...a,
      base_currency: g.base_currency, auto_sync_hours: g.auto_sync_hours, webhook_url: g.webhook_url.trim(),
      required_tags: g.required_tags.split(/[,;]+/).map((x) => x.trim()).filter(Boolean),
      fx_rates: Object.fromEntries(fx.values().map((x) => [x.key.toUpperCase(), Number(String(x.value).replace(",", "."))])),
      idle_cpu: o.idle_cpu, rightsize_cpu: o.rightsize_cpu, rightsize_mem: o.rightsize_mem, snapshot_days: o.snapshot_days,
      off_hours_envs: o.off_hours_envs.split(/[,;\s]+/).filter(Boolean), off_hours_hours: o.off_hours_hours,
      commit_discount_1y: o.commit_1y / 100, commit_discount_3y: o.commit_3y / 100, spot_discount: o.spot / 100,
      iac_max_increase_pct: o.iac_max_increase_pct, iac_max_increase_abs: o.iac_max_increase_abs,
      team_rules: [...rules.children].map((x) => x.val()).filter((x) => x.pattern && x.team) };
    await put("/api/finops/settings", payload);
    toast("Configurações salvas", "ok");
    await ctx.shared.reloadDims();
    settings(body, ctx);
  };
  const ro = (el) => { if (!m) el.querySelectorAll("input, select, textarea, button").forEach((x) => { x.disabled = true; }); return el; };
  mount(body, h("div", { class: "stack" },
    ro(card({ title: "Geral", body: h("div", { class: "stack" }, general,
      h("div", null, h("span", { class: "lbl small muted", text: "Taxas de câmbio (custos em outras moedas são convertidos para a moeda base)" }), fx)) })),
    ro(card({ title: "Detecção de anomalias", subtitle: "Mediana e desvio absoluto mediano (MAD) dos dias anteriores; o alerta exige os três critérios.", body: anomaly })),
    ro(card({ title: "Otimização e CI/CD", subtitle: "Limites das regras e premissas de desconto (premissas, não preços oficiais).", body: opt })),
    ro(card({ title: "Alocação por equipe (showback/chargeback)", subtitle: "Quando a fonte não traz a tag de equipe, o custo do projeto/serviço é atribuído pela primeira regra que casar (glob).",
      body: h("div", { class: "stack sm" }, rules, h("div", null, btn("Adicionar regra", { icon: "plus", cls: "sm", onClick: () => addRule() }))) })),
    m ? h("div", { class: "row" }, btn("Salvar configurações", { icon: "save", cls: "primary", onClick: save }),
      btn("Restaurar padrões", { cls: "ghost", onClick: async () => {
        if (!(await confirmDialog({ title: "Restaurar padrões", message: "Restaurar todas as configurações do FinOps para os valores padrão (o webhook será mantido)?" }))) return;
        await put("/api/finops/settings", { ...r.defaults, webhook_url: s.webhook_url, fx_rates: s.fx_rates }); toast("Padrões restaurados", "ok"); settings(body, ctx);
      } })) : note("Somente leitura: seu perfil não gerencia o FinOps.")));
}
