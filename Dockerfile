# syntax=docker/dockerfile:1
# doomctl — imagem do servidor (Go 1.27 + Debian trixie com as ferramentas DevOps)

ARG GO_VERSION=1.27
ARG DOCKER_CLI_IMAGE=docker:29-cli

# ---------- build (Go) ----------
FROM golang:${GO_VERSION}-trixie AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=0 GOFLAGS=-mod=vendor
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/doomctl ./cmd/doomctl \
 && go build -trimpath -ldflags="-s -w" -o /out/devops-router ./cmd/devops-router

# ---------- ferramentas com download verificado (sha256) ----------
FROM debian:trixie-slim AS tools
ARG TARGETARCH
ARG TOFU_VERSION=1.13.1
# Trivy: NÃO use 0.69.4–0.69.6 nem a tag latest de março/2026 (comprometidas, CVE-2026-33634)
ARG TRIVY_VERSION=0.75.0
ARG KUBECTL_VERSION=v1.37.1
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /tmp/dl
RUN set -eux; arch="${TARGETARCH:-amd64}"; \
    f="tofu_${TOFU_VERSION}_linux_${arch}.tar.gz"; \
    curl -fsSLO "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/${f}"; \
    curl -fsSLO "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_SHA256SUMS"; \
    grep " ${f}\$" "tofu_${TOFU_VERSION}_SHA256SUMS" | sha256sum -c -; \
    tar -xzf "${f}" tofu; install -m 0755 tofu /usr/local/bin/tofu
RUN set -eux; case "${TARGETARCH:-amd64}" in amd64) t=64bit ;; arm64) t=ARM64 ;; *) echo "arch não suportada"; exit 1 ;; esac; \
    f="trivy_${TRIVY_VERSION}_Linux-${t}.tar.gz"; \
    curl -fsSLO "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/${f}"; \
    curl -fsSLO "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_checksums.txt"; \
    grep " ${f}\$" "trivy_${TRIVY_VERSION}_checksums.txt" | sha256sum -c -; \
    tar -xzf "${f}" trivy; install -m 0755 trivy /usr/local/bin/trivy
RUN set -eux; arch="${TARGETARCH:-amd64}"; \
    curl -fsSLo kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl"; \
    echo "$(curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl.sha256")  kubectl" | sha256sum -c -; \
    install -m 0755 kubectl /usr/local/bin/kubectl

# docker CLI + plugin compose (binários oficiais)
FROM ${DOCKER_CLI_IMAGE} AS dockercli

# ---------- runtime ----------
FROM debian:trixie-slim
LABEL org.opencontainers.image.title="doomctl" \
      org.opencontainers.image.description="Ferramentas DevOps & Cloud e Redes & Segurança, self-hosted" \
      org.opencontainers.image.source="https://github.com/doomctl/doomctl"

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tzdata git openssh-client sshpass \
      python3 python3-yaml ansible ansible-lint \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g 10001 doomctl \
 && useradd -u 10001 -g doomctl -d /var/lib/doomctl/home -s /usr/sbin/nologin -M doomctl \
 && install -d -o doomctl -g doomctl -m 0700 /var/lib/doomctl

COPY --from=tools /usr/local/bin/tofu /usr/local/bin/trivy /usr/local/bin/kubectl /usr/local/bin/
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=dockercli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
COPY --from=build /out/doomctl /out/devops-router /usr/local/bin/

ENV DOOMCTL_DATA_DIR=/var/lib/doomctl \
    DOOMCTL_LISTEN=:8443 \
    LANG=C.UTF-8 \
    TZ=America/Sao_Paulo

USER 10001:10001
WORKDIR /var/lib/doomctl
VOLUME ["/var/lib/doomctl"]
EXPOSE 8443
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 CMD ["/usr/local/bin/doomctl", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/doomctl"]
