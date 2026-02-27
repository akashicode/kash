# Agent-Forge CLI Dockerfile
# This builds the agent-forge CLI binary itself (not an agent container)

FROM golang:1.22-alpine AS builder

WORKDIR /build

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /agentforge ./cmd/agent-forge

# --- Runtime stage ---
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /agentforge /app/agentforge

# Runtime API credentials (must be provided at run time)
ENV LLM_BASE_URL=""
ENV LLM_API_KEY=""
ENV LLM_MODEL=""
ENV EMBED_BASE_URL=""
ENV EMBED_API_KEY=""
ENV EMBED_MODEL=""
ENV RERANK_BASE_URL=""
ENV RERANK_API_KEY=""
ENV RERANK_MODEL=""

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://localhost:8000/health || exit 1

ENTRYPOINT ["/app/agentforge", "serve"]
