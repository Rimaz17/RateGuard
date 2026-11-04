# RateGuard task runner. `make help` lists the targets.

SHELL := /bin/sh

BINARY                := bin/rateguard
IMAGE                 := rateguard:dev
GOLANGCI_LINT_VERSION := v2.15.0
RATEGUARD_HOST_PORT   ?= 8080
HEALTHZ_URL           ?= http://127.0.0.1:$(RATEGUARD_HOST_PORT)/healthz
READYZ_URL            ?= http://127.0.0.1:$(RATEGUARD_HOST_PORT)/readyz
# Steps for `make migrate-down`.
N                     ?= 1

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## --- Stack ------------------------------------------------------------------

.env:
	cp configs/.env.example .env
	@echo "created .env from configs/.env.example"

.PHONY: up
up: .env ## Build and start the stack, apply migrations, wait until healthy
	docker compose up -d --build --wait

.PHONY: down
down: ## Stop the stack (keeps the Postgres volume)
	docker compose down

.PHONY: logs
logs: ## Follow logs from every service
	docker compose logs -f

.PHONY: smoke
smoke: ## Check that /healthz and /readyz on the running stack return 200
	curl -fsS --retry 10 --retry-connrefused --retry-delay 1 $(HEALTHZ_URL)
	curl -fsS --retry 10 --retry-all-errors --retry-delay 1 $(READYZ_URL)

## --- Migrations ---------------------------------------------------------------

.PHONY: migrate-up
migrate-up: .env ## Apply every pending migration to the compose database
	docker compose run --rm migrate migrate up

.PHONY: migrate-down
migrate-down: .env ## Revert the last N migrations (default 1) on the compose database
	docker compose run --rm migrate migrate down $(N)

.PHONY: migrate-version
migrate-version: .env ## Print the compose database's schema version
	docker compose run --rm migrate migrate version

## --- Go ---------------------------------------------------------------------

.PHONY: build
build: ## Build the binary into bin/
	go build -trimpath -o $(BINARY) ./cmd/rateguard

.PHONY: run
run: ## Run the service on the host using the current environment
	go run ./cmd/rateguard

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race ./...

.PHONY: test-integration
test-integration: ## Run the testcontainers suite with the race detector (needs Docker)
	go test -race -tags integration ./test/integration/...

.PHONY: cover
cover: ## Run tests with the race detector and write coverage.out
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-clean
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet, including the integration suite
	go vet ./...
	go vet -tags integration ./...

.PHONY: lint
lint: ## Run golangci-lint (must be installed locally)
	golangci-lint run

.PHONY: lint-docker
lint-docker: ## Run the pinned golangci-lint in Docker, for machines without it
	docker run --rm -v "$(CURDIR):/src" -w /src golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run

.PHONY: check
check: fmt-check vet lint test ## Everything that must pass before a commit

## --- Build hygiene ------------------------------------------------------------

.PHONY: docker-build
docker-build: ## Build the container image
	docker build -t $(IMAGE) .

.PHONY: check-go-version
check-go-version: ## Fail if the Dockerfile's Go version differs from go.mod
	@mod="$$(sed -n 's/^go //p' go.mod)"; \
	img="$$(sed -n 's/^ARG GO_VERSION=//p' Dockerfile)"; \
	if [ "$$mod" != "$$img" ]; then echo "go.mod has go $$mod but Dockerfile has GO_VERSION=$$img"; exit 1; fi; \
	echo "go $$mod everywhere"

.PHONY: clean
clean: ## Remove build and coverage output
	rm -rf bin/ coverage.out
