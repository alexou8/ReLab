# ReLab developer commands.
#
# `make check` is the gate every milestone has to pass and is what CI runs.

SHELL := /bin/bash
BIN := bin/relab
PKG := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Point the integration tests at a PostgreSQL instance they may create and drop
# databases on. Unset, those tests skip rather than fail.
export RELAB_TEST_DSN ?= postgres://relab:relab@localhost:5432/postgres?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the relab binary into bin/
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/relab

.PHONY: fmt
fmt: ## Format and fix imports
	gofmt -w .
	@command -v golangci-lint >/dev/null && golangci-lint fmt ./... || true

.PHONY: vet
vet: ## go vet
	go vet $(PKG)

.PHONY: lint
lint: ## golangci-lint
	golangci-lint run $(PKG)

.PHONY: test
test: ## Run every test, including the ones that need PostgreSQL
	go test -race -timeout 10m $(PKG)

.PHONY: test-unit
test-unit: ## Run only tests that need no database
	RELAB_TEST_DSN= go test -race -timeout 5m $(PKG)

.PHONY: check
check: vet lint test ## The milestone gate: vet, lint, and the full test suite

.PHONY: db-up
db-up: ## Start PostgreSQL via docker compose
	docker compose up -d postgres
	@echo "waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U relab >/dev/null 2>&1; do sleep 0.5; done

.PHONY: db-down
db-down: ## Stop and remove the compose stack and its volume
	docker compose down -v

.PHONY: migrate
migrate: build ## Apply migrations to $RELAB_DSN
	$(BIN) migrate

.PHONY: up
up: ## Start the whole stack (postgres, api, workers)
	docker compose up --build -d

.PHONY: down
down: ## Stop the whole stack
	docker compose down -v

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist coverage.out
