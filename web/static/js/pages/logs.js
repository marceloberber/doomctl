import { get, post, download, saveText } from "../api.js";
import {
  h, mount, clear, icon, btn, badge, card, table, field, input, select, terminal, toast, toastErr, loading, pageHead,
  fmtDate, fmtRel, fmtDur, statusBadge, emptyState, copyText, debounce, confirmDialog, streamJobInto,
} from "../ui.js";

const MODULES = [["", "Todas as ferramentas"], ["ansible", "Ansible"], ["docker", "Docker"], ["kubernetes", "Kubernetes"], ["opentofu", "OpenTofu"], ["finops", "FinOps"],
  ["netcalc", "Sub-redes"], ["trivy", "Trivy"], ["ai", "Assistente IA"], ["plugins", "Plug-ins"], ["logs", "Logs"]];
const ADMIN_MODULES = [["auth", "Autenticação"], ["users", "Usuários e permissões"]];
const STATUSES = [["", "Todos os status"], ["success", "Sucesso"], ["failed", "Falhou"], ["running", "Executando"], ["canceled", "Cancelado"],
  ["interrupted", "Interrompido"], ["info", "Registro"]];

export async function render(root, ctx) {
  if (ctx.rest[0]) return detail(root, ctx, Number(ctx.rest[0]));
  const isAdmin = ctx.me.user.role === "admin";
  const mod = select(isAdmin ? [...MODULES, ...ADMIN_MODULES] : MODULES, ctx.query.get("module") || "");
  const st = select(STATUSES, ctx.query.get("status") || "");
  const q = input({ type: "search", placeholder: "Buscar por alvo, ação ou usuário" });
  const list = h("div", null, loading());
  const pager = h("div", { class: "spread", style: { padding: "12px 16px" } });
  let offset = 0;
  const limit = 50;
  const load = async () => {
    let r;
    try {
      r = await get(`/api/logs?limit=${limit}&offset=${offset}&module=${encodeURIComponent(mod.value)}&status=${encodeURIComponent(st.value)}&q=${encodeURIComponent(q.value.trim())}`);
    } catch (e) { return toastErr(e); }
    mount(list, table([
      { label: "#", render: (l) => h("span", { class: "mono small faint", text: String(l.id) }) },
      { label: "Ferramenta", render: (l) => h("strong", { text: (MODULES.concat(ADMIN_MODULES).find((m) => m[0] === l.module) || [l.module, l.module])[1] }) },
      { label: "Ação", render: (l) => h("span", { class: "mono small", text: l.action }) },
      { label: "Alvo", render: (l) => h("span", { class: "small", text: (l.target || "—").slice(0, 70) }) },
      { label: "Usuário", render: (l) => h("span", { text: l.username || "—" }) },
      { label: "Status", render: (l) => statusBadge(l.status) },
      { label: "Início", render: (l) => h("span", { class: "nowrap muted small", text: fmtDate(l.started_at) }) },
      { label: "Duração", cls: "right", render: (l) => h("span", { class: "mono small", text: fmtDur(l.duration_ms) }) },
      { label: "", render: (l) => l.has_artifact ? icon("download", "sm") : null },
    ], r.items, { onRowClick: (l) => ctx.navigate("logs/" + l.id), empty: emptyState("doc", "Nenhum registro encontrado", "Ajuste os filtros.") }));
    mount(pager, h("span", { class: "small muted", text: `${r.total} registro(s)` }), h("div", { class: "row" },
      btn("Anterior", { icon: "left", cls: "sm", disabled: offset === 0, onClick: () => { offset = Math.max(0, offset - limit); load(); } }),
      btn("Próxima", { cls: "sm", disabled: offset + limit >= r.total, onClick: () => { offset += limit; load(); } })));
  };
  [mod, st].forEach((s) => s.addEventListener("change", () => { offset = 0; load(); }));
  q.addEventListener("input", debounce(() => { offset = 0; load(); }, 300));
  mount(root,
    pageHead("Logs", "Saídas e registros de todas as ferramentas, separados por ferramenta.",
      isAdmin ? [btn("Limpar antigos", { icon: "trash", cls: "danger", onClick: async () => {
        const days = await confirmDialog({ title: "Remover registros antigos", danger: true, confirmText: "Remover",
          message: "Remove registros (e artefatos) com mais de 90 dias. Digite 90 para confirmar ou outro número de dias.", requireText: "90" });
        if (!days) return;
        const r = await post("/api/logs/purge", { days: Number(days) });
        toast(`${r.deleted} registro(s) removido(s)`, "ok"); load();
      } })] : null, ["Operação"]),
    h("section", { class: "card" },
      h("div", { class: "card-head" }, h("div", { class: "row grow" }, h("div", { style: { minWidth: "180px" } }, mod), h("div", { style: { minWidth: "160px" } }, st), h("div", { class: "grow", style: { minWidth: "200px" } }, q))),
      list, pager));
  load();
}

