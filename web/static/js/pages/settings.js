// Configurações (administradores): sistema e assistente de IA (agentes, comportamento,
// conexões e MCP, GPU e modelos, laboratório e histórico).
import { h, mount, clear, tabs, pageHead, loading, toastErr } from "../ui.js";
import { createAIState } from "./settings/ai_common.js";

const TABS = [
  { id: "sistema", label: "Sistema", icon: "server", mod: "system" },
  { id: "agentes", label: "Agentes de IA", icon: "sparkles", mod: "ai_agents", ai: true },
  { id: "comportamento", label: "Comportamento", icon: "shield", mod: "ai_behavior", ai: true },
  { id: "conexoes", label: "Conexões & MCP", icon: "network", mod: "ai_connections", ai: true },
  { id: "gpu", label: "GPU & modelos", icon: "cpu", mod: "ai_gpu", ai: true },
  { id: "laboratorio", label: "Laboratório & histórico", icon: "activity", mod: "ai_lab", ai: true },
];

export async function render(root, ctx) {
  const active = TABS.find((t) => t.id === ctx.rest[0]) ? ctx.rest[0] : "sistema";
  const body = h("div", { class: "fo-body" });
  const ai = createAIState();
  const bar = tabs(TABS, active, (id) => { history.replaceState(null, "", "#/settings/" + id); show(id); });
  bar.classList.add("fo-tabs");
  mount(root,
    pageHead("Configurações", "Servidor e assistente de IA: agentes, guardrails, estilo, conexões (API e MCP), GPU e modelos.", null, ["Administração"]),
    bar, ai.saveBar, body);

  let seq = 0;
  async function show(id) {
    const t = TABS.find((x) => x.id === id) || TABS[0];
    const my = ++seq;
    ai.setTab(t);
    mount(body, loading());
    try {
      if (t.ai) await ai.ensure();
      const mod = await import(`./settings/${t.mod}.js`);
      if (my !== seq) return;
      clear(body);
      await mod.render(body, { ...ctx, ai, goTab: (x) => { history.replaceState(null, "", "#/settings/" + x); bar.querySelectorAll(".tab").forEach((b, i) => b.classList.toggle("active", TABS[i].id === x)); show(x); } });
    } catch (e) {
      if (my === seq) { clear(body); toastErr(e); }
    }
  }
  show(active);
  return { destroy: () => ai.destroy() };
}
