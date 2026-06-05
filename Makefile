BINARY := bin/echoclient

.PHONY: all
all: build lint test

.PHONY: build
build: ## Build binary
	go build -o $(BINARY) ./cmd/echoclient

.PHONY: test
test: ## Run all tests (unit & E2E) with coverage
	go test -coverpkg=./... -race -v -timeout 2m -coverprofile=coverage.out -covermode=atomic ./...

.PHONY: coverage
coverage: test ## Show test coverage summary
	go tool cover -func=coverage.out

.PHONY: coverage-html
coverage-html: test ## Show test coverage in browser
	go tool cover -html=coverage.out

.PHONY: lint
lint: ## Run linters
	go vet ./...
	go tool -modfile tools/go.mod golangci-lint run ./...

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-10s\033[0m %s\n", $$1, $$2}'
