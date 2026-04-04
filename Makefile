# Project Variables
PACKAGES  := ./...
BINARY_NAME := echoclient

.PHONY: all
all: check build

## Documentation
.PHONY: help
help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

## Build
.PHONY: build
build: ## Build the command line tool
	@echo "==> Building binary..."
	go build -o bin/$(BINARY_NAME) ./cmd/$(BINARY_NAME)

## Validation
.PHONY: check
check: vet staticcheck test ## Run all checks

.PHONY: lint
lint: vet staticcheck ## Run all logic and style checks

.PHONY: vet
vet: ## Run standard go vet
	@echo "==> Running go vet..."
	go vet $(PACKAGES)

.PHONY: staticcheck
staticcheck: ## Run advanced static analysis
	@echo "==> Running staticcheck..."
	go tool staticcheck $(PACKAGES)

.PHONY: test
test: ## Run all tests
	@echo "==> Running tests..."
	go test -race -v $(PACKAGES)
