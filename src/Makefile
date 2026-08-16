BINARY = bin/service

.PHONY: help build run test test-unit test-integration test-all cover lint fmt tidy clean

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Compile the service
	@go build -trimpath -o $(BINARY) .

run: ## Run the API server against a local Postgres
	@go run .

test: test-unit ## Alias for test-unit

test-unit: ## Run unit tests (no Docker required)
	@go test -race -count=1 ./...

test-integration: ## Run integration tests against a real Postgres (Docker, or TEST_DATABASE_URL)
	@# -p 1: the suites share one database and truncate between tests, so they
	@# must not run concurrently.
	@go test -tags integration -race -count=1 -timeout 10m -p 1 ./tests/...

test-all: test-unit test-integration ## Run every test

cover: ## Unit test coverage summary
	@go test -count=1 -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

lint: ## Static analysis
	@go vet ./...
	@go vet -tags integration ./...

fmt: ## Format the module
	@gofmt -s -w .

tidy: ## Tidy the module graph
	@go mod tidy

clean: ## Remove build artefacts
	@rm -rf bin coverage.out
