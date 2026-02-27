# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Kash is a Go-based CLI framework that compiles raw documents into embedded, pure-Go GraphRAG databases, packaged into ultra-lightweight Docker containers (~50MB).

The "compiler" approach: data ingestion happens at build time, runtime only serves queries. This allows sharing expert AI agents as Docker images without complex infrastructure.

## Build, Lint, and Test Commands

### Build
```bash
go build -o bin/kash ./cmd/Kash

# Cross-platform builds
GOOS=linux GOARCH=amd64 go build -o bin/kash-linux ./cmd/Kash
GOOS=darwin GOARCH=amd64 go build -o bin/kash-darwin ./cmd/Kash
GOOS=windows GOARCH=amd64 go build -o bin/kash.exe ./cmd/Kash
```

### Lint
```bash
golangci-lint run ./...
go fmt ./...
go vet ./...
```

### Test
```bash
go test ./...
go test -v ./...
go test -v -run TestFunctionName ./path/to/package
go test -v ./internal/vector
go test -coverprofile=coverage.out ./...
go test -v -tags=integration ./...  # integration tests
go test -bench=. ./...               # benchmarks
```

### Docker
```bash
docker build -t Kash:latest .
docker run -p 8000:8000 Kash:latest
```

## Architecture

### Core Components

| Component | Technology | Purpose |
|-----------|------------|---------|
| CLI Framework | `spf13/cobra` | Developer interface (`init`, `build`) |
| Vector Memory | `philippgille/chromem-go` | Pure Go embedded vector store |
| Graph Memory | `cayleygraph/cayley` | Embedded Go graph database |
| LLM Client | `sashabaranov/go-openai` | Build-time extraction & run-time serving |
| MCP Protocol | `mark3labs/mcp-go` | HTTP SSE tool exposure for IDEs |

### Three Runtime Interfaces

The Go runtime multiplexes on port 8000:

1. **REST API** (`POST /v1/chat/completions`) - Transparent proxy with Hybrid Search (Vector + Graph) injection
2. **MCP Server** (`GET /mcp`) - Model Context Protocol over HTTP SSE for Cursor/Windsurf
3. **A2A Protocol** (`POST /rpc/agent`) - JSON-RPC for multi-agent orchestration (AutoGen, CrewAI)

### Key Architectural Decisions

1. **Provider Agnostic**: Only OpenAI-compatible APIs. Users provide their own proxies (LiteLLM, Ollama, OneAPI).
2. **Embedded Databases**: No external DB servers. chromem-go and cayley are embedded.
3. **Docker-First Distribution**: Single ~50MB container with baked databases.
4. **Build vs Runtime**: Data ingestion at `build` time; runtime only serves queries.
5. **BYOM (Bring Your Own Model)**: Runtime requires user-provided API keys; no bundled inference.
6. **Single Binary**: One `kash` binary handles CLI (`init`, `build`) and server (`serve`). A multi-arch base image (`ghcr.io/akashicode/kash`) is published to GHCR. Agent Dockerfiles use it as base so users can build cross-platform images with `docker buildx`.

## Configuration

### Global CLI Config (Build-Time)
Location: `~/.Kash/config.yaml`

```yaml
build_providers:
  llm:
    base_url: "http://localhost:4000/v1"
    api_key: "sk-..."
    model: "gpt-4o"
  embedder:
    base_url: "https://api.voyageai.com/v1"
    api_key: "pa-..."
    model: "voyage-3"
```

### Runtime Environment Variables
```bash
LLM_BASE_URL, LLM_API_KEY, LLM_MODEL
EMBED_BASE_URL, EMBED_API_KEY, EMBED_MODEL
# Reranker is optional — must expose a Cohere-compatible POST /rerank endpoint
# Compatible: Cohere, Jina AI, Voyage AI, LiteLLM proxy
RERANK_BASE_URL, RERANK_API_KEY, RERANK_MODEL  # optional
RERANK_ENDPOINT  # optional: full URL override, takes priority over RERANK_BASE_URL
```

## Developer Workflow

1. **`kash init <name>`** - Scaffold project with `data/`, `agent.yaml`, `Dockerfile`
2. **Add documents** to `data/` directory (PDFs, Markdown, etc.)
3. **`kash build`** - Chunk documents, call embedder API, extract graph triples via LLM, generate MCP tool descriptions
4. **`docker build`** - Package into ~50MB container with baked databases
5. **`docker run`** with user's runtime API keys

## Project Structure

```
Kash/
├── cmd/                    # Cobra commands
│   ├── root.go            # Root command, Viper config
│   ├── init.go            # `kash init`
│   ├── build.go           # `kash build`
│   ├── serve.go           # `kash serve` (runtime server)
│   └── version.go         # `kash version`
├── internal/              # Private application code
│   ├── config/            # Viper configuration
│   ├── vector/            # chromem-go operations
│   ├── graph/             # cayley graph operations
│   ├── llm/               # go-openai wrappers
│   ├── mcp/               # MCP protocol server
│   ├── chunker/           # Document chunking
│   └── server/            # HTTP server (REST, MCP, A2A)
├── api/                   # OpenAPI schemas/types
├── docs/                  # Documentation
├── test/                  # Integration test fixtures
├── Makefile
├── Dockerfile
└── .golangci.yml
```

## Code Style

- Use table-driven tests with `testify/assert` and `testify/require`
- Constructor pattern: `NewX()` functions, never zero-value structs
- Error wrapping with `fmt.Errorf("%w", err)` - never discard errors
- Guard clauses with early returns for nil checks
- Struct initialization uses named fields: `&Client{Field: value}`
- Imports ordered: stdlib, third-party, local packages
