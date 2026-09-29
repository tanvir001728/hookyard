SHELL := /bin/bash

BIN_DIR   := bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/tanvir001728/hookyard/internal/version
LDFLAGS   := -s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

GOLANGCI_LINT ?= golangci-lint
REDOCLY       ?= npx --yes @redocly/cli@2.55.0
DEV_COMPOSE   := docker compose -f deploy/docker-compose.dev.yml
DEV_DB_PORT   ?= $(or $(HOOKYARD_DEV_DB_PORT),5432)
DEV_TOKEN     ?= hookyard-dev-token
DEV_DB_URL    ?= postgres://hookyard:hookyard@localhost:$(DEV_DB_PORT)/hookyard?sslmode=disable

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build hookyard and flakyvendor into ./bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/hookyard ./cmd/hookyard
	go build -trimpath -o $(BIN_DIR)/flakyvendor ./cmd/flakyvendor

.PHONY: flakyvendor
flakyvendor: build ## Run the flaky test vendor on :9090
	./$(BIN_DIR)/flakyvendor

.PHONY: run
run: build ## Build and run the server against the dev database (make dev-db)
	HOOKYARD_DATABASE_URL="$${HOOKYARD_DATABASE_URL:-$(DEV_DB_URL)}" \
	HOOKYARD_API_TOKENS="$${HOOKYARD_API_TOKENS:-dev:$(DEV_TOKEN)}" \
	./$(BIN_DIR)/hookyard serve

.PHONY: test
test: ## Run tests with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests and open a coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: api-lint
api-lint: ## Lint the OpenAPI specification
	$(REDOCLY) lint

.PHONY: api-docs
api-docs: ## Build HTML API reference into ./bin/api.html
	$(REDOCLY) build-docs api/openapi.yaml -o $(BIN_DIR)/api.html

.PHONY: fmt
fmt: ## Format Go code
	$(GOLANGCI_LINT) fmt ./...

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: dev-db
dev-db: ## Start a local Postgres for development
	$(DEV_COMPOSE) up -d --wait

.PHONY: dev-db-down
dev-db-down: ## Stop the local Postgres (keeps data)
	$(DEV_COMPOSE) down

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
