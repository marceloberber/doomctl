import { get, post, download } from "../api.js";
import {
  h, mount, clear, icon, btn, badge, card, table, field, input, select, checkbox, terminal, runJob, toast, toastErr,
  pageHead, fmtRel, statusBadge, emptyState,
} from "../ui.js";

const SEVS = ["CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"];

export function sevBadge(s) { return h("span", { class: "badge sev-" + s, text: s }); }

// renderTrivyResult desenha o resumo e as tabelas (usado também na página de Logs).
export function renderTrivyResult(res, logId) {
  if (!res || !res.summary) return h("p", { class: "muted", text: "Sem resultado estruturado." });
  const vfilter = select([["", "Todas as severidades"], ...SEVS.map((s) => [s, s])]);
  const q = input({ type: "search", placeholder: "Filtrar por CVE ou pacote" });
  const vbox = h("div");
  const drawV = () => {
    const list = res.vulnerabilities.filter((v) => (!vfilter.value || v.severity === vfilter.value) &&
      (!q.value || (v.id + v.pkg).toLowerCase().includes(q.value.toLowerCase())));
    mount(vbox, table([
      { label: "Severidade", render: (v) => sevBadge(v.severity) },
      { label: "ID", render: (v) => v.url ? h("a", { href: v.url, target: "_blank", rel: "noopener noreferrer", class: "mono", text: v.id }) : h("span", { class: "mono", text: v.id }) },
      { label: "Pacote", render: (v) => h("strong", { class: "mono", text: v.pkg }) },
      { label: "Instalado", render: (v) => h("span", { class: "mono small", text: v.installed }) },
      { label: "Corrigido em", render: (v) => v.fixed ? h("span", { class: "mono small up", text: v.fixed }) : h("span", { class: "faint small", text: "sem correção" }) },
      { label: "Título", render: (v) => h("span", { class: "small", text: (v.title || "").slice(0, 110) }) },
    ], list.slice(0, 500), { empty: h("p", { class: "muted", style: { padding: "16px" }, text: "Nenhuma vulnerabilidade com esse filtro." }) }));
  };
  vfilter.addEventListener("change", drawV);
  q.addEventListener("input", drawV);
  drawV();
  const s = res.summary;
  return h("div", { class: "stack" },
    h("div", { class: "spread" },
      h("div", { class: "row" }, SEVS.map((k) => h("span", { class: "badge sev-" + k }, `${k}: ${s[k] || 0}`))),
      logId ? btn("Relatório JSON", { icon: "download", cls: "sm", onClick: () => download(`/api/logs/${logId}/artifact`) }) : null),
    res.truncated ? h("p", { class: "small warn", text: "Lista truncada em 2000 itens — baixe o relatório completo." }) : null,
    res.vulnerabilities.length ? h("div", { class: "stack sm" }, h("div", { class: "row nw" }, vfilter, q), vbox) : null,
    res.misconfigurations.length ? h("div", { class: "stack sm" }, h("b", { text: `Misconfigurações (${res.misconfigurations.length})` }), table([
      { label: "Severidade", render: (m) => sevBadge(m.severity) },
      { label: "ID", render: (m) => m.url ? h("a", { href: m.url, target: "_blank", rel: "noopener noreferrer", class: "mono", text: m.id }) : h("span", { class: "mono", text: m.id }) },
      { label: "Problema", render: (m) => h("div", null, h("strong", { text: m.title }), h("div", { class: "small muted", text: m.message })) },
      { label: "Correção", render: (m) => h("span", { class: "small", text: m.resolution }) },
    ], res.misconfigurations)) : null,
    res.secrets.length ? h("div", { class: "stack sm" }, h("b", { text: `Segredos expostos (${res.secrets.length}) — rotacione-os` }), table([
      { label: "Severidade", render: (x) => sevBadge(x.severity) },
      { label: "Regra", render: (x) => h("span", { class: "mono", text: x.rule_id }) },
      { label: "Descrição", key: "title" },
      { label: "Local", render: (x) => h("span", { class: "mono small", text: `${x.target}:${x.line}` }) },
    ], res.secrets)) : null,
    !res.vulnerabilities.length && !res.misconfigurations.length && !res.secrets.length
      ? h("div", { class: "alert info" }, h("div", { class: "alert-icon" }, icon("checkCircle")), h("div", null, h("strong", { text: "Nenhum achado nas severidades selecionadas" }))) : null);
}

