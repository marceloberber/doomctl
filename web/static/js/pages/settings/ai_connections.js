// Aba "Conexões & MCP": provedores de modelos (Ollama remoto, APIs compatíveis com OpenAI,
// Microsoft Foundry) e servidores MCP com a política de ferramentas.
import { get, post, put, del } from "../../api.js";
import { h, mount, clear, icon, btn, badge, card, table, modal, toast, toastErr, confirmDialog, fmtRel, kvEditor, markdown } from "../../ui.js";
import { KIND_LABEL, textField, selectField, toggle, numField, statusPill } from "./ai_common.js";

// add: como Element.append, mas ignora null/false e achata listas (igual ao h()).
const add = (el, ...k) => { el.append(...k.flat(Infinity).filter((x) => x !== null && x !== undefined && x !== false)); return el; };

export async function render(root, ctx) {
  const ai = ctx.ai;
  const view = h("div");
  root.appendChild(view);
  const draw = () => {
    const cfg = ai.cfg;
    mount(view, h("div", { class: "stack" },
      h("div", { class: "alert info" }, h("div", { class: "alert-icon" }, icon("info")), h("div", null,
        h("strong", { text: "Como funciona" }),
        h("p", { text: "Cadastre aqui as conexões e os servidores MCP; depois escolha em Agentes de IA qual persona usa cada um. Chaves e tokens são guardados criptografados (AES-256-GCM) e nunca voltam para o navegador. Nada muda no chat até você vincular uma conexão a uma persona." }))),
      card({ title: "Conexões com modelos", subtitle: "Ollama em outro servidor, APIs compatíveis com OpenAI (Azure OpenAI, Microsoft Foundry, OpenAI, Gemini...) e agentes do Microsoft Foundry.",
        actions: [btn("Nova conexão", { icon: "plus", cls: "sm primary", onClick: () => connModal(ai) })], flush: true,
        body: table([
          { label: "Nome", render: (c) => h("div", null, h("b", { text: c.name }), h("div", { class: "small muted", text: presetLabel(cfg, c.preset) || KIND_LABEL[c.kind] })) },
          { label: "Endpoint", render: (c) => h("span", { class: "mono small ai-ellipsis", title: c.config.base_url, text: c.config.base_url }) },
          { label: "Modelo / agente", render: (c) => h("code", { text: c.config.model || "—" }) },
          { label: "Status", render: (c) => h("div", { class: "row nw" }, c.enabled ? statusPill(c.last_status, c.last_error) : badge("desativada", "warn"),
            c.external ? badge("externo", "outline") : null, c.last_test_at ? h("span", { class: "small faint", text: fmtRel(c.last_test_at) }) : null) },
          { label: "Em uso por", render: (c) => c.in_use.length ? h("span", { class: "small", text: c.in_use.join(", ") }) : h("span", { class: "faint small", text: "—" }) },
          { label: "", cls: "right", render: (c) => h("div", { class: "row nw" },
            btn("Testar", { icon: "play", cls: "xs", onClick: () => testConn(ai, c) }),
            btn("", { icon: "edit", cls: "xs ghost", title: "Editar", onClick: () => connModal(ai, c) }),
            btn("", { icon: "trash", cls: "xs ghost", title: "Excluir", onClick: async () => {
              if (!await confirmDialog({ title: "Excluir conexão", message: `Excluir a conexão "${c.name}"? O segredo guardado é apagado.`, danger: true, confirmText: "Excluir" })) return;
              await del(`/api/ai/connections/${c.id}`); toast("Conexão excluída", "ok"); await ai.reload();
            } })) },
        ], cfg.connections, { empty: h("div", { class: "empty" }, h("div", { class: "ic" }, icon("network", "lg")), h("b", { text: "Nenhuma conexão cadastrada" }),
          h("p", { text: "Os agentes usam o Ollama do .env. Adicione uma conexão para usar outro servidor, uma API ou um agente do Microsoft Foundry." })) }) }),
      card({ title: "Servidores MCP (ferramentas)", subtitle: "Model Context Protocol via Streamable HTTP. Ex.: Google Stitch, documentação Microsoft Learn/AWS, GitHub ou servidores próprios.",
        actions: [btn("Novo servidor MCP", { icon: "plus", cls: "sm primary", onClick: () => mcpModal(ai) })], flush: true,
        body: table([
          { label: "Servidor", render: (m) => h("div", null, h("b", { text: m.name }), h("div", { class: "small muted", text: m.server_info && m.server_info.name ? `${m.server_info.name} ${m.server_info.version || ""}` : (mcpPresetLabel(cfg, m.preset) || "MCP") })) },
          { label: "URL", render: (m) => h("span", { class: "mono small ai-ellipsis", title: m.url, text: m.url }) },
          { label: "Ferramentas", render: (m) => {
            const on = m.tools.filter((t) => toolOn(m, t)).length;
            return m.tools.length ? h("span", null, h("b", { text: `${on}` }), h("span", { class: "muted", text: ` de ${m.tools.length} liberada(s)` })) : h("span", { class: "faint small", text: "não listadas" });
          } },
          { label: "Status", render: (m) => h("div", { class: "row nw" }, m.enabled ? statusPill(m.last_status, m.last_error) : badge("desativado", "warn"), m.external ? badge("externo", "outline") : null) },
          { label: "Em uso por", render: (m) => m.in_use.length ? h("span", { class: "small", text: m.in_use.join(", ") }) : h("span", { class: "faint small", text: "—" }) },
          { label: "", cls: "right", render: (m) => h("div", { class: "row nw" },
            btn("Atualizar", { icon: "refresh", cls: "xs", title: "Conectar e listar ferramentas", onClick: () => refreshMCP(ai, m) }),
            btn("Ferramentas", { icon: "bolt", cls: "xs", disabled: !m.tools.length, onClick: () => toolsModal(ai, m) }),
            btn("", { icon: "edit", cls: "xs ghost", title: "Editar", onClick: () => mcpModal(ai, m) }),
            btn("", { icon: "trash", cls: "xs ghost", title: "Excluir", onClick: async () => {
              if (!await confirmDialog({ title: "Excluir servidor MCP", message: `Excluir "${m.name}"? As personas deixam de usar suas ferramentas.`, danger: true, confirmText: "Excluir" })) return;
              await del(`/api/ai/mcp/${m.id}`); toast("Servidor excluído", "ok"); await ai.reload();
            } })) },
        ], cfg.mcp, { empty: h("div", { class: "empty" }, h("div", { class: "ic" }, icon("bolt", "lg")), h("b", { text: "Nenhum servidor MCP" }),
          h("p", { text: "Ferramentas MCP permitem que Atlas e Sentinela consultem sistemas externos durante a resposta. Somente transporte HTTP (servidores stdio precisam de um proxy HTTP)." })) }) })));
  };
  draw();
  const off = ai.on((ev) => { if (!view.isConnected) return off(); if (ev === "reload" || ev === "saved") draw(); });
}