async function detail(root, ctx, id) {
  mount(root, loading());
  let l;
  try { l = await get(`/api/logs/${id}`); } catch (e) { toastErr(e); return ctx.navigate("logs"); }
  const term = terminal({ title: `${l.module} · ${l.action}`, height: "520px" });
  term.write(l.output || "(sem saída)");
  term.flush();
  term.status(statusBadge(l.status));
  let resultNode = null;
  if (l.result) {
    if (l.module === "trivy") {
      const { renderTrivyResult } = await import("./trivy.js");
      resultNode = renderTrivyResult(l.result, l.has_artifact ? l.id : null);
    } else {
      resultNode = h("div", { class: "codebox" }, h("pre", { text: JSON.stringify(l.result, null, 2) }));
    }
  }
  const params = l.input && Object.keys(l.input).length ? JSON.stringify(l.input, null, 2) : null;
  mount(root,
    pageHead(`Execução #${l.id}`, `${l.module} · ${l.action}${l.target ? " · " + l.target : ""}`,
      [btn("Voltar", { icon: "left", onClick: () => history.length > 1 ? history.back() : ctx.navigate("logs") }),
        btn("Copiar saída", { icon: "copy", onClick: () => copyText(l.output || "") }),
        btn("Baixar .log", { icon: "download", onClick: () => saveText(l.output || "", `doomctl-${l.module}-${l.id}.log`) }),
        l.has_artifact ? btn("Artefato", { icon: "download", cls: "primary", onClick: () => download(`/api/logs/${l.id}/artifact`) }) : null],
      ["Logs", statusBadge(l.status)]),
    h("div", { class: "split wide" },
      card({ title: "Detalhes", body: h("dl", { class: "kv" },
        h("dt", { text: "Ferramenta" }), h("dd", { text: l.module }),
        h("dt", { text: "Ação" }), h("dd", { text: l.action }),
        h("dt", { text: "Alvo" }), h("dd", { text: l.target || "—" }),
        h("dt", { text: "Usuário" }), h("dd", { text: l.username || "—" }),
        h("dt", { text: "Início" }), h("dd", { text: fmtDate(l.started_at) }),
        h("dt", { text: "Fim" }), h("dd", { text: fmtDate(l.finished_at) }),
        h("dt", { text: "Duração" }), h("dd", { text: fmtDur(l.duration_ms) }),
        h("dt", { text: "Exit code" }), h("dd", { text: l.exit_code === null || l.exit_code === undefined ? "—" : String(l.exit_code) }),
        params ? [h("dt", { text: "Parâmetros" }), h("dd", null, h("pre", { class: "small", style: { margin: 0, whiteSpace: "pre-wrap" }, text: params }))] : null) }),
      h("div", { class: "stack" },
        card({ title: "Saída", body: term.el }),
        resultNode ? card({ title: "Resultado", body: resultNode }) : null)));
  if (l.status === "running" || l.status === "queued") {
    try {
      const jobs = await get("/api/jobs");
      const j = jobs.find((x) => x.log_id === l.id);
      if (j) { term.clear(); streamJobInto(j.id, term); }
    } catch (_) { /* ignore */ }
  }
}
