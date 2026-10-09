# doomctl — Documentação

> Plataforma web **self-hosted** de ferramentas **DevOps & Cloud** e **Redes & Segurança**, com assistentes de IA locais (Ollama).
> Versão desta documentação: **0.1.0** · Backend Go 1.27 · PostgreSQL 18 · imagem base Debian 13 (trixie).

---

## Sumário

1. [Visão geral](#1-visão-geral)
2. [Arquitetura](#2-arquitetura)
3. [Requisitos](#3-requisitos)
4. [Deploy](#4-deploy)
5. [Configuração (variáveis de ambiente)](#5-configuração-variáveis-de-ambiente)
6. [TLS e proxy reverso](#6-tls-e-proxy-reverso)
7. [Assistente de IA (Ollama, APIs, MCP e GPU)](#7-assistente-de-ia-ollama-apis-mcp-e-gpu)
8. [Primeiro acesso](#8-primeiro-acesso)
9. [Usuários, perfis, permissões e MFA](#9-usuários-perfis-permissões-e-mfa)
10. [Utilização por módulo](#10-utilização-por-módulo)
11. [Ansible via API](#11-ansible-via-api)
12. [Referência da API](#12-referência-da-api)
13. [Segurança](#13-segurança)
14. [Operação: backup, atualização e troubleshooting](#14-operação-backup-atualização-e-troubleshooting)
15. [Desenvolvimento](#15-desenvolvimento)
16. [Limitações conhecidas e roadmap](#16-limitações-conhecidas-e-roadmap)
17. [Licenças de terceiros](#17-licenças-de-terceiros)

---

## 1. Visão geral

O doomctl roda na sua LAN (ou em qualquer rede com acesso ao servidor — o *doomctl master*) e reúne, numa interface única e responsiva (desktop e mobile, tema claro/escuro):

| Seção | Módulo | Situação |
|---|---|---|
| DevOps & Cloud | **Ansible** — inventário (manual ou CSV), `ansible-vault` nativo, playbooks, roles, syntax-check/lint, execução e ad-hoc | interface web **em breve** (no menu, inativa); backend completo, uso via API (§11) |
| | **Docker** — gerador de Dockerfile e docker-compose (registries, volumes, redes), login em registries/Docker Hub | estável |
| | **Kubernetes** — 23 modelos (workloads, rede, RBAC, HPA, Helm, Kustomize, observabilidade, CI/CD, GitOps, rollout), manifests salvos, console `kubectl` restrito, scan do cluster | **beta** |
| | **OpenTofu** — gerador para **AWS** e **OCI**, editor de arquivos, credenciais criptografadas, `init/validate/fmt/plan/apply/destroy` | estável |
| | **FinOps** — custos AWS/OCI/CSV, breakdown e alocação, anomalias e alertas, orçamentos, rightsizing/ociosos/desperdício, Savings Plans, custo de Kubernetes, previsão, what-if, estimativa de IaC com verificação em CI/CD, políticas, remediação e relatórios showback/chargeback | estável |
| | AWS EC2 & S3 via IAM | em breve |
| Redes & Segurança | **Calculadora de sub-redes** (IPv4/IPv6, subnetting, VLSM) | estável |
| | **Tabela de máscaras e wildcard** (/0–/32) | estável |
| | **Trivy** — CVEs, segredos, misconfigurações (IaC) e SBOM (CycloneDX/SPDX) | estável |
| | Falco (agente remoto) | em breve |
| Operação | **Logs** por ferramenta, **Assistente IA** (Atlas/Sentinela), **Plug-ins** (Portainer CE) | estável |
| Administração | Usuários, perfis (Administrador/Operador/Visualizador), matriz de permissões por módulo, MFA TOTP | estável |

A página inicial exibe o aviso de que o **`doomcli`** (a CLI do DOOMCTL) será lançado em breve.

---

## 2. Arquitetura

```
                    navegador (HTTPS :8443)
                              │
┌─────────────────────────────▼──────────────────────────────┐
│ container doomctl  (Debian trixie, usuário 10001, sem caps)│
│                                                            │
│  binário Go (API REST + SPA embutida)                      │
│   ├─ auth: PBKDF2-SHA256, sessões, CSRF, TOTP (RFC 6238)   │
│   ├─ RBAC por módulo (ler / gerenciar)                     │
│   ├─ jobs: execução assíncrona + SSE + logs no PostgreSQL  │
│   ├─ geradores (Dockerfile, compose, .tf, manifests K8s)   │
│   ├─ netcalc (Go puro)                                     │
│   └─ router de IA (Atlas / Sentinela) ──────────┐          │
│                                                 │          │
│  ferramentas: ansible, ansible-lint, tofu,      │          │
│  trivy, kubectl, docker CLI + compose, sshpass  │          │
└───────┬──────────────────────┬──────────────────┼──────────┘
        │ rede backend         │ docker.sock      │ HTTP
┌───────▼────────┐    ┌────────▼────────┐  ┌──────▼──────────┐
│ PostgreSQL 18  │    │ Docker do host  │  │ Ollama (local,  │
│ (sem acesso    │    │ (Portainer CE,  │  │ container ou    │
│  externo)      │    │ imagens locais) │  │ outro servidor) │
└────────────────┘    └─────────────────┘  └─────────────────┘
```

- **Frontend**: SPA em JavaScript puro (ES modules), sem CDN e sem build — funciona em redes sem internet. Embutida no binário (`embed`).
- **Backend**: Go 1.27, dependências mínimas (`lib/pq`, `go-qrcode`), vendorizadas em `vendor/` (build offline).
- **Persistência**: PostgreSQL 18 (usuários, sessões, permissões, logs, arquivos gerados) + volume `/var/lib/doomctl` (state do OpenTofu, roles Ansible materializadas, cache do Trivy, relatórios/SBOM, certificado TLS).
- **Execuções**: cada ação de ferramenta vira um *job* (fila com limite de concorrência), com saída transmitida por **SSE** e gravada em `tool_logs` ao final.

---

## 3. Requisitos

| Item | Mínimo | Recomendado |
|---|---|---|
| SO do host | Linux x86_64 ou arm64 com Docker Engine 24+ e plugin `docker compose` v2 | Debian 13 / Ubuntu 24.04 |
| CPU / RAM (sem IA) | 2 vCPU / 2 GB | 4 vCPU / 4 GB |
| Disco | 5 GB | 20 GB+ (cache do Trivy ~1 GB, providers do OpenTofu, relatórios) |
| IA local | — | Modelo 27B quantizado: GPU com 24 GB VRAM ou 32 GB+ de RAM (CPU fica lento) |
| Rede | acesso ao servidor pela LAN na porta 8443 | — |

Acesso à internet (no build e em uso) para: imagens base, GitHub (OpenTofu/Trivy), `dl.k8s.io`, `registry.opentofu.org` (providers), banco do Trivy (`mirror.gcr.io`/ghcr), registries de imagens. Em ambiente isolado veja §14.4.

---

## 4. Deploy

### 4.1 Instalação rápida

```bash
git clone https://github.com/<sua-org>/doomctl.git
cd doomctl
./install.sh                      # gera .env com segredos, constrói e sobe
./install.sh --with-ai            # idem + Ollama em container e download do modelo
./install.sh --with-ai --gpu=1    # idem, com aceleração por GPU (NVIDIA ou AMD)
./install.sh --gpu=0              # desliga a GPU do Ollama em container (somente CPU)
```

| Opção | Efeito |
|---|---|
| `--with-ai` | Sobe o Ollama em container (perfil `ai`) e baixa o `OLLAMA_MODEL`. Sem alterações em relação às versões anteriores. |
| `--gpu=1` | Detecta a GPU do host (NVIDIA via `nvidia-smi`; AMD via `/dev/kfd` + `/dev/dri`) e reserva-a para o container do Ollama. |
| `--gpu=nvidia` / `--gpu=amd` | Força o fabricante. NVIDIA exige o **NVIDIA Container Toolkit**; AMD usa a imagem `ollama/ollama:rocm`. |
| `--gpu=0` | Remove a reserva de GPU (somente CPU). |
| *(sem `--gpu`)* | Mantém o que estiver no `.env`. |

O `--gpu` grava `DOOMCTL_AI_GPU` e `COMPOSE_FILE` no `.env` (ex.: `COMPOSE_FILE=docker-compose.yml:docker-compose.gpu-nvidia.yml`), então um `docker compose --profile ai up -d` posterior mantém a GPU. Para a NVIDIA, prepare o host uma vez:

```bash
# driver NVIDIA instalado (nvidia-smi funciona) + NVIDIA Container Toolkit
sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker
./install.sh --with-ai --gpu=1
docker compose exec ollama nvidia-smi -L        # a GPU deve aparecer dentro do container
```

O `install.sh`:
1. valida Docker, `docker compose` e `openssl`;
2. cria `.env` a partir de `.env.example` com `POSTGRES_PASSWORD` e `DOOMCTL_SECRET_KEY` aleatórios (permissão 600) e detecta o `DOCKER_GID` do socket;
3. com `--gpu`, valida a GPU/toolkit e ajusta `DOOMCTL_AI_GPU` e `COMPOSE_FILE`;
4. executa `docker compose up -d --build` (com `--profile ai` quando `--with-ai`);
5. aguarda o healthcheck e mostra a URL e a senha temporária do administrador.

### 4.2 Instalação manual

```bash
cp .env.example .env
sed -i "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(openssl rand -base64 24 | tr -d '/+=')|" .env
sed -i "s|^DOOMCTL_SECRET_KEY=.*|DOOMCTL_SECRET_KEY=$(openssl rand -base64 32)|" .env
sed -i "s|^DOCKER_GID=.*|DOCKER_GID=$(stat -c %g /var/run/docker.sock)|" .env
chmod 600 .env

docker compose up -d --build
docker compose logs -f doomctl       # aguarde "doomctl no ar"
```

Serviços do `docker-compose.yml`:

| Serviço | Imagem | Observações |
|---|---|---|
| `postgres` | `postgres:18-trixie` | rede `backend` (interna). **Volume em `/var/lib/postgresql`** — no PG 18 o `PGDATA` padrão passou a ser `/var/lib/postgresql/18/docker`. |
| `doomctl` | construída do `Dockerfile` | porta `8443`, volume `doomctl-data`, socket do Docker, `cap_drop: ALL`, `no-new-privileges`, usuário 10001. |
| `ollama` | `ollama/ollama` | só com `--profile ai`. Para GPU NVIDIA, descomente o bloco `deploy`. |

### 4.3 O que a imagem contém

| Ferramenta | Origem | Verificação |
|---|---|---|
| doomctl, devops-router | build Go 1.27 (`golang:1.27-trixie`) | — |
| ansible, ansible-lint, sshpass, git, openssh-client, python3-yaml | pacotes Debian trixie | APT assinado |
| OpenTofu 1.13.1 | GitHub releases | `tofu_*_SHA256SUMS` |
| Trivy 0.75.0 | GitHub releases | `trivy_*_checksums.txt` |
| kubectl v1.37.1 | dl.k8s.io | `.sha256` oficial |
| docker CLI + compose | imagem oficial `docker:29-cli` | — |

Versões são `ARG`s do Dockerfile:

```bash
docker compose build --build-arg TOFU_VERSION=1.13.1 --build-arg TRIVY_VERSION=0.75.0 --build-arg KUBECTL_VERSION=v1.37.1
```

> **Trivy e supply chain:** as versões 0.69.4, 0.69.5, 0.69.6 e a tag `latest` publicadas entre 19 e 23/03/2026 foram comprometidas (CVE-2026-33634). Mantenha a versão fixada e prefira digest nos pipelines.

### 4.4 Deploy sem Docker (binário)

```bash
make build                      # bin/doomctl e bin/devops-router
export DOOMCTL_DATABASE_URL='postgres://doomctl:SENHA@127.0.0.1:5432/doomctl?sslmode=disable'
export DOOMCTL_SECRET_KEY="$(openssl rand -base64 32)"
export DOOMCTL_DATA_DIR=/var/lib/doomctl
./bin/doomctl
```

As ferramentas (ansible, tofu, trivy, kubectl, docker) precisam estar no `PATH`; a página **Configurações** mostra o que foi detectado. Exemplo de unit systemd:

```ini
[Unit]
Description=doomctl
After=network-online.target postgresql.service

[Service]
User=doomctl
EnvironmentFile=/etc/doomctl/doomctl.env
ExecStart=/usr/local/bin/doomctl
Restart=on-failure
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/doomctl

[Install]
WantedBy=multi-user.target
```

---

## 5. Configuração (variáveis de ambiente)

| Variável | Padrão | Descrição |
|---|---|---|
| `DOOMCTL_DATABASE_URL` | `postgres://doomctl:doomctl@127.0.0.1:5432/doomctl?sslmode=disable` | DSN do PostgreSQL (o compose monta a partir de `POSTGRES_PASSWORD`). |
| `DOOMCTL_SECRET_KEY` | — | **Obrigatória no compose.** 32 bytes em base64 ou hex. Criptografa (AES-256-GCM) segredos TOTP, senhas de registry, credenciais OpenTofu e kubeconfigs. Sem ela, o binário gera `DATA_DIR/secret.key` (não recomendado). |
| `DOOMCTL_DATA_DIR` | `/var/lib/doomctl` | Diretório de dados. |
| `DOOMCTL_LISTEN` | `:8443` | Endereço de escuta. |
| `DOOMCTL_TLS_MODE` | `self-signed` | `self-signed`, `files` ou `off`. |
| `DOOMCTL_TLS_CERT` / `DOOMCTL_TLS_KEY` | — | PEM do certificado/chave (modo `files`). |
| `DOOMCTL_ADMIN_USER` | `admin` | Administrador criado quando não há usuários. |
| `DOOMCTL_ADMIN_PASSWORD` | — | Senha inicial (deve cumprir a política). Vazio = senha temporária exibida no log. |
| `DOOMCTL_SESSION_TTL` | `12h` | Validade máxima da sessão (há também expiração por inatividade de 2h). |
| `DOOMCTL_MAX_JOBS` | `4` | Execuções simultâneas; as demais aguardam na fila. |
| `DOOMCTL_DOCKER_SOCKET` | `/var/run/docker.sock` | Socket do Docker. |
| `DOOMCTL_PORTAINER_IMAGE` | `portainer/portainer-ce:lts` | Imagem do plug-in. |
| `DOOMCTL_PORTAINER_PORT` | `9443` | Porta HTTPS publicada do Portainer. |
| `OLLAMA_HOST` | `http://127.0.0.1:11434` (compose: `http://ollama:11434`) | Endpoint do Ollama. |
| `OLLAMA_MODEL` | `qwen3.8` | Modelo. Confirme a tag com `ollama list` (ex.: `qwen3.8:27b`). |
| `ROUTER_TEMPERATURE` | `0.25` | Temperatura das personas — **somente 0.2–0.3**; fora disso o servidor não inicia. |
| `OLLAMA_THINK` | — | Opcional: envia o campo `think` ao Ollama. |
| `DOOMCTL_AI_GPU` | — | Como o Ollama em container foi instalado: `off`, `nvidia` ou `amd` (gravado pelo `install.sh --gpu`; informativo no painel). |
| `DOOMCTL_OLLAMA_CONTAINER` | — | Nome do container do Ollama (padrão: descoberto pelo rótulo do compose). Usado para status de GPU e reinício pelo navegador. |
| `COMPOSE_FILE` (compose) | — | Gravado pelo `install.sh --gpu` para incluir `docker-compose.gpu-nvidia.yml` ou `docker-compose.gpu-amd.yml`. |
| `DOOMCTL_DEBUG` | — | `1` = log detalhado. |
| `DOCKER_GID` (compose) | `999` | GID do grupo dono do socket no host. |
| `DOOMCTL_BIND` / `DOOMCTL_PORT` (compose) | `0.0.0.0` / `8443` | Publicação da porta. |

---

## 6. TLS e proxy reverso

**`self-signed` (padrão)** — gera um certificado ECDSA P-256 (validade 2 anos, renovado 30 dias antes de expirar) com `localhost`, hostname e IPs do container como SAN, em `DATA_DIR/tls/`. O navegador pede para aceitar a exceção no primeiro acesso.

**`files`** — use um certificado da sua CA interna:

```yaml
# docker-compose.yml → serviço doomctl
volumes:
  - ./certs:/certs:ro
```
```dotenv
DOOMCTL_TLS_MODE=files
DOOMCTL_TLS_CERT=/certs/doomctl.crt
DOOMCTL_TLS_KEY=/certs/doomctl.key
```

**`off` + proxy reverso** — o doomctl reconhece `X-Forwarded-Proto: https` para marcar o cookie como `Secure`. Exemplo com Caddy:

```caddyfile
doomctl.empresa.local {
    tls internal
    reverse_proxy doomctl:8443 {
        flush_interval -1   # streaming (SSE e IA)
    }
}
```

Nginx: desative o buffering (`proxy_buffering off;`) — o servidor já envia `X-Accel-Buffering: no` nas rotas de streaming.

---

## 7. Assistente de IA (Ollama, APIs, MCP e GPU)

O roteador original de `agents/` foi incorporado ao servidor (`internal/agents`) e mantido como CLI (`devops-router`). As personas, guardrails e parâmetros estão em [`internal/agents/AGENTS.md`](internal/agents/AGENTS.md).

**Opções de execução do Ollama**

| Cenário | `OLLAMA_HOST` |
|---|---|
| container do compose (`./install.sh --with-ai` ou `docker compose --profile ai up -d`) | `http://ollama:11434` |
| Ollama instalado no próprio host | `http://host.docker.internal:11434` (o Ollama deve escutar em `0.0.0.0` ou no IP do bridge) |
| servidor de GPU dedicado | `http://10.0.0.50:11434` |

```bash
docker compose exec ollama ollama pull qwen3.8      # confirme a tag exata
docker compose exec ollama ollama list
```

**Como funciona o roteamento** (AGENTS.md §7): a pergunta é normalizada e pontuada por palavras-chave → se a heurística decide, vai direto para **Atlas** (Cloud & DevOps) ou **Sentinela** (Redes & Segurança); em empate, score zero ou sinal de pedido fora do escopo, um classificador LLM com temperatura 0 e saída JSON decide. Fora de escopo retorna exatamente `Não posso responder a este tipo de pergunta`, sem chamar a persona. Se o classificador falhar e não houver pontuação, a falha é **fechada** (recusa).

**Na interface**
- Página **Assistente IA** (conversa completa) e botão **Assistente** no topo (painel lateral disponível em qualquer tela).
- Botões **Revisar com IA** em Docker, Kubernetes e OpenTofu anexam o arquivo atual à pergunta. O anexo **não** influencia o roteamento (evita injeção de instruções); só a pergunta é classificada.
- Histórico de até 10 mensagens por persona e por usuário (em memória; configurável em *Comportamento → Limites*); **Nova conversa** limpa.
- Cada pergunta/resposta fica registrada em **Logs** (módulo `ai`).

**CLI original**

```bash
docker compose exec doomctl devops-router "como calculo uma /27?"
OLLAMA_HOST=http://127.0.0.1:11434 ./bin/devops-router     # modo interativo
```

A CLI usa sempre a configuração do `.env` (não lê as configurações do navegador).

### 7.1 Configuração pelo navegador

Administradores configuram os agentes em **Configurações** (ou no botão **Configurar** da página Assistente IA). **Sem nada configurado, o comportamento é exatamente o da instalação com `--with-ai`**: Ollama do `.env`, prompts do AGENTS.md, temperatura do `ROUTER_TEMPERATURE` — há testes automatizados que garantem que os prompts e as requisições ao Ollama são idênticos byte a byte. O estilo do chat (bolhas, roteamento, recusa, streaming, Markdown) não muda; só textos informativos (provedor em uso) acompanham a configuração.

| Aba | O que configura |
|---|---|
| **Agentes de IA** | Por persona (Atlas e Sentinela): conexão (Ollama padrão, outro Ollama, API ou agente do Microsoft Foundry), modelo, temperatura, `top_p`, `repeat_penalty`, `num_ctx`, limite de tokens, raciocínio (*think*), **prompt em Markdown** (padrão, acrescentar ou substituir a persona), palavras-chave extras de roteamento, servidores MCP e máximo de chamadas de ferramenta. Classificador de escopo: liga/desliga o LLM, conexão e modelo. |
| **Comportamento** | Guardrails (trava de temperatura 0.2–0.3, recusa fora de escopo e mensagem de recusa, anti-alucinação, segurança operacional, termos bloqueados, redação de segredos, anexos para provedores externos), estilo (tom, detalhamento, idioma, estrutura Resumo/Passos/Validação/Riscos, marcadores 🛑/⚠️), **instruções da organização** (Markdown) e limites (tokens de saída, tamanho da pergunta/anexo, perguntas a cada 5 min, tempo limite, memória da conversa, tamanho do resultado de ferramenta). |
| **Conexões & MCP** | Cadastro e teste de conexões e servidores MCP, política de ferramentas e teste manual de ferramentas. |
| **GPU & modelos** | Aceleração por GPU (liga/desliga, camadas na GPU, `keep_alive`, threads), status da GPU e do container, modelos na memória (CPU/GPU, VRAM), baixar/carregar/descarregar/remover modelos, reiniciar o Ollama. |
| **Laboratório & histórico** | Executa perguntas com o **rascunho** (sem salvar), lado a lado com a configuração salva; versões da configuração (restaurar com um clique); uso por persona, conexão e usuário; exportar/importar JSON. |

As alterações ficam num rascunho; a barra **Alterações não salvas** permite ver o prompt final, descartar ou **Salvar e aplicar** — vale na hora para todos, sem reiniciar. Cada gravação gera uma versão (as 50 mais recentes) e um registro em Logs. Os limites de segurança ofensiva e a proteção contra injeção de prompt **não** podem ser desligados. Se uma conexão estiver inválida ou desativada, a persona volta para o Ollama padrão e o aviso aparece em *Agentes de IA*.

### 7.2 Conexões com modelos

| Provedor (modelo de preenchimento) | Tipo | URL base | Autenticação |
|---|---|---|---|
| Ollama (outro servidor) | Ollama | `http://<host>:11434` | nenhuma / bearer (proxy) |
| Servidor compatível com OpenAI (vLLM, LM Studio, LocalAI, llama.cpp) | API | `http://<host>:<porta>/v1` | nenhuma / bearer |
| Microsoft Foundry — modelos (deployments) | API | `https://<recurso>.services.ai.azure.com/openai/v1` | `api-key` ou Entra ID |
| Azure OpenAI | API | `https://<recurso>.openai.azure.com/openai/v1` (API antiga: `/openai/deployments/<deployment>` + `api-version` na query) | `api-key` ou Entra ID |
| Microsoft Foundry — agente / aplicação publicada / agente clássico | Agente | endpoint do projeto (ver §7.3) | Entra ID |
| OpenAI, Google Gemini (endpoint compatível), OpenRouter, Groq, Mistral, outras | API | URL oficial do provedor | bearer |

Tipos de autenticação: nenhuma, bearer, `api-key`, cabeçalho personalizado, **Microsoft Entra ID** (service principal: tenant, client ID, client secret e escopo), **OAuth 2.0 client credentials** e **service account do Google** (JSON; token JWT RS256). Chaves, tokens, client secrets e JSON de service account são criptografados com a `DOOMCTL_SECRET_KEY` (AES-256-GCM) e **nunca voltam ao navegador** nem entram na exportação. **Testar** faz uma chamada real mínima e lista modelos/agentes; o resultado fica na tabela. Em **Avançado**: caminhos, query string, `max_tokens` × `max_completion_tokens`, enviar ou não temperatura (modelos de raciocínio recusam), uso de tokens no streaming, tempo limite e cabeçalhos extras.

### 7.3 Agentes do Microsoft Foundry (Azure AI Foundry)

| Modo | Quando usar | Chamada |
|---|---|---|
| **Agente** (padrão) | Agentes novos do Foundry Agent Service (nome e versão) | `POST {endpoint}/openai/v1/responses` com `agent_reference` (`nome` ou `nome:versão`); a conversa continua no Foundry via `previous_response_id`. Se o recurso não tiver a API `v1`, o **Testar** troca para `api-version=2025-11-15-preview` automaticamente. |
| **Aplicação publicada** | Agente publicado como aplicação (`…/applications/<app>/protocols/openai`) | API Responses sem estado: o doomctl envia o histórico da conversa. |
| **Clássico** | Agentes `asst_…` (threads/runs) | `threads` → `messages` → `runs` (streaming), `api-version=2025-05-01`. A Microsoft recomenda migrar para os agentes novos. |

- Endpoint do projeto: `https://<recurso>.services.ai.azure.com/api/projects/<projeto>` (portal do Foundry → Visão geral do projeto).
- Autenticação: **Microsoft Entra ID** (escopo `https://ai.azure.com/.default`). Crie um *app registration* com client secret e dê a ele o papel **Azure AI User** no projeto (ou no recurso).
- A persona, o modelo, as ferramentas (inclusive MCP do lado do Foundry) e o conhecimento vêm do **agente**. Com **Enviar guardrails** ligado, o doomctl manda as regras, o estilo e as instruções da organização como mensagem *developer* no primeiro turno.
- O doomctl não executa funções do cliente (`function_call`/`requires_action`) nem aprova ferramentas MCP do agente: configure as ferramentas com `require_approval="never"` no Foundry.
- O classificador de escopo precisa de um modelo (Ollama ou API), não de um agente.

### 7.4 Ferramentas MCP (Model Context Protocol)

Servidores MCP remotos (transporte **Streamable HTTP**, JSON ou SSE, com `Mcp-Session-Id`) dão ferramentas às personas que usam Ollama ou API (*function calling*: Qwen 3, Llama 3.1+, GPT-4.1 etc.). Fluxo: cadastrar → **Atualizar** (initialize + `tools/list`) → liberar as ferramentas → marcar o servidor na persona em *Agentes de IA*.

| Modelo | URL | Autenticação |
|---|---|---|
| Google Stitch | `https://stitch.googleapis.com/mcp` | cabeçalho `X-Goog-Api-Key` (chave criada em Stitch → Settings → API Keys); alternativa: service account |
| Microsoft Learn (documentação) | `https://learn.microsoft.com/api/mcp` | nenhuma |
| AWS Knowledge (documentação) | `https://knowledge-mcp.global.api.aws` | nenhuma |
| GitHub | `https://api.githubcopilot.com/mcp/` | bearer (PAT com o mínimo de escopos) |
| Personalizado | qualquer servidor HTTP | todos os tipos da §7.2 |

**Política de ferramentas:** as marcadas como somente leitura (`readOnlyHint`) vêm liberadas; as que alteram ou apagam algo ficam **bloqueadas** até o administrador liberá-las. O teste manual de uma ferramenta de escrita pede confirmação. Cada chamada aparece como etiqueta na resposta do chat (`ferramenta: nome`) e no registro do Logs; o resultado repassado ao modelo é truncado pelo limite configurado. Servidores somente *stdio* precisam de um proxy HTTP (ex.: `mcp-proxy`); fluxos OAuth interativos não são suportados.

### 7.5 Aceleração por GPU

Há dois níveis, complementares:

1. **Container** (`install.sh --gpu=1|0`, §4.1): reserva ou não a GPU para o container do Ollama.
2. **Navegador** (Configurações → GPU & modelos): **Aceleração por GPU** ligada usa a GPU disponível (camadas automáticas ou número fixo de camadas, `num_gpu`); desligada envia `num_gpu=0` e tudo roda na CPU — vale para o Ollama padrão e para conexões Ollama. Também define `keep_alive` e `num_thread`. O Ollama recarrega o modelo sozinho quando a opção muda; **Recarregar** aplica na hora.

A aba mostra o modo de instalação, se o container tem GPU reservada, GPUs NVIDIA (`nvidia-smi`: VRAM e uso), modelos em memória com a divisão CPU/GPU (`/api/ps`) e permite baixar (`ollama pull`, acompanhado como execução), carregar, descarregar e remover modelos e **reiniciar o Ollama** (`docker restart`, requer o socket do Docker).

### 7.6 Privacidade e segurança dos provedores externos

- **Redação de segredos** (padrão: só para provedores externos): chaves AWS, tokens GitHub/GitLab/Slack, chaves Google/OpenAI, JWT, chaves privadas PEM, senhas em URLs e pares `senha=…` são mascarados antes do envio; o tipo de dado redigido fica no log.
- **Anexos** (arquivos dos botões *Revisar com IA*) podem ser bloqueados para provedores externos.
- **Termos bloqueados** recebem a mensagem de recusa sem chegar ao modelo.
- Um endereço é considerado *externo* quando não é localhost, rede privada (RFC 1918) ou nome sem domínio/`.local`/`.internal`/`.lan`.
- Cada pergunta registra em Logs a conexão, o modelo, tokens, ferramentas usadas e redações.

---

## 8. Primeiro acesso

1. Acesse `https://<ip-do-servidor>:8443`.
2. Usuário `admin` (ou `DOOMCTL_ADMIN_USER`). Senha: a definida em `DOOMCTL_ADMIN_PASSWORD` ou a temporária do log:
   ```bash
   docker compose logs doomctl | grep -A1 "administrador inicial"
   ```
3. A troca de senha é **obrigatória** no primeiro login.
4. Em **Meu perfil → Configurar MFA**, cadastre o TOTP e guarde os códigos de recuperação.
5. Em **Usuários**, crie os demais usuários (de preferência com **Exigir MFA** marcado).
6. Em **Configurações**, confira as ferramentas detectadas e o status do Ollama.

---

## 9. Usuários, perfis, permissões e MFA

### 9.1 Perfis

| Perfil | Padrão |
|---|---|
| **Administrador** | Acesso total e fixo (não editável), único que gerencia usuários, permissões, configurações e vê eventos de autenticação. |
| **Operador** | Ler + gerenciar (criar, editar, excluir, executar) em todas as ferramentas; Plug-ins somente leitura. |
| **Visualizador** | Apenas **relatórios (Visão Geral e FinOps) e Logs de execução**. Nunca gerencia nada. |

### 9.2 Matriz de permissões

Em **Usuários → Permissões por módulo**, o administrador ajusta, por módulo, *ler* e *gerenciar* para Operador e *ler* para Visualizador. Regras invioláveis aplicadas no servidor: gerenciar implica ler; Visualizador nunca gerencia; Usuários/Configurações são exclusivos do Admin. As mudanças valem imediatamente (cache recarregado).

Módulos: `dashboard`, `ansible`, `docker`, `kubernetes`, `opentofu`, `aws`, `finops`, `netcalc`, `trivy`, `falco`, `ai`, `logs`, `plugins` (+ `users`, `settings` exclusivos do Admin).

### 9.3 MFA (TOTP)

- Padrão RFC 6238: SHA1, 6 dígitos, 30 s — compatível com **Microsoft Authenticator, Google Authenticator**, Authy, FreeOTP, Aegis, 2FAS, Bitwarden etc. QR code gerado no servidor (não sai da rede).
- Tolerância de ±30 s; **anti-replay** (o mesmo código não é aceito duas vezes).
- 10 **códigos de recuperação** de uso único (armazenados como hash).
- Segredo TOTP guardado criptografado com `DOOMCTL_SECRET_KEY`.

| Quem | Pode |
|---|---|
| Administrador (sobre outros usuários) | **Exigir** (ativar a obrigatoriedade; sem cadastro, o próximo login força o cadastro), **deixar de exigir**, **resetar** (apaga o segredo e exige recadastro — celular perdido) e **desativar**. |
| Operador / Visualizador (sobre si) | Ativar e desativar o próprio MFA — **desativar só se não for exigido** pelo administrador. Gerar novos códigos de recuperação. |

### 9.4 Senhas, sessões e bloqueio

- Hash **PBKDF2-SHA256 (600.000 iterações)**; política: ≥ 10 caracteres e 3 dos 4 grupos (minúsculas, maiúsculas, números, símbolos).
- 5 senhas erradas → conta bloqueada por 15 min; limites de taxa por IP e por usuário.
- Sessão em cookie `HttpOnly`, `SameSite=Strict` (`Secure` com HTTPS); token CSRF por sessão em toda requisição de escrita; expiração absoluta (`DOOMCTL_SESSION_TTL`) e por inatividade (2 h).
- Troca de senha encerra as outras sessões; o usuário vê e encerra sessões em **Meu perfil**.

---

## 10. Utilização por módulo

### 10.1 Visão Geral

KPIs do período (diário/semanal/mensal): execuções (vs período anterior), sucesso, falhas, jobs ativos, taxa de sucesso (donut), atividade diária, uso por ferramenta e execuções recentes. Abas: **DevOps & Cloud** (hosts, playbooks, projetos OpenTofu, manifests), **Redes & Segurança** (CVEs críticas do último scan, cobertura de MFA) e **Assistente IA**. O sino no topo mostra execuções em andamento.

### 10.2 Docker

- **Dockerfile**: modelos (Go multi-stage → distroless, Node, Python, nginx sem privilégios, Debian), multi-stage, pacotes (apt/apk/dnf conforme a base), COPY/RUN/ENV/ARG, usuário não-root (UID 10001), HEALTHCHECK, ENTRYPOINT/CMD em formato exec; gera também o `.dockerignore`.
- **docker-compose**: serviços (imagem com prefixo de registry, build, portas, env, `env_file`, volumes, redes, `depends_on` com `service_healthy` automático, healthcheck, limites de CPU/memória, `read_only`, `no-new-privileges`, `cap_drop: ALL`), **redes** (bridge/macvlan/ipvlan/overlay, IPAM subnet/gateway, interface pai, `internal`, `external`) e **volumes** (driver e `driver_opts`, ex.: NFS). Sem a chave obsoleta `version`.
- **Arquivos salvos**: editar, baixar, **validar** compose (`docker compose config`) e analisar Dockerfile com **Trivy** (misconfig).
- **Registries**: cadastro (Docker Hub = endereço vazio, GHCR, registry privado com porta), senha/token criptografado, **login/logout** com `--password-stdin` (o Trivy reaproveita a autenticação para imagens privadas).

### 10.3 Kubernetes (beta)

- **Gerador** (23 modelos): Deployment, StatefulSet, Job, CronJob, Service, Ingress, Gateway API (Gateway + HTTPRoute), NetworkPolicy (default deny + liberação + DNS), Namespace (Pod Security Admission + ResourceQuota), ConfigMap, Secret, PVC, PV (NFS), ServiceAccount, RBAC (sem `*`), HPA (autoscaling/v2), PDB, **Helm Chart**, **Kustomize** (base + overlays dev/prod), **Observabilidade** (ServiceMonitor + PrometheusRule), **CI/CD** (GitHub Actions ou GitLab CI: build → Trivy → registry → deploy com rollback), **GitOps** (Argo CD Application ou Flux GitRepository + Kustomization), **runbook de rollout/rollback**.
  Todos os workloads saem com requests/limits, probes, `runAsNonRoot`, `readOnlyRootFilesystem`, `drop: [ALL]` e seccomp `RuntimeDefault`.
- **Validar YAML** (sintaxe + `apiVersion`/`kind`/`metadata.name`), baixar (`.tar.gz` para modelos com vários arquivos), salvar como manifest, revisar com IA.
- **Clusters**: cadastre um kubeconfig (criptografado). kubeconfigs com `exec`/`auth-provider` são recusados — use token ou certificado de uma ServiceAccount com RBAC mínimo.
  - **Console**: `get`, `describe`, `logs`, `top`, `events`, `explain`, `api-resources`, `version`, `cluster-info`, `auth can-i`, `rollout`, `scale`. Bloqueados: `exec`, `delete`, `cp`, `port-forward`, `-f`, `--kubeconfig`, `--token`, `--as` etc.
  - **Aplicar manifest salvo**: `diff`, `apply --dry-run=server` ou `apply` (sempre precedido de dry-run server).
  - **Scan Trivy** do cluster (`trivy k8s --report summary`).
- **Troubleshooting**: cole `describe`/`logs`/eventos e peça o diagnóstico ao Atlas.

### 10.4 OpenTofu

1. **Novo projeto** → escolha AWS ou OCI e os recursos:
   - **AWS**: VPC + subnet pública + IGW + route table + Security Group (SSH só do CIDR informado — `0.0.0.0/0` é recusado), EC2 com **IMDSv2 obrigatório** e disco gp3 criptografado (AMI informada ou data source da AMI oficial Debian 13), S3 com Block Public Access, SSE e versionamento. Provider `hashicorp/aws ~> 6.0`.
   - **OCI**: VCN + IGW + route table + security list + subnet, instância (shapes Flex com OCPUs/memória), Block Volume com anexo paravirtualizado. Provider `oracle/oci ~> 9.0`.
2. Edite os arquivos livremente (abas por arquivo, adicionar/remover).
3. **Credenciais** (criptografadas, injetadas só durante a execução e mascaradas na saída):
   - AWS: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` (opcional), `AWS_REGION`.
   - OCI: `TF_VAR_tenancy_ocid`, `TF_VAR_user_ocid`, `TF_VAR_fingerprint`, `TF_VAR_private_key` (PEM), `TF_VAR_compartment_ocid`, `TF_VAR_image_ocid`, `TF_VAR_ssh_public_key`.
   - Qualquer `AWS_*`, `OCI_*` ou `TF_VAR_*` adicional.
4. Fluxo seguro: `init` → `validate` → `plan` → **`apply`** (aplica **somente** o plano salvo, válido por 24 h e feito sobre os arquivos atuais). Para destruir: `plan -destroy` → **`destroy`** (exige digitar o nome do projeto). `fmt -check` e `output` também disponíveis; uma ação por projeto por vez.
5. O state fica local em `DATA_DIR/tofu/<id>/` (volume `doomctl-data`). Para produção configure backend remoto com lock e a criptografia de state do OpenTofu. Excluir um projeto com state exige confirmação.

### 10.5 Calculadora de sub-redes

Aceita `10.0.0.1/24`, `10.0.0.1 255.255.255.0`, `10.0.0.1/255.255.255.0` ou IPv6 (`2001:db8::/48`). Mostra rede, máscara, wildcard, broadcast, primeiro/último host, total, utilizáveis (com regras de /31 e /32), **utilizáveis na AWS (−5) e OCI (−3)**, classe, tipo (RFC 1918, CGNAT, link-local…), binário, hex, inteiro e zona reversa. **Dividir** por quantidade ou novo prefixo (até 4096 sub-redes) e **VLSM** (maior → menor, com alinhamento). Exporta CSV.

### 10.6 Máscaras & Wildcard

Tabela /0–/32 com máscara, wildcard, hex, endereços, hosts úteis e equivalência classful; filtro e exportação CSV.

### 10.7 Trivy

- **Scan de imagem**: imagens locais (via socket — sugestões automáticas) ou remotas; severidades, `--ignore-unfixed`, scanner de segredos, modo offline (`--skip-db-update`). Resultado com resumo por severidade, tabela filtrável de CVEs (pacote, versão instalada, correção), misconfigurações e segredos; relatório JSON baixável.
- **SBOM**: CycloneDX ou SPDX (JSON), baixável em Logs.
- **Scan de configuração (IaC)**: cole um Dockerfile, manifest ou `.tf`; itens salvos são analisados pelos botões nas próprias páginas.
- **Atualizar banco** de vulnerabilidades (trivy-db + java-db) — cache em `DATA_DIR/trivy-cache`.

### 10.8 Logs

Todas as ferramentas geram saída e registro, **separados por ferramenta**: filtro por módulo, status e texto, paginação. O detalhe mostra parâmetros, saída completa (com reconexão a execuções em andamento), resultado estruturado (Trivy renderizado) e artefatos (relatórios, SBOM). Administradores veem também eventos de autenticação e de usuários e podem remover registros antigos.

### 10.9 Plug-ins (Portainer Server CE)

Instala o `portainer/portainer-ce:lts` como aplicação à parte (porta `9443` HTTPS, opcional `8000` para Edge Agents) com volume `doomctl_portainer_data`. Ações: iniciar, parar, reiniciar, atualizar imagem e desinstalar (opção de apagar os dados com confirmação). **Crie o admin do Portainer em até 5 minutos** após instalar.

### 10.10 FinOps

Análise e otimização de custos de **AWS** e **OCI** (e de qualquer nuvem/fatura via CSV), em nove abas. Tudo é calculado no servidor doomctl, a partir dos dados gravados no PostgreSQL — nenhum serviço externo além das APIs dos provedores.

> Para conhecer o módulo sem conectar uma nuvem: **FinOps → Fontes → Carregar exemplo** (ou o botão na tela vazia). São ~120 dias de custos sintéticos de AWS e OCI, inventário com desperdícios, preços, orçamentos, políticas e cenários de exemplo — todos marcados com `[exemplo]` e removíveis com **Remover exemplo**.

#### 10.10.1 Fontes de dados

| Fonte | O que coleta | Como |
|---|---|---|
| **AWS** | custo diário por serviço × região (Cost Explorer), alocação pelas tags de projeto/ambiente/equipe, tipos de uso de rede e armazenamento (opcional), recomendações de rightsizing do Cost Explorer (opcional) | `ce:GetCostAndUsage`, `ce:GetRightsizingRecommendation` |
| **AWS — inventário** | instâncias EC2, volumes EBS, IPs elásticos, snapshots próprios e instâncias RDS das regiões configuradas; CPU média/máxima de 14 dias (CloudWatch, opcional) | `ec2:Describe*`, `rds:DescribeDBInstances`, `cloudwatch:GetMetricStatistics` |
| **OCI** | custo diário por serviço × região (Usage API, janelas de 31 dias), alocação por tags e SKUs de rede/armazenamento | `RequestSummarizedUsages` (assinatura de requisição da OCI) |
| **CSV** | custos (qualquer nuvem, fatura, on-premises) e inventário de recursos | importação pela interface ou API |

As integrações usam somente a biblioteca padrão do Go (SigV4 e assinatura da OCI implementadas e testadas com os vetores oficiais). As credenciais são criptografadas com AES-256-GCM e nunca voltam pela API (a interface mostra só uma dica, ex.: `AKIA…WXYZ`).

**Política IAM somente leitura (AWS):**

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "DoomctlFinOpsSomenteLeitura",
    "Effect": "Allow",
    "Action": [
      "ce:GetCostAndUsage", "ce:GetRightsizingRecommendation",
      "ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeAddresses", "ec2:DescribeSnapshots",
      "rds:DescribeDBInstances", "cloudwatch:GetMetricStatistics", "pricing:GetProducts"
    ],
    "Resource": "*"
  }]
}
```

- Cada requisição à API do Cost Explorer custa **US$ 0,01**; uma sincronização faz de 3 a 6 requisições (mais páginas em contas grandes). Com sincronização a cada 24 h, o custo fica em poucos dólares por mês.
- Ative as tags de alocação (ex.: `Project`, `Environment`) em **Billing → Cost allocation tags**; elas só aparecem no Cost Explorer a partir da ativação.
- Para as recomendações de rightsizing, habilite-as nas preferências do Cost Explorer.
- **Organizações**: com credenciais da conta pagadora, crie uma fonte por conta usando o campo *Conta vinculada* (filtro `LINKED_ACCOUNT`).

**OCI:** crie um usuário de API num grupo com a política

```
Allow group doomctl-finops to read usage-report in tenancy
```

e informe tenancy OCID, user OCID, fingerprint, **home region** e a chave privada RSA (PEM, sem passphrase). Tags definidas usam `Namespace.Chave`; tags livres, só a chave.

**Sincronização:** a primeira busca *Histórico na 1ª sincronização* (padrão 90 dias, máx. 395); as seguintes reprocessam os últimos 7 dias (os provedores ajustam custos recentes). O dia corrente não é importado por estar incompleto. O agendador interno sincroniza as fontes com *Sincronizar automaticamente* a cada `auto_sync_hours` (padrão 24 h) e roda como usuário `agendador` nos Logs. Ações manuais: **Sincronizar**, **Sincronizar período maior**, **Testar conexão**, **Atualizar inventário**.

**CSV de custos** (vírgula ou ponto e vírgula; decimais no formato brasileiro ou americano; datas `AAAA-MM-DD` ou `DD/MM/AAAA`):

```csv
date,provider,account,service,region,project,environment,team,cost,currency,usage_type
2026-09-01,aws,prod,Amazon EC2,us-east-1,portal,prod,Produto Web,123.45,USD,
```

Obrigatórias: `date`, `service`, `cost`. A importação substitui os dados da fonte no período coberto pelo arquivo.

**CSV de inventário:** `provider, region, resource_id, type, name, sku, size_gb, state, monthly_cost, currency, cpu_avg, cpu_max, mem_avg, attached, age_days, tags` (`type`: instance, volume, snapshot, public_ip, database, load_balancer, nat_gateway, bucket, other; `tags`: `k=v;k2=v2`).

**Endpoints alternativos** (LocalStack, proxies corporativos): somente administradores podem configurar, porque recebem as requisições assinadas.

#### 10.10.2 Custos (cost analysis, breakdown e alocação)

- Filtros: período (7/30/90/180/365 dias, mês atual, mês anterior), fonte, provedor, conta, serviço, região, projeto, ambiente e equipe.
- KPIs: custo do período × período anterior, média diária, mês atual até ontem e **previsão de fechamento**.
- Custo diário empilhado pelos 5 maiores serviços; quebras por serviço, provedor, conta, região, projeto, ambiente e equipe (clique para filtrar); maiores variações; cobertura de tags.
- **Moeda base** configurável; custos em outras moedas são convertidos pelas taxas de câmbio cadastradas (custos sem taxa são ignorados com aviso).
- **Alocação**: as APIs de custo agrupam no máximo duas dimensões, então projeto/ambiente/equipe vêm de consultas por tag (cada uma soma o total do dia). Por isso a alocação não pode ser cruzada com conta/região. Custo sem tag aparece como `(sem tag)`.
- **Equipes / centros de custo**: quando a fonte não traz a tag de equipe, regras `projeto|serviço → equipe` (glob, ex.: `portal*`, `*SageMaker*`) em **Fontes → Configurações** fazem a atribuição.
- **Analisar com IA** envia o resumo (sem credenciais) ao Atlas.

#### 10.10.3 Anomalias e alertas

Cada dia é comparado com a **mediana** e o **desvio absoluto mediano (MAD)** dos 14 dias anteriores (configurável). Um pico é anomalia quando atende aos três critérios: aumento ≥ 40%, aumento ≥ US$ 5/dia e z robusto ≥ 3. Também detecta **custo novo** (serviço/projeto que não existia). Escopos: total (com os serviços que mais contribuíram), serviço, região, conta, projeto e ambiente.

O agendador avalia a cada 10 minutos anomalias dos últimos 3 dias (severidade média/alta) e os orçamentos, e envia **cada alerta uma única vez** ao webhook (`{"text": ..., "content": ...}` — Slack, Microsoft Teams, Google Chat, Mattermost, Rocket.Chat, Discord). Botões **Avaliar agora** e **Testar webhook**; histórico com status de entrega.

#### 10.10.4 Orçamentos

Orçamentos mensais por: todo o custo, provedor, fonte, conta, serviço, região, projeto, ambiente ou equipe; limites de alerta em % (ex.: 80, 100) e alerta de **previsão acima do limite**. A barra mostra o gasto (até ontem), a previsão de fechamento (hachurado) e o acumulado do mês.

#### 10.10.5 Otimização

| Regra | Categoria | Economia estimada |
|---|---|---|
| Volume sem anexo | desperdício | custo do volume (snapshot antes de excluir) |
| IP público sem associação | desperdício | preço/hora × 730 |
| Snapshot mais antigo que N dias (90) | desperdício | limite superior (snapshots são incrementais) |
| Instância parada | desperdício | volumes anexados |
| Load balancer / NAT sem destinos (inventário CSV) | desperdício | custo do recurso |
| Instância ociosa (CPU média < 5%) | ociosos | custo da instância |
| Superdimensionada (CPU máx. < 40%, memória < 50%) | rightsizing | diferença para o tamanho menor da mesma família |
| Recomendações do AWS Cost Explorer | rightsizing | valor informado pela AWS |
| gp2 → gp3 | modernização | diferença de preço por GB |
| Geração anterior (t2, m4, c4, r4…; OCI Standard2/E2) | modernização | diferença de preço, quando cadastrado |
| Ambientes não produtivos 24×7 (dev, staging, qa…) | agendamento | custo × (1 − horas ligadas/168) |
| Workloads tolerantes a interrupção (worker, batch, CI…) | Spot | premissa de desconto (50%) |
| RDS superdimensionado, Multi-AZ fora de produção, storage gp2, banco parado | bancos de dados | diferença de classe / 50% |
| Tags obrigatórias ausentes | tags | — |
| NAT Gateway, tráfego entre AZs/regiões, saída para internet | rede | recomendações (depende do tráfego) |
| Objetos só na classe padrão, snapshots, IOPS provisionados | armazenamento | estimativa de lifecycle |

Sub-abas: **Recomendações** (filtros, detalhe, descartar/restaurar com motivo, comandos sugeridos), **Savings Plans & reservas** (base estável P10 dos últimos 30 dias por grupo: Compute Savings Plans, Reservas de RDS/ElastiCache/OpenSearch/Redshift, compromisso anual OCI; economia de 1 e 3 anos com **premissas configuráveis** de desconto — confirme nas recomendações do Cost Explorer/proposta da Oracle), **Rede & armazenamento**, **Inventário** e **Violações de políticas**.

**Remediação automatizada:**

1. **Script**: selecione recomendações e gere um script `bash` com os comandos (AWS CLI, OCI CLI, kubectl). Comandos destrutivos vêm com `--dry-run`.
2. **Execução pela API (AWS)**: parar instância, migrar volume para gp3, liberar IP, excluir snapshot, excluir volume (sempre cria um snapshot de segurança antes). Exige: fonte com **Permitir remediação automatizada** (só administrador habilita), credenciais com as permissões abaixo, **Simular (dry-run)** — a AWS confirma com `DryRunOperation` — e, para executar, digitar o ID do recurso. Tudo fica nos Logs.

```json
{ "Sid": "DoomctlFinOpsRemediacao", "Effect": "Allow",
  "Action": ["ec2:StopInstances", "ec2:ModifyVolume", "ec2:CreateSnapshot", "ec2:DeleteSnapshot", "ec2:DeleteVolume", "ec2:ReleaseAddress"],
  "Resource": "*" }
```

3. **Código (IaC)**: correções aplicadas direto no projeto OpenTofu (ver §10.10.8).

#### 10.10.6 Kubernetes

Custo por **namespace**, **aplicação** (`app.kubernetes.io/name`/`app`), **workload** (Deployment, StatefulSet, DaemonSet, CronJob, Job, Pod avulso) e **nó**, com capacidade ociosa. O custo de cada nó é dividido entre CPU e memória com peso 7,5 : 1 (proporção dos preços padrão do OpenCost) e cada pod paga `max(request, uso)`.

- Fonte dos dados: **cluster cadastrado** no módulo Kubernetes (coleta somente leitura: `get pods/nodes/hpa` e `top pods`; exige *gerenciar Kubernetes*) ou **colar** as saídas do `kubectl`.
- Preço dos nós: `instance:<tipo>` do catálogo (pela região do nó), `k8s:vcpu` + `k8s:memory_gb`, ou o **custo mensal informado** do cluster.
- Recomendações: pods sem requests, requests de CPU/memória superdimensionados (precisa do metrics-server), réplicas em namespaces não produtivos, réplicas fixas sem HPA, capacidade ociosa ≥ 40% e nós sem Spot.

#### 10.10.7 Previsão, what-if e cenários

- **Previsão**: regressão linear sobre até 56 dias + sazonalidade semanal (com ≥ 28 dias), faixa de ~80%, fechamento do mês, próximo mês e run-rate de 7 dias. Aceita os mesmos filtros da visão de custos.
- **What-if do gasto atual**: variações em % por serviço (e global) sobre os últimos 30 dias.
- **Cenários** A × B com itens do catálogo (SKU, quantidade, horas/mês ou GB): "3 → 10 nós", troca de tipos, **AWS × OCI**, e a mesma configuração **em várias regiões**. Viewers podem simular; salvar exige gerenciar.

#### 10.10.8 IaC, CI/CD e políticas

**Estimativa antes do apply**: leitura estática do código OpenTofu/Terraform (sem `plan` e sem credenciais) de um projeto salvo no módulo OpenTofu ou colado. Entende `variable` (default, `.tfvars` e sobrescritas na tela), `locals`, `count`/`for_each` literais, interpolação simples, `merge()`, condicionais com variáveis, blocos aninhados e `default_tags`/`freeform_tags`.

Recursos com modelo de custo: `aws_instance` (+ discos), `aws_ebs_volume`, `aws_eip`, `aws_nat_gateway`, `aws_lb`/`aws_alb`, `aws_db_instance`, `aws_elasticache_cluster`, `aws_eks_cluster`, `aws_eks_node_group`, `oci_core_instance` (Flex: OCPU + memória; boot volume), `oci_core_volume` (GB + VPU), `oci_load_balancer`, `oci_core_public_ip`. Recursos sem custo fixo (VPC, subnets, SGs, IAM…) e cobrados por uso (S3, Lambda, DynamoDB…) são identificados; os demais aparecem como *sem modelo*.

**Otimizações aplicáveis ao código** (`"gp2"` → `"gp3"`, tipos de geração anterior → atuais, inclusive no default da variável): com *gerenciar FinOps + OpenTofu*, **Aplicar ao projeto** altera as linhas no projeto salvo (o plano atual deixa de valer; rode `plan` antes do `apply`).

**Verificação de custo no CI/CD**: compara a versão atual (base) com a proposta e **bloqueia** quando o aumento mensal passa dos dois limites configurados (padrão +10% **e** +US$ 50/mês; para projetos novos, só o valor absoluto) ou quando uma política com ação *bloquear* é violada. Gera o comentário em Markdown para o pull/merge request. No pipeline use o subcomando offline do binário:

```bash
# 1) FinOps → IaC → Verificação CI/CD → Exportar catálogo (preços, políticas e limites)
#    versione o arquivo em .finops/doomctl-finops-catalog.json
# 2) no job do PR/MR (exit 0 = aprovado, 1 = bloqueado, 2 = erro):
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/w" -w /w doomctl:0.1.0 cost-check \
  --catalog .finops/doomctl-finops-catalog.json --base base/infra --head infra --markdown cost.md
# opções: --max-pct 10 --max-abs 50 --var nome=valor --region us-east-1 --env prod --json
```

A aba mostra exemplos completos para GitHub Actions (com comentário no PR via `gh pr comment`) e GitLab CI. Exija o job na proteção da branch para que o bloqueio impeça o merge.

**Políticas de custo** (avaliadas no inventário, na estimativa de IaC e no cost-check), aplicadas por ambiente (tag `Environment`, glob, ex.: `prod*`):

| Tipo | Parâmetro | Exemplo |
|---|---|---|
| Custo mensal máximo por recurso | valor | produção até US$ 500/mês por recurso |
| Tipos de instância proibidos | padrões glob | dev sem `*.metal`, `p*`, `*.8xlarge` |
| Regiões permitidas | lista | somente `sa-east-1`, `us-east-1` |
| Tags obrigatórias | lista | `Project`, `Environment`, `Owner` |
| Tamanho máximo de volume | GB | até 1000 GB |

Ação **avisar** só sinaliza; **bloquear** reprova o cost-check.

#### 10.10.9 Relatórios (showback e chargeback)

Por mês (ou período personalizado), agrupado por serviço, provedor, conta, região, projeto, ambiente ou equipe:

- **Showback**: custo, participação, período anterior e variação.
- **Chargeback**: custo direto + **rateio** do custo sem alocação (proporcional ao custo direto) = total a cobrar.

O relatório inclui resumo, previsão do mês, maiores aumentos, orçamentos, anomalias do período e principais oportunidades de economia. Exporta **Markdown** e **CSV** (`;`, decimais com vírgula, UTF-8 com BOM para o Excel); **Resumo executivo com IA** gera um texto para a diretoria.

#### 10.10.10 Catálogo de preços e configurações

O catálogo alimenta inventário, IaC, Kubernetes e cenários (os custos de faturamento vêm das APIs e não dependem dele). Unidades: `hour`, `gb-month`, `month`, `gb-hour`, `unit`; região `*` vale para qualquer região. Origens: manual, CSV, **AWS Price List** (botão *Buscar na AWS*, preços públicos Linux sob demanda via `pricing:GetProducts`) e exemplo.

| SKU | Unidade | Uso |
|---|---|---|
| `instance:<tipo>` | hour | EC2, shapes fixos OCI, nós Kubernetes |
| `volume:<tipo>` | gb-month | EBS (`gp3`, `gp2`, `io1`…) |
| `snapshot`, `public_ip`, `nat_gateway`, `eks_cluster` | gb-month / hour | |
| `load_balancer:<application\|network>` | hour | |
| `db:<classe>`, `db-storage:<tipo>` | hour / gb-month | RDS |
| `cache:<tipo>` | hour | ElastiCache |
| `ocpu:<shape>`, `memory:<shape>` | hour | OCI Flex (por OCPU e por GB) |
| `volume:storage`, `volume:vpu` | gb-month | OCI Block Volume (GB e VPU/GB) |
| `k8s:vcpu`, `k8s:memory_gb` (provedor `k8s`) | hour | clusters sem preço por nó |

**Configurações**: moeda base e câmbio, tags obrigatórias, webhook, sincronização automática, sensibilidade das anomalias, limites de ocioso/rightsizing/snapshots, ambientes e horas do agendamento, premissas de desconto (1 ano 20%, 3 anos 40%, Spot 50% — premissas, não preços oficiais), regras de equipe e limites do CI/CD.

**Permissões**: Operador lê e gerencia; **Visualizador lê** (relatórios de custo), mas não vê a URL do webhook nem altera nada. Endpoints alternativos e a remediação automatizada são exclusivos do administrador.

### 10.11 Em breve

Ansible (interface web; a API já funciona — §11), AWS EC2 & S3 via IAM e Falco (agente remoto via WebSocket) aparecem no menu com o selo **em breve**.

---

## 11. Ansible via API

Nesta versão o módulo Ansible **não tem interface web**; todas as funções estão disponíveis na API (mesmas permissões: `ansible` ler/gerenciar). O Ansible e seus utilitários (`ansible-vault`, `ansible-playbook`, `ansible-lint`, `ansible-inventory`, `ansible-doc`) estão instalados no container.

### 11.1 Autenticação na API (curl)

```bash
H=https://doomctl.local:8443
J=/tmp/doomctl.cookies

# login (com MFA: em seguida POST /api/auth/mfa {"code":"123456"} com o CSRF recebido)
CSRF=$(curl -sk -c $J -H 'Content-Type: application/json' \
  -d '{"username":"maria.ops","password":"********"}' $H/api/auth/login | jq -r .csrf)

api() { curl -sk -b $J -c $J -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' "$@"; }
```

Escritas exigem o cabeçalho `X-CSRF-Token`. O `GET /api/auth/me` também devolve o token atual.

### 11.2 Inventário

```bash
# um host
api -d '{"name":"web01","address":"192.168.0.10","port":22,"groups":["web","prod"],"vars":{"ansible_user":"deploy"}}' $H/api/ansible/hosts

# importar CSV (cabeçalho: name,address,port,groups,vars — grupos com ";" e vars "k=v;k2=v2"; aceita "," ou ";")
jq -Rs '{csv: ., replace: false}' hosts.csv | api -d @- $H/api/ansible/hosts/import

# exportar
api $H/api/ansible/inventory               # INI
api "$H/api/ansible/inventory?format=json"  # YAML/JSON do Ansible
```

`ansible_connection` só aceita conexões remotas (ssh, paramiko, winrm, psrp, network_cli, netconf, httpapi).

### 11.3 Vaults (ansible-vault)

O conteúdo é cifrado pelo próprio `ansible-vault` (formato `$ANSIBLE_VAULT;1.1;AES256`); o doomctl **não guarda a senha do vault**. A senha é exigida para **criar, editar e visualizar**; a exclusão pede só confirmação.

```bash
api -d '{
  "name": "prod", "description": "credenciais de produção", "password": "senha-do-vault",
  "data": { "ansible_user": "deploy", "ansible_password": "********", "ansible_become_password": "********",
            "ssh_private_key": "", "extra": [ {"key":"db_password","value":"********"} ] }
}' $H/api/ansible/vaults

api -d '{"password":"senha-do-vault"}' $H/api/ansible/vaults/1/view                     # visualizar
api -X PUT -d '{"password":"senha-do-vault","new_password":"","data":{...}}' $H/api/ansible/vaults/1   # editar
api -X DELETE $H/api/ansible/vaults/1                                                  # excluir
```

`ansible_user`/`ansible_password` (o Ansible usa `ansible_user`, não `ansible_username`) e as variáveis extras viram variáveis da execução (`-e @vault.yml --vault-password-file`). A chave SSH opcional é gravada num arquivo temporário 0600 e passada como `--private-key`.

### 11.4 Playbooks

```bash
api $H/api/ansible/task-types                    # tipos de tarefa do gerador (módulos ansible.builtin)
api -d '{"name":"Base","hosts":"web","become":true,"gather_facts":true,
  "tasks":[{"type":"apt","params":{"name":"nginx","update_cache":"true"}},
           {"type":"service","params":{"name":"nginx","state":"started","enabled":"true"}}]}' \
  $H/api/ansible/generate/playbook               # → {"content": "..."}

api -d "$(jq -n --rawfile c site.yml '{name:"site", content:$c}')" $H/api/ansible/playbooks
api -d '{"lint":true}' $H/api/ansible/playbooks/1/check        # --syntax-check (+ ansible-lint)
api -d '{"vault_id":1,"vault_password":"senha-do-vault","check":true,"diff":true,"limit":"web01","tags":"","verbosity":0}' \
  $H/api/ansible/playbooks/1/run                               # → {"job_id": "...", "log_id": 42}
```

Recomendação: rode primeiro com `check` + `diff` (`--check --diff`).

### 11.5 Roles

```bash
api "$H/api/ansible/roles/skeleton?name=nginx"   # tasks, handlers, meta, defaults, vars
api -d '{"name":"nginx","description":"...","tasks":"---\n...","handlers":"...","meta":"...","defaults":"...","vars":""}' $H/api/ansible/roles
curl -sk -b $J -OJ $H/api/ansible/roles/1/download   # .tar.gz
```

As roles salvas ficam disponíveis no `roles_path` dos playbooks.

### 11.6 Comandos ad-hoc

`POST /api/ansible/adhoc` com `{"command": "...", "vault_id": 0, "vault_password": ""}`. Aceita `ansible <padrão> -m <módulo> -a <args>` com um conjunto restrito de opções, além de `ansible-inventory --graph|--list` e `ansible-doc <módulo>`. Execução no próprio servidor, templates Jinja/lookups, `-e @arquivo` e módulos que leem ou gravam arquivos do servidor (copy, template, fetch, script, unarchive) são recusados — use playbooks para esses casos.

### 11.7 Acompanhar execuções

```bash
curl -sk -N -b $J $H/api/jobs/<job_id>/stream     # SSE: eventos meta, chunk e end
api $H/api/logs/42                                # saída completa e status
api -d '{}' $H/api/jobs/<job_id>/cancel
```

---

## 12. Referência da API

Base: `https://<host>:8443`. Respostas JSON; erros como `{"error": "mensagem"}`. Permissão: **R** = ler, **M** = gerenciar, **Admin**, **Sessão** = qualquer usuário autenticado.

| Método | Rota | Permissão | Descrição |
|---|---|---|---|
| POST | `/api/auth/login` | público | `{username,password}` → `{status: ok\|mfa\|mfa_enroll, csrf, must_change_password}` |
| POST | `/api/auth/mfa` | pré-sessão | `{code}` (TOTP ou recuperação) |
| GET | `/api/auth/mfa/setup` | pré-sessão/sessão | segredo, URI `otpauth://` e QR (data URL) |
| POST | `/api/auth/mfa/enable` | pré-sessão/sessão | `{code}` → códigos de recuperação |
| POST | `/api/auth/logout` | sessão | encerra a sessão |
| GET | `/api/auth/me` | — | usuário, CSRF, permissões, módulos |
| POST | `/api/auth/password` | Sessão | `{current,new}` |
| GET | `/api/health` | público | saúde (banco) |
| PUT | `/api/me/profile` | Sessão | nome e e-mail |
| POST | `/api/me/mfa/disable` · `/api/me/mfa/recovery` | Sessão | `{password,code}` |
| GET / POST | `/api/me/sessions` · `/api/me/sessions/revoke-others` | Sessão | sessões |
| GET/POST/PUT/DELETE | `/api/users[/{id}]` | Admin | CRUD de usuários |
| POST | `/api/users/{id}/password` · `/mfa` · `/revoke-sessions` | Admin | senha temporária; MFA `{action: require\|release\|reset\|disable}`; sessões |
| GET / PUT | `/api/permissions` | Sessão / Admin | matriz de permissões |
| GET | `/api/system` | Admin | versões, ferramentas, Ollama, configuração |
| GET | `/api/dashboard?period=day\|week\|month` | R dashboard | KPIs |
| GET | `/api/logs` (`module,status,q,limit,offset`) · `/api/logs/{id}` · `/api/logs/{id}/artifact` | R logs | logs e artefatos |
| POST | `/api/logs/purge` | Admin | `{days}` |
| GET | `/api/jobs` · `/api/jobs/{id}/stream` | Sessão | execuções ativas; SSE |
| POST | `/api/jobs/{id}/cancel` | dono ou M | cancela |
| — | `/api/ansible/...` | R/M ansible | ver §11 |
| GET | `/api/docker/presets` | R docker | modelos de Dockerfile |
| POST | `/api/docker/generate/dockerfile` · `/compose` | R docker | geradores |
| GET/POST/PUT/DELETE | `/api/docker/files[/{id}]` | R/M docker | arquivos salvos |
| POST | `/api/docker/files/{id}/validate` | M docker | compose config / trivy config |
| GET/POST/DELETE | `/api/docker/registries[/{id}]` | R/M docker | registries |
| POST | `/api/docker/registries/{id}/login` · `/logout` | M docker | docker login/logout |
| POST | `/api/tofu/generate` | R opentofu | gerador AWS/OCI |
| GET/POST/PUT/DELETE | `/api/tofu/projects[/{id}]` | R/M opentofu | projetos |
| GET | `/api/tofu/projects/{id}/download` | R opentofu | `.tar.gz` |
| PUT | `/api/tofu/projects/{id}/credentials` | M opentofu | `{env:{K:V}, merge}` |
| POST | `/api/tofu/projects/{id}/action/{init\|validate\|fmt\|output\|plan\|plan-destroy\|apply\|destroy}` | M opentofu | `destroy` exige `{confirm: "<nome>"}` |
| GET | `/api/k8s/templates` | R kubernetes | catálogo |
| POST | `/api/k8s/render` · `/api/k8s/bundle` · `/api/k8s/validate` | R kubernetes | gerar, `.tar.gz`, validar |
| GET/POST/PUT/DELETE | `/api/k8s/manifests[/{id}]` | R/M kubernetes | manifests |
| GET/POST/DELETE | `/api/k8s/clusters[/{id}]` | R/M kubernetes | kubeconfigs |
| POST | `/api/k8s/clusters/{id}/kubectl` · `/apply` · `/scan` | M kubernetes | console, aplicar, trivy k8s |
| POST | `/api/net/calc` · `/api/net/split` · `/api/net/vlsm` | R netcalc | cálculos |
| GET | `/api/net/masks` | R netcalc | tabela |
| GET | `/api/trivy/images` | R trivy | imagens locais |
| POST | `/api/trivy/scan` · `/api/trivy/db-update` | M trivy | `{kind: image\|sbom\|config, ...}` |
| GET | `/api/finops/overview` (`days` ou `start,end`; `source_id,provider,account,service,region,project,environment,team`) | R finops | KPIs, série diária e quebras |
| GET | `/api/finops/dimensions` · `/anomalies?days=` · `/forecast` · `/budgets` · `/usage` · `/commitments` | R finops | filtros, anomalias + alertas, previsão, orçamentos, rede/armazenamento, compromissos |
| GET | `/api/finops/findings` · `/resources` | R finops | recomendações (economia, violações) e inventário |
| POST | `/api/finops/report` | R finops | `{month\|start,end, group_by, mode: showback\|chargeback, distribute}` → JSON com `markdown` e `csv` |
| POST | `/api/finops/k8s/analyze` | R finops (+ M kubernetes com `cluster_id`) | `{cluster_id}` ou `{pods,nodes,top,hpas,cluster_monthly}` ou `{demo:true}` |
| POST | `/api/finops/iac/estimate` · `/iac/check` | R finops (+ R opentofu com `project_id`) | `{project_id\|files, vars, region, env}`; check: `{base_project_id\|base_files, files}` |
| POST | `/api/finops/iac/fix` | M finops + M opentofu | `{project_id, fixes:[id]}` |
| POST | `/api/finops/scenarios/price` · `/whatif` · `/remediation/script` | R finops | cenário ad-hoc `{a,b,regions}`; what-if `{global, adjustments}`; script `{keys}` |
| GET/POST/PUT/DELETE | `/api/finops/budgets[/{id}]` · `/policies[/{id}]` · `/scenarios[/{id}]` | R/M finops | CRUD |
| GET/PUT | `/api/finops/settings` | R/M finops | configurações |
| GET/POST/PUT/DELETE | `/api/finops/sources[/{id}]` | R/M finops | fontes (endpoints e remediação: Admin) |
| PUT | `/api/finops/sources/{id}/credentials` | M finops | AWS `{access_key_id, secret_access_key, session_token}` · OCI `{tenancy_ocid, user_ocid, fingerprint, region, private_key}` |
| POST | `/api/finops/sources/{id}/test` · `/sync` (`{days}`) · `/scan` | M finops | teste síncrono; sincronização e inventário como jobs (SSE) |
| POST | `/api/finops/sources/{id}/import-costs` · `/import-resources` | M finops | `{csv, replace}` (até 48 MiB) |
| POST / DELETE | `/api/finops/demo` | M finops | carrega/remove dados de exemplo |
| GET | `/api/finops/prices` · `/prices/export` | R finops | catálogo; exportação para o `cost-check` |
| POST / DELETE | `/api/finops/prices[/{id}]` · `/prices/import` · `/prices/aws-lookup` | M finops | preço manual, CSV, AWS Price List `{source_id, region, kind: ec2\|ebs, values}` |
| POST | `/api/finops/findings/dismiss` | M finops | `{key, reason, restore}` |
| POST | `/api/finops/remediation/execute` | M finops | `{key, dry_run, confirm: "<id do recurso>"}` → job |
| POST | `/api/finops/alerts/test` · `/alerts/evaluate` | M finops | webhook de teste; avaliação imediata |
| GET | `/api/plugins` | R plugins | status |
| POST | `/api/plugins/portainer/{install\|start\|stop\|restart\|update\|uninstall}` | M plugins | ações |
| GET | `/api/ai/status` | R ai | Ollama/modelo, personas em uso, temperatura, recusa |
| POST | `/api/ai/chat` | M ai | `{message, context:{module,filename,content}}` → NDJSON (`meta`, `chunk`, `refusal`, `tool`, `notice`, `error`, `done`) |
| POST | `/api/ai/reset` | R ai | limpa o histórico (e as conversas nos agentes do Foundry) |
| GET / PUT | `/api/ai/config` | Admin | configuração dos agentes (`{settings, note}` no PUT; aplica na hora) |
| POST | `/api/ai/config/reset` | Admin | volta ao padrão |
| GET | `/api/ai/config/history` | Admin | versões |
| POST | `/api/ai/config/history/{id}/restore` | Admin | restaura uma versão |
| GET / POST | `/api/ai/config/export` · `/api/ai/config/import` | Admin | JSON sem segredos |
| POST | `/api/ai/config/preview` | Admin | prompt final das personas para `{settings}` |
| POST | `/api/ai/playground` | Admin | `{settings, route, message}` → NDJSON (+ `stats`); não salva |
| GET / POST | `/api/ai/connections` | Admin | lista / cria (`{name, kind, preset, config, secret, enabled}`) |
| PUT / DELETE | `/api/ai/connections/{id}` | Admin | edita (sem `secret` = mantém) / exclui (bloqueado se em uso) |
| POST | `/api/ai/connections/{id}/test` | Admin | chamada real mínima + modelos |
| GET | `/api/ai/connections/{id}/models` | Admin | modelos ou agentes (`0` = Ollama padrão) |
| GET / POST | `/api/ai/mcp` | Admin | servidores MCP |
| PUT / DELETE | `/api/ai/mcp/{id}` | Admin | edita / exclui |
| POST | `/api/ai/mcp/{id}/refresh` | Admin | conecta e lista ferramentas |
| PUT | `/api/ai/mcp/{id}/tools` | Admin | `{tools_enabled: {nome: bool}}` |
| POST | `/api/ai/mcp/{id}/call` | Admin | `{tool, args, confirm}` (teste manual) |
| GET | `/api/ai/ollama?conn=0` | Admin | modelos, em memória (CPU/GPU), versão, container e GPUs |
| POST | `/api/ai/ollama/pull` · `/delete` · `/load` | Admin | `{conn, model}` (`pull` vira execução; `load` com `action: load\|unload`) |
| POST | `/api/ai/ollama/restart` | Admin | reinicia o container do Ollama (execução) |
| GET | `/api/ai/usage?days=30` | Admin | uso por persona, conexão, usuário e dia |

---

## 13. Segurança

**Implementado**

- Senhas PBKDF2-SHA256 (600k iterações), comparação em tempo constante, *dummy hash* contra enumeração de usuários, bloqueio por tentativas e rate limit.
- MFA TOTP com anti-replay e códigos de recuperação com hash; exigência controlada pelo administrador.
- Sessões com token aleatório de 256 bits (somente o SHA-256 vai ao banco), `HttpOnly` + `SameSite=Strict` + CSRF + checagem de `Origin`.
- Cabeçalhos: CSP restritiva (`script-src 'self'`), `X-Frame-Options: DENY`, `nosniff`, HSTS (HTTPS), `Referrer-Policy`.
- Segredos em repouso com AES-256-GCM (TOTP, registries, credenciais de nuvem e do FinOps, kubeconfigs); vaults no formato nativo do `ansible-vault`.
- Execuções **sem shell** (argv), timeout, cancelamento do grupo de processos, limite de concorrência, saída mascarada para senhas e tokens conhecidos.
- Processos filhos recebem um ambiente **filtrado** (sem `DOOMCTL_SECRET_KEY`, URL do banco etc.); o processo do doomctl é marcado como *não dumpable* (filhos não leem `/proc/<pid>/environ`).
- Container sem root (UID 10001), `cap_drop: ALL`, `no-new-privileges`; PostgreSQL numa rede interna.
- Guardrails: SSH `0.0.0.0/0` recusado no gerador OpenTofu; RBAC com `*` recusado; apply/destroy só com plano salvo; consoles com listas de permissão.
- Downloads de ferramentas verificados por SHA-256 no build.

**Riscos que você precisa conhecer**

1. **Gerenciar Ansible, OpenTofu ou Kubernetes equivale a executar código no servidor doomctl.** Playbooks, providers e módulos rodam no controlador por natureza (assim como no AWX/Rundeck). Conceda *gerenciar* apenas a pessoas de confiança.
2. **Socket do Docker = root no host.** Ele é montado para login em registries, imagens locais no Trivy e o Portainer. Se não precisar, remova a linha do `docker-compose.yml`.
3. A chave privada TLS autoassinada fica no volume de dados; em produção prefira certificado da CA interna ou TLS no proxy reverso.
4. A senha inicial gerada aparece **uma vez** no log do container — troque-a (o sistema obriga) e, se necessário, limpe o log.
5. **FinOps**: use credenciais de nuvem somente leitura; a remediação automatizada exige credenciais com escrita e deve ser habilitada apenas em fontes que realmente precisem. O webhook de alertas recebe o texto dos alertas (nomes de serviços/projetos e valores) — use um canal restrito.
6. `DOOMCTL_SECRET_KEY` é a raiz da confidencialidade: guarde-a fora do servidor. Quem tem o `.env` + backup do banco lê os segredos.

---

## 14. Operação: backup, atualização e troubleshooting

### 14.1 Backup

```bash
# banco
docker compose exec -T postgres pg_dump -U doomctl -Fc doomctl > doomctl-$(date +%F).dump
# dados (state OpenTofu, roles, relatórios, TLS)
docker run --rm -v doomctl_doomctl-data:/d -v "$PWD":/b debian:trixie-slim tar czf /b/doomctl-data-$(date +%F).tgz -C /d .
# e o .env (DOOMCTL_SECRET_KEY!) em local seguro
```

Restauração:

```bash
docker compose up -d postgres
docker compose exec -T postgres pg_restore -U doomctl -d doomctl --clean --if-exists < doomctl-AAAA-MM-DD.dump
docker run --rm -v doomctl_doomctl-data:/d -v "$PWD":/b debian:trixie-slim tar xzf /b/doomctl-data-AAAA-MM-DD.tgz -C /d
docker compose up -d
```

### 14.2 Atualização

```bash
git pull
docker compose build --pull
docker compose up -d
```

As migrations do banco rodam automaticamente na inicialização. Execuções em andamento durante o restart são marcadas como **interrompidas**.

### 14.3 Troubleshooting

| Sintoma | Causa / solução |
|---|---|
| `postgres` reinicia com erro de diretório de dados | No PG 18 o volume deve ser montado em `/var/lib/postgresql` (não em `.../data`). Ao migrar de volumes do PG 17, use `pg_upgrade`/dump-restore. |
| Docker/Trivy/Portainer: "permission denied ... docker.sock" | `DOCKER_GID` não confere: `stat -c %g /var/run/docker.sock` e ajuste o `.env`. |
| Assistente: "Ollama offline" | Verifique `OLLAMA_HOST` (de dentro do container), firewall e se o Ollama escuta no IP correto. |
| Assistente: "modelo não encontrado" | `ollama pull <OLLAMA_MODEL>`; confira a tag exata com `ollama list`. |
| Servidor não sobe: "temperatura fora da faixa" | `ROUTER_TEMPERATURE` deve estar entre 0.2 e 0.3. |
| `tofu init` falha ao baixar providers | Sem acesso a `registry.opentofu.org`: libere a saída ou configure um mirror (`.tofurc` com `provider_installation`). |
| Trivy "Falling back to embedded checks" | Sem acesso ao bundle de checks; o scan continua com as regras embutidas. |
| Trivy sem banco em rede isolada | Copie o cache (`DATA_DIR/trivy-cache`) de uma máquina conectada e use "modo offline". |
| FinOps: `AccessDeniedException` / `UnauthorizedOperation` | Falta permissão na política IAM da fonte (veja §10.10.1). O erro aparece em Fontes e no log da sincronização. |
| FinOps: projeto/ambiente sempre `(sem tag)` | Tags de alocação não ativadas em Billing → Cost allocation tags (AWS) ou chave diferente da configurada na fonte (respeita maiúsculas). |
| FinOps: rightsizing do Cost Explorer vazio | Habilite as recomendações de rightsizing nas preferências do Cost Explorer (levam até 24 h). |
| FinOps (OCI): `NotAuthenticated` | Confira fingerprint, OCIDs, home region e se a chave privada corresponde à chave pública cadastrada no usuário; chaves com passphrase não são aceitas. |
| FinOps: custos ignorados por moeda | Cadastre a taxa de câmbio da moeda em Fontes → Configurações. |
| IA: persona voltou para o "Ollama padrão" | A conexão escolhida está desativada, foi excluída ou o segredo não pôde ser lido (troca da `DOOMCTL_SECRET_KEY`); veja o aviso em Configurações → Agentes de IA. |
| IA: `HTTP 401/403` num agente do Foundry | O service principal precisa do papel **Azure AI User** no projeto; confira tenant, client ID e secret. |
| IA: API recusa `temperature` ou `max_tokens` | Na conexão → Avançado, desmarque *Enviar temperatura* ou troque para `max_completion_tokens`. |
| IA: ferramenta MCP não é usada | Confirme que a ferramenta está liberada, o servidor marcado na persona e que o modelo suporta *function calling*. |
| IA: GPU não aparece | `./install.sh --with-ai --gpu=1`, depois `docker compose exec ollama nvidia-smi -L`; em Configurações → GPU & modelos, a coluna Processador mostra CPU/GPU. |
| FinOps: inventário/IaC com "sem preço" | Cadastre os SKUs no catálogo (ou *Buscar na AWS*). |
| Login bloqueado | Aguarde 15 min ou peça ao administrador para **redefinir a senha** (desbloqueia). |
| Perdi o celular do MFA | Use um código de recuperação ou peça ao administrador o **reset** do MFA. |
| Perdi o acesso de administrador | `docker compose exec postgres psql -U doomctl -c "UPDATE users SET locked_until=NULL, failed_logins=0 WHERE username='admin'"`; para recriar, apague o usuário e reinicie com `DOOMCTL_ADMIN_PASSWORD` (só é recriado se não houver usuários). |

### 14.4 Ambiente sem internet

O frontend e o build Go não precisam de internet (sem CDN, dependências em `vendor/`). Construa a imagem numa máquina conectada e transfira com `docker save`/`docker load`. Para uso: espelhe registries de imagens, configure mirror de providers do OpenTofu e leve o cache do Trivy.

---

## 15. Desenvolvimento

```
cmd/doomctl/            servidor (main, TLS autoassinado)
cmd/devops-router/      CLI original do router de IA
internal/agents/        router, prompts, cliente Ollama, AGENTS.md (+ testes)
internal/server/        API: autenticação/MFA (auth.go), admin, logs/jobs e um arquivo por módulo (finops*.go + agendador)
internal/safeenv/       ambiente filtrado dos processos filhos
internal/sysinfo/       detecção das ferramentas e execuções curtas
internal/finops/        FinOps: SigV4/AWS, assinatura OCI, análises, regras, Kubernetes, IaC, CSV, relatórios (+ testes)
internal/gen/           geradores: YAML seguro, Dockerfile/compose, playbooks/roles, OpenTofu, Kubernetes
internal/jobs/          execução assíncrona com SSE
internal/netcalc/       calculadora IPv4/IPv6, split, VLSM, máscaras
internal/rbac/          módulos e matriz de permissões
internal/secure/        AES-GCM, PBKDF2, TOTP
internal/store/         conexão e migrations (SQL embutido)
web/static/             SPA (index.html, css, js/pages/*)
docs/                   especificação original, logo, screenshots
```

```bash
make test            # testes unitários (TOTP/RFC 6238, SigV4 com o vetor oficial, assinatura OCI, FinOps, router, geradores, RBAC)
make vet
make run             # precisa de PostgreSQL e das variáveis do §4.4
```

Os testes dos geradores validam todo YAML gerado com o PyYAML (o mesmo parser do Ansible) quando `python3-yaml` está disponível. Ao alterar personas ou guardrails, atualize **juntos** `internal/agents/AGENTS.md` e `internal/agents/prompts.go`.

---

## 16. Limitações conhecidas e roadmap

- **Ansible**: aparece no menu como **em breve**; a interface web ainda não existe (API completa, §11).
- **Nmap**: previsto na especificação, não foi implementado nesta versão.
- **AWS EC2/S3 via IAM e Falco**: em breve.
- **FinOps**: inventário automático só na AWS (OCI e outras nuvens via CSV); métricas de memória dependem do agente do CloudWatch/CSV; estimativas usam o catálogo de preços (sem descontos negociados); a estimativa de IaC é estática (não executa `plan`, então módulos remotos e `for_each` dinâmicos não são resolvidos).
- **doomcli** (CLI do DOOMCTL) e **agentes remotos via WebSocket**: planejados.
- Histórico do assistente de IA é mantido em memória (reinicia com o servidor).
- **Assistente de IA — integrações externas**: os clientes Azure OpenAI/Microsoft Foundry, agentes do Foundry, OpenAI-compatíveis e MCP seguem as APIs públicas documentadas e foram validados com servidores simulados; use **Testar** ao cadastrar. MCP só via HTTP (stdio requer proxy) e sem OAuth interativo; o doomctl não executa funções do cliente pedidas por agentes do Foundry.
- Sem SSO/LDAP/OIDC por enquanto.

---

## 17. Licenças de terceiros

| Componente | Licença |
|---|---|
| Fonte *vhs/vcr osd* (sokXX, baseada em "VCR OSD MONO" de AFK AFK) — usada no logo e nos números | CC BY-SA 3.0 |
| github.com/lib/pq | MIT |
| github.com/skip2/go-qrcode | MIT |
| Ícones de traço inspirados em Heroicons | MIT |
| Ansible, OpenTofu, Trivy, kubectl, Docker CLI, Portainer CE, Ollama | licenças próprias de cada projeto |