const toolOn = (m, t) => (t.name in (m.config.tools_enabled || {}) ? m.config.tools_enabled[t.name] : t.read_only);
const presetLabel = (cfg, id) => (cfg.presets.find((p) => p.id === id) || {}).label;
const mcpPresetLabel = (cfg, id) => (cfg.mcp_presets.find((p) => p.id === id) || {}).label;

async function testConn(ai, c) {
  toast(`Testando "${c.name}"...`);
  try {
    const r = await post(`/api/ai/connections/${c.id}/test`, {});
    if (r.ok) {
      toast(`"${c.name}" ok em ${r.latency_ms} ms${r.models ? ` · ${r.models.length} modelo(s)/agente(s)` : ""}`, "ok");
      if (r.note) toast(r.note);
    } else {
      modal({ title: `Falha ao testar "${c.name}"`, size: "wide", actions: [{ label: "Fechar" }],
        body: h("div", { class: "stack sm" }, h("div", { class: "alert err" }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: "Erro" }), h("p", { class: "mono small", text: r.error }))),
          h("p", { class: "small muted", text: hintFor(c, r.error) })) });
    }
  } catch (e) { toastErr(e); }
  await ai.reload();
}

function hintFor(c, err) {
  const e = (err || "").toLowerCase();
  if (e.includes("401") || e.includes("403") || e.includes("unauthorized") || e.includes("permission")) {
    if (c.kind === "foundry_agent") return "Agentes do Foundry exigem Microsoft Entra ID: o service principal precisa do papel \"Azure AI User\" no projeto (ou recurso).";
    return "Credencial recusada: confira a chave/token, o tipo de autenticação e as permissões.";
  }
  if (e.includes("404")) return "Endpoint ou recurso não encontrado: confira a URL base, o nome do deployment/agente e a api-version.";
  if (e.includes("temperature")) return "O modelo não aceita temperatura: edite a conexão e desmarque \"Enviar temperatura\".";
  if (e.includes("max_tokens") || e.includes("max_completion_tokens")) return "Troque o parâmetro de limite de tokens em Avançado (max_tokens ↔ max_completion_tokens).";
  if (e.includes("connection refused") || e.includes("no such host") || e.includes("timeout")) return "O servidor não respondeu: confira a URL, o DNS e se o container do doomctl alcança o endereço.";
  return "Confira URL, autenticação e modelo. Detalhes completos em Logs (módulo ai).";
}

