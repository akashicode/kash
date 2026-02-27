package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v3"

	agentconfig "github.com/agent-forge/agent-forge/internal/config"
	"github.com/agent-forge/agent-forge/internal/display"
	"github.com/agent-forge/agent-forge/internal/graph"
	"github.com/agent-forge/agent-forge/internal/llm"
	"github.com/agent-forge/agent-forge/internal/vector"
)

// AgentConfig represents the runtime agent configuration loaded from agent.yaml.
type AgentConfig struct {
	Agent struct {
		Name         string `yaml:"name"`
		Description  string `yaml:"description"`
		Version      string `yaml:"version"`
		SystemPrompt string `yaml:"system_prompt"`
	} `yaml:"agent"`
	Runtime struct {
		Embedder struct {
			Dimensions int `yaml:"dimensions"`
		} `yaml:"embedder"`
	} `yaml:"runtime"`
	MCP struct {
		Tools []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"tools"`
	} `yaml:"mcp"`
	ServerConfig struct {
		Port        int      `yaml:"port"`
		CORSOrigins []string `yaml:"cors_origins"`
	} `yaml:"server"`
}

// Server is the Agent-Forge runtime HTTP server.
type Server struct {
	vectorStore *vector.Store
	graphDB     *graph.DB
	llmClient   *llm.Client
	reranker    *llm.Reranker
	agentCfg    *AgentConfig
	appCfg      *agentconfig.Config
	mux         *http.ServeMux
	log         *slog.Logger
	apiKey string // optional API key for auth; empty = open access
}

// Config holds the runtime server configuration.
type Config struct {
	VectorStorePath string
	GraphDBPath     string
	AgentYAMLPath   string
	AppCfg          *agentconfig.Config
}

// New creates and initializes a new runtime Server.
func New(cfg Config) (*Server, error) {
	if cfg.AppCfg == nil {
		return nil, fmt.Errorf("application config is required")
	}

	// Load agent.yaml
	agentCfg, err := loadAgentConfig(cfg.AgentYAMLPath)
	if err != nil {
		return nil, fmt.Errorf("load agent config: %w", err)
	}

	// Apply agent.yaml dimensions as fallback if not set via env/config
	agentconfig.ApplyAgentYAMLDimensions(cfg.AppCfg, cfg.AgentYAMLPath)

	// Initialize vector store
	vs, err := vector.NewStoreFromPath(cfg.VectorStorePath, &cfg.AppCfg.Embedder)
	if err != nil {
		return nil, fmt.Errorf("open vector store: %w", err)
	}

	// Initialize graph DB
	gdb, err := graph.NewDBFromPath(cfg.GraphDBPath)
	if err != nil {
		return nil, fmt.Errorf("open graph db: %w", err)
	}

	// Initialize LLM client
	llmClient, err := llm.NewClient(&cfg.AppCfg.LLM)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}

	// Initialize reranker (optional — skip if not configured)
	var reranker *llm.Reranker
	if cfg.AppCfg.Reranker.BaseURL != "" {
		reranker, err = llm.NewReranker(&cfg.AppCfg.Reranker)
		if err != nil {
			return nil, fmt.Errorf("create reranker: %w", err)
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Optional API key — enables auth on all endpoints (except /health)
	apiKey := os.Getenv("AGENT_API_KEY")

	s := &Server{
		vectorStore: vs,
		graphDB:     gdb,
		llmClient:   llmClient,
		reranker:    reranker,
		agentCfg:    agentCfg,
		appCfg:      cfg.AppCfg,
		mux:         http.NewServeMux(),
		log:         logger,
		apiKey:      apiKey,
	}

	logger.Info("server initialized",
		"agent", agentCfg.Agent.Name,
		"vectors", vs.Count(),
		"triples", gdb.Count(),
		"llm_model", cfg.AppCfg.LLM.Model,
		"embed_model", cfg.AppCfg.Embedder.Model,
		"embed_dimensions", cfg.AppCfg.Embedder.Dimensions,
		"auth_enabled", apiKey != "",
	)

	s.registerRoutes()
	return s, nil
}

// Info returns a ServerInfo struct for displaying the startup banner.
func (s *Server) Info() display.ServerInfo {
	info := display.ServerInfo{
		AgentName:        s.agentCfg.Agent.Name,
		AgentDescription: s.agentCfg.Agent.Description,
		AgentVersion:     s.agentCfg.Agent.Version,
		VectorCount:      s.vectorStore.Count(),
		TripleCount:      s.graphDB.Count(),
		MCPTools:         len(s.agentCfg.MCP.Tools),
		EmbedDimensions:  s.appCfg.Embedder.Dimensions,
		EmbedModel:       s.appCfg.Embedder.Model,
		EmbedBaseURL:     s.appCfg.Embedder.BaseURL,
		LLMModel:         s.appCfg.LLM.Model,
		LLMBaseURL:       s.appCfg.LLM.BaseURL,
		RerankModel:      s.appCfg.Reranker.Model,
		RerankBaseURL:    s.appCfg.Reranker.BaseURL,
		Port:             s.appCfg.Port,
		AuthEnabled:      s.apiKey != "",
	}
	return info
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	return s.loggingMiddleware(corsMiddleware(s.authMiddleware(s.mux)))
}

// authMiddleware enforces API key auth when AGENT_API_KEY is set.
// The /health endpoint is always public. All other endpoints require
// Authorization: Bearer <AGENT_API_KEY> when auth is enabled.
// This is compatible with:
//   - curl / HTTP clients: -H "Authorization: Bearer <key>"
//   - OpenAI SDK: pass AGENT_API_KEY as the SDK's api_key
//   - MCP clients: set API_KEY env var in MCP server config
//   - A2A clients: standard Bearer auth per A2A spec
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No API key configured — open access
		if s.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		// /health is always public
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// CORS preflight must pass through
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization: Bearer <key>
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || strings.TrimPrefix(auth, prefix) != s.apiKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid or missing API key — pass via Authorization: Bearer <AGENT_API_KEY>"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs every inbound HTTP request with colorful output.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		display.LogRequest(r.Method, r.URL.Path, wrapped.status, time.Since(start), r.RemoteAddr)
	})
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher so streaming responses work through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) registerRoutes() {
	// Health check
	s.mux.HandleFunc("/health", s.handleHealth)

	// OpenAI-compatible REST API
	s.mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)

	// MCP (Model Context Protocol) over HTTP SSE
	s.mux.HandleFunc("/mcp", s.handleMCP)

	// A2A (Agent-to-Agent) JSON-RPC
	s.mux.HandleFunc("/rpc/agent", s.handleA2A)
}

// hybridSearch performs both vector and graph search, then merges results.
func (s *Server) hybridSearch(ctx context.Context, query string) (string, error) {
	s.log.Debug("hybrid search starting", "query", query)

	// Vector search
	vectorResults, err := s.vectorStore.Query(ctx, query, 5)
	if err != nil {
		s.log.Error("vector search failed", "error", err, "query", query)
		return "", fmt.Errorf("vector search: %w", err)
	}
	s.log.Info("vector search completed", "results", len(vectorResults), "query", query)

	// Graph search
	graphResults, err := s.graphDB.Search(ctx, query, 10)
	if err != nil {
		s.log.Warn("graph search failed (non-fatal)", "error", err, "query", query)
		graphResults = nil
	} else {
		s.log.Info("graph search completed", "results", len(graphResults), "query", query)
	}

	var sb strings.Builder

	// Add vector results
	if len(vectorResults) > 0 {
		sb.WriteString("## Relevant Knowledge\n\n")
		for i, r := range vectorResults {
			sb.WriteString(fmt.Sprintf("**[%d] Source: %s** (similarity: %.2f)\n", i+1, r.Source, r.Similarity))
			sb.WriteString(r.Content)
			sb.WriteString("\n\n")
		}
	}

	// Add graph results
	graphCtx := graph.FormatResults(graphResults)
	if graphCtx != "" {
		sb.WriteString("\n## Knowledge Graph Context\n\n")
		sb.WriteString(graphCtx)
	}

	return sb.String(), nil
}

// handleHealth returns a detailed health status including all key metrics.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"status":           "ok",
		"agent":            s.agentCfg.Agent.Name,
		"version":          s.agentCfg.Agent.Version,
		"vectors":          s.vectorStore.Count(),
		"triples":          s.graphDB.Count(),
		"mcp_tools":        len(s.agentCfg.MCP.Tools),
		"embed_dimensions": s.appCfg.Embedder.Dimensions,
		"llm_model":        s.appCfg.LLM.Model,
		"embed_model":      s.appCfg.Embedder.Model,
		"reranker_enabled": s.appCfg.Reranker.BaseURL != "",
		"auth_enabled":     s.apiKey != "",
		"time":             time.Now().UTC().Format(time.RFC3339),
	}

	if s.appCfg.Reranker.BaseURL != "" {
		resp["rerank_model"] = s.appCfg.Reranker.Model
	}

	json.NewEncoder(w).Encode(resp)
}

// handleChatCompletions handles POST /v1/chat/completions.
// It runs hybrid search and injects context before forwarding to the LLM.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Extract user query for retrieval
	userQuery := extractLastUserMessage(req.Messages)
	s.log.Info("chat completion request", "query", userQuery, "stream", req.Stream)

	// Run hybrid search
	retrievedCtx, err := s.hybridSearch(ctx, userQuery)
	if err != nil {
		s.log.Error("hybrid search failed, proceeding without RAG context", "error", err)
		retrievedCtx = ""
	}

	if retrievedCtx == "" {
		s.log.Warn("no RAG context retrieved for query", "query", userQuery)
	} else {
		s.log.Debug("RAG context injected", "context_length", len(retrievedCtx))
	}

	// Build augmented messages with system prompt and context
	augmented := buildAugmentedMessages(s.agentCfg.Agent.SystemPrompt, retrievedCtx, req.Messages)

	if req.Stream {
		s.handleStreamingCompletion(w, r, req, augmented)
		return
	}

	// Non-streaming response
	s.log.Debug("calling LLM", "messages", len(augmented))
	response, err := s.llmClient.ChatWithContext(ctx, augmented, "")
	if err != nil {
		s.log.Error("LLM call failed", "error", err)
		http.Error(w, "upstream LLM request failed", http.StatusBadGateway)
		return
	}
	s.log.Info("LLM response received", "length", len(response))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
		ID:      "chatcmpl-" + generateID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   s.llmClient.Model(),
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: response,
				},
				FinishReason: openai.FinishReasonStop,
			},
		},
	})
}

func (s *Server) handleStreamingCompletion(w http.ResponseWriter, r *http.Request, req openai.ChatCompletionRequest, messages []openai.ChatCompletionMessage) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	req.Messages = messages
	id := "chatcmpl-" + generateID()

	err := s.llmClient.ChatCompletionStream(r.Context(), req, func(delta string) error {
		chunk := openai.ChatCompletionStreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   s.llmClient.Model(),
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index: 0,
					Delta: openai.ChatCompletionStreamChoiceDelta{
						Role:    openai.ChatMessageRoleAssistant,
						Content: delta,
					},
				},
			},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return nil
	})

	if err != nil {
		s.log.Error("streaming LLM error", "error", err)
		errPayload, _ := json.Marshal(map[string]string{"error": "upstream LLM request failed"})
		fmt.Fprintf(w, "data: %s\n\n", errPayload)
		flusher.Flush()
		return
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func extractLastUserMessage(messages []openai.ChatCompletionMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == openai.ChatMessageRoleUser {
			return messages[i].Content
		}
	}
	return ""
}

func buildAugmentedMessages(systemPrompt, retrievedCtx string, original []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	augmented := make([]openai.ChatCompletionMessage, 0, len(original)+2)

	// Add agent system prompt
	if systemPrompt != "" {
		augmented = append(augmented, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}

	// Add retrieved context
	if retrievedCtx != "" {
		augmented = append(augmented, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: "Here is relevant context from the knowledge base:\n\n" + retrievedCtx,
		})
	}

	// Add original messages (skip any existing system messages to avoid duplication)
	for _, msg := range original {
		if msg.Role != openai.ChatMessageRoleSystem {
			augmented = append(augmented, msg)
		}
	}

	return augmented
}

func loadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config %q: %w", path, err)
	}
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}
	return &cfg, nil
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
