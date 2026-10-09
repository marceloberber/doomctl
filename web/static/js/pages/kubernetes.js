import { get, post, put, del, download, saveText } from "../api.js";
import {
  h, mount, clear, icon, btn, badge, card, table, tabs, modal, confirmDialog, field, input, select, editor, terminal, runJob,
  toast, toastErr, loading, pageHead, fmtRel, formBuilder, emptyState, copyText, filePicker, jobModal,
} from "../ui.js";

export async function render(root, ctx) {
  const tab0 = ctx.query.get("tab") || "generator";
  const body = h("div");
  const T = [
    { id: "generator", label: "Gerador", icon: "template" },
    { id: "manifests", label: "Manifests salvos", icon: "folder" },
    { id: "clusters", label: "Clusters", icon: "helm" },
    { id: "diagnose", label: "Troubleshooting", icon: "activity" },
  ];
  const show = (id) => { clear(body); ({ generator: generatorView, manifests: manifestsView, clusters: clustersView, diagnose: diagnoseView })[id](body, ctx); };
  mount(root,
    pageHead("Kubernetes", "Manifests com boas práticas (probes, requests/limits, SecurityContext), Helm, Kustomize, GitOps e CI/CD.", null,
      ["DevOps & Cloud", h("span", { class: "tag beta", text: "beta" })]),
    h("section", { class: "card" }, tabs(T, tab0, show), h("div", { class: "card-body" }, body)));
  show(tab0);
}

// ---------------- gerador ----------------
async function generatorView(root, ctx) {
  mount(root, loading());
  let tpls;
  try { tpls = await get("/api/k8s/templates"); } catch (e) { return toastErr(e); }
  const cats = [...new Set(tpls.map((t) => t.category))];
  const picker = h("div", { class: "card" });
  const formBox = h("div", { class: "stack" });
  const preview = h("div", { class: "stack sm" });
  let current = null;
  const list = h("div", { class: "list" });
  for (const c of cats) {
    list.appendChild(h("div", { class: "nav-section", text: c }));
    for (const t of tpls.filter((x) => x.category === c)) {
      const it = h("div", { class: "list-item", role: "button", tabindex: "0", dataset: { id: t.id } }, icon(t.multi ? "folder" : "file", "sm"),
        h("div", { class: "grow" }, h("b", { text: t.name })));
      it.addEventListener("click", () => choose(t));
      it.addEventListener("keydown", (e) => { if (e.key === "Enter") choose(t); });
      list.appendChild(it);
    }
  }
  mount(picker, h("div", { class: "card-head" }, h("h3", { text: "Modelos" })), list);
  const choose = (t) => {
    current = t;
    list.querySelectorAll(".list-item").forEach((x) => x.classList.toggle("active", x.dataset.id === t.id));
    const form = formBuilder(t.fields.map((f) => ({ name: f.name, label: f.label + (f.required ? " *" : ""), type: f.type === "int" ? "number" : f.type,
      options: f.options, value: f.type === "bool" ? f.default === "true" : f.default || "", hint: f.help, mono: f.type === "text",
      span: f.type === "textarea" })));
    mount(formBox, h("div", null, h("h3", { text: t.name }), h("p", { class: "muted", text: t.description })), form,
      h("div", null, btn("Gerar", { icon: "bolt", cls: "primary", onClick: async () => {
        const vals = {};
        Object.entries(form.values()).forEach(([k, v]) => { vals[k] = String(v); });
        const r = await post("/api/k8s/render", { template: t.id, values: vals });
        showFiles(r.files, t, vals.name || t.id);
      } })));
    mount(preview, emptyState("template", "Pré-visualização", "Ajuste os campos e clique em Gerar."));
  };
  const showFiles = (files, t, base) => {
    const names = Object.keys(files).sort();
    let cur = names.find((n) => /\.ya?ml$/.test(n) && !n.includes("/.")) || names[0];
    const ed = editor({ value: files[cur], rows: 24, onInput: (v) => { files[cur] = v; } });
    const ftabs = h("div", { class: "editor-tabs" });
    names.forEach((n) => ftabs.appendChild(h("button", { type: "button", class: n === cur ? "active" : "", onclick: (e) => {
      ftabs.querySelectorAll("button").forEach((b) => b.classList.remove("active")); e.currentTarget.classList.add("active"); cur = n; ed.value = files[n];
    } }, icon("file", "sm"), n)));
    const isYaml = (n) => /\.ya?ml$/.test(n) && !n.includes("templates/");
    mount(preview,
      h("div", { class: "row" },
        isYaml(cur) ? btn("Validar YAML", { icon: "check", cls: "sm", onClick: () => validate(files[cur]) }) : null,
        btn(names.length > 1 ? "Baixar .tar.gz" : "Baixar", { icon: "download", cls: "sm", onClick: () => names.length > 1
          ? download("/api/k8s/bundle", { name: base, files }, base + ".tar.gz") : saveText(files[cur], cur) }),
        btn("Copiar", { icon: "copy", cls: "sm", onClick: () => copyText(files[cur]) }),
        ctx.canManage && names.length === 1 && isYaml(cur) ? btn("Salvar manifest", { icon: "save", cls: "sm primary", onClick: async () => {
          await post("/api/k8s/manifests", { name: cur.replace(/\.ya?ml$/, ""), content: files[cur] });
          toast("Manifest salvo — veja em Manifests salvos", "ok");
        } }) : null,
        ctx.can("ai") ? btn("Revisar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "kubernetes", filename: cur, content: files[cur],
          prompt: `Revise este ${t.name} do Kubernetes (segurança, recursos, probes e boas práticas).` }) }) : null),
      ftabs, ed.el);
  };
  mount(root, h("div", { class: "split" }, picker, h("div", { class: "stack" }, formBox, h("hr"), preview)));
  choose(tpls[0]);
}