export async function render(root, ctx) {
  const M = ctx.canManage;
  const image = input({ placeholder: "nginx:1.29-alpine · registry.local:5000/app:1.0", class: "mono", list: "trivy-images" });
  const dl = h("datalist", { id: "trivy-images" });
  const src = select([["auto", "Automático (local → remoto)"], ["docker", "Somente imagens locais (Docker)"], ["remote", "Somente registry remoto"]]);
  const sevBoxes = SEVS.map((s) => checkbox(s, s !== "UNKNOWN" && s !== "LOW"));
  const unfixed = checkbox("Ignorar sem correção (--ignore-unfixed)", false);
  const secrets = checkbox("Procurar segredos na imagem", true);
  const skipDB = checkbox("Não atualizar o banco (modo offline)", false);
  const sbomFmt = select([["cyclonedx", "CycloneDX (JSON)"], ["spdx-json", "SPDX (JSON)"]]);
  const term = terminal({ title: "trivy", height: "280px" });
  const resultBox = h("div");
  const localInfo = h("span", { class: "small muted" });

  const sevs = () => sevBoxes.filter((b) => b.input.checked).map((b) => b.querySelector("span").textContent);
  const run = async (body, label) => {
    term.write(`\n$ trivy ${label}\n`);
    mount(resultBox, h("p", { class: "muted", text: "Executando... o resultado aparece aqui ao final." }));
    const res = await post("/api/trivy/scan", body);
    const end = await runJob(res, term);
    if (end.result) mount(resultBox, renderTrivyResult(end.result, end.log_id));
    else if (end.status === "success" && body.kind === "sbom") mount(resultBox, h("div", { class: "row" }, h("span", { text: "SBOM gerado." }),
      btn("Baixar SBOM", { icon: "download", cls: "sm primary", onClick: () => download(`/api/logs/${end.log_id}/artifact`) })));
    else clear(resultBox);
    loadHistory();
  };

  // config (conteúdo colado)
  const cfgName = input({ value: "Dockerfile", class: "mono" });
  const cfgText = h("textarea", { rows: 10, placeholder: "Cole um Dockerfile, manifest Kubernetes (.yaml) ou arquivo .tf" });

  const history = h("div");
  const loadHistory = async () => {
    try {
      const r = await get("/api/logs?module=trivy&limit=10");
      mount(history, table([
        { label: "Ação", render: (l) => badge(l.action, "info") },
        { label: "Alvo", render: (l) => h("span", { class: "mono small", text: l.target }) },
        { label: "Status", render: (l) => statusBadge(l.status) },
        { label: "Quando", render: (l) => h("span", { class: "muted", text: fmtRel(l.started_at) }) },
      ], r.items, { onRowClick: ctx.can("logs") ? (l) => ctx.navigate("logs/" + l.id) : null, empty: emptyState("shield", "Nenhum scan ainda", "") }));
    } catch (_) { clear(history); }
  };

  mount(root,
    pageHead("Trivy", "Vulnerabilidades (CVE), segredos e misconfigurações em imagens e arquivos de IaC, além de SBOM.",
      M ? [btn("Atualizar banco de vulnerabilidades", { icon: "refresh", onClick: async () => {
        term.write("\n$ trivy image --download-db-only\n");
        await runJob(await post("/api/trivy/db-update", {}), term);
      } })] : null, ["Redes & Segurança"]),
    h("div", { class: "grid g2" },
      card({ title: "Scan de imagem", subtitle: "Imagens locais (via socket do Docker) ou de registries — inclusive privados com login feito no módulo Docker.",
        body: h("div", { class: "stack" },
          field("Imagem", image, null), localInfo, dl,
          h("div", { class: "form-grid" }, field("Origem da imagem", src), field("Formato do SBOM", sbomFmt)),
          h("div", { class: "field" }, h("span", { class: "lbl", text: "Severidades" }), h("div", { class: "row" }, sevBoxes)),
          h("div", { class: "stack sm" }, unfixed, secrets, skipDB),
          M ? h("div", { class: "row" },
            btn("Escanear imagem", { icon: "shield", cls: "primary", onClick: () => {
              if (!image.value.trim()) return toast("Informe a imagem", "err");
              return run({ kind: "image", image: image.value.trim(), severities: sevs(), ignore_unfixed: unfixed.input.checked, secrets: secrets.input.checked,
                skip_db_update: skipDB.input.checked, image_src: src.value }, `image ${image.value.trim()}`);
            } }),
            btn("Gerar SBOM", { icon: "doc", onClick: () => {
              if (!image.value.trim()) return toast("Informe a imagem", "err");
              return run({ kind: "sbom", image: image.value.trim(), sbom_format: sbomFmt.value, image_src: src.value, skip_db_update: skipDB.input.checked }, `image --format ${sbomFmt.value} ${image.value.trim()}`);
            } })) : h("p", { class: "muted", text: "Seu perfil pode apenas consultar resultados." })) }),
      card({ title: "Scan de configuração (IaC)", subtitle: "Dockerfile, Kubernetes, Terraform/OpenTofu — itens salvos podem ser analisados nas próprias páginas.",
        body: h("div", { class: "stack" }, field("Nome do arquivo", cfgName, "Define o tipo detectado: Dockerfile, *.yaml, *.tf"), cfgText,
          M ? h("div", null, btn("Analisar configuração", { icon: "shield", cls: "primary", onClick: () => {
            if (!cfgText.value.trim()) return toast("Cole o conteúdo do arquivo", "err");
            return run({ kind: "config", source: "content", filename: cfgName.value.trim(), content: cfgText.value, severities: sevs() }, `config ${cfgName.value.trim()}`);
          } })) : null) })),
    h("div", { class: "stack", style: { marginTop: "16px" } },
      card({ title: "Saída", body: term.el }),
      card({ title: "Resultado", body: resultBox }),
      card({ title: "Histórico recente", body: history, flush: true })));

  mount(resultBox, h("p", { class: "muted", text: "Execute um scan para ver o resultado." }));
  loadHistory();
  try {
    const r = await get("/api/trivy/images");
    r.images.forEach((i) => dl.appendChild(h("option", { value: i.ref }, `${i.size} · ${i.created}`)));
    localInfo.textContent = r.warning ? r.warning : `${r.images.length} imagem(ns) local(is) disponível(is) — comece a digitar para ver sugestões.`;
  } catch (_) { /* ignore */ }
}
