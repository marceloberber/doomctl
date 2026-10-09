#!/usr/bin/env bash
# doomctl — instalação com Docker Compose (Debian/Ubuntu/RHEL com Docker Engine + plugin compose)
set -euo pipefail
cd "$(dirname "$0")"

say()  { printf '\033[1;31m▶\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
uso: ./install.sh [--with-ai] [--gpu=1|0|nvidia|amd]

  --with-ai     sobe o Ollama em container (perfil "ai") e baixa o modelo do .env
  --gpu=1       aceleração por GPU para o Ollama em container (detecta NVIDIA ou AMD)
  --gpu=nvidia  força GPU NVIDIA (requer NVIDIA Container Toolkit)
  --gpu=amd     força GPU AMD/ROCm (imagem ollama/ollama:rocm, /dev/kfd e /dev/dri)
  --gpu=0       desativa a GPU (somente CPU)

Sem --gpu, a configuração atual de GPU do .env é mantida.
A GPU também pode ser ligada/desligada pelo navegador (Configurações → GPU & modelos),
desde que o container tenha acesso a ela.
EOF
}

command -v docker >/dev/null || die "Docker não encontrado. Instale o Docker Engine: https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "Plugin 'docker compose' não encontrado."
command -v openssl >/dev/null || die "openssl não encontrado (usado para gerar segredos)."

WITH_AI=0
GPU=""   # vazio = não altera a configuração de GPU
for a in "$@"; do
  case "$a" in
    --with-ai) WITH_AI=1 ;;
    --gpu|--gpu=1|--gpu=on|--gpu=true|--gpu=yes|--gpu=auto) GPU=auto ;;
    --gpu=0|--gpu=off|--gpu=false|--gpu=no|--gpu=cpu) GPU=off ;;
    --gpu=nvidia) GPU=nvidia ;;
    --gpu=amd|--gpu=rocm) GPU=amd ;;
    --gpu=*) die "valor inválido em $a (use --gpu=1, --gpu=0, --gpu=nvidia ou --gpu=amd)" ;;
    -h|--help) usage; exit 0 ;;
    *) warn "opção desconhecida ignorada: $a" ;;
  esac
done

# set_env CHAVE VALOR: altera ou acrescenta a variável no .env
set_env() {
  if grep -qE "^#? ?$1=" .env; then
    sed -i -E "s|^#? ?$1=.*|$1=$2|" .env
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}
unset_env() { sed -i -E "/^$1=/d" .env; }

nvidia_ok() { command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L >/dev/null 2>&1; }
nvidia_runtime_ok() {
  docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q nvidia || command -v nvidia-container-toolkit >/dev/null 2>&1 || command -v nvidia-ctk >/dev/null 2>&1
}
amd_ok() { [ -e /dev/kfd ] && [ -d /dev/dri ]; }

if [ ! -f .env ]; then
  say "Gerando .env com segredos aleatórios"
  cp .env.example .env
  pg=$(openssl rand -base64 24 | tr -d '/+=' | cut -c1-28)
  key=$(openssl rand -base64 32)
  sed -i "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pg}|" .env
  sed -i "s|^DOOMCTL_SECRET_KEY=.*|DOOMCTL_SECRET_KEY=${key}|" .env
  if [ -S /var/run/docker.sock ]; then
    gid=$(stat -c %g /var/run/docker.sock)
    sed -i "s|^DOCKER_GID=.*|DOCKER_GID=${gid}|" .env
  fi
  if [ "$WITH_AI" = 0 ]; then
    warn "Assistente de IA: defina OLLAMA_HOST no .env (ou rode com --with-ai para subir o Ollama em container)."
  fi
  chmod 600 .env
  warn "Faça backup do .env (DOOMCTL_SECRET_KEY): sem ela os segredos guardados não podem ser lidos."
else
  say ".env já existe — mantendo"
fi

