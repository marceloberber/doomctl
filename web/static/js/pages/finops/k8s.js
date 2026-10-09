import { get, post } from "../../api.js";
import { h, mount, card, btn, loading, table, badge, field, input, select, toastErr, emptyState, tabs, modal } from "../../ui.js";
import { fmtMoney, hbars, sevBadge, statTile, note, codeBlock, kpiMoney } from "./common.js";

export async function render(root, ctx) {
  const out = h("div", { class: "stack" });
  let clusters = [];
  const canK8s = ctx.can("kubernetes");
  if (canK8s) { try { clusters = await get("/api/k8s/clusters"); } catch (_) { clusters = []; } }
  const canCollect = ctx.can("kubernetes", "manage");
  const clusterSel = select([["", clusters.length ? "Selecione um cluster" : "Nenhum cluster cadastrado"], ...clusters.map((c) => [String(c.id), c.name])], "", { "aria-label": "Cluster" });
  const monthly = input({ type: "number", min: "0", step: "0.01", placeholder: "opcional", "aria-label": "Custo mensal do cluster" });
  const pods = h("textarea", { rows: 4, class: "mono", placeholder: "kubectl get pods -A -o json" });
  const nodes = h("textarea", { rows: 4, class: "mono", placeholder: "kubectl get nodes -o json" });
  const top = h("textarea", { rows: 3, class: "mono", placeholder: "kubectl top pods -A --no-headers (opcional)" });
  const hpas = h("textarea", { rows: 3, class: "mono", placeholder: "kubectl get hpa -A -o json (opcional)" });
  const name = input({ placeholder: "nome do cluster", "aria-label": "Nome do cluster" });

  const analyze = async (body) => {
    mount(out, loading("Analisando custos do cluster..."));
    try {
      const rep = await post("/api/finops/k8s/analyze", { ...body, cluster_monthly: Number(monthly.value || 0) });
      show(rep);
    } catch (e) { mount(out, ""); toastErr(e); }
  };
  let mode = canCollect && clusters.length ? "cluster" : "paste";
  const src = h("div");
  const drawSrc = () => mount(src, mode === "cluster"
    ? h("div", { class: "row" }, clusterSel, field("Custo mensal informado", monthly, "Substitui o catálogo: distribuído pelos nós", "fo-inline"),
      btn("Coletar e analisar", { icon: "play", cls: "primary", onClick: () => {
        if (!clusterSel.value) throw new Error("Selecione um cluster");
        return analyze({ cluster_id: Number(clusterSel.value) });
      } }),
      h("span", { class: "small muted", text: "Somente leitura: get pods/nodes/hpa e top pods." }))
    : h("div", { class: "stack sm" },
      h("div", { class: "grid g2" }, field("Pods (JSON)", pods), field("Nós (JSON)", nodes), field("Uso (kubectl top)", top), field("HPAs (JSON)", hpas)),
      h("div", { class: "row" }, name, field("Custo mensal informado", monthly, null, "fo-inline"),
        btn("Analisar", { icon: "play", cls: "primary", onClick: () => analyze({ pods: pods.value, nodes: nodes.value, top: top.value, hpas: hpas.value, cluster_name: name.value }) }))));
  const modes = [{ id: "paste", label: "Colar saídas do kubectl", icon: "terminal" }];
  if (canCollect) modes.unshift({ id: "cluster", label: "Cluster cadastrado", icon: "helm" });
  drawSrc();
  mount(root, h("div", { class: "stack" },
    card({ title: "Custo do Kubernetes", subtitle: "Custo dos nós rateado por requests/uso de CPU e memória (peso 7,5:1, como no OpenCost) por namespace, aplicação e workload.",
      actions: btn("Ver exemplo", { icon: "eye", cls: "sm", onClick: () => analyze({ demo: true }) }),
      body: h("div", { class: "stack sm" }, tabs(modes, mode, (id) => { mode = id; drawSrc(); }), src,
        h("p", { class: "small muted", text: "Preços dos nós: catálogo instance:<tipo> (pela região do nó) ou k8s:vcpu + k8s:memory_gb. Sem preço, informe o custo mensal do cluster." })) }),
    out));

  function show(r) {
    const cur = r.currency;
    let sub = "ns";
    const subBody = h("div");
    const draw = () => {
      if (sub === "ns") mount(subBody, h("div", { class: "grid g2" }, h("div", null, h("h3", { class: "fo-h3", text: "Por namespace" }), hbars(r.namespaces, cur, { showDelta: false, limit: 15 })),
        h("div", null, h("h3", { class: "fo-h3", text: "Por aplicação" }), hbars(r.apps, cur, { showDelta: false, limit: 15 }))));
      else if (sub === "wl") mount(subBody, table([
        { label: "Workload", render: (w) => h("div", null, h("strong", { text: w.name }), h("div", { class: "small muted", text: `${w.namespace} · ${w.kind}` })) },
        { label: "Pods", key: "pods", cls: "right" },
        { label: "CPU req / uso", cls: "right", render: (w) => h("span", { class: "small", text: `${w.cpu_request} / ${w.cpu_usage != null ? w.cpu_usage : "—"}` }) },
        { label: "Mem GiB req / uso", cls: "right", render: (w) => h("span", { class: "small", text: `${w.mem_request} / ${w.mem_usage != null ? w.mem_usage : "—"}` }) },
        { label: "Alertas", render: (w) => h("div", { class: "row nw" }, w.no_requests ? badge("sem requests", "warn") : null, w.no_mem_limit ? badge("sem limit de memória", "") : null, w.has_hpa ? badge("HPA", "info") : null) },
        { label: "Custo/mês", cls: "right", render: (w) => h("b", { text: fmtMoney(w.monthly, cur) }) },
      ], r.workloads));
      else mount(subBody, table([
        { label: "Nó", render: (n) => h("div", null, h("strong", { text: n.name }), h("div", { class: "small muted", text: `${n.instance_type || "?"} · ${n.region || ""}` })) },
        { label: "Capacidade", render: (n) => h("span", { class: "small", text: `${n.cpu} vCPU · ${n.mem_gib.toFixed(1)} GiB` }) },
        { label: "Reservado", render: (n) => h("span", { class: "small", text: `${n.cpu_requested} vCPU · ${n.mem_requested} GiB` }) },
        { label: "Tipo", render: (n) => n.spot ? badge("Spot", "ok") : badge("sob demanda", "") },
        { label: "Preço", render: (n) => h("span", { class: "small muted", text: n.price_source || "sem preço" }) },
        { label: "Ocioso", cls: "right", render: (n) => fmtMoney(n.idle, cur) },
        { label: "Custo/mês", cls: "right", render: (n) => h("b", { text: fmtMoney(n.monthly, cur) }) },
      ], r.nodes));
    };
    draw();
    const savings = r.findings.reduce((s, f) => s + (f.monthly_savings || 0), 0);
    mount(out,
      (r.notes || []).map((n) => note(n, n.includes("sem preço") ? "warn" : "info")),
      h("div", { class: "stat-row" },
        statTile("Custo mensal", kpiMoney(r.monthly, cur), `${r.nodes.length} nós · ${r.priced_nodes} com preço`),
        statTile("Alocado a workloads", kpiMoney(r.allocated, cur), "max(request, uso)"),
        statTile("Capacidade ociosa", kpiMoney(r.idle, cur), `${r.idle_pct.toFixed(0)}% do cluster`),
        statTile("Economia potencial", kpiMoney(savings, cur), `${r.findings.length} recomendações`)),
      card({ title: `Distribuição de custo${r.cluster ? " — " + r.cluster : ""}`, body: h("div", { class: "stack sm" },
        tabs([{ id: "ns", label: "Namespaces & aplicações" }, { id: "wl", label: `Workloads (${r.workloads.length})` }, { id: "nodes", label: `Nós (${r.nodes.length})` }], sub, (id) => { sub = id; draw(); }), subBody),
        actions: ctx.can("ai") ? btn("Otimizar com IA", { icon: "sparkles", cls: "sm", onClick: () => ctx.openAI({ module: "finops", filename: "k8s-custos.txt",
          content: `Cluster ${r.cluster}: ${fmtMoney(r.monthly, cur)}/mês, ocioso ${r.idle_pct}%\n` + r.workloads.slice(0, 25).map((w) => `${w.namespace}/${w.kind}/${w.name} pods=${w.pods} cpuReq=${w.cpu_request} cpuUso=${w.cpu_usage} memReq=${w.mem_request} memUso=${w.mem_usage} custo=${w.monthly}`).join("\n"),
          prompt: "Sugira ajustes de requests/limits, réplicas, HPA/VPA e consolidação de nós para reduzir o custo deste cluster sem perder disponibilidade.", send: true }) }) : null }),
      card({ title: "Recomendações", flush: true, body: table([
        { label: "Recomendação", render: (f) => h("div", null, h("strong", { text: f.title }), h("div", { class: "small muted", text: f.name })) },
        { label: "Severidade", render: (f) => sevBadge(f.severity) },
        { label: "Detalhe", render: (f) => h("span", { class: "small", text: f.detail }) },
        { label: "Economia/mês", cls: "right", render: (f) => f.monthly_savings != null ? h("b", { class: "up", title: f.savings_note || "", text: fmtMoney(f.monthly_savings, cur) }) : h("span", { class: "faint", text: "—" }) },
      ], r.findings, { onRowClick: (f) => modal({ title: f.title, size: "wide", actions: [{ label: "Fechar" }], body: h("div", { class: "stack sm" },
        h("p", { text: f.detail }), note(f.recommendation), f.remediation ? codeBlock(f.remediation) : null, f.savings_note ? h("p", { class: "small muted", text: "Premissa: " + f.savings_note }) : null) }),
        empty: emptyState("checkCircle", "Sem recomendações", "") }) }));
  }
  if (!canK8s && !ctx.query.get("demo")) out.appendChild(note("Para coletar direto de um cluster cadastrado é preciso permissão de gerenciar Kubernetes; você pode colar as saídas do kubectl."));
}