// ---------- modal de conexão ----------
function connModal(ai, existing) {
  const cfg = ai.cfg;
  const isNew = !existing;
  const c = existing ? JSON.parse(JSON.stringify(existing)) : { name: "", kind: "openai", preset: "", enabled: true, config: { headers: {}, auth: { type: "none" } } };
  let preset = cfg.presets.find((p) => p.id === c.preset) || null;
  let secret = null; // null = mantém
  const body = h("div", { class: "stack" });
  const presetSel = h("select", { "aria-label": "Provedor" });
  presetSel.appendChild(h("option", { value: "" }, isNew ? "Escolha o provedor..." : "(sem modelo de preenchimento)"));
  const groups = {};
  for (const p of cfg.presets) {
    if (!groups[p.group]) { groups[p.group] = h("optgroup", { label: p.group }); presetSel.appendChild(groups[p.group]); }
    groups[p.group].appendChild(h("option", { value: p.id, selected: p.id === c.preset }, p.label));
  }
  const form = h("div", { class: "stack" });
  const applyPreset = (p) => {
    preset = p;
    c.preset = p ? p.id : "";
    if (!p) return;
    c.kind = p.kind;
    const cf = c.config;
    if (isNew || !cf.base_url) cf.base_url = p.base_url || "";
    cf.token_param = p.token_param || cf.token_param || "";
    cf.send_temperature = p.send_temperature;
    cf.include_usage = p.include_usage;
    cf.mode = p.mode || "";
    cf.api_version = p.api_version || "";
    cf.chat_path = ""; cf.models_path = ""; cf.query = "";
    cf.auth = { type: p.auth[0], header_name: p.auth_header || "", scope: "" };
    if (!c.name) c.name = p.label.split(" (")[0].replace(" — ", " ");
  };
  presetSel.addEventListener("change", () => { applyPreset(cfg.presets.find((p) => p.id === presetSel.value) || null); drawForm(); });

  const drawForm = () => {
    clear(form);
    if (isNew && !preset) { form.appendChild(h("p", { class: "muted small", text: "Escolha um provedor para preencher os campos." })); return; }
    const cf = c.config;
    const p = preset || { auth: Object.keys(cfg.auth_types), model_label: "Modelo", model_hint: "", base_hint: "" };
    const authTypes = (p.auth && p.auth.length ? p.auth : Object.keys(cfg.auth_types)).filter((t) => cfg.auth_types[t]);
    if (!authTypes.includes(cf.auth.type)) authTypes.unshift(cf.auth.type);
    const authBox = h("div", { class: "stack sm" });
    const drawAuth = () => {
      clear(authBox);
      const a = cf.auth;
      const secLabel = { bearer: "Token / chave de API", "api-key": "Chave (api-key)", header: "Valor do cabeçalho", azure_entra: "Client secret", oauth2: "Client secret", google_sa: "JSON da service account" }[a.type];
      const sec = a.type === "google_sa" ? h("textarea", { rows: 4, class: "mono", placeholder: existing && existing.has_secret ? "•••• guardado — preencha só para trocar" : "{ \"type\": \"service_account\", ... }" })
        : h("input", { type: "password", autocomplete: "new-password", placeholder: existing && existing.has_secret ? "•••• guardado — preencha só para trocar" : "" });
      sec.addEventListener("input", () => { secret = sec.value; });
      const grid = h("div", { class: "form-grid" });
      if (a.type === "header") grid.appendChild(textField("Nome do cabeçalho", a.header_name, { mono: true, placeholder: "X-Api-Key" }, (v) => { a.header_name = v; }));
      if (a.type === "azure_entra") {
        grid.append(textField("Tenant ID", a.tenant_id, { mono: true }, (v) => { a.tenant_id = v; }), textField("Client ID (app registration)", a.client_id, { mono: true }, (v) => { a.client_id = v; }),
          textField("Escopo", a.scope, { mono: true, placeholder: (preset && preset.scope) || "https://cognitiveservices.azure.com/.default", hint: "Vazio = padrão do tipo de conexão." }, (v) => { a.scope = v; }));
      }
      if (a.type === "oauth2") {
        grid.append(textField("URL de token", a.token_url, { mono: true, placeholder: "https://login.exemplo.com/oauth2/token" }, (v) => { a.token_url = v; }),
          textField("Client ID", a.client_id, { mono: true }, (v) => { a.client_id = v; }), textField("Escopo", a.scope, { mono: true }, (v) => { a.scope = v; }));
      }
      if (a.type === "google_sa") {
        grid.append(textField("Escopo", a.scope, { mono: true, placeholder: "https://www.googleapis.com/auth/cloud-platform" }, (v) => { a.scope = v; }),
          textField("Projeto de cota (opcional)", a.project, { mono: true, hint: "Enviado em x-goog-user-project." }, (v) => { a.project = v; }));
      }
      authBox.append(selectField("Autenticação", authTypes.map((t) => [t, cfg.auth_types[t]]), a.type, (v) => { cf.auth = { type: v }; drawAuth(); }), grid);
      if (secLabel) authBox.appendChild(h("label", { class: "field" }, h("span", { class: "lbl", text: secLabel }), sec,
        existing && existing.has_secret ? h("span", { class: "hint" }, "Já existe um segredo guardado. ", h("a", { href: "#", onclick: (e) => { e.preventDefault(); secret = ""; sec.value = ""; toast("O segredo será removido ao salvar"); } }, "remover")) : null));
    };
    drawAuth();
    const isFoundry = c.kind === "foundry_agent";
    const headers = kvEditor(Object.entries(cf.headers || {}), { keyPlaceholder: "Cabeçalho", valuePlaceholder: "valor" });
    form._headers = headers;
    const adv = h("details", { class: "ai-details" }, h("summary", { text: "Avançado" }), h("div", { class: "stack", style: { marginTop: "12px" } },
      c.kind === "openai" ? h("div", { class: "form-grid" },
        textField("Caminho do chat", cf.chat_path, { mono: true, placeholder: "/chat/completions" }, (v) => { cf.chat_path = v.trim(); }),
        textField("Caminho de modelos", cf.models_path, { mono: true, placeholder: "/models" }, (v) => { cf.models_path = v.trim(); }),
        textField("Query string", cf.query, { mono: true, placeholder: "api-version=2024-10-21", hint: "Acrescentada a todas as chamadas (API antiga do Azure OpenAI)." }, (v) => { cf.query = v.trim(); }),
        selectField("Parâmetro de limite de tokens", [["max_tokens", "max_tokens"], ["max_completion_tokens", "max_completion_tokens"]], cf.token_param || "max_tokens", (v) => { cf.token_param = v; }),
        toggle("Enviar temperatura e top_p", cf.send_temperature, (v) => { cf.send_temperature = v; }, "Desmarque para modelos de raciocínio que recusam temperatura."),
        toggle("Pedir uso de tokens no streaming", cf.include_usage, (v) => { cf.include_usage = v; }, "stream_options.include_usage (algumas APIs não aceitam).")) : null,
      isFoundry ? h("div", { class: "form-grid" },
        selectField("Modo", [["agent", "Agente (API Responses)"], ["application", "Aplicação publicada (sem estado)"], ["classic", "Clássico (threads/runs)"]], cf.mode || "agent", (v) => { cf.mode = v; }),
        textField("api-version", cf.api_version, { mono: true, placeholder: "v1 / 2025-11-15-preview / 2025-05-01", hint: "Vazio = padrão do modo. O teste tenta a preview se a v1 não existir." }, (v) => { cf.api_version = v.trim(); })) : null,
      numField("Tempo limite (s)", cf.timeout_seconds || 0, { min: 0, max: 3600, hint: "0 = padrão (10 min)." }, (v) => { cf.timeout_seconds = v; }),
      h("div", { class: "field" }, h("span", { class: "lbl", text: "Cabeçalhos extras (não secretos)" }), headers,
        h("span", { class: "hint", text: "Ex.: OpenAI-Organization, HTTP-Referer. Para segredos use o campo de autenticação." }))));
    add(form, 
      preset ? h("p", { class: "small muted" }, preset.description || "", preset.docs ? [" ", h("a", { href: preset.docs, target: "_blank", rel: "noopener" }, "Documentação ↗")] : null) : null,
      h("div", { class: "form-grid" },
        textField("Nome", c.name, { maxlength: 60 }, (v) => { c.name = v; }),
        textField(isFoundry ? "Endpoint" : "URL base", cf.base_url, { mono: true, placeholder: p.base_hint, hint: p.base_hint }, (v) => { cf.base_url = v.trim(); }),
        (c.config.mode !== "application") ? textField(p.model_label || "Modelo", cf.model, { mono: true, placeholder: p.model_hint, hint: isFoundry ? "As personas podem trocar o agente em Agentes de IA." : "Padrão da conexão; cada persona pode usar outro." }, (v) => { cf.model = v.trim(); }) : null,
        toggle("Conexão ativa", c.enabled, (v) => { c.enabled = v; })),
      h("div", { class: "fieldset" }, h("div", { class: "legend" }, "Autenticação"), authBox),
      adv);
  };
  body.append(h("label", { class: "field" }, h("span", { class: "lbl", text: "Provedor" }), presetSel), form);
  drawForm();
  const save = async (test) => {
    if (form._headers) c.config.headers = Object.fromEntries(form._headers.values().map((p) => [p.key, p.value]));
    const payload = { name: c.name, kind: c.kind, preset: c.preset, config: c.config, enabled: c.enabled };
    if (secret !== null) payload.secret = secret;
    const r = existing ? await put(`/api/ai/connections/${existing.id}`, payload) : await post("/api/ai/connections", payload);
    toast("Conexão salva", "ok");
    await ai.reload();
    if (test) { const saved = ai.cfg.connections.find((x) => x.id === r.id); if (saved) await testConn(ai, saved); }
  };
  modal({ title: existing ? `Editar conexão — ${existing.name}` : "Nova conexão", size: "wide", body,
    actions: [{ label: "Cancelar" }, { label: "Salvar", onClick: () => save(false) }, { label: "Salvar e testar", primary: true, icon: "play", onClick: () => save(true) }] });
}

