.PHONY: help dev dev-api dev-web dev-mock web build test fmt tidy docker docker-dev clean

BINARY ?= caffeine
PKG := ./...

help:
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Run Go + Vite dev servers locally (two terminals recommended; this runs both via docker compose dev file)
	docker compose -f docker-compose.dev.yml up --build

dev-api: ## Run just the Go server locally (no docker)
	# Live recorder captures new shots instantly; periodic reconcile is
	# just a safety net so 15m is plenty for local dev.
	SYNC_INTERVAL=15m go run ./cmd/caffeine

dev-web: ## Run just the Vite dev server locally (no docker)
	cd web && npm install && npm run dev

dev-mock: ## Run the fake Meticulous machine on :8090 (point MACHINE_URL=http://localhost:8090 at it)
	go run ./cmd/mockmachine -addr :8090 -simulate 60s -shots 8

web: ## Build the web bundle into internal/web/dist
	cd web && npm install && npm run build

build: web ## Build the Go binary with embedded web assets
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) ./cmd/caffeine

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
