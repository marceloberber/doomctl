import { get, post, put, del } from "../api.js";
import {
  h, mount, clear, icon, btn, badge, card, table, tabs, modal, confirmDialog, field, input, select, checkbox, toast, toastErr,
  loading, pageHead, fmtRel, formBuilder, copyText,
} from "../ui.js";

const ROLES = [["admin", "Administrador"], ["operator", "Operador"], ["viewer", "Visualizador"]];
const roleBadge = (r) => badge(ROLES.find((x) => x[0] === r)[1], r === "admin" ? "brand" : r === "operator" ? "info" : "");

export async function render(root, ctx) {
  const body = h("div");
  const show = (id) => { clear(body); (id === "perms" ? permsView : usersView)(body, ctx); };
  mount(root,
    pageHead("Usuários e permissões", "Perfis Administrador, Operador e Visualizador; permissões de leitura e gerenciamento por ferramenta; MFA TOTP.", null, ["Administração"]),
    h("section", { class: "card" }, tabs([{ id: "users", label: "Usuários", icon: "users" }, { id: "perms", label: "Permissões por módulo", icon: "lock" }], "users", show),
      h("div", { class: "card-body" }, body)));
  show("users");
}

function showTempPassword(user, pw) {
  modal({ title: "Senha temporária", body: h("div", { class: "stack" },
    h("p", { text: `Entregue esta senha a ${user} por um canal seguro. Ela será trocada obrigatoriamente no primeiro acesso.` }),
    h("div", { class: "codebox" }, h("pre", { style: { fontSize: "18px", textAlign: "center" }, text: pw }), btn("", { icon: "copy", cls: "xs", onClick: () => copyText(pw) }))),
    actions: [{ label: "OK", primary: true }] });
}

function mfaBadge(u) {
  if (u.mfa_enabled) return badge(u.mfa_required ? "ativo · exigido" : "ativo", "ok", true);
  if (u.mfa_required) return badge("exigido · pendente", "warn", true);
  return badge("desativado", "", true);
}

async function usersView(root, ctx) {
  const list = h("div", null, loading());
  const load = async () => {
    let us;
    try { us = await get("/api/users"); } catch (e) { return toastErr(e); }
    mount(list, table([
      { label: "Usuário", render: (u) => h("div", null, h("strong", { class: "mono", text: u.username }), h("div", { class: "small muted", text: u.full_name || u.email || "" })) },
      { label: "Perfil", render: (u) => roleBadge(u.role) },
      { label: "Status", render: (u) => !u.active ? badge("desativado", "err") : u.locked ? badge("bloqueado (tentativas)", "warn") : u.must_change_password ? badge("trocar senha", "warn") : badge("ativo", "ok") },
      { label: "MFA", render: mfaBadge },
      { label: "Sessões", render: (u) => String(u.sessions) },
      { label: "Último login", render: (u) => h("span", { class: "muted", text: fmtRel(u.last_login_at) }) },
      { label: "", cls: "actions", render: (u) => h("div", { class: "row nw", style: { justifyContent: "flex-end", gap: "6px" } },
        btn("Editar", { icon: "edit", cls: "xs", onClick: () => edit(u) }),
        btn("MFA", { icon: "key", cls: "xs", onClick: () => mfa(u) }),
        btn("Senha", { icon: "lock", cls: "xs", onClick: async () => {
          if (!(await confirmDialog({ title: "Redefinir senha", message: `Gerar nova senha temporária para ${u.username}? As sessões dele serão encerradas.`, confirmText: "Redefinir" }))) return;
          const r = await post(`/api/users/${u.id}/password`, {}); showTempPassword(u.username, r.temporary_password); load();
        } }),
        u.id !== ctx.me.user.id ? btn("", { icon: "trash", cls: "xs danger", title: "Excluir", onClick: async () => {
          if (!(await confirmDialog({ title: "Excluir usuário", danger: true, confirmText: "Excluir", message: `Excluir ${u.username}? Os registros de log são mantidos.` }))) return;
          await del(`/api/users/${u.id}`); toast("Usuário excluído", "ok"); load();
        } }) : null) },
    ], us));
  };
  const create = () => {
    const f = formBuilder([
      { name: "username", label: "Usuário", placeholder: "joao.silva", mono: true },
      { name: "role", label: "Perfil", type: "select", options: ROLES, value: "operator" },
      { name: "full_name", label: "Nome completo" },
      { name: "email", label: "E-mail" },
      { name: "password", label: "Senha inicial", type: "password", hint: "Deixe vazio para gerar uma senha temporária." },
      { name: "mfa_required", label: "Exigir MFA (cadastro obrigatório no 1º login)", type: "bool", value: true },
    ]);
    modal({ title: "Novo usuário", size: "wide", body: h("div", { class: "stack" }, f, h("p", { class: "small muted", text: "Visualizadores só acessam relatórios (Visão Geral) e Logs, salvo liberação na matriz de permissões." })),
      actions: [{ label: "Cancelar" }, { label: "Criar", primary: true, onClick: async () => {
        const v = f.values();
        const r = await post("/api/users", v);
        toast("Usuário criado", "ok");
        if (r.temporary_password) showTempPassword(v.username, r.temporary_password);
        load();
      } }] });
  };
  const edit = (u) => {
    const f = formBuilder([
      { name: "full_name", label: "Nome completo" }, { name: "email", label: "E-mail" },
      { name: "role", label: "Perfil", type: "select", options: ROLES }, { name: "active", label: "Conta ativa", type: "bool" },
    ], u);
    modal({ title: `Editar ${u.username}`, body: f, actions: [
      { label: "Encerrar sessões", onClick: async () => { await post(`/api/users/${u.id}/revoke-sessions`, {}); toast("Sessões encerradas", "ok"); load(); } },
      { label: "Salvar", primary: true, onClick: async () => { await put(`/api/users/${u.id}`, f.values()); toast("Usuário atualizado", "ok"); load(); } }] });
  };
  const mfa = (u) => {
    const doAct = async (action, msg) => {
      if (!(await confirmDialog({ title: "MFA de " + u.username, message: msg, confirmText: "Confirmar", danger: action === "disable" || action === "reset" }))) return;
      const r = await post(`/api/users/${u.id}/mfa`, { action }); toast(r.message, "ok"); load();
    };
    const m = modal({ title: `MFA — ${u.username}`, body: h("div", { class: "stack" },
      h("div", { class: "row" }, h("span", { text: "Situação:" }), mfaBadge(u)),
      h("p", { class: "muted small", text: "Somente o administrador exige, libera ou desativa o MFA de outros usuários. Operadores e visualizadores gerenciam o próprio MFA quando não é exigido." }),
      h("div", { class: "stack sm" },
        btn("Exigir MFA (ativar)", { icon: "lock", cls: "primary", disabled: u.mfa_required, onClick: () => { m.close(); doAct("require", "O usuário precisará cadastrar o TOTP no próximo login (se ainda não tiver). Sessões sem MFA serão encerradas."); } }),
        btn("Deixar de exigir", { icon: "check", disabled: !u.mfa_required, onClick: () => { m.close(); doAct("release", "O MFA deixa de ser obrigatório. Se estiver ativo, continua ativo até o usuário desativar."); } }),
        btn("Resetar (recadastrar no próximo login)", { icon: "refresh", disabled: !u.mfa_enabled, onClick: () => { m.close(); doAct("reset", "Apaga o segredo TOTP (ex.: celular perdido) e exige novo cadastro no próximo login."); } }),
        btn("Desativar MFA", { icon: "x", cls: "danger", disabled: !u.mfa_enabled && !u.mfa_required, onClick: () => { m.close(); doAct("disable", "Desativa o MFA e apaga o segredo. A conta fica protegida apenas por senha."); } })))
    });
  };
  mount(root, h("div", { class: "stack" }, h("div", null, btn("Novo usuário", { icon: "plus", cls: "primary sm", onClick: create })), list));
  load();
}

