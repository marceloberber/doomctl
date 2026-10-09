import { get, saveText } from "../api.js";
import { h, mount, card, table, input, pageHead, fmtNum, toastErr, btn } from "../ui.js";

export async function render(root) {
  let rows = [];
  try { rows = await get("/api/net/masks"); } catch (e) { return toastErr(e); }
  const filter = input({ type: "search", placeholder: "Filtrar por /prefixo, máscara ou wildcard (ex.: 255.255.255.0, /27, 0.0.0.63)" });
  const box = h("div");
  const draw = () => {
    const q = filter.value.trim().replace(/^\//, "");
    const list = q ? rows.filter((r) => String(r.prefix) === q || r.netmask.includes(q) || r.wildcard.includes(q)) : rows;
    mount(box, table([
      { label: "Prefixo", render: (r) => h("strong", { class: "mono", text: "/" + r.prefix }) },
      { label: "Máscara", render: (r) => h("span", { class: "mono", text: r.netmask }) },
      { label: "Wildcard", render: (r) => h("span", { class: "mono", text: r.wildcard }) },
      { label: "Hex", render: (r) => h("span", { class: "mono small muted", text: r.hex }) },
      { label: "Endereços", cls: "right", render: (r) => fmtNum(r.total) },
      { label: "Hosts úteis", cls: "right", render: (r) => fmtNum(r.usable) },
      { label: "Equivalência", render: (r) => h("span", { class: "small muted", text: r.classful }) },
    ], list));
  };
  filter.addEventListener("input", draw);
  mount(root,
    pageHead("Máscaras de rede & Wildcard", "Tabela completa de /0 a /32 — wildcard é o inverso da máscara (usado em ACLs Cisco e OSPF).",
      [btn("Exportar CSV", { icon: "download", onClick: () => saveText("prefix,netmask,wildcard,hex,total,usable\n" +
        rows.map((r) => [r.prefix, r.netmask, r.wildcard, r.hex, r.total, r.usable].join(",")).join("\n") + "\n", "mascaras-wildcard.csv", "text/csv") })],
      ["Redes & Segurança"]),
    card({ body: h("div", { class: "stack" }, filter, box), flush: false }));
  draw();
}
