# Agent-Forge Makefile

BINARY=bin/agent-forge
CMD_DIR=./cmd/agent-forge
MODULE=github.com/agent-forge/agent-forge

# Go build flags
GOFLAGS=-trimpath
LDFLAGS=-s -w

.PHONY: all build build-linux build-darwin build-windows clean test lint fmt vet coverage

all: build

## Build the binary for the current OS/arch
build:
	@mkdir -p bin
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD_DIR)
	@echo "Built: $(BINARY)"

## Build for Linux amd64
build-linux:
	@mkdir -p bin
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agent-forge-linux $(CMD_DIR)
	@echo "Built: bin/agent-forge-linux"

## Build for macOS amd64
build-darwin:
	@mkdir -p bin
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agent-forge-darwin $(CMD_DIR)
	@echo "Built: bin/agent-forge-darwin"

## Build for Windows amd64
build-windows:
	@mkdir -p bin
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agent-forge.exe $(CMD_DIR)
	@echo "Built: bin/agent-forge.exe"

## Build all platforms
build-all: build-linux build-darwin build-windows

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

## Build Docker image (for development)
docker-build:
	docker build -t agent-forge:latest .

## Run Docker container (development)
docker-run:
	docker run -p 8000:8000 agent-forge:latest

## Show help
help:
	@grep -E '^## ' Makefile | sed 's/## //'
