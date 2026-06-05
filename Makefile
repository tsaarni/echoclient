BINARY := bin/echoclient

.PHONY: all
all: build lint test

.PHONY: build
build: ## Build binary
	go build -o $(BINARY) ./cmd/echoclient

.PHONY: test
test: ## Run tests
	go test -race -v -timeout 2m ./...

.PHONY: lint
lint: ## Run linters
	go vet ./...
	go tool -modfile tools/go.mod golangci-lint run ./...

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-10s\033[0m %s\n", $$1, $$2}'
