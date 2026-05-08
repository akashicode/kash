<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.25" />
  <img src="https://img.shields.io/badge/Docker-Ready-2496ED?style=for-the-badge&logo=docker&logoColor=white" alt="Docker" />
  <img src="https://img.shields.io/badge/GraphRAG-Vector+Graph-blueviolet?style=for-the-badge" alt="GraphRAG" />
  <img src="https://img.shields.io/badge/License-MIT-green?style=for-the-badge" alt="MIT License" />
</p>

<h1 align="center">⚡ Kash</h1>

<p align="center">
  <strong>Cache your knowledge. Channel the Akashic.</strong><br/>
  <em>Compile your documents into an embedded GraphRAG brain. Ship AI agents as Docker images.</em>
</p>

---

## 💡 What is Kash?

Kash is a **Go CLI** that turns your raw documents (PDFs, Markdown, text files) into a **self-contained AI agent** packaged in a **lightweight Docker container** (base size ~50MB, final size depends on the amount of knowledge data added).

No Python runtime. No external vector databases. No infrastructure headaches.

```text
Your Documents  →  kash build  →  Docker Image  →  Ship Anywhere 🚀
```

---

## ⚡ Quick Start

### 5-Minute Setup

```bash
# 1. Install Kash
go install github.com/akashicode/kash/cmd/kash@latest

# 2. Scaffold a new agent
kash init my-expert
```

> **Important:** The `~/.kash/` directory and `config.yaml` are auto-generated the first time you run `kash init`. There's no need to manually create the files, but **you must edit `~/.kash/config.yaml`** to fill in your API keys (e.g., OpenAI, Voyage) with proper values before proceeding.

```bash
# 3. Add your knowledge
cp ~/docs/*.pdf my-expert/data/
cp ~/notes/*.md my-expert/data/

# 4. Compile the knowledge base
cd my-expert
kash build --dir .

# 5. Serve locally (no Docker needed!)
kash serve -d .
```

Your agent is now live at **http://localhost:8000**.

---

## 🏗️ Architecture

Kash runs a **Hybrid RAG Pipeline**:
1. **Vector Memory**: `philippgille/chromem-go`
2. **Graph Memory**: `cayleygraph/cayley`
3. **LLM Client**: `sashabaranov/go-openai`

During build time, Kash parses documents, embeds chunks into a vector database, and extracts triples into a knowledge graph. At runtime, it performs hybrid search, optionally reranks, and feeds context to the LLM.

---

## 🖥️ CLI Reference

- `kash init <name>`: Scaffolds a new agent project.
- `kash build`: Compiles documents into vector + graph databases (from `data/` dir).
- `kash serve`: Starts the runtime HTTP server.
- `kash version`: Displays the Kash version.

---

## 🔌 Runtime Interfaces

Kash concurrently serves three interfaces on the same port:

1. **REST API** (`POST /v1/chat/completions`): Drop-in replacement for the OpenAI API. Intercepts requests, runs hybrid RAG, injects context, proxies to your LLM.
2. **MCP Server** (`GET /mcp`): [Model Context Protocol](https://modelcontextprotocol.io). Exposes your knowledge base as tools to IDEs like Cursor or Windsurf.
3. **A2A Protocol** (`POST /rpc/agent`): JSON-RPC for multi-agent frameworks (e.g., AutoGen, CrewAI). *(WIP - Still under testing)*

> Note: You can secure all endpoints by setting the `AGENT_API_KEY` environment variable.

---

## 🚀 Running Your Agent

### Option 1: Local (No Docker)
Set your environment variables (`LLM_BASE_URL`, `LLM_API_KEY`, `EMBED_BASE_URL`, `EMBED_API_KEY`, etc.), then run `kash build` and `kash serve`.

### Option 2: Docker Compose (Recommended)
Fill in your `.env` with API keys.
```bash
kash build
docker compose up --build
```

### Option 3: Docker Run
```bash
docker build -t my-agent:latest .
docker run -p 8000:8000 --env-file .env my-agent:latest
```

---

## ⚙️ Configuration

### Build-Time: `~/.kash/config.yaml`
Auto-generated upon `kash init`. Fill this in before running `kash build`.
```yaml
build_providers:
  llm:
    base_url: "https://api.openai.com/v1"
    api_key: "sk-..."
    model: "gpt-4o"
  embedder:
    base_url: "https://api.voyageai.com/v1"
    api_key: "pa-..."
    model: "voyage-3"
```

### Runtime: Environment Variables
Used by `kash serve` and Docker containers. Provide variables like `LLM_BASE_URL`, `LLM_API_KEY`, `EMBED_BASE_URL`, `EMBED_API_KEY`, and optionally reranker variables.

> **Note:** When running locally, `kash serve` will automatically fall back to the values set in `~/.kash/config.yaml` if environment variables are not specified. However, when running via Docker, you must explicitly provide these values as environment variables (e.g., using a `.env` file).

### Agent Config: `agent.yaml`
Generated in your project directory. Contains the persona, embedding dimensions, and MCP tools configuration.

---

## 🔨 Building from Source

```bash
git clone https://github.com/akashicode/kash.git
cd kash
make build
# Or build for all platforms: make build-all
```

---

## 📜 License

MIT License.
