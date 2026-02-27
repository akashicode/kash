# Agent-Forge Makefile

BINARY=bin/agentforge
CMD_DIR=./cmd/agent-forge
MODULE=github.com/agent-forge/agent-forge

# Go build flags
GOFLAGS=-trimpath
LDFLAGS=-s -w

.PHONY: all build build-linux build-darwin build-windows build-all install clean test lint fmt vet coverage

all: build

## Build the binary for the current OS/arch
build:
	@mkdir -p bin
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD_DIR)
	@echo "Built: $(BINARY)"

## Build for Linux amd64
build-linux:
	@mkdir -p bin
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agentforge-linux $(CMD_DIR)
	@echo "Built: bin/agentforge-linux"

## Build for macOS amd64
build-darwin:
	@mkdir -p bin
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agentforge-darwin $(CMD_DIR)
	@echo "Built: bin/agentforge-darwin"

## Build for Windows amd64
build-windows:
	@mkdir -p bin
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agentforge.exe $(CMD_DIR)
	@echo "Built: bin/agentforge.exe"

## Build all platforms
build-all: build-linux build-darwin build-windows

## Install to /usr/local/bin (Linux/macOS only)
install: build
	@echo "Installing to /usr/local/bin/..."
	install -m 755 $(BINARY) /usr/local/bin/agentforge
	@echo "Installed: agentforge"

## Run all tests
test:
	go test -v ./...

## Run tests with coverage
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Run linter
lint:
	golangci-lint run ./...

## Format code
fmt:
	go fmt ./...

## Run go vet
vet:
	go vet ./...

## Download dependencies
tidy:
	go mod tidy

## Clean build artifacts
clean:
	rm -rf bin/ coverage.out coverage.html

## Build Docker base image locally
docker-build:
	docker build -t agentforge:latest .

## Build multi-arch Docker base image and push to registry
## Usage: make docker-push REGISTRY=ghcr.io/agent-forge
docker-push:
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(REGISTRY)/agentforge:latest --push .

## Show help
help:
	@grep -E '^## ' Makefile | sed 's/## //'
