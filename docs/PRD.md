# Product Requirements Document (PRD): Agent-Forge

**Version:** 2.0 (Final Architecture)
**Status:** Build Ready
**Product Name:** Agent-Forge
**Tagline:** "The Static Site Generator for AI Minds. Compile your knowledge into a microchip."

---

## 1. Executive Summary & Vision

**The Problem:** Distributing deep, stateful AI agents is complex. Developers currently have to deploy heavy Python microservices, standalone vector databases, and complex orchestration layers just to share a single expert agent. Furthermore, handling different provider schemas (OpenAI, Anthropic, Cohere) creates massive integration friction.

**The Solution:** **Agent-Forge** is a Go-based CLI framework that compiles raw documents into embedded, pure-Go GraphRAG databases, packaging them into an ultra-lightweight (under 50MB) Docker container.

**The Vision:** We provide the compiler; the user provides the knowledge and the compute. By standardizing exclusively on **OpenAI-compatible endpoints** for reasoning, embedding, and reranking, we decouple the agent's memory from the inference provider. Anyone can build an agent, push it to Docker Hub, and run it anywhere using their preferred local or cloud models.

---

## 2. Technology Stack (100% Go-Native)

Agent-Forge eliminates external database servers and heavy Python runtimes.

| Component | Technology | Purpose |
| --- | --- | --- |
| **CLI Framework** | `spf13/cobra` | The developer interface (`init`, `build`). |
| **Vector Memory** | `philippgille/chromem-go` | 100% pure Go embedded vector store for semantic search. |
| **Graph Memory** | `cayleygraph/cayley` | Embedded Go graph database for deep entity-relationship RAG. |
| **LLM Proxy & Client** | `sashabaranov/go-openai` | Handles build-time extraction and run-time REST/MCP serving. |
| **MCP Protocol** | `mark3labs/mcp-go` | Exposes the memory as an HTTP SSE Tool to Cursor, Windsurf, or Claude. |

---

## 3. Configuration & The "BYOM" Stance

Agent-Forge is **strictly provider-agnostic** and demands **OpenAI-compatible APIs** for all three core AI functions: Reasoning (LLM), Embedding, and Reranking.

We do not endorse or bundle any specific proxy. If a user wants to use Cohere for reranking or Anthropic for reasoning, it is *their responsibility* to run an OpenAI-compatible reverse proxy (like LiteLLM, OneAPI, or Ollama) locally or in the cloud. Agent-Forge only speaks standard OpenAI JSON.

### Global CLI Configuration (Build-Time)

Before building, the developer must configure the CLI. Agent-Forge generates a global config file at `~/.agent-forge/config.yaml` to handle the heavy-lifting extraction APIs.

```yaml
# ~/.agent-forge/config.yaml
build_providers:
  llm:
    base_url: "http://localhost:4000/v1" # E.g., user's local proxy or OpenAI direct
    api_key: "sk-..."
    model: "gpt-4o"
  embedder:
    base_url: "https://api.voyageai.com/v1"
    api_key: "pa-..."
    model: "voyage-3"

```

---

## 4. Step-by-Step Developer Workflow

### Step 1: Initialization (`agentforge init`)

**Command:** `agentforge init my-expert-agent`
**Action:** The CLI creates a new project directory and scaffolds the necessary boilerplate to define the agent and prepare it for Dockerization.

**Output Artifacts:**

```text
my-expert-agent/
├── data/               # Empty directory. User drops PDFs/Markdown here.
├── agent.yaml          # Defines the persona, desired models, and interfaces.
├── Dockerfile          # Pre-configured to copy data/ into the Go runtime base image.
├── .env.example        # Template showing the 3 runtime ENV vars needed.
└── .dockerignore       # Ensures raw PDFs aren't bloated into the final image.

```

### Step 2: Data Preparation

**Action:** The user manually places their raw knowledge files (e.g., `book_1.pdf`, `architecture.md`) into the `data/` directory. They edit `agent.yaml` to define the system prompt and MCP tool names.

### Step 3: Compilation (`agentforge build`)

**Command:** `cd my-expert-agent && agentforge build`
**Action:** 1. The CLI reads the raw files in `data/`.
2. It connects to the APIs defined in `~/.agent-forge/config.yaml`.
3. It chunks the text, calls the Embedder API, and writes the vector index.
4. It calls the LLM API to extract Knowledge Graph triples (Subject, Predicate, Object).
5. It uses the LLM to auto-generate highly optimized tool `description` strings inside `agent.yaml` for the MCP interface.

**Output Artifacts (in-place generation):**

```text
my-expert-agent/
├── data/
│   ├── memory.chromem/     # NEW: The compiled vector database.
│   └── knowledge.cayley/   # NEW: The compiled graph database.
├── agent.yaml              # UPDATED: Injected with MCP descriptions.

```

### Step 4: Containerization & Deployment

**Action:** Because the `Dockerfile` was created during `init` and expects the `chromem` and `cayley` files to exist, the user simply runs standard Docker commands.
**Command:** `docker build -t my-registry/expert-agent:v1 .`
**Result:** A ~50MB Docker image containing the Go binary, the baked databases, and the YAML config. The user can push this to Docker Hub to share their "Agentic Mind" with the world.

---

## 5. Inference Time (The Runtime)

When someone downloads the published Docker container, they run it by providing their *own* runtime API keys. No data ingestion happens at runtime; the container boots in <50ms and maps the pre-computed databases into RAM.

**Execution Command:**

```bash
docker run -p 8000:8000 \
  -e LLM_BASE_URL="https://api.openai.com/v1" \
  -e LLM_API_KEY="sk-..." \
  -e LLM_MODEL="gpt-4o" \
  -e EMBED_BASE_URL="..." \
  -e EMBED_API_KEY="..." \
  -e EMBED_MODEL="..." \
  # optionally openai compatible reranker
  -e RERANK_BASE_URL="..." \
  -e RERANK_API_KEY="..." \
  -e RERANK_MODEL="..." \
  my-registry/expert-agent:v1

```

### The 3 Exposure Interfaces

The Go runtime multiplexes three endpoints concurrently on port `8000`:

1. **REST API (`POST /v1/chat/completions`)**
* **Behavior:** Acts as a transparent proxy. It intercepts standard OpenAI chat requests, runs a Hybrid Search (Vector + Graph) against the embedded memory, injects the retrieved context as a system message, and forwards the payload to the user's `LLM_BASE_URL`.
* **Use Case:** Drop-in integration for LibreChat, AnythingLLM, or custom web UIs.


2. **MCP Server (`GET /mcp`)**
* **Behavior:** Implements the Model Context Protocol over HTTP SSE.
* **Use Case:** Exposes tools like `search_expert_knowledge` directly to IDEs (Cursor, Windsurf). The IDE's local LLM decides when to call the tool based on the description auto-generated during the `build` phase.


3. **A2A Protocol (`POST /rpc/agent`)**
* **Behavior:** Standardized JSON-RPC endpoint.
* **Use Case:** Allows a multi-agent orchestration framework (e.g., AutoGen, CrewAI) to query the container programmatically without conversational overhead.



---

## 6. Success Metrics (MVP)

1. **Developer Friction:** A developer must be able to go from `init` -> drop PDF -> `build` -> `docker run` in under 3 commands, assuming API keys are configured.