async function validate(content) {
  const r = await post("/api/k8s/validate", { content });
  if (r.ok) toast(`YAML válido: ${r.documents.map((d) => `${d.kind}/${d.name}`).join(", ")}`, "ok");
  else modal({ title: "Problemas no YAML", body: h("ul", null, r.errors.map((e) => h("li", { class: "mono small", text: e }))) });
  return r;
}

// ---------------- manifests ----------------
async function manifestsView(root, ctx) {
  const M = ctx.canManage;
  const list = h("div", null, loading());
  let clusters = [];
  const open = async (m) => {
    const full = m.id ? await get(`/api/k8s/manifests/${m.id}`) : { name: "", content: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: exemplo\ndata:\n  CHAVE: valor\n" };
    const ed = editor({ value: full.content, rows: 22, readOnly: !M });
    const name = input({ value: full.name, placeholder: "nome-do-manifest" });
    const save = async () => {
      const payload = { name: name.value, content: ed.value };
      const r = full.id ? await put(`/api/k8s/manifests/${full.id}`, payload) : await post("/api/k8s/manifests", payload);
      full.id = r.id || full.id;
      load();
    };
    const applyBox = h("div", { class: "row" });
    if (M && clusters.length) {
      const cl = select(clusters.map((c) => [c.id, c.name]));
      const mode = select([["diff", "kubectl diff"], ["dry-run", "apply --dry-run=server"], ["apply", "apply (com dry-run antes)"]]);
      const ns = input({ placeholder: "namespace (opcional)", class: "mono", style: { maxWidth: "200px" } });
      applyBox.append(cl, mode, ns, btn("Executar", { icon: "play", cls: "sm", onClick: async () => {
        await save();
        if (mode.value === "apply" && !(await confirmDialog({ title: "Aplicar no cluster", message: `Aplicar "${name.value}" no cluster selecionado?`, confirmText: "Aplicar" }))) return;
        jobModal(`kubectl ${mode.value} — ${name.value}`, () => post(`/api/k8s/clusters/${cl.value}/apply`, { manifest_id: full.id, mode: mode.value, namespace: ns.value.trim() }));
      } }));
    }
    modal({ title: full.id ? `Manifest — ${full.name}` : "Novo manifest", size: "xwide",
      body: h("div", { class: "stack sm" }, field("Nome", name), ed.el, applyBox.children.length ? h("div", { class: "fieldset" }, h("div", { class: "legend", text: "Aplicar em cluster" }), applyBox) : null),
      actions: [
        { label: "Validar", icon: "check", onClick: async () => { await validate(ed.value); return false; } },
        ...(ctx.can("trivy", "manage") && full.id ? [{ label: "Trivy (misconfig)", icon: "shield", onClick: async () => { await save(); jobModal(`trivy config — ${name.value}`, () => post("/api/trivy/scan", { kind: "config", source: `k8s:${full.id}` })); return false; } }] : []),
        ...(M ? [{ label: "Salvar", icon: "save", primary: true, onClick: async () => { await save(); toast("Manifest salvo", "ok"); } }] : []),
      ] });
  };
  const load = async () => {
    let ms;
    try { [ms, clusters] = await Promise.all([get("/api/k8s/manifests"), get("/api/k8s/clusters")]); } catch (e) { return toastErr(e); }
    mount(list, table([
      { label: "Nome", render: (r) => h("strong", { text: r.name }) },
      { label: "Kinds", render: (r) => h("div", { class: "row", style: { gap: "4px" } }, (r.kind || "—").split(", ").map((k) => badge(k, "violet"))) },
      { label: "Atualizado", render: (r) => h("span", { class: "muted", text: fmtRel(r.updated_at) }) },
      { label: "", cls: "actions", render: (r) => h("div", { class: "row", style: { justifyContent: "flex-end" } },
        btn("Abrir", { icon: "edit", cls: "xs", onClick: () => open(r) }),
        M ? btn("", { icon: "trash", cls: "xs danger", title: "Excluir", onClick: async () => {
          if (!(await confirmDialog({ title: "Excluir manifest", message: `Excluir "${r.name}"? (não remove nada do cluster)`, danger: true, confirmText: "Excluir" }))) return;
          await del(`/api/k8s/manifests/${r.id}`); toast("Excluído", "ok"); load();
        } }) : null) },
    ], ms, { onRowClick: open, empty: emptyState("folder", "Nenhum manifest salvo", "Gere pelo Gerador ou crie um manifest em branco.") }));
  };
  mount(root, h("div", { class: "stack" }, M ? h("div", null, btn("Novo manifest", { icon: "plus", cls: "primary sm", onClick: () => open({}) })) : null, list));
  load();
}

// ---------------- clusters ----------------
async function clustersView(root, ctx) {
  const M = ctx.canManage;
  const list = h("div", null, loading());
  const consoleBox = h("div");
  const load = async () => {
    let cs;
    try { cs = await get("/api/k8s/clusters"); } catch (e) { return toastErr(e); }
    mount(list, table([
      { label: "Cluster", render: (r) => h("strong", { text: r.name }) },
      { label: "Contexto", render: (r) => h("span", { class: "mono", text: r.context || "(padrão do kubeconfig)" }) },
      { label: "Cadastrado", render: (r) => h("span", { class: "muted", text: fmtRel(r.created_at) }) },
      { label: "", cls: "actions", render: (r) => M ? h("div", { class: "row", style: { justifyContent: "flex-end" } },
        btn("Console", { icon: "terminal", cls: "xs", onClick: () => openConsole(r) }),
        btn("Scan Trivy", { icon: "shield", cls: "xs", onClick: () => jobModal(`trivy k8s — ${r.name}`, () => post(`/api/k8s/clusters/${r.id}/scan`, {})) }),
        btn("", { icon: "trash", cls: "xs danger", title: "Remover", onClick: async () => {
          if (!(await confirmDialog({ title: "Remover cluster", message: `Remover o kubeconfig de "${r.name}" do doomctl?`, danger: true, confirmText: "Remover" }))) return;
          await del(`/api/k8s/clusters/${r.id}`); toast("Removido", "ok"); load();
        } })) : null },
    ], cs, { empty: emptyState("helm", "Nenhum cluster cadastrado", "Adicione um kubeconfig (token ou certificado de uma ServiceAccount com RBAC mínimo).") }));
  };
  const openConsole = (c) => {
    const term = terminal({ title: `kubectl — ${c.name}`, prompt: "kubectl", placeholder: "get pods -A", height: "380px",
      onCommand: async (cmd) => { const res = await post(`/api/k8s/clusters/${c.id}/kubectl`, { command: cmd }); await runJob(res, term); } });
    term.write("[doomctl] verbos permitidos: get, describe, logs, top, events, explain, api-resources, version, cluster-info, auth can-i, rollout, scale\n");
    const chips = ["get nodes -o wide", "get pods -A", "get events -A --sort-by=.lastTimestamp", "top pods -A", "auth can-i --list"].map((c2) =>
      h("button", { class: "badge outline", type: "button", onclick: () => { term.input.value = c2; term.input.focus(); } }, c2));
    mount(consoleBox, card({ title: `Console — ${c.name}`, subtitle: "Somente comandos de leitura e operações de rollout/scale.", body: h("div", { class: "stack sm" }, h("div", { class: "row" }, chips), term.el) }));
    term.input.focus();
    consoleBox.scrollIntoView({ behavior: "smooth" });
  };
  const add = () => {
    const kc = h("textarea", { rows: 12, placeholder: "apiVersion: v1\nkind: Config\nclusters: ...", spellcheck: "false" });
    const f = formBuilder([{ name: "name", label: "Nome", placeholder: "prod-oke" }, { name: "context", label: "Contexto (opcional)", mono: true }]);
    modal({ title: "Adicionar cluster", size: "wide",
      body: h("div", { class: "stack" }, f, field("kubeconfig", kc, "Criptografado no banco. kubeconfigs com 'exec' ou 'auth-provider' são recusados (executariam binários no servidor)."),
        h("div", null, btn("Carregar arquivo", { icon: "upload", cls: "sm", onClick: () => filePicker("", (t) => { kc.value = t; }) }))),
      actions: [{ label: "Cancelar" }, { label: "Salvar", primary: true, onClick: async () => {
        await post("/api/k8s/clusters", { ...f.values(), kubeconfig: kc.value }); toast("Cluster salvo", "ok"); load();
      } }] });
  };
  mount(root, h("div", { class: "stack" },
    h("div", { class: "spread" }, h("p", { class: "muted", text: "Use uma ServiceAccount com RBAC mínimo para o doomctl." }), M ? btn("Adicionar cluster", { icon: "plus", cls: "primary sm", onClick: add }) : null),
    list, consoleBox));
  load();
}

// ---------------- troubleshooting ----------------
function diagnoseView(root, ctx) {
  const ta = h("textarea", { rows: 14, placeholder: "Cole aqui a saída de kubectl describe pod, kubectl logs --previous, kubectl get events..." });
  const q = input({ value: "Meu pod está em CrashLoopBackOff. Qual a causa provável e como corrigir?" });
  mount(root, h("div", { class: "grid g2" },
    h("div", { class: "stack" },
      field("Pergunta", q), field("Evidências (describe, logs, eventos)", ta),
      h("div", { class: "row" }, ctx.can("ai") ? btn("Diagnosticar com IA", { icon: "sparkles", cls: "primary", onClick: () => {
        ctx.openAI({ module: "kubernetes", filename: "evidencias.txt", content: ta.value, prompt: q.value, send: true });
      } }) : h("span", { class: "muted", text: "Seu perfil não tem acesso ao assistente." }))),
    h("div", { class: "stack" },
      h("div", { class: "fieldset" }, h("div", { class: "legend", text: "Roteiro rápido de diagnóstico" }),
        h("ol", { class: "small", style: { paddingLeft: "18px", margin: 0, lineHeight: 1.9 } },
          ["kubectl get pods -n <ns> -o wide", "kubectl describe pod <pod> -n <ns>  (Events no final)", "kubectl logs <pod> -n <ns> --previous",
            "kubectl get events -n <ns> --sort-by=.lastTimestamp", "kubectl top pod -n <ns>  (OOMKilled? limits?)",
            "kubectl rollout history deploy/<app> -n <ns>", "kubectl rollout undo deploy/<app> -n <ns>  (🛑 rollback)"]
            .map((c) => h("li", null, h("code", { text: c }))))),
      h("p", { class: "muted small", text: "Os comandos de leitura podem ser executados no Console de cada cluster (aba Clusters)." }))));
}
