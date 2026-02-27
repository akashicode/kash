# Agent-Forge — Local Manual Testing Guide

End-to-end workflow for testing the full pipeline on your own PC.

---

## Prerequisites

```powershell
# Go 1.22+ — https://go.dev/dl/
go version

# Docker Desktop — https://www.docker.com/products/docker-desktop/
docker version
```

---

## Step 1: Build the CLI binary

```powershell
cd C:\Users\admin\Desktop\projects\agent-forge

go build -o bin\agentforge.exe .\cmd\agent-forge

# Add to PATH for this session
$env:PATH += ";C:\Users\admin\Desktop\projects\agent-forge\bin"

# Verify
agentforge version
```

---

## Step 2: Scaffold a test agent project

```powershell
agentforge init my-test-agent
```

This creates the `my-test-agent/` directory **and** generates `~/.agentforge/config.yaml` with an empty skeleton.

---

## Step 3: Fill in your API keys

```powershell
notepad "$env:USERPROFILE\.agentforge\config.yaml"
```

```yaml
llm:
  base_url: "https://api.openai.com/v1"
  api_key: "sk-..."
  model: "gpt-4o"

embedder:
  base_url: "https://api.voyageai.com/v1"
  api_key: "pa-..."
  model: "voyage-3"

reranker:           # optional — leave empty to disable
  base_url: ""
  api_key: ""
  model: ""

port: 8000
```

> **Note:** Environment variables (`LLM_BASE_URL`, `LLM_API_KEY`, etc.) take priority over this file.
> Inside Docker containers you use env vars and can ignore this file entirely.

---

## Step 4: Add documents and run build

```powershell
cd my-test-agent

# Add at least one document (markdown, txt, or PDF)
echo "# AI Overview`nArtificial intelligence is the simulation of human intelligence in machines." > data\test.md

# Compile the knowledge base
agentforge build
```

Expected output:
```
Agent-Forge Build Pipeline
==========================
[1/5] Loading documents from data/...
[2/5] Chunking documents...
[3/5] Building vector index...
[4/5] Extracting knowledge graph triples...
[5/5] Generating optimized MCP tool descriptions...
==========================
Build complete!
```

After this, `data/memory.chromem/` and `data/knowledge.cayley/` will be populated.

---

## Step 5: Test serve locally (without Docker)

```powershell
# Uses ~/.agentforge/config.yaml
agentforge serve
```

Or override with env vars:

```powershell
$env:LLM_BASE_URL="https://api.openai.com/v1"
$env:LLM_API_KEY="sk-..."
$env:LLM_MODEL="gpt-4o"
$env:EMBED_BASE_URL="https://api.voyageai.com/v1"
$env:EMBED_API_KEY="pa-..."
$env:EMBED_MODEL="voyage-3"
agentforge serve
```

### Test the three endpoints

```powershell
# Health
curl http://localhost:8000/health

# REST — OpenAI-compatible chat
curl -X POST http://localhost:8000/v1/chat/completions `
  -H "Content-Type: application/json" `
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"what do you know?"}]}'

# MCP — tool listing over HTTP SSE
curl http://localhost:8000/mcp

# A2A — agent-to-agent JSON-RPC
curl -X POST http://localhost:8000/rpc/agent `
  -H "Content-Type: application/json" `
  -d '{"jsonrpc":"2.0","method":"agent.info","id":1}'
```

---

## Step 5b: Test API key auth (optional)

Set `AGENT_API_KEY` to enable authentication. `/health` always stays public.

```powershell
$env:AGENT_API_KEY="my-test-secret"
agentforge serve
```

**Without the key — should get 401:**
```powershell
curl -X POST http://localhost:8000/v1/chat/completions `
  -H "Content-Type: application/json" `
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
# → {"error":"invalid or missing API key — pass via Authorization: Bearer <AGENT_API_KEY>"}
```

**With the key — should succeed:**
```powershell
# curl
curl -X POST http://localhost:8000/v1/chat/completions `
  -H "Authorization: Bearer my-test-secret" `
  -H "Content-Type: application/json" `
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'

# A2A with auth
curl -X POST http://localhost:8000/rpc/agent `
  -H "Authorization: Bearer my-test-secret" `
  -H "Content-Type: application/json" `
  -d '{"jsonrpc":"2.0","id":1,"method":"agent.info"}'

# Health is always public (no header needed)
curl http://localhost:8000/health
# → includes "auth_enabled": true
```

**OpenAI Python SDK with API key:**
```python
from openai import OpenAI
client = OpenAI(
    base_url="http://localhost:8000/v1",
    api_key="my-test-secret",  # ← AGENT_API_KEY goes here
)
resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "what do you know?"}]
)
print(resp.choices[0].message.content)
```

**MCP config with API key (Cursor / Claude Desktop):**
```json
{
  "mcpServers": {
    "my-test-agent": {
      "url": "http://localhost:8000/mcp",
      "env": { "API_KEY": "my-test-secret" }
    }
  }
}
```

---

## Step 6: Build the Docker base image locally

> The generated agent `Dockerfile` uses `FROM ghcr.io/agent-forge/agentforge:latest`.
> Until you publish a release to GHCR, build the base image locally first.

```powershell
# Back in the repo root
cd C:\Users\admin\Desktop\projects\agent-forge

docker build -t ghcr.io/agent-forge/agentforge:latest .
```

---

## Step 7: Build and run the agent Docker image

```powershell
cd my-test-agent

# Build the agent image
docker build -t my-test-agent:latest .

# Copy .env.example → .env and fill in your runtime API keys
copy .env.example .env
notepad .env

# Run with plain docker
docker run -p 8000:8000 --env-file .env my-test-agent:latest

# OR with Docker Compose (builds + runs in one step)
docker compose up --build
```

---

## Step 8: Verify the container

```powershell
# Health check
curl http://localhost:8000/health

# Chat completion
curl -X POST http://localhost:8000/v1/chat/completions `
  -H "Content-Type: application/json" `
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"what do you know?"}]}'
```

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `missing required config` on build | `~/.agentforge/config.yaml` is empty and no env vars set | Fill in LLM + embedder fields in config.yaml |
| `docker pull` fails for base image | GHCR image not yet published | Build base image locally (Step 6) |
| `connection refused` on serve | Wrong port or serve not started | Check `port` in config.yaml or set `PORT` env var |
| Build fails with `no supported documents` | `data/` dir is empty | Add at least one `.md`, `.txt`, or `.pdf` file |
| Container exits immediately | Missing env vars in `.env` | Copy `.env.example` → `.env` and fill all required keys |
| `agent.yaml not found` on build | Not in the agent project directory | `cd my-test-agent` before running `agentforge build` |
| `401 unauthorized` on all endpoints | `AGENT_API_KEY` is set but not passed | Add `-H "Authorization: Bearer <key>"` to requests, or unset the env var for local dev |
