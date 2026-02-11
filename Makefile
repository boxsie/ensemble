BINARY_NAME = ensemble
GO = /usr/local/go/bin/go
GOFLAGS = -trimpath
MODULE = github.com/ensemble/ensemble

.PHONY: build run run-headless test test-integration test-race test-cover lint proto clean help

## Build

build: ## Build the binary
	$(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME) .

run: build ## Build and run (daemon + TUI)
	./bin/$(BINARY_NAME)

run-headless: build ## Build and run in headless mode
	./bin/$(BINARY_NAME) --headless

## Test

test: ## Run unit tests
	$(GO) test ./internal/... ./testutil/...

test-integration: ## Run integration tests (requires network + Tor)
	$(GO) test -tags=integration ./internal/...

test-race: ## Run tests with race detector
	$(GO) test -race ./internal/... ./testutil/...

test-cover: ## Generate coverage report
	$(GO) test -coverprofile=coverage.out ./internal/... ./testutil/...
	$(GO) tool cover -html=coverage.out

## Code quality

lint: ## Run linters
	golangci-lint run ./...

fmt: ## Format code
	$(GO) fmt ./...

vet: ## Run go vet
	$(GO) vet ./...

## Protobuf

proto: ## Regenerate protobuf code
	protoc --go_out=api/pb --go_opt=paths=source_relative \
		--go-grpc_out=api/pb --go-grpc_opt=paths=source_relative \
		-Iapi/proto \
		api/proto/ensemble.proto
	protoc --go_out=internal/protocol/pb --go_opt=paths=source_relative \
		-Iinternal/protocol/proto \
		internal/protocol/proto/messages.proto

## Cross-compilation

build-linux-amd64: ## Build for Linux amd64
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 .

build-linux-arm64: ## Build for Linux arm64 (Raspberry Pi)
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 .

build-darwin-amd64: ## Build for macOS amd64
	GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 .

build-darwin-arm64: ## Build for macOS arm64 (Apple Silicon)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 .

build-windows-amd64: ## Build for Windows amd64
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe .

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64 ## Build for all platforms

## Housekeeping

clean: ## Remove build artifacts
	rm -rf bin/ coverage.out

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
