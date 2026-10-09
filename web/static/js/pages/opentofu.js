import { get, post, put, del, download } from "../api.js";
import {
  h, mount, clear, icon, btn, badge, card, table, modal, confirmDialog, field, input, select, editor, terminal, runJob,
  toast, toastErr, loading, pageHead, fmtRel, fmtDate, formBuilder, emptyState, kvEditor,
} from "../ui.js";

const PROVIDERS = [["aws", "AWS"], ["oci", "Oracle Cloud (OCI)"]];

export async function render(root, ctx) {
  if (ctx.rest[0]) return projectView(root, ctx, Number(ctx.rest[0]));
  if (ctx.query.get("new") && ctx.canManage) return generatorView(root, ctx);
  return listView(root, ctx);
}

// ---------------- lista ----------------
async function listView(root, ctx) {
  const list = h("div", null, loading());
  mount(root,
    pageHead("OpenTofu", "Infraestrutura como código para AWS e OCI: gere os .tf, rode plan, apply e destroy com segurança.",
      ctx.canManage ? [btn("Novo projeto", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("opentofu?new=1") })] : null, ["DevOps & Cloud"]),
    card({ title: "Projetos", subtitle: "Cada projeto tem seus arquivos, credenciais criptografadas e state local no servidor.", body: list, flush: true }));
  let ps;
  try { ps = await get("/api/tofu/projects"); } catch (e) { return toastErr(e); }
  mount(list, table([
    { label: "Projeto", render: (r) => h("strong", { text: r.name }) },
    { label: "Provider", render: (r) => badge(r.provider.toUpperCase(), r.provider === "aws" ? "warn" : "err") },
    { label: "Credenciais", render: (r) => r.credential_keys.length ? badge(`${r.credential_keys.length} variável(is)`, "ok") : badge("não configuradas", "") },
    { label: "Plano", render: (r) => planBadge(r) },
    { label: "State", render: (r) => r.has_state ? badge("possui state", "info") : h("span", { class: "faint", text: "—" }) },
    { label: "Atualizado", render: (r) => h("span", { class: "muted", text: fmtRel(r.updated_at) }) },
  ], ps, { onRowClick: (r) => ctx.navigate("opentofu/" + r.id),
    empty: emptyState("layers", "Nenhum projeto ainda", "Use o gerador para criar VPC/VCN, instâncias, S3 e Block Volumes.",
      ctx.canManage ? btn("Criar projeto", { icon: "plus", cls: "primary", onClick: () => ctx.navigate("opentofu?new=1") }) : null) }));
}

function planBadge(p) {
  if (!p.plan_kind) return badge("sem plano", "");
  if (!p.plan_valid) return badge("plano expirado", "warn");
  return p.plan_kind === "destroy" ? badge("plano de destroy pronto", "err", true) : badge("plano pronto para apply", "ok", true);
}

