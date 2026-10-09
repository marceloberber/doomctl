import { h, mount, icon, card, pageHead } from "../ui.js";

const CONTENT = {
  ansible: {
    title: "Ansible", icon: "server", section: "DevOps & Cloud",
    text: "A interface web do Ansible está em desenvolvimento. Enquanto isso, o módulo pode ser usado pela API do doomctl (veja a documentação).",
    items: [["Inventário", "hosts e grupos, cadastro manual ou CSV"], ["Vault", "segredos no formato do ansible-vault"],
      ["Playbooks", "validação e execução com logs"], ["Roles", "criação e download"]],
  },
  aws: {
    title: "AWS EC2 & S3 via IAM", icon: "cloud", section: "DevOps & Cloud",
    text: "Gestão de instâncias EC2 e buckets S3 diretamente pela API da AWS, com credenciais IAM de menor privilégio. Enquanto isso, use o módulo OpenTofu (provider AWS).",
    items: [["EC2", "listar, iniciar, parar e inspecionar instâncias (IMDSv2)"], ["S3", "buckets, versionamento, lifecycle e Block Public Access"],
      ["IAM", "roles e policies de menor privilégio, sem *:*"], ["Security Groups", "auditoria de regras abertas para 0.0.0.0/0"]],
  },
  falco: {
    title: "Falco", icon: "activity", section: "Redes & Segurança",
    text: "Segurança de containers em tempo real via agente remoto (WebSocket) conectado ao doomctl master.",
    items: [["Agente doomctl", "coleta de eventos Falco em cada host"], ["Regras", "shell em container, escrita em binários, escalonamento de privilégio"],
      ["Alertas", "eventos centralizados nos Logs do doomctl"], ["Resposta", "sugestão de remediação pelo assistente Sentinela"]],
  },
};

export async function render(root, ctx) {
  const c = CONTENT[ctx.route.path];
  mount(root,
    pageHead(c.title, null, null, [c.section, h("span", { class: "tag soon", text: "em breve" })]),
    card({ body: h("div", null,
      h("div", { class: "soon-hero" }, h("div", { class: "ic" }, icon(c.icon, "lg")), h("h2", { text: "Em breve" }), h("p", { class: "muted", style: { maxWidth: "620px", margin: "8px auto 0" }, text: c.text })),
      h("div", { class: "feature-list" }, c.items.map(([t, d]) => h("div", null, h("b", { text: t }), " — ", d)))) }));
}
