import { get, post } from "../api.js";
import { h, mount, icon, btn, badge, card, checkbox, confirmDialog, modal, jobModal, toastErr, loading, pageHead } from "../ui.js";

const ST = { running: ["em execução", "ok"], stopped: ["parado", "warn"], not_installed: ["não instalado", ""], unavailable: ["indisponível", "err"] };

export async function render(root, ctx) {
  const M = ctx.canManage;
  const box = h("div", null, loading());
  mount(root, pageHead("Plug-ins de terceiros", "Aplicações instaladas à parte, acessadas em outra porta.", null, ["Operação"]), box);
  const load = async () => {
    let list;
    try { list = await get("/api/plugins"); } catch (e) { return toastErr(e); }
    mount(box, h("div", { class: "grid g2" }, list.map((p) => pluginCard(p, M, load))));
  };
  load();
}

function pluginCard(p, M, reload) {
  const [label, kind] = ST[p.status] || [p.status, ""];
  const url = `${p.scheme}://${location.hostname}:${p.port}`;
  const act = (action, body = {}) => jobModal(`Portainer — ${action}`, () => post(`/api/plugins/${p.id}/${action}`, body), { onEnd: reload });
  const actions = [];
  if (M) {
    if (p.status === "not_installed") {
      actions.push(btn("Instalar", { icon: "download", cls: "primary", onClick: () => {
        const edge = checkbox("Publicar também a porta 8000 (túnel para Edge Agents)", false);
        modal({ title: "Instalar Portainer CE", body: h("div", { class: "stack" },
          h("p", { text: `Será executado o container ${p.image} na porta ${p.port} (HTTPS), com volume persistente e acesso ao socket do Docker.` }),
          h("div", { class: "alert warn" }, h("div", { class: "alert-icon" }, icon("alert")),
            h("div", null, h("strong", { text: "Crie o administrador do Portainer em até 5 minutos" }), h("p", { text: "Passado esse prazo, o Portainer bloqueia o setup inicial por segurança e precisa ser reiniciado." }))),
          edge),
          actions: [{ label: "Cancelar" }, { label: "Instalar", primary: true, onClick: () => { act("install", { edge_port: edge.input.checked }); } }] });
      } }));
    } else if (p.status !== "unavailable") {
      if (p.status === "running") actions.push(btn("Parar", { icon: "stop", onClick: () => act("stop") }), btn("Reiniciar", { icon: "refresh", onClick: () => act("restart") }));
      else actions.push(btn("Iniciar", { icon: "play", cls: "primary", onClick: () => act("start") }));
      actions.push(btn("Atualizar imagem", { icon: "download", onClick: async () => {
        if (await confirmDialog({ title: "Atualizar Portainer", message: "Baixa a imagem configurada e recria o container (os dados no volume são mantidos).", confirmText: "Atualizar" })) act("update");
      } }));
      actions.push(btn("Desinstalar", { icon: "trash", cls: "danger", onClick: async () => {
        const purge = checkbox("Apagar também os dados (volume doomctl_portainer_data)", false);
        modal({ title: "Desinstalar Portainer", body: h("div", { class: "stack" }, h("p", { text: "O container será removido." }), purge),
          actions: [{ label: "Cancelar" }, { label: "Desinstalar", danger: true, onClick: async () => {
            if (purge.input.checked) {
              const c = await confirmDialog({ title: "🛑 Apagar dados do Portainer", danger: true, requireText: "portainer", confirmText: "Apagar tudo",
                message: "Usuários, endpoints e configurações do Portainer serão perdidos." });
              if (!c) return false;
              act("uninstall", { purge: true, confirm: c });
            } else act("uninstall");
          } }] });
      } }));
    }
  }
  return card({
    title: p.name,
    subtitle: p.description,
    actions: badge(label, kind, true),
    body: h("div", { class: "stack" },
      h("dl", { class: "kv" },
        h("dt", { text: "Imagem" }), h("dd", { text: p.image }),
        h("dt", { text: "Porta" }), h("dd", { text: `${p.port} (HTTPS)` }),
        h("dt", { text: "Acesso" }), h("dd", null, p.status === "running" ? h("a", { href: url, target: "_blank", rel: "noopener noreferrer" }, url, " ", icon("external", "sm")) : h("span", { class: "faint", text: url }))),
      p.error ? h("div", { class: "alert err" }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: p.error }))) : null,
      h("ul", { class: "small muted", style: { margin: 0, paddingLeft: "18px" } }, p.notes.map((n) => h("li", { text: n })))),
    foot: actions.length ? actions : null,
  });
}