// ---------- MCP ----------
async function refreshMCP(ai, m) {
  try {
    const r = await post(`/api/ai/mcp/${m.id}/refresh`, {});
    toast(`${m.name}: ${r.tools.length} ferramenta(s) em ${r.latency_ms} ms`, "ok");
    await ai.reload();
    const fresh = ai.cfg.mcp.find((x) => x.id === m.id);
    if (fresh && fresh.tools.length) toolsModal(ai, fresh);
  } catch (e) { toastErr(e); await ai.reload(); }
}

function mcpModal(ai, existing) {
  const cfg = ai.cfg;
  const m = existing ? JSON.parse(JSON.stringify(existing)) : { name: "", url: "", preset: "", enabled: true, config: { headers: {}, auth: { type: "none" }, timeout_seconds: 0 } };
  let secret = null;
  const form = h("div", { class: "stack" });
  const presetSel = h("select", { "aria-label": "Modelo de servidor" },
    h("option", { value: "" }, existing ? "(sem modelo)" : "Escolha..."),
    cfg.mcp_presets.map((p) => h("option", { value: p.id, selected: p.id === m.preset }, p.label)));
  let preset = cfg.mcp_presets.find((p) => p.id === m.preset) || null;
  presetSel.addEventListener("change", () => {
    preset = cfg.mcp_presets.find((p) => p.id === presetSel.value) || null;
    m.preset = preset ? preset.id : "";
    if (preset) {
      if (preset.url) m.url = preset.url;
      if (!m.name || !existing) m.name = preset.id === "custom" ? m.name : preset.label.split(" (")[0];
      m.config.auth = { type: preset.auth[0], header_name: preset.auth_header || "", scope: "" };
    }
    draw();
  });
  const draw = () => {
    clear(form);
    if (!existing && !preset) { form.appendChild(h("p", { class: "muted small", text: "Escolha um modelo de servidor (ou \"Servidor MCP\" para um personalizado)." })); return; }
    const a = m.config.auth;
    const allowed = (preset && preset.auth.length ? preset.auth : Object.keys(cfg.auth_types)).filter((t) => cfg.auth_types[t]);
    if (!allowed.includes(a.type)) allowed.unshift(a.type);
    const sec = a.type === "google_sa" ? h("textarea", { rows: 4, class: "mono", placeholder: existing && existing.has_secret ? "•••• guardado — preencha só para trocar" : "JSON da service account" })
      : h("input", { type: "password", autocomplete: "new-password", placeholder: existing && existing.has_secret ? "•••• guardado — preencha só para trocar" : "" });
    sec.addEventListener("input", () => { secret = sec.value; });
    const headers = kvEditor(Object.entries(m.config.headers || {}), { keyPlaceholder: "Cabeçalho", valuePlaceholder: "valor" });
    form._headers = headers;
    add(form, 
      preset ? h("p", { class: "small muted", text: preset.description }) : null,
      h("div", { class: "form-grid" },
        textField("Nome", m.name, { maxlength: 60 }, (v) => { m.name = v; }),
        textField("URL do endpoint MCP", m.url, { mono: true, placeholder: "https://servidor/mcp" }, (v) => { m.url = v.trim(); }),
        selectField("Autenticação", allowed.map((t) => [t, cfg.auth_types[t]]), a.type, (v) => { m.config.auth = { type: v, header_name: v === "header" && preset ? preset.auth_header || "" : "" }; draw(); }),
        a.type === "header" ? textField("Nome do cabeçalho", a.header_name, { mono: true, placeholder: "X-Goog-Api-Key" }, (v) => { a.header_name = v; }) : null,
        a.type === "azure_entra" ? [textField("Tenant ID", a.tenant_id, { mono: true }, (v) => { a.tenant_id = v; }), textField("Client ID", a.client_id, { mono: true }, (v) => { a.client_id = v; }), textField("Escopo", a.scope, { mono: true }, (v) => { a.scope = v; })] : null,
        a.type === "oauth2" ? [textField("URL de token", a.token_url, { mono: true }, (v) => { a.token_url = v; }), textField("Client ID", a.client_id, { mono: true }, (v) => { a.client_id = v; }), textField("Escopo", a.scope, { mono: true }, (v) => { a.scope = v; })] : null,
        a.type === "google_sa" ? textField("Escopo", a.scope, { mono: true, placeholder: (preset && preset.scope) || "https://www.googleapis.com/auth/cloud-platform" }, (v) => { a.scope = v; }) : null,
        a.type !== "none" ? h("label", { class: "field span-all" }, h("span", { class: "lbl", text: a.type === "google_sa" ? "JSON da service account" : "Segredo (chave/token)" }), sec) : null,
        numField("Tempo limite (s)", m.config.timeout_seconds || 0, { min: 0, max: 600, hint: "0 = 60 s." }, (v) => { m.config.timeout_seconds = v; }),
        toggle("Servidor ativo", m.enabled, (v) => { m.enabled = v; })),
      h("details", { class: "ai-details" }, h("summary", { text: "Cabeçalhos extras" }), h("div", { style: { marginTop: "10px" } }, headers)));
  };
  draw();
  modal({ title: existing ? `Editar servidor MCP — ${existing.name}` : "Novo servidor MCP", size: "wide",
    body: h("div", { class: "stack" }, h("label", { class: "field" }, h("span", { class: "lbl", text: "Modelo" }), presetSel), form),
    actions: [{ label: "Cancelar" }, { label: "Salvar e conectar", primary: true, icon: "refresh", onClick: async () => {
      if (form._headers) m.config.headers = Object.fromEntries(form._headers.values().map((p) => [p.key, p.value]));
      const payload = { name: m.name, url: m.url, preset: m.preset, config: { headers: m.config.headers, auth: m.config.auth, timeout_seconds: m.config.timeout_seconds }, enabled: m.enabled };
      if (secret !== null) payload.secret = secret;
      const r = existing ? await put(`/api/ai/mcp/${existing.id}`, payload) : await post("/api/ai/mcp", payload);
      toast("Servidor salvo", "ok");
      await ai.reload();
      const saved = ai.cfg.mcp.find((x) => x.id === r.id);
      if (saved && saved.enabled) refreshMCP(ai, saved);
    } }] });
}

