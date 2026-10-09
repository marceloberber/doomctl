# Projeto Doomctl

---

## Descrição

O projeto `doomctl` será um sistema/plataforma web, que oferecerá ferramentas de `DevOps & Cloud` e `Redes & Segurança`. O sistema possuirá um assistente guia de IA (agentes locais com `ollama`). O sistema basicamente irá gerar os dockerfiles, docker-composes, playbooks yaml do Ansible, os arquivos terraform e assim sucessivamente. A intenção é que seja um serviço self-hosted, para que as ferramentas possam ser executadas em LAN, ou redes com acesso ao server (doomctl master). Mas futuramente pretendo implementar agentes remotos comunicando-se via websocket.

O logo é o `doomctl_logo.png`. Pode converter para `.svg`, Ajuste de acordo com o tamanho da tela.

A aplicação deverá ser responsiva para ser utilizada em mobile.

Deixe um alerta na página inicial, dizendo que o `doomcli` (CLI do DOOMCTL) será lançado em breve. Onde poderá ser utilizado via linha de comando.

---

## Agentes de IA

Descrição detalhada em `agents/AGENTS.md`

O router, prompts estão dentro do diretório `agents/`

---

## Funcionamento

O `doomctl` terá uma interface simples e objetiva. As categoria serão separadas por subcategorias. Sendo:

* Seção `DevOps & Cloud`:
  - Ansible
  - Docker
  - Kubernetes (*beta*)
  - OpenTofu (alternativa open source do Terraform)
  - AWS EC2 e S3 via IAM (coloque *em breve* por enquanto)
  - FinOps (coloque *em breve*)

* Seção `Redes & Segurança`
  - Calculadora de sub-redes
  - Tabela de netmask e wildcard
  - `Nmap` para scan de rede na rede do server
  - `Trivy` para container security & SBOM
  - `Falco` para container security em realtime

---

## Descrição das subcategorias

Aqui, entrará o agente de IA.

