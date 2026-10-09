# doomctl — atalhos de desenvolvimento
VERSION ?= 0.1.0
GO ?= go

.PHONY: build test vet run docker up down logs fmt clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/doomctl ./cmd/doomctl
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o bin/devops-router ./cmd/devops-router

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal web

run: build
	DOOMCTL_DATA_DIR=./.data DOOMCTL_LISTEN=127.0.0.1:8443 ./bin/doomctl

docker:
	docker build --build-arg VERSION=$(VERSION) -t doomctl:$(VERSION) .

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f doomctl

clean:
	rm -rf bin .data
