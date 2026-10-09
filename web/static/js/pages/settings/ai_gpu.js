// Aba "GPU & modelos": aceleração por GPU (opções do Ollama), status da GPU/container,
// modelos carregados na memória e gestão de modelos (baixar, carregar, remover).
import { get, post } from "../../api.js";
import { h, mount, icon, btn, badge, card, table, loading, toast, toastErr, confirmDialog, fmtRel, jobModal } from "../../ui.js";
import { numField, selectField, segmented, fmtBytes } from "./ai_common.js";

export async function render(root, ctx) {
  const ai = ctx.ai;
  const view = h("div", { class: "stack" });
  root.appendChild(view);
  const gpuCard = h("div");
  const statusBox = h("div", { class: "stack" }, loading("Consultando o Ollama..."));
  let conn = 0;

  const drawGPU = () => {
    const g = ai.draft.gpu;
    const set = (k, v) => { g[k] = v; ai.change(); };
    const on = g.mode !== "cpu";
    const detail = h("div", { class: "form-grid three" + (on ? "" : " ai-dim") },
      h("div", { class: "field" }, h("span", { class: "lbl", text: "Distribuição das camadas" }),
        segmented([["auto", "Automática"], ["layers", "Camadas fixas"]], g.mode === "layers" ? "layers" : "auto", (v) => { set("mode", v); drawGPU(); }, "Distribuição das camadas"),
        h("span", { class: "hint", text: "Automática: o Ollama calcula quantas camadas cabem na VRAM (recomendado)." })),
      g.mode === "layers" ? numField("Camadas na GPU (num_gpu)", g.layers, { min: 0, max: 999, hint: "Menos camadas = menos VRAM e mais CPU. 999 = todas." }, (v) => set("layers", v)) : h("div"),
      h("div"));
    const toggleEl = h("label", { class: "ai-switch" },
      h("input", { type: "checkbox", checked: on, role: "switch", "aria-label": "Aceleração por GPU", onchange: (e) => { set("mode", e.target.checked ? "auto" : "cpu"); drawGPU(); } }),
      h("span", { class: "ai-switch-ui" }),
      h("span", null, h("b", { text: on ? "Aceleração por GPU ligada" : "Aceleração por GPU desligada (somente CPU)" }),
        h("span", { class: "hint", text: on ? "Os modelos usam a GPU disponível no servidor do Ollama." : "Envia num_gpu=0: tudo roda na CPU, mesmo havendo GPU. Útil para liberar a placa ou diagnosticar." })));
    mount(gpuCard, card({ title: "Aceleração por GPU", subtitle: "Opções enviadas ao Ollama em cada resposta (padrão e conexões Ollama). Salve na barra acima para aplicar.",
      body: h("div", { class: "stack" }, toggleEl, detail,
        h("div", { class: "form-grid three" },
          selectField("Manter o modelo na memória (keep_alive)", [["", "Padrão do Ollama (5 min)"], ["30m", "30 minutos"], ["1h", "1 hora"], ["4h", "4 horas"], ["-1", "Sempre carregado"], ["0", "Descarregar após responder"]],
            ["", "30m", "1h", "4h", "-1", "0"].includes(g.keep_alive) ? g.keep_alive : "", (v) => set("keep_alive", v), "Mais tempo = respostas rápidas e memória ocupada."),
          numField("Threads de CPU (num_thread)", g.num_thread, { min: 0, max: 512, hint: "0 = automático." }, (v) => set("num_thread", v)),
          h("div", { class: "field" }, h("span", { class: "lbl", text: "No servidor (install.sh)" }), installBadge(ai.cfg.env.gpu_install),
            h("span", { class: "hint" }, "Reserva da GPU para o container: ", h("code", { text: "./install.sh --with-ai --gpu=1" }), " (ou ", h("code", { text: "--gpu=0" }), ")."))),
        h("p", { class: "small muted", text: "Mudar a GPU recarrega o modelo automaticamente na próxima pergunta. Para aplicar já, use \"Recarregar\" no modelo em memória abaixo." })) }));
  };

  const drawStatus = async () => {
    mount(statusBox, loading("Consultando o Ollama..."));
    let o;
    try { o = await get(`/api/ai/ollama?conn=${conn}`); } catch (e) { mount(statusBox, h("div", { class: "alert err" }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: e.message })))); return; }
    const ollamaConns = [[0, `Ollama padrão (.env) — ${ai.cfg.env.ollama_host}`], ...ai.cfg.connections.filter((c) => c.kind === "ollama").map((c) => [c.id, `${c.name} — ${c.config.base_url}`])];
    const sel = h("select", { "aria-label": "Servidor Ollama" }, ollamaConns.map(([v, l]) => h("option", { value: v, selected: v === conn }, l)));
    sel.addEventListener("change", () => { conn = Number(sel.value); drawStatus(); });
    const defModel = o.default_model;
    const running = o.running || [];
    const gpus = o.gpus || [];
    const ct = o.container;
    const reload = async (model) => {
      await post("/api/ai/ollama/load", { conn, model, action: "unload" });
      const r = await post("/api/ai/ollama/load", { conn, model, action: "load" });
      const loaded = (r.running || []).find((x) => x.name === model || x.name.replace(/:latest$/, "") === model) || {};
      toast(`${model} recarregado em ${(r.duration_ms / 1000).toFixed(1)} s${loaded.processor ? " · " + loaded.processor : ""}`, "ok");
      drawStatus();
    };
    mount(statusBox,
      card({ title: "Servidor Ollama", actions: [sel, btn("", { icon: "refresh", cls: "sm ghost", title: "Atualizar", onClick: drawStatus })],
        body: h("div", { class: "grid g2" },
          h("dl", { class: "kv" },
            h("dt", { text: "Endereço" }), h("dd", { text: o.host }),
            h("dt", { text: "Situação" }), h("dd", null, o.online ? badge(`online${o.version ? " · v" + o.version : ""}`, "ok", true) : badge("offline", "err", true), o.error ? h("div", { class: "small muted", text: o.error }) : null),
            h("dt", { text: "Modelo padrão" }), h("dd", { text: defModel || "—" }),
            ct ? [h("dt", { text: "Container" }), h("dd", null, ct.found ? h("span", null, h("span", { text: `${ct.name} (${ct.state}) ` }), gpuBadge(ct.gpu)) : h("span", { class: "muted", text: ct.error || "não encontrado (Ollama fora do compose)" }))] : null),
          h("div", { class: "stack sm" },
            gpus.length ? gpus.map((g) => h("div", { class: "ai-gpu" }, h("div", { class: "spread" }, h("b", { text: g.name }), h("span", { class: "small muted", text: `${g.util_pct}% de uso` })),
              h("div", { class: "progress" + (g.mem_used_mb / g.mem_total_mb > 0.9 ? "" : " ok") }, h("i", { style: { width: Math.min(100, (g.mem_used_mb / Math.max(1, g.mem_total_mb)) * 100) + "%" } })),
              h("span", { class: "small muted", text: `VRAM ${(g.mem_used_mb / 1024).toFixed(1)} / ${(g.mem_total_mb / 1024).toFixed(1)} GiB` })))
              : h("p", { class: "small muted", text: ct && ct.found && ct.gpu === "none" ? "O container do Ollama não tem GPU reservada. Reinstale com ./install.sh --with-ai --gpu=1." : (o.gpus_error ? "nvidia-smi: " + o.gpus_error : "Detalhes de GPU disponíveis para o container do Ollama com NVIDIA. Use a coluna Processador abaixo para ver CPU/GPU.") }),
            ct && ct.found ? h("div", { class: "row" }, btn("Reiniciar Ollama", { icon: "refresh", cls: "sm", onClick: async () => {
              if (!await confirmDialog({ title: "Reiniciar o Ollama", message: "Reinicia o container do Ollama: os modelos são descarregados e respostas em andamento são interrompidas.", confirmText: "Reiniciar" })) return;
              await jobModal("Reiniciando o Ollama", () => post("/api/ai/ollama/restart", {}), { onEnd: () => setTimeout(drawStatus, 1500) });
            } })) : null)) }),
      card({ title: "Modelos na memória", subtitle: "Onde cada modelo está rodando agora (dados de /api/ps).", flush: true, body: table([
        { label: "Modelo", render: (m) => h("b", { class: "mono", text: m.name }) },
        { label: "Processador", render: (m) => badge(m.processor, m.processor.includes("GPU") ? (m.processor.startsWith("100% GPU") ? "ok" : "warn") : "outline", true) },
        { label: "Memória", render: (m) => h("span", { class: "small", text: `${fmtBytes(m.size)}${m.size_vram ? ` (VRAM ${fmtBytes(m.size_vram)})` : ""}` }) },
        { label: "Contexto", render: (m) => h("span", { class: "mono small", text: m.context_length || "—" }) },
        { label: "Expira", render: (m) => h("span", { class: "small muted", text: m.expires_at && !m.expires_at.startsWith("2318") ? fmtRel(m.expires_at) : "nunca" }) },
        { label: "", cls: "right", render: (m) => h("div", { class: "row nw" },
          btn("Recarregar", { icon: "refresh", cls: "xs", title: "Aplicar as opções de GPU atuais", onClick: () => reload(m.name) }),
          btn("Descarregar", { icon: "stop", cls: "xs ghost", onClick: async () => { await post("/api/ai/ollama/load", { conn, model: m.name, action: "unload" }); toast("Modelo descarregado", "ok"); drawStatus(); } })) },
      ], running, { empty: h("p", { class: "muted small ai-pad", text: "Nenhum modelo carregado. O primeiro uso carrega o modelo (pode levar alguns segundos)." }) }) }),
      modelsCard(o, conn, drawStatus, reload));
  };

  view.append(gpuCard, statusBox);
  drawGPU();
  drawStatus();
  const off = ai.on((ev) => { if (!view.isConnected) return off(); if (ev !== "reload") drawGPU(); });
}

