import { get, post, put, del, saveText } from "../api.js";
import {
  h, mount, clear, icon, btn, badge, card, table, tabs, modal, confirmDialog, field, input, select, editor,
  toast, toastErr, loading, pageHead, fmtRel, formBuilder, jobModal, copyText, emptyState,
} from "../ui.js";

const DF_FIELDS = [
  { name: "base_image", label: "Imagem base (runtime)", placeholder: "debian:trixie-slim", mono: true },
  { name: "workdir", label: "WORKDIR", placeholder: "/app", mono: true },
  { name: "multi_stage", label: "Multi-stage (estágio de build separado)", type: "bool" },
  { name: "builder_image", label: "Imagem do estágio de build", placeholder: "golang:1.27-trixie", mono: true },
  { name: "builder_steps", label: "Passos do build (um por linha; RUN implícito)", type: "textarea", span: true, rows: 4 },
  { name: "artifact_from", label: "Artefato no build (COPY --from=build)", placeholder: "/out/app", mono: true },
  { name: "artifact_to", label: "Destino na imagem final", placeholder: "/app", mono: true },
  { name: "packages", label: "Pacotes do sistema", placeholder: "ca-certificates curl", hint: "apt, apk ou dnf conforme a imagem base" },
  { name: "expose", label: "Portas (EXPOSE)", placeholder: "8080" },
  { name: "copy", label: "COPY (origem destino, um por linha)", type: "textarea", rows: 3 },
  { name: "run", label: "RUN extras na imagem final (um por linha)", type: "textarea", rows: 3 },
  { name: "env", label: "ENV (CHAVE=valor por linha)", type: "textarea", rows: 3 },
  { name: "args", label: "ARG (NOME=padrão por linha)", type: "textarea", rows: 3 },
  { name: "non_root", label: "Criar e usar usuário não-root (UID 10001)", type: "bool" },
  { name: "user", label: "USER (opcional, sobrepõe o não-root)", placeholder: "nonroot:nonroot", mono: true },
  { name: "healthcheck", label: "HEALTHCHECK (comando)", placeholder: "curl -fsS http://127.0.0.1:8080/health || exit 1", span: true, mono: true },
  { name: "entrypoint", label: "ENTRYPOINT", placeholder: "/app", mono: true },
  { name: "cmd", label: "CMD", placeholder: "node dist/index.js", mono: true },
  { name: "labels", label: "LABEL (chave=valor por linha)", type: "textarea", rows: 2, span: true },
];

const SVC_FIELDS = (registries) => [
  { name: "name", label: "Nome do serviço", placeholder: "web", mono: true },
  { name: "image", label: "Imagem", placeholder: "nginx:1.29-alpine", mono: true },
  { name: "registry", label: "Registry (prefixo)", type: "select", options: [["", "— Docker Hub / nenhum —"], ...registries.filter((r) => r.url).map((r) => [r.url, `${r.name} (${r.url})`])] },
  { name: "build", label: "Build (contexto, opcional)", placeholder: "./api", mono: true },
  { name: "dockerfile", label: "Dockerfile do build", placeholder: "Dockerfile", mono: true },
  { name: "container_name", label: "container_name", mono: true },
  { name: "restart", label: "Restart", type: "select", options: ["unless-stopped", "always", "on-failure", "no"], value: "unless-stopped" },
  { name: "user", label: "user", placeholder: "10001:10001", mono: true },
  { name: "ports", label: "Portas (host:container, uma por linha)", type: "textarea", rows: 2, placeholder: "8080:80" },
  { name: "volumes", label: "Volumes (origem:destino[:ro], um por linha)", type: "textarea", rows: 2, placeholder: "dados:/var/lib/app" },
  { name: "environment", label: "Environment (CHAVE=valor por linha)", type: "textarea", rows: 3, placeholder: "TZ=America/Sao_Paulo" },
  { name: "env_file", label: "env_file", placeholder: ".env", mono: true },
  { name: "networks", label: "Redes (vírgula)", placeholder: "backend, frontend" },
  { name: "depends_on", label: "depends_on (vírgula)", placeholder: "db" },
  { name: "command", label: "command", placeholder: "--config /etc/app.yaml", mono: true, span: true },
  { name: "hc_test", label: "Healthcheck (comando)", placeholder: "wget -qO- http://127.0.0.1/ || exit 1", mono: true, span: true },
  { name: "mem_limit", label: "Limite de memória", placeholder: "512m" },
  { name: "cpus", label: "Limite de CPUs", placeholder: "1.5" },
  { name: "cap_add", label: "cap_add (vírgula)", placeholder: "NET_BIND_SERVICE" },
  { name: "read_only", label: "read_only (FS somente leitura)", type: "bool" },
  { name: "no_new_privileges", label: "no-new-privileges", type: "bool", value: true },
  { name: "cap_drop_all", label: "cap_drop: ALL", type: "bool" },
  { name: "labels", label: "Labels (chave=valor)", type: "textarea", rows: 2, span: true },
];

