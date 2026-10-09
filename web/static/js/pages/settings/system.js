import { get } from "../../api.js";
import { h, mount, btn, badge, card, table, loading, fmtDate, toastErr } from "../../ui.js";

export async function render(root) {
  mount(root, loading());
  const load = async (refresh) => {
    let s;
    try { s = await get("/api/system" + (refresh ? "?refresh=1" : "")); } catch (e) { return toastErr(e); }
    const o = s.ollama;
    const cfg = Object.entries(s.config);
    mount(root,
      h("div", { class: "stack" },
        h("div", { class: "spread" }, h("p", { class: "muted small", text: "Estado do servidor, ferramentas instaladas e configuração ativa (variáveis de ambiente)." }),
          btn("Recarregar", { icon: "refresh", cls: "sm", onClick: () => load(true) })),
        h("div", { class: "stat-row" },
          stat("Versão", s.version), stat("Go", s.go.replace("go", "")), stat("PostgreSQL", (s.postgres || "").split(" ")[0]),
          stat("Jobs ativos", String(s.running_jobs))),
        h("div", { class: "grid g2" },
          card({ title: "Ferramentas no servidor", subtitle: "Detectadas no PATH do container doomctl.", flush: true, body: table([
            { label: "Ferramenta", render: (t) => h("strong", { class: "mono", text: t.name }) },
            { label: "Status", render: (t) => t.installed ? badge("instalada", "ok", true) : badge("ausente", "err", true) },
            { label: "Versão", render: (t) => h("span", { class: "mono", text: t.version || "—" }) },
            { label: "Caminho", render: (t) => h("span", { class: "mono small muted", text: t.path || "—" }) },
          ], s.tools) }),
          h("div", { class: "stack" },
            card({ title: "Assistente IA (Ollama padrão)", actions: [h("a", { class: "btn sm", href: "#/settings/agentes" }, "Configurar agentes")], body: h("dl", { class: "kv" },
              h("dt", { text: "Servidor" }), h("dd", null, o.online ? badge("online", "ok", true) : badge("offline", "err", true), " ", h("span", { text: s.config.ollama_host })),
              h("dt", { text: "Modelo" }), h("dd", null, h("span", { text: o.model + " " }), o.model_found ? badge("disponível", "ok") : badge("não baixado", "warn")),
              h("dt", { text: "Modelos locais" }), h("dd", { text: (o.models || []).join(", ") || "—" }),
              o.error ? [h("dt", { text: "Erro" }), h("dd", { text: o.error })] : null) }),
            card({ title: "Docker", body: h("dl", { class: "kv" },
              h("dt", { text: "Socket" }), h("dd", null, h("span", { text: s.docker_socket.path + " " }), s.docker_socket.present ? badge("montado", "ok") : badge("ausente", "warn")),
              h("dt", { text: "Relatórios" }), h("dd", { text: (s.reports_bytes / 1048576).toFixed(1) + " MiB em artefatos" })) }))),
        card({ title: "Configuração ativa", subtitle: "Para alterar, edite o .env e reinicie o container (docker compose up -d).", flush: true, body: table([
          { label: "Chave", render: ([k]) => h("span", { class: "mono", text: k }) },
          { label: "Valor", render: ([, v]) => h("span", { class: "mono", text: String(v) }) },
        ], cfg) }),
        h("p", { class: "small faint", text: "Horário do servidor: " + fmtDate(s.time) })));
  };
  load(false);
}

function stat(l, n) { return h("div", { class: "stat" }, h("div", { class: "n", text: n || "—" }), h("div", { class: "l", text: l })); }
