.PHONY: help dev dev-api dev-web web build test fmt tidy docker docker-dev clean

BINARY ?= rivolt
PKG := ./...
# VERSION stamps the compiled binary. Defaults to `git describe` when
# available (e.g. v0.1.0, v0.1.0-3-gabcdef1), falling back to "dev".
VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)

help:
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Run Go + Vite dev servers locally (two terminals recommended; this runs both via docker compose dev file)
	docker compose -f docker-compose.dev.yml up --build

dev-api: ## Run just the Go server locally (no docker)
	go run ./cmd/rivolt

dev-web: ## Run just the Vite dev server locally (no docker)
	cd web && npm install && npm run dev

web: ## Build the web bundle into internal/web/dist
	cd web && npm install && npm run build

build: web ## Build the Go binary with embedded web assets
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/rivolt

test: ## Run Go tests
	go test $(PKG)

fmt: ## Format Go code
	go fmt $(PKG)

tidy: ## Tidy go.mod
	go mod tidy

docker: ## Build production docker image
	docker compose build

docker-up: ## Run production image via docker compose
	docker compose up -d

docker-dev: ## Run dev docker compose stack (Go + Vite with HMR)
	docker compose -f docker-compose.dev.yml up --build

clean:
	rm -rf bin dist web/dist internal/web/dist
	mkdir -p internal/web/dist
	@echo '<!-- placeholder --><p>build with make web</p>' > internal/web/dist/index.html
