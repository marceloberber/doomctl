import { post, saveText } from "../api.js";
import { h, mount, clear, icon, btn, card, table, field, input, select, toast, pageHead, fmtNum, copyText } from "../ui.js";

const V4_LABELS = [
  ["cidr", "Rede (CIDR)"], ["address", "Endereço"], ["netmask", "Máscara"], ["wildcard", "Wildcard"], ["network", "Endereço de rede"],
  ["broadcast", "Broadcast"], ["first_host", "Primeiro host"], ["last_host", "Último host"], ["total", "Total de endereços"],
  ["usable", "Hosts utilizáveis"], ["aws_usable", "Utilizáveis na AWS (−5)"], ["oci_usable", "Utilizáveis na OCI (−3)"], ["class", "Classe"],
  ["type", "Tipo"], ["binary_address", "Endereço (binário)"], ["binary_netmask", "Máscara (binário)"], ["hex_address", "Endereço (hex)"],
  ["int_address", "Endereço (inteiro)"], ["reverse_zone", "Zona reversa (PTR)"], ["note", "Observação"],
];
const V6_LABELS = [["network", "Rede"], ["address", "Endereço"], ["expanded", "Expandido"], ["first", "Primeiro endereço"], ["last", "Último endereço"],
  ["total", "Total de endereços"], ["subnets_64", "Sub-redes /64"], ["type", "Tipo"]];