// ---------------- gerador ----------------
function generatorView(root, ctx) {
  const common = formBuilder([
    { name: "provider", label: "Provider", type: "select", options: PROVIDERS, value: "aws" },
    { name: "name", label: "Nome do projeto / prefixo dos recursos", placeholder: "lab-web", mono: true },
    { name: "region", label: "Região", placeholder: "sa-east-1 · sa-saopaulo-1", mono: true, value: "sa-east-1" },
    { name: "provider_version", label: "Versão do provider", placeholder: "~> 6.0 (AWS) · ~> 9.0 (OCI)", mono: true, hint: "Fixe a versão do provider (boas práticas)." },
    { name: "tags", label: "Tags (chave=valor por linha)", type: "textarea", rows: 2, placeholder: "Env=lab\nOwner=infra", span: true },
  ]);
  const net = formBuilder([
    { name: "network", label: "Criar rede (VPC/VCN, subnet pública, gateway e regra de SSH)", type: "bool", value: true, span: true },
    { name: "net_cidr", label: "CIDR da VPC/VCN", value: "10.20.0.0/16", mono: true },
    { name: "subnet_cidr", label: "CIDR da subnet", value: "10.20.1.0/24", mono: true },
    { name: "ssh_cidr", label: "CIDR liberado para SSH", placeholder: "203.0.113.10/32", mono: true, hint: "Seu IP administrativo. 0.0.0.0/0 é recusado." },
    { name: "az", label: "Zona de disponibilidade (AWS)", placeholder: "sa-east-1a", mono: true },
  ]);
  const awsCompute = formBuilder([
    { name: "compute", label: "Instância EC2 (IMDSv2 obrigatório, disco criptografado)", type: "bool", span: true },
    { name: "instance_type", label: "Tipo de instância", value: "t3.micro", mono: true },
    { name: "root_size_gb", label: "Disco raiz (GB)", type: "number", value: 20 },
    { name: "use_debian_ami", label: "Usar AMI oficial Debian 13 (data source)", type: "bool", value: true },
    { name: "ami", label: "ou AMI ID", placeholder: "ami-0abc...", mono: true },
    { name: "key_name", label: "Key pair (opcional)", mono: true },
  ]);
  const awsStorage = formBuilder([
    { name: "bucket", label: "Bucket S3 (Block Public Access + criptografia)", type: "bool", span: true },
    { name: "bucket_name", label: "Nome do bucket (único global)", mono: true },
    { name: "versioning", label: "Versionamento", type: "bool", value: true },
  ]);
  const ociCompute = formBuilder([
    { name: "compute", label: "Instância Compute (VM)", type: "bool", span: true },
    { name: "shape", label: "Shape", value: "VM.Standard.E5.Flex", mono: true },
    { name: "ocpus", label: "OCPUs (Flex)", type: "number", value: 1 },
    { name: "memory_gb", label: "Memória GB (Flex)", type: "number", value: 8 },
    { name: "root_size_gb", label: "Boot volume (GB, mín. 50)", type: "number", value: 50 },
  ]);
  const ociStorage = formBuilder([
    { name: "block_volume", label: "Block Volume (anexo paravirtualizado)", type: "bool", span: true },
    { name: "volume_size_gb", label: "Tamanho (GB, mín. 50)", type: "number", value: 100 },
    { name: "vpus", label: "VPUs por GB (10 = balanceado)", type: "number", value: 10 },
  ]);
  const awsBox = h("div", { class: "stack" }, sec("Compute", awsCompute), sec("Armazenamento", awsStorage));
  const ociBox = h("div", { class: "stack hidden" }, sec("Compute", ociCompute), sec("Armazenamento", ociStorage));
  const prov = common.ctrl("provider");
  prov.addEventListener("change", () => {
    const aws = prov.value === "aws";
    awsBox.classList.toggle("hidden", !aws);
    ociBox.classList.toggle("hidden", aws);
    common.ctrl("region").value = aws ? "sa-east-1" : "sa-saopaulo-1";
  });
  const preview = h("div", { class: "stack sm" }, emptyState("layers", "Pré-visualização", "Preencha o formulário e clique em Gerar."));
  let files = null;
  const generate = btn("Gerar arquivos", { icon: "bolt", cls: "primary", onClick: async () => {
    const c = common.values();
    const spec = { ...c, ...net.values(), ...(c.provider === "aws" ? { ...awsCompute.values(), ...awsStorage.values() } : { ...ociCompute.values(), ...ociStorage.values() }) };
    const r = await post("/api/tofu/generate", spec);
    files = r.files;
    renderPreview(c);
  } });
  const renderPreview = (c) => {
    const names = Object.keys(files).sort();
    const ed = editor({ value: files[names[0]], rows: 22, readOnly: true });
    const tabsEl = h("div", { class: "editor-tabs" });
    names.forEach((n, i) => tabsEl.appendChild(h("button", { type: "button", class: i === 0 ? "active" : "", onclick: (e) => {
      tabsEl.querySelectorAll("button").forEach((b) => b.classList.remove("active")); e.currentTarget.classList.add("active"); ed.value = files[n];
    } }, icon("file", "sm"), n)));
    mount(preview, h("div", { class: "spread" }, h("b", { text: `${names.length} arquivos gerados` }),
      btn("Criar projeto", { icon: "check", cls: "primary sm", onClick: async () => {
        const r = await post("/api/tofu/projects", { name: c.name, provider: c.provider, description: `Gerado pelo doomctl (${c.region})`, files });
        toast("Projeto criado", "ok");
        ctx.navigate("opentofu/" + r.id);
      } })), tabsEl, ed.el);
  };
  mount(root,
    pageHead("Novo projeto OpenTofu", "Gere a base do projeto; depois edite os arquivos livremente.",
      [btn("Voltar", { icon: "left", onClick: () => ctx.navigate("opentofu") })], ["OpenTofu"]),
    h("div", { class: "grid g2" },
      h("div", { class: "stack" }, sec("Projeto", common), sec("Rede", net), awsBox, ociBox, h("div", null, generate)),
      h("div", { class: "card" }, h("div", { class: "card-body" }, preview))));
}

