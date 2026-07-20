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

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/manifest"
	"github.com/akashicode/kash/internal/vector"
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

// Server is the Kash runtime HTTP server.
type Server struct {
	vectorStore *vector.Store
	graphDB     *graph.DB
	llmClient   *llm.Client
	reranker    *llm.Reranker
	agentCfg    *AgentConfig
	appCfg      *agentconfig.Config
	mux           *http.ServeMux
	log           *slog.Logger
	apiKey        string             // optional API key for auth; empty = open access
	corpusVersion int                // 0 = unknown (no build manifest found)
	buildManifest *manifest.Manifest // nil when no manifest is present
}

// Config holds the runtime server configuration.
type Config struct {
	VectorStorePath string
	GraphDBPath     string
	AgentYAMLPath   string
	// ManifestPath is the optional build manifest path; when present the
	// corpus version is exposed on /health.
	ManifestPath string
	AppCfg       *agentconfig.Config
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

	// Corpus manifest (optional) — feeds /health and the dashboard UI
	corpusVersion := 0
	var buildManifest *manifest.Manifest
	if cfg.ManifestPath != "" {
		if m, mErr := manifest.LoadOrNew(cfg.ManifestPath); mErr == nil && len(m.Documents) > 0 {
			buildManifest = m
			corpusVersion = m.Version
		}
	}

	s := &Server{
		vectorStore:   vs,
		graphDB:       gdb,
		llmClient:     llmClient,
		reranker:      reranker,
		agentCfg:      agentCfg,
		appCfg:        cfg.AppCfg,
		mux:           http.NewServeMux(),
		log:           logger,
		apiKey:        apiKey,
		corpusVersion: corpusVersion,
		buildManifest: buildManifest,
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

		// /health and the dashboard shell are always public (the dashboard's
		// data APIs stay protected; the page asks for the key client-side)
		if r.URL.Path == "/health" || r.URL.Path == "/" {
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
	// Dashboard UI (static shell — the APIs it calls are auth-protected)
	s.mux.HandleFunc("/", s.handleUI)

	// Dashboard APIs
	s.mux.HandleFunc("/api/info", s.handleAPIInfo)
	s.mux.HandleFunc("/api/graph", s.handleAPIGraph)
	s.mux.HandleFunc("/api/search", s.handleAPISearch)

	// Health check
	s.mux.HandleFunc("/health", s.handleHealth)

	// OpenAI-compatible REST API
	s.mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)

	// MCP (Model Context Protocol) over HTTP SSE
	s.mux.HandleFunc("/mcp", s.handleMCP)

	// A2A (Agent-to-Agent) JSON-RPC
	s.mux.HandleFunc("/rpc/agent", s.handleA2A)
}

// defaultTopK is the number of chunks injected as context when the caller
// does not specify one.
const defaultTopK = 5

// retrievalResult bundles structured hybrid search output.
type retrievalResult struct {
	Chunks []vector.SearchResult `json:"chunks"`
	Facts  []graph.SearchResult  `json:"facts"`
}

// retrieve performs both vector and graph search and returns structured results.
//
// Retrieval is two-stage: a wide candidate pool is pulled from the vector
// store (so relevant chunks from any document can surface), then narrowed to
// topK — by the reranker when configured, by similarity order otherwise —
// with a per-source cap so a single document cannot monopolize the context.
func (s *Server) retrieve(ctx context.Context, query string, topK int) (retrievalResult, error) {
	if topK <= 0 {
		topK = defaultTopK
	}
	s.log.Debug("hybrid search starting", "query", query, "top_k", topK)

	// Candidate pool: 4x the requested results (min 20, max 40), capped at
	// the collection size — chromem errors when nResults exceeds it.
	candidateK := topK * 4
	if candidateK < 20 {
		candidateK = 20
	}
	if candidateK > 40 {
		candidateK = 40
	}
	if count := s.vectorStore.Count(); candidateK > count {
		candidateK = count
	}

	var vectorResults []vector.SearchResult
	if candidateK > 0 {
		var err error
		vectorResults, err = s.vectorStore.Query(ctx, query, candidateK)
		if err != nil {
			s.log.Error("vector search failed", "error", err, "query", query)
			return retrievalResult{}, fmt.Errorf("vector search: %w", err)
		}
	}
	s.log.Info("vector search completed", "candidates", len(vectorResults), "query", query)

	// Graph search
	graphResults, err := s.graphDB.Search(ctx, query, 10)
	if err != nil {
		s.log.Warn("graph search failed (non-fatal)", "error", err, "query", query)
		graphResults = nil
	} else {
		s.log.Info("graph search completed", "results", len(graphResults), "query", query)
	}

	// Narrow candidates: rerank when configured, similarity order otherwise.
	ranked := vectorResults
	if s.reranker != nil && len(vectorResults) > 0 {
		docs := make([]string, len(vectorResults))
		for i, r := range vectorResults {
			docs[i] = r.Content
		}
		rerankResults, rerankErr := s.reranker.Rerank(ctx, query, docs)
		if rerankErr != nil {
			s.log.Warn("reranker failed (using similarity order)", "error", rerankErr)
		} else {
			s.log.Info("reranker completed", "candidates", len(rerankResults),
				"top_score", fmt.Sprintf("%.3f", rerankResults[0].RelevanceScore))
			ranked = make([]vector.SearchResult, len(rerankResults))
			for i, r := range rerankResults {
				ranked[i] = vectorResults[r.Index]
			}
		}
	}

	selected := diversifyBySource(ranked, topK)

	return retrievalResult{Chunks: selected, Facts: graphResults}, nil
}

// hybridSearch runs retrieve and formats the results as a context string for
// injection into LLM prompts.
func (s *Server) hybridSearch(ctx context.Context, query string, topK int) (string, error) {
	result, err := s.retrieve(ctx, query, topK)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	if len(result.Chunks) > 0 {
		sb.WriteString("## Relevant Knowledge\n\n")
		for i, r := range result.Chunks {
			fmt.Fprintf(&sb, "**[%d] Source: %s**\n", i+1, r.Source)
			sb.WriteString(r.Content)
			sb.WriteString("\n\n")
		}
	}

	graphCtx := graph.FormatResults(result.Facts)
	if graphCtx != "" {
		sb.WriteString("\n## Knowledge Graph Context\n\n")
		sb.WriteString(graphCtx)
	}

	return sb.String(), nil
}

// diversifyBySource picks up to topK results from a ranked list while capping
// how many may come from a single source document. Remaining slots are
// backfilled in rank order when the cap leaves them unfilled.
func diversifyBySource(ranked []vector.SearchResult, topK int) []vector.SearchResult {
	if len(ranked) <= topK {
		return ranked
	}

	maxPerSource := (topK + 1) / 2
	if maxPerSource < 1 {
		maxPerSource = 1
	}

	selected := make([]vector.SearchResult, 0, topK)
	perSource := map[string]int{}
	skipped := []vector.SearchResult{}

	for _, r := range ranked {
		if len(selected) >= topK {
			break
		}
		if perSource[r.Source] >= maxPerSource {
			skipped = append(skipped, r)
			continue
		}
		perSource[r.Source]++
		selected = append(selected, r)
	}

	// Backfill from skipped results if the cap left slots open
	for _, r := range skipped {
		if len(selected) >= topK {
			break
		}
		selected = append(selected, r)
	}

	return selected
}

// handleHealth returns a detailed health status including all key metrics.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"status":           "ok",
		"agent":            s.agentCfg.Agent.Name,
		"version":          s.agentCfg.Agent.Version,
		"corpus_version":   s.corpusVersion,
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

	// Extract user query for retrieval, rewriting follow-ups into standalone
	// queries using the conversation history
	userQuery := s.buildRetrievalQuery(ctx, req.Messages)
	s.log.Info("chat completion request", "query", userQuery, "stream", req.Stream)

	// Run hybrid search
	retrievedCtx, err := s.hybridSearch(ctx, userQuery, defaultTopK)
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

// buildRetrievalQuery returns the search query to use for retrieval. On the
// first turn this is the user's message as-is. On follow-up turns the message
// is rewritten into a standalone query via the LLM, so references like "tell
// me more about that" resolve against the conversation instead of retrieving
// nothing. Falls back to the raw message on any rewrite failure.
func (s *Server) buildRetrievalQuery(ctx context.Context, messages []openai.ChatCompletionMessage) string {
	userQuery := extractLastUserMessage(messages)
	if userQuery == "" {
		return ""
	}

	// Collect conversational history (user/assistant turns) before the last
	// user message. No history → nothing to resolve, skip the LLM call.
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == openai.ChatMessageRoleUser {
			lastUserIdx = i
			break
		}
	}
	var history []openai.ChatCompletionMessage
	for _, m := range messages[:lastUserIdx] {
		if m.Role == openai.ChatMessageRoleUser || m.Role == openai.ChatMessageRoleAssistant {
			history = append(history, m)
		}
	}
	if len(history) == 0 {
		return userQuery
	}
	// Only the recent turns matter for reference resolution
	const maxHistory = 6
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}

	rewritten, err := s.llmClient.RewriteQuery(ctx, history, userQuery)
	if err != nil {
		s.log.Warn("query rewrite failed (using raw message)", "error", err)
		return userQuery
	}
	if rewritten != userQuery {
		s.log.Info("query rewritten for retrieval", "original", userQuery, "rewritten", rewritten)
	}
	return rewritten
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

	// Add retrieved context with citation instructions — the sources are
	// attached to every chunk and fact, but the model must be told to use them
	if retrievedCtx != "" {
		augmented = append(augmented, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleSystem,
			Content: "Here is relevant context from the knowledge base:\n\n" + retrievedCtx +
				"\n\nWhen you answer, cite the source documents you drew from inline, e.g. (source: book.pdf). " +
				"Only cite sources that appear in the context above; never invent a source.",
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