async function permsView(root) {
  mount(root, loading());
  let data;
  try { data = await get("/api/permissions"); } catch (e) { return toastErr(e); }
  const mods = data.modules.filter((m) => !m.admin_only);
  const m = data.matrix;
  const cb = (role, mod, act) => {
    const c = h("input", { type: "checkbox", checked: m[role][mod][act], "aria-label": `${role} ${act} ${mod}` });
    c.addEventListener("change", () => {
      m[role][mod][act] = c.checked;
      if (act === "manage" && c.checked) { m[role][mod].read = true; c.closest("tr").querySelector(`[data-k="${role}-read"]`).checked = true; }
      if (act === "read" && !c.checked) { m[role][mod].manage = false; const x = c.closest("tr").querySelector(`[data-k="${role}-manage"]`); if (x) x.checked = false; }
    });
    c.dataset.k = `${role}-${act}`;
    return c;
  };
  const tbl = h("div", { class: "table-wrap" }, h("table", { class: "t" },
    h("thead", null, h("tr", null, h("th", { text: "Módulo" }), h("th", { text: "Admin" }), h("th", { text: "Operador · ler" }), h("th", { text: "Operador · gerenciar" }), h("th", { text: "Visualizador · ler" }))),
    h("tbody", null, mods.map((md) => h("tr", null,
      h("td", null, h("strong", { text: md.name }), md.status === "beta" ? h("span", { class: "tag beta", style: { marginLeft: "6px" }, text: "beta" }) : md.status === "soon" ? h("span", { class: "tag soon", style: { marginLeft: "6px" }, text: "em breve" }) : null),
      h("td", null, h("input", { type: "checkbox", checked: true, disabled: true, "aria-label": "admin" })),
      h("td", null, cb("operator", md.id, "read")), h("td", null, cb("operator", md.id, "manage")), h("td", null, cb("viewer", md.id, "read")))))));
  mount(root, h("div", { class: "stack" },
    h("div", { class: "alert info" }, h("div", { class: "alert-icon" }, icon("info")), h("div", null,
      h("strong", { text: "Ler × gerenciar" }),
      h("p", { text: "Ler: ver itens, resultados e saídas. Gerenciar: criar, editar, excluir e executar. Visualizadores nunca gerenciam. Usuários, Configurações e eventos de autenticação são exclusivos do administrador." }))),
    tbl,
    h("div", null, btn("Salvar permissões", { icon: "save", cls: "primary", onClick: async () => {
      await put("/api/permissions", { operator: m.operator, viewer: m.viewer });
      toast("Permissões salvas — valem imediatamente", "ok");
    } }))));
}