const NET_FIELDS = [
  { name: "name", label: "Nome", placeholder: "backend", mono: true },
  { name: "driver", label: "Driver", type: "select", options: ["bridge", "macvlan", "ipvlan", "overlay"], value: "bridge" },
  { name: "subnet", label: "Subnet (IPAM)", placeholder: "172.30.0.0/24", mono: true },
  { name: "gateway", label: "Gateway", placeholder: "172.30.0.1", mono: true },
  { name: "parent", label: "Interface pai (macvlan/ipvlan)", placeholder: "eth0", mono: true },
  { name: "internal", label: "internal (sem saída externa)", type: "bool" },
  { name: "external", label: "external (rede já existente)", type: "bool" },
];

const VOL_FIELDS = [
  { name: "name", label: "Nome", placeholder: "dados", mono: true },
  { name: "driver", label: "Driver", placeholder: "local", mono: true },
  { name: "driver_opts", label: "driver_opts (chave=valor por linha)", type: "textarea", rows: 2, placeholder: "type=nfs\no=addr=10.0.0.5,nfsvers=4\ndevice=:/srv/nfs/dados" },
  { name: "external", label: "external (volume já existente)", type: "bool" },
];

export async function render(root, ctx) {
  const tab0 = ctx.query.get("tab") || "dockerfile";
  const body = h("div");
  const show = (id) => { clear(body); ({ dockerfile: dockerfileView, compose: composeView, registries: registriesView, files: filesView })[id](body, ctx); };
  mount(root,
    pageHead("Docker", "Gere Dockerfiles e docker-compose.yaml, gerencie registries e faça login no Docker Hub.", null, ["DevOps & Cloud"]),
    h("section", { class: "card" }, tabs([
      { id: "dockerfile", label: "Dockerfile", icon: "file" },
      { id: "compose", label: "docker-compose", icon: "layers" },
      { id: "registries", label: "Registries", icon: "key" },
      { id: "files", label: "Arquivos salvos", icon: "folder" },
    ], tab0, show), h("div", { class: "card-body" }, body)));
  show(tab0);
}