export async function render(root, ctx) {
  const inp = input({ value: "192.168.10.77/27", class: "mono", placeholder: "10.0.0.1/24 · 10.0.0.1 255.255.255.0 · 2001:db8::/48", style: { fontSize: "16px", height: "44px" } });
  const result = h("div");
  const calc = async () => {
    const v = inp.value.trim();
    if (!v) return;
    try {
      const r = await post("/api/net/calc", { input: v });
      const labels = v.includes(":") ? V6_LABELS : V4_LABELS;
      const rows = labels.filter(([k]) => r[k] !== undefined && r[k] !== null && r[k] !== "");
      const text = rows.map(([k, l]) => `${l}: ${r[k]}`).join("\n");
      mount(result, h("dl", { class: "kv" }, rows.flatMap(([k, l]) => [h("dt", { text: l }),
        h("dd", { text: typeof r[k] === "number" && k !== "int_address" ? fmtNum(r[k]) : String(r[k]) })])),
      h("div", { class: "row", style: { marginTop: "14px" } }, btn("Copiar resultado", { icon: "copy", cls: "sm", onClick: () => copyText(text) })));
    } catch (e) { mount(result, h("div", { class: "alert err" }, h("div", { class: "alert-icon" }, icon("alert")), h("div", null, h("strong", { text: e.message })))); }
  };
  inp.addEventListener("keydown", (e) => { if (e.key === "Enter") calc(); });
  const examples = ["10.0.0.0/8", "172.16.5.4/20", "192.168.1.130 255.255.255.192", "100.64.0.0/10", "10.20.1.0/31", "2001:db8:abcd::1/48"].map((ex) =>
    h("button", { class: "badge outline", type: "button", onclick: () => { inp.value = ex; calc(); } }, ex));

  // divisão
  const sNet = input({ value: "10.0.0.0/16", class: "mono" });
  const sMode = select([["count", "Quantidade de sub-redes"], ["prefix", "Novo prefixo (/n)"]], "count");
  const sVal = input({ type: "number", value: 6, min: 1, max: 4096 });
  const sOut = h("div");
  const split = async () => {
    const body = { network: sNet.value.trim() };
    if (sMode.value === "count") body.count = Number(sVal.value); else body.prefix = Number(sVal.value);
    const r = await post("/api/net/split", body);
    mount(sOut, h("p", { class: "muted small", style: { margin: "10px 0" }, text: `${r.subnets.length} sub-redes /${r.prefix} — ${fmtNum(r.subnets[0].usable)} hosts utilizáveis cada` }),
      subnetTable(r.subnets, false),
      h("div", { class: "row", style: { marginTop: "10px" } }, btn("Exportar CSV", { icon: "download", cls: "sm", onClick: () => exportCSV(r.subnets, "subnets.csv") })));
  };

  // VLSM
  const vNet = input({ value: "192.168.0.0/24", class: "mono" });
  const vRows = h("div", { class: "stack sm" });
  const addReq = (n = "", hosts = "") => {
    const ni = input({ placeholder: "nome (ex.: servidores)", value: n });
    const hi = input({ type: "number", placeholder: "hosts", value: hosts, min: 1 });
    const row = h("div", { class: "kv-row" }, ni, hi, h("button", { class: "btn sm ghost", type: "button", "aria-label": "remover", onclick: () => row.remove() }, icon("trash", "sm")));
    row._n = ni; row._h = hi;
    vRows.appendChild(row);
  };
  [["usuarios", 100], ["servidores", 50], ["voip", 20], ["link-wan", 2]].forEach(([a, b]) => addReq(a, b));
  const vOut = h("div");
  const vlsm = async () => {
    const reqs = [...vRows.children].map((r) => ({ name: r._n.value.trim() || "subrede", hosts: Number(r._h.value) })).filter((r) => r.hosts > 0);
    const r = await post("/api/net/vlsm", { network: vNet.value.trim(), requests: reqs });
    mount(vOut, h("p", { class: "muted small", style: { margin: "10px 0" }, text: r.note }), subnetTable(r.subnets, true),
      h("div", { class: "row", style: { marginTop: "10px" } }, btn("Exportar CSV", { icon: "download", cls: "sm", onClick: () => exportCSV(r.subnets, "vlsm.csv") })));
  };

  mount(root,
    pageHead("Calculadora de sub-redes", "IPv4 e IPv6 — cálculo em Go no servidor, com divisão de redes e VLSM.", null, ["Redes & Segurança"]),
    h("div", { class: "stack" },
      card({ title: "Calcular", subtitle: "Aceita CIDR, máscara decimal ou endereço IPv6.", body: h("div", { class: "stack" },
        h("div", { class: "row nw" }, h("div", { class: "grow" }, inp), btn("Calcular", { icon: "calc", cls: "primary", onClick: calc })),
        h("div", { class: "row", style: { gap: "6px" } }, h("span", { class: "small muted", text: "Exemplos:" }), examples), result) }),
      h("div", { class: "grid g2" },
        card({ title: "Dividir rede (subnetting)", subtitle: "Divide em partes iguais.", body: h("div", { class: "stack sm" },
          h("div", { class: "form-grid three" }, field("Rede", sNet), field("Modo", sMode), field("Valor", sVal)),
          h("div", null, btn("Dividir", { icon: "grid", cls: "primary sm", onClick: split })), sOut) }),
        card({ title: "VLSM", subtitle: "Aloca do maior para o menor requisito, com alinhamento.", body: h("div", { class: "stack sm" },
          field("Rede disponível", vNet), h("b", { class: "small", text: "Requisitos (nome · hosts)" }), vRows,
          h("div", { class: "row" }, btn("Requisito", { icon: "plus", cls: "sm", onClick: () => addReq() }), btn("Calcular VLSM", { icon: "calc", cls: "primary sm", onClick: vlsm })),
          vOut) }))));
  calc();
}

function subnetTable(subs, named) {
  const cols = [];
  if (named) cols.push({ label: "Nome", render: (r) => h("strong", { text: r.name }) }, { label: "Pedido", render: (r) => String(r.needed) });
  cols.push(
    { label: "CIDR", render: (r) => h("span", { class: "mono", text: r.cidr }) },
    { label: "Máscara", render: (r) => h("span", { class: "mono small", text: r.netmask }) },
    { label: "Faixa de hosts", render: (r) => h("span", { class: "mono small", text: `${r.first_host} – ${r.last_host}` }) },
    { label: "Broadcast", render: (r) => h("span", { class: "mono small", text: r.broadcast }) },
    { label: "Hosts", cls: "right", render: (r) => fmtNum(r.usable) });
  return table(cols, subs);
}

function exportCSV(subs, name) {
  const head = "name,cidr,netmask,first_host,last_host,broadcast,usable\n";
  saveText(head + subs.map((s) => [s.name || "", s.cidr, s.netmask, s.first_host, s.last_host, s.broadcast, s.usable].join(",")).join("\n") + "\n", name, "text/csv");
  toast("CSV exportado", "ok");
}