function toolsModal(ai, m) {
  const pol = { ...(m.config.tools_enabled || {}) };
  const rows = m.tools.map((t) => {
    const cb = h("input", { type: "checkbox", checked: toolOn(m, t), "aria-label": "liberar " + t.name });
    cb.addEventListener("change", () => { pol[t.name] = cb.checked; });
    return h("div", { class: "ai-tool" },
      h("label", { class: "check" }, cb, h("span", null, h("b", { class: "mono", text: t.name }), t.title ? h("span", { class: "muted", text: " — " + t.title }) : null)),
      h("div", { class: "row nw" }, t.read_only ? badge("somente leitura", "ok") : t.destructive ? badge("pode alterar/apagar", "err") : badge("altera dados", "warn"),
        btn("Testar", { icon: "play", cls: "xs ghost", onClick: () => callModal(m, t) })),
      t.description ? h("div", { class: "small muted ai-tool-desc", text: t.description }) : null);
  });
  const info = m.server_info && m.server_info.instructions ? h("details", { class: "ai-details" }, h("summary", { text: "Instruções do servidor" }), h("div", { class: "md small" }, markdown(m.server_info.instructions))) : null;
  modal({ title: `Ferramentas — ${m.name}`, size: "wide",
    body: h("div", { class: "stack" },
      h("p", { class: "small muted", text: "Ferramentas marcadas como somente leitura vêm liberadas. As que alteram algo ficam bloqueadas até você liberar — o modelo decide quando chamá-las durante a resposta, então libere só o necessário." }),
      info, h("div", { class: "ai-tools" }, rows)),
    actions: [{ label: "Cancelar" }, { label: "Salvar política", primary: true, onClick: async () => {
      await put(`/api/ai/mcp/${m.id}/tools`, { tools_enabled: pol }); toast("Política de ferramentas salva", "ok"); await ai.reload();
    } }] });
}