```
* Ansible:

  - O `doomctl` terá suporte nativo e configuração ao `ansible-vault`, para env files locais personalizados. Também armazenar `ansible_username` e `ansible_password` no vault, solicitando senha para **criação**, **edição**, **visualização**. Para exclusão, apenas exibir um pop-up de confirmação. O ansible e seus utilitários deverão estar instalados no server.
  - Será possível gerar `roles`: `handlers`, `tasks`, `meta`.
  - O ansible deverá ser modular. O inventário deverá ser visual, o usuário poderá inserir os hosts diretamente do dashboard no browser, ou poderá `importar` de um arquivo `.csv`.
  - O usuário poderá executar comandos ad-hoc, diretamente de um terminal.
  - Gerar `playbooks`. Checar sintaxe. Executar playbooks (utilizar vaults criados).


* Docker:

 - Será possível criar `Dockerfile`
 - Criação/geração de `docker-compose.yaml`:
   - Configuração de registries
   - Configuração de volumes
   - Configuração de rede do docker
 - Login no dockerhub

* OpenTofu

  - Criação de `.tf`:
    - Providers suportados: `AWS` e `OCI`
  - OpenTofu plan
  - OpenTofu apply
  - OpenTofu destroy

* Kubernetes (coloque *beta*)

  - Deployments — aplicações stateless.
  - Services — exposição e comunicação entre workloads.
  - Ingress / Gateway — exposição HTTP/HTTPS externa.
  - ConfigMaps — configurações.
  - Secrets — credenciais e dados sensíveis.
  - PersistentVolume / PersistentVolumeClaim (PV/PVC) — armazenamento persistente.
  - StatefulSets — bancos e aplicações stateful.
  - Jobs / CronJobs — tarefas batch e agendadas.
  - Namespaces — isolamento lógico.
  - RBAC — Roles, ClusterRoles e permissões.
  - ServiceAccounts — identidade dos workloads.
  - Configuração de recursos — requests e limits.
  - HPA — autoscaling horizontal.
  - Probes — liveness, readiness e startup.
  - NetworkPolicies — controle de tráfego entre pods.
  - Pod Security / SecurityContext — hardening dos containers.
  - Helm Charts — empacotamento e parametrização das aplicações.
  - Kustomize — customização por ambiente.
  - Observabilidade — Prometheus/Grafana, logs e alertas.
  - Troubleshooting — análise de pods, logs, eventos, recursos e networking.
  - Security scanning — Trivy, misconfigurations, CVEs e secrets.
  - CI/CD — build → scan → registry → deploy.
  - GitOps — principalmente Argo CD ou Flux.
  - Rolling updates / rollback — estratégias de deployment.
  - Diagnóstico e remediation — identificar problemas e sugerir/gerar a correção.

* FinOps

  - Análise de custos cloud — AWS, OCI
  - Breakdown de custos — por conta, projeto, serviço, região, ambiente e aplicação.
  - Cost allocation — distribuição de custos por equipe, cliente, produto ou centro de custo.
  - Tagging — criação e validação de políticas de tags/labels.
  - Budget — criação de orçamentos e limites de gastos.
  - Cost alerts — alertas de aumento ou anomalias de custo.
  - Anomaly detection — identificar gastos anormais.
  - Rightsizing — identificar VMs, databases e outros recursos superdimensionados.
  - Idle resources — detectar recursos ociosos ou subutilizados.
  - Waste detection — identificar desperdícios, como volumes, IPs, snapshots e VMs sem uso.
  - Kubernetes cost analysis — custo por cluster, namespace, deployment, pod, workload ou aplicação.
  - Cost optimization Kubernetes — analisar requests, limits, replicas e autoscaling para reduzir custos.
  - Reserved Instances / Savings Plans — analisar oportunidades de compromisso de consumo.
  - Spot Instances — identificar workloads adequados para Spot/Preemptible.
  - Storage optimization — analisar volumes, snapshots, S3/Object Storage e classes de armazenamento.
  - Database cost optimization — identificar databases superdimensionados e oportunidades de redução.
  - Network cost analysis — analisar custos de NAT Gateway, egress, inter-region traffic etc.
  - Forecasting — previsão de gastos futuros.
  - Cost comparison — comparar custo entre arquiteturas, regiões ou provedores.
  - What-if analysis — "quanto custaria se eu aumentasse de 3 para 10 nodes?"
  - FinOps reporting — gerar relatórios de custos e tendências.
  - Showback — mostrar custos para equipes/projetos sem necessariamente cobrar internamente.
  - Chargeback — atribuir custos diretamente aos responsáveis.
  - Cost policies — definir regras como "produção não pode criar recursos acima de X".
  - Automated remediation — desligar recursos ociosos, ajustar configurações ou abrir PRs com otimizações.
  - IaC cost estimation — estimar custo antes de aplicar Terraform/OpenTofu.
  - CI/CD cost checks — bloquear PRs quando uma mudança aumentar significativamente o custo.
  - Cost-aware architecture — sugerir arquiteturas considerando custo, performance e disponibilidade.

Para uma IA de Infra/DevOps, os mais interessantes seriam:

Cost Analysis → Anomaly Detection → Rightsizing → Waste Detection → Kubernetes Cost → Forecasting → What-if → IaC Cost Estimation → Automated Remediation.

### Redes e Segurança

* Aba com uma Calculadora de sub-redes feita em Golang mesmo.

* Aba com tabela de máscaras de rede e wildcard.

* nmap
  
  - Scanner de rede, de acordo com a interface selecionada. Para isto, o server do doomctl deverá estar em bridge, para que haja comunicação com o host e a LAN, consequentemente.

* Trivy

  - Análise de imagens. Armazenadas localmente pelo próprio sistema ou não.

* Falco

  - Análise de containeres em realtime via agente (*em breve*)


Todas as ferramentas deverão gerar outputs e logs, respectivamente. Todos os registros deverão ser armazenados numa aba `Logs`. Os logs são separados por ferramentas.

---

## Painéis administrativos, operacionais e visualizadores

O `doomctl` deverá conter três perfis de usuário:

* Administradores

* Operadores

* Visualizadores

Separe por regras as permissões de leitura e gerenciamento de recursos. Separe pelos módulos (ferramentas).

Os usuários visualizadores poderão apenas visualizar relatórios e logs de execução.

---

## Plugins de terceiros

Adicione uma área de Plug-ins de terceiros. Por enquanto, será possível instalar apenas o `Portainer Server Community Edition`. O portainer será uma apliação a parte, acessada em outra porta mesmo.

---

## Infraestrutura e back-end

 * Tudo em Docker (imagem base debian trixie stable)
 * Back-end em Golang current stable (go1.27.0)
 * Utilizar Python se necessário
 * Banco de dados PostgreSQL 18
 * Utilizar javascript onde for necessário

## Front-end

Imagem de referência em anexo. Crie também um switch button para dark/light mode.

## Extra

Implemente MFA com aplicativos TOTP, compatíveis com os principais: MS Authenticatos, Google Authenticator, entre outros.

O administrador é o único que pode exigir, desativar e ativar o MFA dos outros usuários. O operador e visualizador pode habilitar/desabilitar apenas o MFA de seus próprios usuários, se não for exigido pelo administrador.