function sec(title, content) {
  return h("div", { class: "fieldset" }, h("div", { class: "legend", text: title }), content);
}

// ---------------- projeto ----------------
const CRED_FIELDS = {
  aws: [["AWS_ACCESS_KEY_ID", "Access key ID"], ["AWS_SECRET_ACCESS_KEY", "Secret access key", "password"], ["AWS_SESSION_TOKEN", "Session token (opcional)", "password"], ["AWS_REGION", "Região padrão (opcional)"]],
  oci: [["TF_VAR_tenancy_ocid", "Tenancy OCID"], ["TF_VAR_user_ocid", "User OCID"], ["TF_VAR_fingerprint", "Fingerprint da API key"],
    ["TF_VAR_private_key", "Chave privada da API key (PEM)", "textarea"], ["TF_VAR_compartment_ocid", "Compartment OCID"],
    ["TF_VAR_image_ocid", "Image OCID (se usar Compute)"], ["TF_VAR_ssh_public_key", "Chave SSH pública (se usar Compute)", "textarea"]],
};

async function projectView(root, ctx, id) {
  const M = ctx.canManage;
  mount(root, loading());
  let p, busy;
  const reload = async () => { const r = await get(`/api/tofu/projects/${id}`); p = r.project; busy = r.busy; };
  try { await reload(); } catch (e) { toastErr(e); return ctx.navigate("opentofu"); }

  let files = { ...p.files };
  let current = Object.keys(files).sort()[0];
  const ed = editor({ value: files[current] || "", rows: 24, readOnly: !M, onInput: (v) => { files[current] = v; dirty = true; } });
  let dirty = false;
  const fileTabs = h("div", { class: "editor-tabs" });
  const renderTabs = () => {
    clear(fileTabs);
    for (const n of Object.keys(files).sort()) {
      fileTabs.appendChild(h("button", { type: "button", class: n === current ? "active" : "", onclick: () => { current = n; ed.value = files[n]; renderTabs(); } },
        icon("file", "sm"), n,
        M ? h("span", { title: "remover arquivo", onclick: async (e) => {
          e.stopPropagation();
          if (Object.keys(files).length === 1) return toast("O projeto precisa de ao menos um arquivo", "err");
          if (!(await confirmDialog({ title: "Remover arquivo", message: `Remover ${n} do projeto? (só vale ao salvar)`, confirmText: "Remover" }))) return;
          delete files[n]; dirty = true;
          if (current === n) { current = Object.keys(files).sort()[0]; ed.value = files[current]; }
          renderTabs();
        } }, " ×") : null));
    }
    if (M) fileTabs.appendChild(h("button", { type: "button", onclick: () => {
      const inp = input({ placeholder: "network.tf", class: "mono" });
      modal({ title: "Novo arquivo", body: field("Nome (.tf, .tfvars, .tf.json, .tftpl)", inp), actions: [{ label: "Cancelar" }, { label: "Criar", primary: true, onClick: () => {
        const n = inp.value.trim();
        if (!/^[A-Za-z0-9_-]{1,60}\.(tf|tfvars|tf\.json|tfvars\.json|tftpl)$/.test(n)) { toast("Nome inválido", "err"); return false; }
        files[n] = files[n] || ""; current = n; ed.value = files[n]; dirty = true; renderTabs();
      } }] });
    } }, icon("plus", "sm"), "arquivo"));
  };
  renderTabs();

  const term = terminal({ title: `tofu — ${p.name}`, height: "360px" });
  term.write("[doomctl] fluxo seguro: init → validate → plan → apply. O apply só usa um plano salvo e válido (< 24h) sobre os arquivos atuais.\n");
  const head = h("div");
  const credBox = h("div");
  const actionsBar = h("div", { class: "row" });

  const saveFiles = async () => {
    await put(`/api/tofu/projects/${id}`, { name: p.name, provider: p.provider, description: p.description, files });
    dirty = false;
    await reload();
    renderHead();
  };

  const act = async (action, extra = {}) => {
    if (dirty) { await saveFiles(); toast("Arquivos salvos antes da execução", "ok"); }
    term.write(`\n$ tofu ${action}\n`);
    const res = await post(`/api/tofu/projects/${id}/action/${action}`, extra);
    setBusy(true);
    await runJob(res, term);
    await reload();
    setBusy(false);
    renderHead();
  };
  const setBusy = (b) => actionsBar.querySelectorAll("button").forEach((x) => { x.disabled = b; });

  const renderHead = () => {
    mount(head, pageHead(p.name, p.description || "Projeto OpenTofu",
      [btn("Voltar", { icon: "left", onClick: () => ctx.navigate("opentofu") }),
        btn("Baixar .tar.gz", { icon: "download", onClick: () => download(`/api/tofu/projects/${id}/download`) }),
        M ? btn("Excluir", { icon: "trash", cls: "danger", onClick: async () => {
          const c = await confirmDialog({ title: "Excluir projeto", danger: true, confirmText: "Excluir",
            message: p.has_state ? "🛑 Este projeto possui state local: recursos podem existir na nuvem e ficarão órfãos. Rode plan-destroy + destroy antes." : "O projeto e seus arquivos serão removidos do doomctl.",
            requireText: p.has_state ? p.name : undefined });
          if (!c) return;
          await del(`/api/tofu/projects/${id}`, { confirm: p.has_state ? p.name : "" });
          toast("Projeto excluído", "ok"); ctx.navigate("opentofu");
        } }) : null],
      [badge(p.provider.toUpperCase(), p.provider === "aws" ? "warn" : "err"), planBadge(p), p.has_state ? badge("state local", "info") : null]));
    clear(actionsBar);
    if (!M) return;
    const a = (label, action, opts = {}) => btn(label, { ...opts, cls: "sm " + (opts.cls || ""), onClick: () => act(action) });
    actionsBar.append(
      a("init", "init", { icon: "refresh", title: "tofu init -upgrade" }), a("validate", "validate", { icon: "check" }), a("fmt -check", "fmt"),
      a("plan", "plan", { icon: "eye", cls: "primary" }),
      btn("apply", { icon: "play", cls: "sm primary", disabled: !(p.plan_valid && p.plan_kind === "apply"), title: "Aplica o plano salvo",
        onClick: async () => {
          if (!(await confirmDialog({ title: "Aplicar plano", message: "Aplicar o plano salvo? Revise a saída do plan antes.", confirmText: "Aplicar" }))) return;
          act("apply");
        } }),
      a("output", "output"),
      h("span", { class: "vsep" }),
      a("plan -destroy", "plan-destroy", { icon: "eye", cls: "danger" }),
      btn("destroy", { icon: "trash", cls: "sm danger solid", disabled: !(p.plan_valid && p.plan_kind === "destroy"), onClick: async () => {
        const c = await confirmDialog({ title: "🛑 Destroy", danger: true, confirmText: "Destruir recursos", requireText: p.name,
          message: "Todos os recursos do plano de destroy serão removidos na nuvem. Esta ação não pode ser desfeita." });
        if (c) act("destroy", { confirm: c });
      } }),
      ctx.can("trivy", "manage") ? btn("Trivy (misconfig)", { icon: "shield", cls: "sm", onClick: async () => {
        if (dirty) await saveFiles();
        term.write("\n$ trivy config (projeto)\n");
        await runJob(await post("/api/trivy/scan", { kind: "config", source: `tofu:${id}` }), term);
      } }) : null,
      ctx.can("ai") ? btn("Revisar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "opentofu", filename: current, content: files[current],
        prompt: `Revise este arquivo OpenTofu (${p.provider.toUpperCase()}): segurança, custo e boas práticas.` }) }) : null);
    if (busy) setBusy(true);
  };

  const renderCreds = () => {
    mount(credBox, h("div", { class: "stack sm" },
      p.credential_keys.length ? h("div", { class: "row", style: { gap: "6px" } }, p.credential_keys.map((k) => badge(k, "ok"))) : h("p", { class: "muted small", text: "Nenhuma credencial configurada." }),
      M ? btn("Configurar credenciais", { icon: "key", cls: "sm", onClick: editCreds }) : null,
      h("p", { class: "faint small", text: "Guardadas criptografadas (AES-256-GCM) e injetadas como variáveis de ambiente só durante a execução. Valores sensíveis são mascarados na saída." })));
  };
  const editCreds = () => {
    const defs = CRED_FIELDS[p.provider].map(([name, label, type]) => ({ name, label, type: type || "text", mono: true,
      placeholder: p.credential_keys.includes(name) ? "•••••• (configurado — deixe vazio para manter)" : "" }));
    const f = formBuilder(defs, {}, { cols: 2 });
    const extra = kvEditor([], { keyPlaceholder: "TF_VAR_nome", valuePlaceholder: "valor" });
    const rm = h("div", { class: "row", style: { gap: "6px" } }, p.credential_keys.map((k) => h("button", { class: "badge outline", type: "button", title: "remover", onclick: async (e) => {
      if (!(await confirmDialog({ title: "Remover credencial", message: `Remover ${k}?`, confirmText: "Remover" }))) return;
      const r = await put(`/api/tofu/projects/${id}/credentials`, { env: { [k]: "" }, merge: true });
      p.credential_keys = r.credential_keys; e.target.remove(); renderCreds();
    } }, k, " ×")));
    modal({ title: `Credenciais — ${p.provider.toUpperCase()}`, size: "wide",
      body: h("div", { class: "stack" }, f, h("b", { text: "Variáveis adicionais (AWS_*, OCI_*, TF_VAR_*)" }), extra,
        p.credential_keys.length ? h("div", { class: "stack sm" }, h("b", { text: "Configuradas (clique para remover)" }), rm) : null),
      actions: [{ label: "Cancelar" }, { label: "Salvar", primary: true, onClick: async () => {
        const env = {};
        Object.entries(f.values()).forEach(([k, v]) => { if (v) env[k] = v; });
        extra.values().forEach(({ key, value }) => { if (value) env[key] = value; });
        if (!Object.keys(env).length) { toast("Nada a salvar", "err"); return false; }
        const r = await put(`/api/tofu/projects/${id}/credentials`, { env, merge: true });
        p.credential_keys = r.credential_keys; renderCreds(); toast("Credenciais salvas", "ok");
      } }] });
  };

  renderHead();
  renderCreds();
  mount(root, head,
    h("div", { class: "split wide" },
      h("div", { class: "stack" },
        card({ title: "Credenciais", body: credBox }),
        card({ title: "Informações", body: h("dl", { class: "kv" },
          h("dt", { text: "Provider" }), h("dd", { text: p.provider }),
          h("dt", { text: "Arquivos" }), h("dd", { text: String(Object.keys(p.files).length) }),
          h("dt", { text: "Último plano" }), h("dd", { text: p.plan_at ? `${p.plan_kind} · ${fmtDate(p.plan_at)}` : "—" }),
          h("dt", { text: "Atualizado" }), h("dd", { text: fmtDate(p.updated_at) })) })),
      h("div", { class: "stack" },
        card({ title: "Arquivos", subtitle: M ? "Edite e salve; as ações salvam automaticamente alterações pendentes." : "Somente leitura",
          actions: M ? btn("Salvar arquivos", { icon: "save", cls: "sm primary", onClick: async () => { await saveFiles(); toast("Arquivos salvos (o plano anterior foi invalidado se houve mudança)", "ok"); } }) : null,
          body: h("div", null, fileTabs, ed.el) }),
        card({ title: "Execução", body: h("div", { class: "stack sm" }, actionsBar, term.el) }))));
}
