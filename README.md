# Agent-Forge

> **The Static Site Generator for AI Minds. Compile your knowledge into a microchip.**

Agent-Forge is a Go-based CLI that compiles raw documents (PDFs, Markdown, text) into embedded, pure-Go **GraphRAG** databases and packages them into ultra-lightweight (~50 MB) Docker containers. Ship expert AI agents as Docker images — no Python, no external vector databases, no infrastructure.

---

## How It Works

```
Documents (PDF/MD/TXT)
        │
        ▼
  agentforge build
        │
        ├── Chunks text
        ├── Calls Embedder API  ──► data/memory.chromem/   (vector index)
        ├── Calls LLM API       ──► data/knowledge.cayley/ (graph triples)
        └── Updates agent.yaml  ──► MCP tool descriptions
        │
        ▼
  docker build
        │
        ▼
  ~50 MB Docker Image
  (binary + baked databases)
        │
        ▼
  docker run (user supplies API keys)
        │
        ├── POST /v1/chat/completions  (OpenAI-compatible REST)
        ├── GET  /mcp                  (Model Context Protocol / SSE)
        └── POST /rpc/agent            (A2A JSON-RPC)
```

---

## Table of Contents

- [Architecture: Single Binary](#architecture-single-binary)
- [Prerequisites](#prerequisites)
- [Building the CLI from Source](#building-the-cli-from-source)
  - [Linux](#linux)
  - [macOS](#macos)
  - [Windows](#windows)
  - [Cross-compiling](#cross-compiling)
- [Pre-built Binaries](#pre-built-binaries)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [CLI Reference](#cli-reference)
- [Runtime Interfaces](#runtime-interfaces)
- [Docker Deployment](#docker-deployment)
- [Development](#development)

---

## Architecture: Single Binary

Agent-Forge ships as a **single binary** called `agentforge` that handles everything:

| Command | Purpose |
|---------|--------|
| `agentforge init <name>` | Scaffold a new agent project |
| `agentforge build` | Compile documents into vector + graph databases |
| `agentforge serve` | Start the runtime HTTP server (REST, MCP, A2A) |
| `agentforge version` | Print version info |

There is **no separate server binary**. The `serve` subcommand starts the HTTP server that exposes all three interfaces (REST, MCP, A2A) on port 8000.

**How agent Docker images work:**

When you run `agentforge init`, the generated `Dockerfile` uses `FROM ghcr.io/agent-forge/agentforge:latest` as its base image. This multi-arch base image (amd64 + arm64) contains the `agentforge` binary and is published automatically by the release workflow. During `docker build`, Docker pulls the variant matching the target platform. Your compiled databases and config are layered on top, producing an agent image of ~50 MB.

To publish a cross-platform agent that runs on both x86 and ARM (e.g., Raspberry Pi):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t my-registry/my-agent:v1 --push .
```

You can also download the binary directly from [GitHub Releases](https://github.com/agent-forge/agent-forge/releases/latest) to use as a local CLI tool — just add it to your `PATH`.

---

## Prerequisites

To **build from source**, you need:

| Tool | Minimum Version | Install |
|------|----------------|---------|
| Go   | 1.22           | https://go.dev/dl |
| Git  | any            | https://git-scm.com |

To **run a built agent**, you need:
- Docker (for packaging/distribution)
- An **OpenAI-compatible** LLM API (OpenAI, Ollama, LiteLLM proxy, etc.)
- An **OpenAI-compatible** Embedding API (Voyage AI, OpenAI, etc.)

---

## Building the CLI from Source

### 1. Clone the Repository

```bash
git clone https://github.com/agent-forge/agent-forge.git
cd agent-forge
```

### 2. Download Dependencies

```bash
go mod download
```

---

### Linux

#### Build for your current machine (amd64 or arm64)

```bash
go build \
  -trimpath \
  -ldflags "-s -w \
    -X github.com/agent-forge/agent-forge/cmd.version=$(git describe --tags --always) \
    -X github.com/agent-forge/agent-forge/cmd.commit=$(git rev-parse --short HEAD) \
    -X github.com/agent-forge/agent-forge/cmd.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o bin/agentforge \
  ./cmd/agent-forge
```

#### Install system-wide

```bash
sudo mv bin/agentforge /usr/local/bin/
agentforge --help
```

#### Build using Make

```bash
make build          # builds for current OS/arch → bin/agentforge
make build-linux    # explicitly targets linux/amd64
make build-all      # builds linux + macOS + windows
```

---

### macOS

#### Prerequisites

Make sure Go is installed. With Homebrew:

```bash
brew install go
```

#### Build for your Mac (Intel or Apple Silicon — Go auto-detects)

```bash
go build \
  -trimpath \
  -ldflags "-s -w \
    -X github.com/agent-forge/agent-forge/cmd.version=$(git describe --tags --always) \
    -X github.com/agent-forge/agent-forge/cmd.commit=$(git rev-parse --short HEAD) \
    -X github.com/agent-forge/agent-forge/cmd.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o bin/agentforge \
  ./cmd/agent-forge
```

#### Install system-wide

```bash
sudo mv bin/agentforge /usr/local/bin/
agentforge --help
```

#### Build for Apple Silicon (arm64) explicitly

```bash
GOOS=darwin GOARCH=arm64 go build \
  -trimpath \
  -ldflags "-s -w" \
  -o bin/agentforge-darwin-arm64 \
  ./cmd/agent-forge
```

#### Build for Intel Mac (amd64) explicitly

```bash
GOOS=darwin GOARCH=amd64 go build \
  -trimpath \
  -ldflags "-s -w" \
  -o bin/agentforge-darwin-amd64 \
  ./cmd/agent-forge
```

#### Build using Make

```bash
make build          # builds for current OS/arch → bin/agentforge
make build-darwin   # explicitly targets darwin/amd64
```

---

### Windows

#### Prerequisites

1. Install Go from https://go.dev/dl (choose the `.msi` installer)
2. Install Git from https://git-scm.com

Open **PowerShell** or **Command Prompt** and `cd` into the cloned repository.

#### Build in PowerShell

```powershell
# Set version variables
$VERSION = git describe --tags --always
$COMMIT  = git rev-parse --short HEAD
$DATE    = (Get-Date -UFormat "%Y-%m-%dT%H:%M:%SZ")

# Create output directory
New-Item -ItemType Directory -Force -Path bin | Out-Null

# Build
go build `
  -trimpath `
  -ldflags "-s -w -X github.com/agent-forge/agent-forge/cmd.version=$VERSION -X github.com/agent-forge/agent-forge/cmd.commit=$COMMIT -X github.com/agent-forge/agent-forge/cmd.buildDate=$DATE" `
  -o bin\agentforge.exe `
  .\cmd\agent-forge

# Test it
.\bin\agentforge.exe --help
```

#### Build in Command Prompt (cmd.exe)

```cmd
mkdir bin

go build ^
  -trimpath ^
  -ldflags "-s -w" ^
  -o bin\agentforge.exe ^
  .\cmd\agent-forge

bin\agentforge.exe --help
```

#### Install system-wide (PowerShell, as Administrator)

```powershell
Copy-Item bin\agentforge.exe C:\Windows\System32\agentforge.exe
# or add the bin\ directory to your PATH
```

#### Build using Make (requires GNU Make, e.g. via Git Bash or Chocolatey)

```bash
make build-windows   # produces bin/agentforge.exe
```

---

### Cross-compiling

Go supports cross-compilation out of the box — no toolchain changes needed. Set `GOOS` and `GOARCH` environment variables before building.

| Target Platform     | `GOOS`    | `GOARCH` |
|---------------------|-----------|----------|
| Linux 64-bit        | `linux`   | `amd64`  |
| Linux ARM64         | `linux`   | `arm64`  |
| macOS Intel         | `darwin`  | `amd64`  |
| macOS Apple Silicon | `darwin`  | `arm64`  |
| Windows 64-bit      | `windows` | `amd64`  |
| Windows ARM64       | `windows` | `arm64`  |

#### Build all platforms at once (Linux/macOS shell)

```bash
for OS in linux darwin windows; do
  for ARCH in amd64 arm64; do
    EXT=""
    [ "$OS" = "windows" ] && EXT=".exe"
    echo "Building $OS/$ARCH..."
    GOOS=$OS GOARCH=$ARCH go build \
      -trimpath \
      -ldflags "-s -w" \
      -o "dist/agentforge_${OS}_${ARCH}${EXT}" \
      ./cmd/agent-forge
  done
done
```

#### Build all platforms using Make

```bash
make build-all
```

---

## Pre-built Binaries

Download the latest release for your platform from the [GitHub Releases page](https://github.com/agent-forge/agent-forge/releases/latest).

| Platform            | File                                  |
|---------------------|----------------------------------------|
| Linux amd64         | `agent-forge_linux_amd64.tar.gz`      |
| Linux arm64         | `agent-forge_linux_arm64.tar.gz`      |
| macOS Intel         | `agent-forge_darwin_amd64.tar.gz`     |
| macOS Apple Silicon | `agent-forge_darwin_arm64.tar.gz`     |
| Windows 64-bit      | `agent-forge_windows_amd64.zip`       |

Verify your download with `checksums.txt` (SHA-256):

```bash
sha256sum -c checksums.txt
```

---

## Quick Start

### Step 1 — Configure build providers

```bash
mkdir -p ~/.agent-forge
cat > ~/.agent-forge/config.yaml << 'EOF'
build_providers:
  llm:
    base_url: "http://localhost:4000/v1"   # or https://api.openai.com/v1
    api_key: "sk-..."
    model: "gpt-4o"
  embedder:
    base_url: "https://api.voyageai.com/v1"
    api_key: "pa-..."
    model: "voyage-3"
EOF
```

### Step 2 — Scaffold a new agent project

```bash
agentforge init my-expert-agent
cd my-expert-agent
```

### Step 3 — Add your documents

```bash
cp ~/my-docs/*.pdf data/
cp ~/my-notes/*.md data/
```

### Step 4 — Compile

```bash
agentforge build
```

This produces `data/memory.chromem/` (vector index) and `data/knowledge.cayley/` (knowledge graph), and injects optimized MCP tool descriptions into `agent.yaml`.

### Step 5 — Package as Docker image

```bash
docker build -t my-registry/my-expert-agent:v1 .
docker push my-registry/my-expert-agent:v1
```

### Step 6 — Run anywhere

```bash
docker run -p 8000:8000 \
  -e LLM_BASE_URL="https://api.openai.com/v1" \
  -e LLM_API_KEY="sk-..." \
  -e LLM_MODEL="gpt-4o" \
  -e EMBED_BASE_URL="https://api.voyageai.com/v1" \
  -e EMBED_API_KEY="pa-..." \
  -e EMBED_MODEL="voyage-3" \
  -e RERANK_BASE_URL=""   `# optional` \
  -e RERANK_API_KEY=""    `# optional` \
  -e RERANK_MODEL=""      `# optional` \
  my-registry/my-expert-agent:v1
```

Your agent is now live at `http://localhost:8000`.

---

## Configuration

### Build-time: `~/.agent-forge/config.yaml`

Used by `agentforge build` to call LLM and embedding APIs.

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
  # reranker is optional
  # reranker:
  #   base_url: "..."
  #   api_key: "..."
  #   model: "..."
```

All fields accept any **OpenAI-compatible** endpoint. Use [LiteLLM](https://github.com/BerriAI/litellm) or [Ollama](https://ollama.com) as a local proxy for non-OpenAI models.

### Runtime: environment variables

Used by `agentforge serve` (and the Docker container).

| Variable         | Required | Description                         |
|------------------|----------|-------------------------------------|
| `LLM_BASE_URL`   | ✅       | OpenAI-compatible LLM base URL      |
| `LLM_API_KEY`    | ✅       | API key for the LLM                 |
| `LLM_MODEL`      | ✅       | Model name (e.g. `gpt-4o`)          |
| `EMBED_BASE_URL` | ✅       | OpenAI-compatible embedder base URL |
| `EMBED_API_KEY`  | ✅       | API key for the embedder            |
| `EMBED_MODEL`    | ✅       | Embedding model name                |
| `RERANK_BASE_URL`| ❌       | Reranker base URL (optional)        |
| `RERANK_API_KEY` | ❌       | API key for the reranker (optional) |
| `RERANK_MODEL`   | ❌       | Reranker model name (optional)      |
| `PORT`           | ❌       | Override listen port (default 8000) |

---

## CLI Reference

### `agentforge init <name>`

Scaffolds a new agent project directory.

```
my-expert-agent/
├── data/           # Drop your PDFs and Markdown here
├── agent.yaml      # Agent persona and MCP tool config
├── Dockerfile      # Ready for docker build
├── .env.example    # Runtime env var template
└── .dockerignore
```

### `agentforge build`

Reads `data/`, calls configured APIs, and writes compiled databases.

```
[1/5] Loading documents from data/
[2/5] Chunking documents
[3/5] Building vector index  → data/memory.chromem/
[4/5] Extracting graph triples → data/knowledge.cayley/
[5/5] Generating MCP tool descriptions → agent.yaml
```

### `agentforge serve`

Starts the runtime HTTP server (requires compiled databases).

```bash
agentforge serve --port 8000 --agent agent.yaml
```

### `agentforge version`

```
agentforge v1.2.0
  commit:     a3f9c12
  built:      2026-02-27T10:00:00Z
  go version: go1.22.0
  os/arch:    linux/amd64
```

---

## Runtime Interfaces

All three interfaces are served concurrently on port `8000`.

### 1. REST API — `POST /v1/chat/completions`

Drop-in replacement for the OpenAI chat completions API. Intercepts requests, runs hybrid search (vector + graph), injects retrieved context, and proxies to your LLM.

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Explain the key concepts"}]
  }'
```

Works as a drop-in with **LibreChat**, **AnythingLLM**, **Open WebUI**, and any OpenAI-compatible client.

### 2. MCP Server — `GET /mcp`

[Model Context Protocol](https://modelcontextprotocol.io) over HTTP SSE. Exposes the knowledge base as tools to IDEs like **Cursor** and **Windsurf**.

Add to your MCP client config:

```json
{
  "mcpServers": {
    "my-expert-agent": {
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

Available methods: `initialize`, `tools/list`, `tools/call`

### 3. A2A Protocol — `POST /rpc/agent`

JSON-RPC endpoint for multi-agent frameworks (AutoGen, CrewAI, LangGraph).

```bash
# Get agent info
curl http://localhost:8000/rpc/agent \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"agent.info"}'

# Query the knowledge base
curl http://localhost:8000/rpc/agent \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"agent.query","params":{"query":"your question"}}'

# Raw search (no LLM)
curl http://localhost:8000/rpc/agent \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":3,"method":"agent.search","params":{"query":"your question","top_k":5}}'
```

### Health Check — `GET /health`

```bash
curl http://localhost:8000/health
# {"agent":"my-expert-agent","status":"ok","triples":1423,"vectors":892,"time":"..."}
```

---

## Docker Deployment

After running `agentforge build`, the `Dockerfile` in your project is ready to use:

```bash
# Build for current architecture
docker build -t my-registry/expert-agent:v1 .

# Or build multi-arch and push (works on x86 and ARM / Raspberry Pi)
docker buildx build --platform linux/amd64,linux/arm64 \
  -t my-registry/expert-agent:v1 --push .

# Run with env file
cp .env.example .env
# Edit .env with your API keys
docker run -p 8000:8000 --env-file .env my-registry/expert-agent:v1

# Share with the world
docker push my-registry/expert-agent:v1
```

The resulting image is ~50 MB and starts in under 50 ms. No external databases, no Python runtime.

### How it works under the hood

The generated `Dockerfile` uses `FROM ghcr.io/agent-forge/agentforge:latest` as a multi-arch base image that already contains the `agentforge` binary (for both `amd64` and `arm64`). Your compiled databases and `agent.yaml` are layered on top via `COPY`. The entrypoint runs `agentforge serve`. No Go toolchain, no `curl` — just your data on top of the base image.

Because the base image is multi-arch, you can build a single agent image that runs on both x86 servers and ARM devices like Raspberry Pi using `docker buildx`.

---

## Development

```bash
# Run tests
make test

# Run tests with coverage report
make coverage

# Format code
make fmt

# Vet
make vet

# Lint (requires golangci-lint)
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
make lint

# Build all platforms
make build-all
```

### Project Layout

```
agent-forge/
├── cmd/                      # Cobra CLI commands
│   ├── agent-forge/main.go   # Entry point
│   ├── root.go               # Root command + Viper config
│   ├── init.go               # `agentforge init`
│   ├── build.go              # `agentforge build`
│   ├── serve.go              # `agentforge serve`
│   └── version.go            # `agentforge version`
├── internal/
│   ├── config/               # Build + runtime config structs
│   ├── chunker/              # Text chunking
│   ├── llm/                  # LLM client, embedder, reranker
│   ├── reader/               # Document loading (PDF, MD, TXT)
│   ├── vector/               # chromem-go vector store
│   ├── graph/                # cayley knowledge graph
│   └── server/               # HTTP server (REST, MCP, A2A)
├── .github/workflows/
│   ├── ci.yml                # Test + cross-platform build on every push
│   └── release.yml           # Publish binaries on git tag push
├── Makefile
├── Dockerfile
└── .golangci.yml
```

### Releasing a New Version

Releases are automated via GitHub Actions. Push a version tag and the workflow will:
1. Build CLI binaries for all 5 platforms and publish them to GitHub Releases
2. Build and push a multi-arch Docker base image (`linux/amd64`, `linux/arm64`) to GHCR

```bash
git tag v1.2.0
git push origin v1.2.0
```

**CLI binaries** are published for:
- Linux amd64 / arm64
- macOS amd64 (Intel) / arm64 (Apple Silicon)
- Windows amd64

**Docker base image** is published to:
- `ghcr.io/agent-forge/agentforge:latest`
- `ghcr.io/agent-forge/agentforge:<version>`

The CLI binaries are for local use (add to `PATH`). The Docker base image is used by agent Dockerfiles (generated by `agentforge init`) to produce cross-platform agent containers.

---

## License

MIT