// resultado gerado + ações (salvar, baixar, copiar, IA)
function resultPanel(ctx, kind, defaultName, filename) {
  const ed = editor({ rows: 24, placeholder: "O arquivo gerado aparece aqui. Você também pode editar livremente." });
  const name = input({ placeholder: "nome", value: defaultName });
  let savedId = null;
  const actions = h("div", { class: "row" },
    ctx.canManage ? btn("Salvar", { icon: "save", cls: "primary sm", onClick: async () => {
      if (!ed.value.trim()) return toast("Nada para salvar", "err");
      const payload = { kind, name: name.value, content: ed.value };
      const r = savedId ? await put(`/api/docker/files/${savedId}`, payload) : await post("/api/docker/files", payload);
      savedId = r.id;
      toast("Arquivo salvo", "ok");
    } }) : null,
    btn("Baixar", { icon: "download", cls: "sm", onClick: () => saveText(ed.value, filename) }),
    btn("Copiar", { icon: "copy", cls: "sm", onClick: () => copyText(ed.value) }),
    ctx.can("ai") ? btn("Revisar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "docker", filename, content: ed.value,
      prompt: kind === "dockerfile" ? "Revise este Dockerfile (segurança, tamanho da imagem e boas práticas)." : "Revise este docker-compose (segurança, redes e volumes)." }) }) : null);
  const el = h("div", { class: "stack sm" }, h("div", { class: "row nw" }, field("Nome para salvar", name, null, "grow")), actions, ed.el);
  return { el, ed, setName: (n) => { name.value = n; } };
}

// ---------------- Dockerfile ----------------
async function dockerfileView(root, ctx) {
  mount(root, loading());
  let presets = {};
  try { presets = await get("/api/docker/presets"); } catch (e) { toastErr(e); }
  const form = formBuilder(DF_FIELDS, presets.debian || {});
  const preset = select([["", "— escolha um ponto de partida —"], ["go", "Go (multi-stage → distroless)"], ["node", "Node.js (multi-stage)"],
    ["python", "Python (slim, não-root)"], ["nginx", "Site estático (nginx sem privilégios)"], ["debian", "Debian trixie genérico"]], "debian");
  preset.addEventListener("change", () => {
    const p = presets[preset.value];
    if (!p) return;
    const blank = Object.fromEntries(DF_FIELDS.map((f) => [f.name, f.type === "bool" ? false : ""]));
    form.set({ ...blank, ...p });
  });
  const out = resultPanel(ctx, "dockerfile", "app", "Dockerfile");
  let ignore = "";
  const gen = btn("Gerar Dockerfile", { icon: "bolt", cls: "primary", onClick: async () => {
    const r = await post("/api/docker/generate/dockerfile", form.values());
    out.ed.value = r.content;
    ignore = r.dockerignore;
    toast("Dockerfile gerado", "ok");
  } });
  mount(root, h("div", { class: "grid g2" },
    h("div", { class: "stack" },
      h("div", { class: "alert info" }, h("div", { class: "alert-icon" }, icon("info")),
        h("div", null, h("strong", { text: "Boas práticas aplicadas" }), h("p", { text: "Multi-stage, usuário não-root, cache de pacotes limpo e CMD/ENTRYPOINT em formato exec. Prefira tags fixas ou digest (@sha256)." }))),
      field("Modelo", preset), form, h("div", { class: "row" }, gen,
        btn(".dockerignore", { icon: "download", cls: "sm", onClick: () => ignore ? saveText(ignore, ".dockerignore") : toast("Gere o Dockerfile primeiro", "err") }))),
    out.el));
}