function installBadge(v) {
  const map = { nvidia: ["GPU NVIDIA reservada", "ok"], amd: ["GPU AMD (ROCm) reservada", "ok"], off: ["sem GPU (--gpu=0)", "outline"] };
  const [l, k] = map[v] || ["não informado", "outline"];
  return h("div", null, badge(l, k, true));
}

function gpuBadge(g) {
  return { nvidia: badge("GPU NVIDIA", "ok", true), amd: badge("GPU AMD", "ok", true), none: badge("sem GPU", "outline", true) }[g] || badge("GPU ?", "outline");
}

function modelsCard(o, conn, refresh, reload) {
  const inp = h("input", { type: "text", class: "mono", placeholder: "ex.: qwen3.8, llama3.2:3b, qwen2.5-coder:7b", "aria-label": "Modelo para baixar" });
  const pull = btn("Baixar", { icon: "download", cls: "sm primary", onClick: async () => {
    const model = inp.value.trim();
    if (!model) return toast("Informe o modelo", "err");
    await jobModal(`ollama pull ${model}`, () => post("/api/ai/ollama/pull", { conn, model }), { onEnd: refresh });
  } });
  inp.addEventListener("keydown", (e) => { if (e.key === "Enter") pull.click(); });
  return card({ title: "Modelos instalados", subtitle: "Modelos disponíveis no servidor Ollama selecionado. Catálogo: ollama.com/library.",
    actions: [h("div", { class: "row nw ai-pull" }, inp, pull)], flush: true,
    body: table([
      { label: "Modelo", render: (m) => h("div", null, h("b", { class: "mono", text: m.name }), m.name === o.default_model || m.name.replace(/:latest$/, "") === o.default_model ? [" ", badge("padrão", "info")] : null) },
      { label: "Família", render: (m) => h("span", { class: "small", text: (m.details && m.details.family) || "—" }) },
      { label: "Parâmetros", render: (m) => h("span", { class: "mono small", text: (m.details && m.details.parameter_size) || "—" }) },
      { label: "Quantização", render: (m) => h("span", { class: "mono small", text: (m.details && m.details.quantization_level) || "—" }) },
      { label: "Tamanho", render: (m) => h("span", { class: "small", text: fmtBytes(m.size) }) },
      { label: "Atualizado", render: (m) => h("span", { class: "small muted", text: m.modified_at ? fmtRel(m.modified_at) : "—" }) },
      { label: "", cls: "right", render: (m) => h("div", { class: "row nw" },
        btn("Carregar", { icon: "play", cls: "xs", title: "Carregar na memória com as opções de GPU atuais", onClick: () => reload(m.name) }),
        btn("", { icon: "trash", cls: "xs ghost", title: "Remover", onClick: async () => {
          if (!await confirmDialog({ title: "Remover modelo", message: `Remover ${m.name} do servidor Ollama? Será preciso baixá-lo de novo para usar.`, danger: true, confirmText: "Remover" })) return;
          try { await post("/api/ai/ollama/delete", { conn, model: m.name }); toast("Modelo removido", "ok"); refresh(); } catch (e) { toastErr(e); }
        } })) },
    ], o.models || [], { empty: h("p", { class: "muted small ai-pad", text: o.online ? "Nenhum modelo baixado." : "Servidor offline." }) }) });
}

