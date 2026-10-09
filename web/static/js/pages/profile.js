import { get, post, put } from "../api.js";
import { h, mount, icon, btn, badge, card, table, modal, field, input, toast, toastErr, loading, pageHead, fmtRel, formBuilder } from "../ui.js";
import { mfaEnrollForm, roleName } from "../app.js";

export async function render(root, ctx) {
  const draw = async () => {
    const me = await ctx.reloadMe();
    const u = me.user;
    const prof = formBuilder([{ name: "full_name", label: "Nome completo" }, { name: "email", label: "E-mail" }], u);
    const cur = input({ type: "password", autocomplete: "current-password" });
    const nw = input({ type: "password", autocomplete: "new-password" });
    const nw2 = input({ type: "password", autocomplete: "new-password" });
    const sessions = h("div", null, loading());
    mount(root,
      pageHead(u.full_name || u.username, `${u.username} · ${roleName(u.role)}`, null, ["Meu perfil"]),
      h("div", { class: "grid g2", style: { alignItems: "start" } },
        h("div", { class: "stack" },
          card({ title: "Dados", body: prof, foot: btn("Salvar", { icon: "save", cls: "primary sm", onClick: async () => { await put("/api/me/profile", prof.values()); toast("Perfil atualizado", "ok"); } }) }),
          card({ title: "Senha", body: h("div", { class: "stack sm" }, field("Senha atual", cur), field("Nova senha", nw, "Mínimo 10 caracteres com 3 destes: minúsculas, maiúsculas, números, símbolos."), field("Confirmar nova senha", nw2)),
            foot: btn("Alterar senha", { icon: "lock", cls: "primary sm", onClick: async () => {
              if (nw.value !== nw2.value) return toast("As senhas não conferem", "err");
              await post("/api/auth/password", { current: cur.value, new: nw.value });
              cur.value = nw.value = nw2.value = "";
              toast("Senha alterada — outras sessões foram encerradas", "ok");
            } }) })),
        h("div", { class: "stack" },
          card({ title: "Verificação em duas etapas (MFA)", subtitle: "TOTP: Microsoft Authenticator, Google Authenticator, Authy, Aegis, 2FAS...",
            actions: u.mfa_enabled ? badge("ativo", "ok", true) : u.mfa_required ? badge("exigido", "warn", true) : badge("desativado", "", true),
            body: mfaSection(u, draw) }),
          card({ title: "Sessões ativas", flush: true, body: sessions,
            foot: btn("Encerrar outras sessões", { icon: "logout", cls: "sm", onClick: async () => { await post("/api/me/sessions/revoke-others", {}); toast("Outras sessões encerradas", "ok"); loadSessions(); } }) }))));
    const loadSessions = async () => {
      try {
        const ss = await get("/api/me/sessions");
        mount(sessions, table([
          { label: "Origem", render: (s) => h("div", null, h("strong", { class: "mono", text: s.ip }), s.current ? h("span", { style: { marginLeft: "6px" } }, badge("esta sessão", "brand")) : null,
            h("div", { class: "small muted", text: (s.user_agent || "").slice(0, 70) })) },
          { label: "Início", render: (s) => h("span", { class: "muted", text: fmtRel(s.created_at) }) },
          { label: "Atividade", render: (s) => h("span", { class: "muted", text: fmtRel(s.last_seen_at) }) },
        ], ss));
      } catch (e) { toastErr(e); }
    };
    loadSessions();
  };
  await draw();
}

function mfaSection(u, redraw) {
  if (!u.mfa_enabled) {
    return h("div", { class: "stack" },
      h("p", { class: "muted", text: u.mfa_required ? "O administrador exige MFA para a sua conta." : "Proteja sua conta com um segundo fator." }),
      h("div", null, btn("Configurar MFA", { icon: "key", cls: "primary", onClick: () => {
        const m = modal({ title: "Configurar MFA", size: "wide", body: mfaEnrollForm({ onDone: () => { m.close(); toast("MFA ativado", "ok"); redraw(); } }) });
      } })));
  }
  const pw = input({ type: "password", autocomplete: "current-password" });
  const code = input({ class: "otp", inputmode: "numeric", placeholder: "000000", autocomplete: "one-time-code" });
  const ask = (title, onOk) => modal({ title, body: h("div", { class: "stack sm" }, field("Senha", pw), field("Código do autenticador (ou de recuperação)", code)),
    actions: [{ label: "Cancelar" }, { label: "Confirmar", primary: true, onClick: () => onOk(pw.value, code.value) }] });
  return h("div", { class: "stack" },
    h("p", { class: "muted", text: "O MFA está ativo. Guarde os códigos de recuperação em local seguro." }),
    h("div", { class: "row" },
      btn("Novos códigos de recuperação", { icon: "refresh", cls: "sm", onClick: () => ask("Gerar novos códigos", async (p, c) => {
        const r = await post("/api/me/mfa/recovery", { password: p, code: c });
        modal({ title: "Novos códigos de recuperação", body: h("div", { class: "codes" }, r.recovery_codes.map((x) => h("span", { text: x }))), actions: [{ label: "Guardei", primary: true }] });
      }) }),
      u.mfa_required ? h("span", { class: "small muted" }, icon("lock", "sm"), " exigido pelo administrador — não pode ser desativado") :
        btn("Desativar MFA", { icon: "x", cls: "sm danger", onClick: () => ask("Desativar MFA", async (p, c) => {
          await post("/api/me/mfa/disable", { password: p, code: c }); toast("MFA desativado", "ok"); redraw();
        }) })));
}