// ---------------- docker-compose ----------------
async function composeView(root, ctx) {
  mount(root, loading());
  let registries = [];
  try { registries = await get("/api/docker/registries"); } catch (_) { /* sem permissão ou vazio */ }
  const project = input({ placeholder: "minha-stack", class: "mono" });
  const services = h("div", { class: "stack" });
  const networks = h("div", { class: "stack" });
  const volumes = h("div", { class: "stack" });
  const item = (container, defs, values, title) => {
    const form = formBuilder(defs, values);
    const box = h("div", { class: "fieldset" },
      h("div", { class: "legend" }, h("span", { text: title }), h("button", { class: "btn xs ghost", type: "button", onclick: () => box.remove() }, icon("trash", "sm"), "remover")),
      form);
    box.form = form;
    container.appendChild(box);
  };
  const addSvc = (v = {}) => item(services, SVC_FIELDS(registries), v, "Serviço");
  addSvc({ name: "web", image: "nginx:1.29-alpine", ports: "8080:80", networks: "frontend", no_new_privileges: true });
  const out = resultPanel(ctx, "compose", "stack", "compose.yaml");
  const gen = btn("Gerar docker-compose.yaml", { icon: "bolt", cls: "primary", onClick: async () => {
    const csv = (s) => (s || "").split(",").map((x) => x.trim()).filter(Boolean);
    const spec = {
      name: project.value.trim(),
      services: [...services.children].map((b) => {
        const v = b.form.values();
        return { ...v, networks: csv(v.networks), depends_on: csv(v.depends_on),
          healthcheck: { test: v.hc_test, interval: "30s", timeout: "5s", retries: 3 } };
      }),
      networks: [...networks.children].map((b) => b.form.values()),
      volumes: [...volumes.children].map((b) => b.form.values()),
    };
    const r = await post("/api/docker/generate/compose", spec);
    out.ed.value = r.content;
    if (spec.name) out.setName(spec.name);
    toast("docker-compose gerado", "ok");
  } });
  mount(root, h("div", { class: "grid g2" },
    h("div", { class: "stack" },
      field("Nome do projeto (name:)", project, "Opcional. Formato atual do Compose — sem a chave version."),
      h("div", { class: "spread" }, h("b", { text: "Serviços" }), btn("Serviço", { icon: "plus", cls: "sm", onClick: () => addSvc() })), services,
      h("div", { class: "spread" }, h("b", { text: "Redes" }), btn("Rede", { icon: "plus", cls: "sm", onClick: () => item(networks, NET_FIELDS, {}, "Rede") })), networks,
      h("div", { class: "spread" }, h("b", { text: "Volumes" }), btn("Volume", { icon: "plus", cls: "sm", onClick: () => item(volumes, VOL_FIELDS, {}, "Volume") })), volumes,
      h("div", null, gen)),
    out.el));
}

// ---------------- Registries ----------------
async function registriesView(root, ctx) {
  const M = ctx.canManage;
  const list = h("div", null, loading());
  const load = async () => {
    let regs;
    try { regs = await get("/api/docker/registries"); } catch (e) { return toastErr(e); }
    mount(list, table([
      { label: "Nome", render: (r) => h("strong", { text: r.name }) },
      { label: "Registry", render: (r) => h("span", { class: "mono", text: r.url || "Docker Hub (docker.io)" }) },
      { label: "Usuário", render: (r) => h("span", { class: "mono", text: r.username || "—" }) },
      { label: "Sessão", render: (r) => r.logged_in ? badge("logado", "ok", true) : badge("deslogado", "", true) },
      { label: "Último login", render: (r) => h("span", { class: "muted", text: fmtRel(r.last_login_at) }) },
      { label: "", cls: "actions", render: (r) => M ? h("div", { class: "row", style: { justifyContent: "flex-end" } },
        btn("Login", { icon: "key", cls: "xs", onClick: () => jobModal(`docker login — ${r.name}`, () => post(`/api/docker/registries/${r.id}/login`, {}), { onEnd: load }) }),
        btn("Logout", { cls: "xs", onClick: () => jobModal(`docker logout — ${r.name}`, () => post(`/api/docker/registries/${r.id}/logout`, {}), { onEnd: load }) }),
        btn("", { icon: "trash", cls: "xs danger", title: "Remover", onClick: async () => {
          if (!(await confirmDialog({ title: "Remover registry", message: `Remover "${r.name}" do doomctl? (a sessão do docker login não é encerrada automaticamente)`, danger: true, confirmText: "Remover" }))) return;
          await del(`/api/docker/registries/${r.id}`); toast("Registry removido", "ok"); load();
        } })) : null },
    ], regs, { empty: emptyState("key", "Nenhum registry cadastrado", "Cadastre o Docker Hub ou um registry privado para fazer login.") }));
  };
  const add = () => {
    const f = formBuilder([
      { name: "name", label: "Nome", placeholder: "dockerhub" },
      { name: "url", label: "Endereço do registry", placeholder: "vazio = Docker Hub · ex.: ghcr.io, registry.local:5000", mono: true },
      { name: "username", label: "Usuário", mono: true },
      { name: "password", label: "Senha ou token de acesso", type: "password", hint: "Guardada criptografada (AES-256-GCM). No Docker Hub, prefira um Personal Access Token." },
    ]);
    modal({ title: "Novo registry", body: f, actions: [{ label: "Cancelar" }, { label: "Salvar", primary: true, onClick: async () => {
      await post("/api/docker/registries", f.values()); toast("Registry salvo", "ok"); load();
    } }] });
  };
  mount(root, h("div", { class: "stack" },
    h("div", { class: "spread" }, h("p", { class: "muted", text: "O login usa --password-stdin e grava as credenciais no DOCKER_CONFIG do doomctl (usado também pelo Trivy para imagens privadas)." }),
      M ? btn("Novo registry", { icon: "plus", cls: "primary sm", onClick: add }) : null),
    list));
  load();
}