function callModal(m, t) {
  const ta = h("textarea", { rows: 6, class: "mono" });
  const props = (t.inputSchema && t.inputSchema.properties) || {};
  ta.value = JSON.stringify(Object.fromEntries(Object.keys(props).map((k) => [k, props[k].type === "number" || props[k].type === "integer" ? 0 : props[k].type === "boolean" ? false : ""])), null, 2);
  const out = h("div");
  modal({ title: `Testar ${t.name}`, size: "wide",
    body: h("div", { class: "stack" },
      !t.read_only ? h("div", { class: "alert warn" }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: "Esta ferramenta pode alterar dados no serviço remoto." }), h("p", { text: "A execução será registrada em Logs." }))) : null,
      h("details", { class: "ai-details" }, h("summary", { text: "Esquema de entrada" }), h("pre", { class: "mono small", text: JSON.stringify(t.inputSchema || {}, null, 2) })),
      h("label", { class: "field" }, h("span", { class: "lbl", text: "Argumentos (JSON)" }), ta), out),
    actions: [{ label: "Fechar" }, { label: "Executar", primary: true, icon: "play", onClick: async () => {
      let args;
      try { args = JSON.parse(ta.value || "{}"); } catch (_) { toast("JSON inválido", "err"); return false; }
      if (!t.read_only && !await confirmDialog({ title: "Confirmar execução", message: `Executar ${t.name} em ${m.name}? Pode alterar dados.`, danger: true, confirmText: "Executar" })) return false;
      try {
        const r = await post(`/api/ai/mcp/${m.id}/call`, { tool: t.name, args, confirm: !t.read_only });
        mount(out, h("div", { class: "stack sm" }, h("div", { class: "row" }, r.is_error ? badge("erro", "err", true) : badge("ok", "ok", true), h("span", { class: "small muted", text: `${r.duration_ms} ms` })),
          h("pre", { class: "mono small ai-out", text: r.output || "(vazio)" })));
      } catch (e) { toastErr(e); }
      return false;
    } }] });
}