# ---------- aceleração por GPU (Ollama em container) ----------
if [ -n "$GPU" ]; then
  if [ "$GPU" = auto ]; then
    if nvidia_ok; then GPU=nvidia
    elif amd_ok; then GPU=amd
    else die "Nenhuma GPU compatível encontrada (nvidia-smi ou /dev/kfd). Instale o driver ou use --gpu=0."
    fi
  fi
  case "$GPU" in
    nvidia)
      nvidia_ok || warn "nvidia-smi não respondeu no host — confira o driver NVIDIA."
      nvidia_runtime_ok || die "NVIDIA Container Toolkit não encontrado. Instale-o e rode: sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker"
      set_env DOOMCTL_AI_GPU nvidia
      set_env COMPOSE_FILE docker-compose.yml:docker-compose.gpu-nvidia.yml
      say "GPU NVIDIA habilitada para o Ollama (docker-compose.gpu-nvidia.yml)"
      ;;
    amd)
      amd_ok || die "/dev/kfd ou /dev/dri ausente — instale o driver amdgpu/ROCm."
      set_env DOOMCTL_AI_GPU amd
      set_env COMPOSE_FILE docker-compose.yml:docker-compose.gpu-amd.yml
      say "GPU AMD (ROCm) habilitada para o Ollama (imagem ollama/ollama:rocm)"
      ;;
    off)
      set_env DOOMCTL_AI_GPU off
      unset_env COMPOSE_FILE
      say "GPU desabilitada: o Ollama em container usará somente CPU"
      ;;
  esac
fi
docker compose config -q || die "docker-compose inválido (confira COMPOSE_FILE no .env)."

profile=()
[ "$WITH_AI" = 1 ] && profile=(--profile ai)
if [ -n "$GPU" ] && [ "$WITH_AI" = 0 ]; then
  if [ -n "$(docker compose --profile ai ps -a -q ollama 2>/dev/null)" ]; then
    profile=(--profile ai)
    say "Recriando o container do Ollama com a nova configuração de GPU"
  else
    warn "O Ollama em container não está instalado: a GPU será usada quando você rodar ./install.sh --with-ai. Com Ollama no host/outro servidor, a GPU é configurada nele."
  fi
fi

say "Construindo e subindo os containers"
docker compose "${profile[@]}" up -d --build

if [ "$WITH_AI" = 1 ]; then
  model=$(grep -E '^OLLAMA_MODEL=' .env | cut -d= -f2)
  say "Baixando o modelo ${model} no Ollama (pode demorar)"
  docker compose exec -T ollama ollama pull "${model}" || warn "Falha ao baixar ${model}. Confira a tag com 'ollama list' / ollama.com/library."
  if grep -qE '^DOOMCTL_AI_GPU=nvidia' .env; then
    if docker compose exec -T ollama nvidia-smi -L 2>/dev/null | sed 's/^/   /'; then :; else warn "A GPU não ficou visível no container do Ollama (confira o NVIDIA Container Toolkit)."; fi
  fi
fi

port=$(grep -E '^DOOMCTL_PORT=' .env | cut -d= -f2); port=${port:-8443}
say "Aguardando o doomctl ficar saudável..."
for _ in $(seq 1 40); do
  st=$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose ps -q doomctl)" 2>/dev/null || true)
  [ "$st" = "healthy" ] && break
  sleep 3
done

ip=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
say "doomctl no ar: https://${ip:-localhost}:${port}"
echo "   Usuário inicial: $(grep -E '^DOOMCTL_ADMIN_USER=' .env | cut -d= -f2)"
if ! grep -qE '^DOOMCTL_ADMIN_PASSWORD=.+' .env; then
  echo "   Senha temporária (troca obrigatória no 1º acesso):"
  docker compose logs doomctl 2>/dev/null | grep -o 'senha=[^ ]*' | tail -1 | sed 's/^/     /' || true
fi
echo "   O certificado é autoassinado: o navegador vai pedir para aceitar a exceção."