// ---------------- Arquivos salvos ----------------
async function filesView(root, ctx) {
  const M = ctx.canManage;
  const list = h("div", null, loading());
  const open = async (f) => {
    const full = await get(`/api/docker/files/${f.id}`);
    const ed = editor({ value: full.content, rows: 22, readOnly: !M });
    const name = input({ value: full.name });
    modal({
      title: `${f.kind === "compose" ? "docker-compose" : "Dockerfile"} — ${f.name}`, size: "xwide",
      body: h("div", { class: "stack sm" }, field("Nome", name), ed.el),
      actions: [
        { label: "Baixar", icon: "download", onClick: () => { saveText(ed.value, f.kind === "compose" ? "compose.yaml" : "Dockerfile"); return false; } },
        ...(M ? [
          { label: f.kind === "compose" ? "Validar (compose config)" : "Analisar (trivy config)", icon: "shield", onClick: async () => {
            await put(`/api/docker/files/${f.id}`, { kind: f.kind, name: name.value, content: ed.value });
            jobModal(`Validação — ${name.value}`, () => post(`/api/docker/files/${f.id}/validate`, {}));
            return false;
          } },
          { label: "Salvar", primary: true, icon: "save", onClick: async () => {
            await put(`/api/docker/files/${f.id}`, { kind: f.kind, name: name.value, content: ed.value }); toast("Salvo", "ok"); load();
          } }] : []),
      ],
    });
  };
  const load = async () => {
    let files;
    try { files = await get("/api/docker/files"); } catch (e) { return toastErr(e); }
    mount(list, table([
      { label: "Tipo", render: (r) => badge(r.kind === "compose" ? "compose" : "Dockerfile", r.kind === "compose" ? "violet" : "info") },
      { label: "Nome", render: (r) => h("strong", { text: r.name }) },
      { label: "Atualizado", render: (r) => h("span", { class: "muted", text: fmtRel(r.updated_at) }) },
      { label: "", cls: "actions", render: (r) => h("div", { class: "row", style: { justifyContent: "flex-end" } },
        btn("Abrir", { icon: "edit", cls: "xs", onClick: () => open(r) }),
        M ? btn("", { icon: "trash", cls: "xs danger", title: "Excluir", onClick: async () => {
          if (!(await confirmDialog({ title: "Excluir arquivo", message: `Excluir "${r.name}"?`, danger: true, confirmText: "Excluir" }))) return;
          await del(`/api/docker/files/${r.id}`); toast("Excluído", "ok"); load();
        } }) : null) },
    ], files, { onRowClick: open, empty: emptyState("folder", "Nenhum arquivo salvo", "Gere um Dockerfile ou docker-compose e clique em Salvar.") }));
  };
  mount(root, list);
  load();
}
