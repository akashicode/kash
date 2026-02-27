package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v3"

	agentconfig "github.com/agent-forge/agent-forge/internal/config"
	"github.com/agent-forge/agent-forge/internal/graph"
	"github.com/agent-forge/agent-forge/internal/llm"
	"github.com/agent-forge/agent-forge/internal/vector"
)

// AgentConfig represents the runtime agent configuration loaded from agent.yaml.
type AgentConfig struct {
	Agent struct {
		Name         string `yaml:"name"`
		Description  string `yaml:"description"`
		SystemPrompt string `yaml:"system_prompt"`
	} `yaml:"agent"`
	Runtime struct {
		LLM struct {
			Model string `yaml:"model"`
		} `yaml:"llm"`
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
	runtimeCfg  *agentconfig.RuntimeConfig
	mux         *http.ServeMux
}

// Config holds the runtime server configuration.
type Config struct {
	VectorStorePath string
	GraphDBPath     string
	AgentYAMLPath   string
	RuntimeCfg      *agentconfig.RuntimeConfig
}

// New creates and initializes a new runtime Server.
func New(cfg Config) (*Server, error) {
	if cfg.RuntimeCfg == nil {
		return nil, fmt.Errorf("runtime config is required")
	}

	// Load agent.yaml
	agentCfg, err := loadAgentConfig(cfg.AgentYAMLPath)
	if err != nil {
		return nil, fmt.Errorf("load agent config: %w", err)
	}

	// Initialize vector store
	vs, err := vector.NewStoreFromPath(cfg.VectorStorePath, &cfg.RuntimeCfg.Embedder)
	if err != nil {
		return nil, fmt.Errorf("open vector store: %w", err)
	}

	// Initialize graph DB
	gdb, err := graph.NewDBFromPath(cfg.GraphDBPath)
	if err != nil {
		return nil, fmt.Errorf("open graph db: %w", err)
	}

	// Initialize LLM client
	llmClient, err := llm.NewClient(&cfg.RuntimeCfg.LLM)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}

	// Initialize reranker (optional)
	reranker, err := llm.NewReranker(&cfg.RuntimeCfg.Reranker)
	if err != nil {
		return nil, fmt.Errorf("create reranker: %w", err)
	}

	s := &Server{
		vectorStore: vs,
		graphDB:     gdb,
		llmClient:   llmClient,
		reranker:    reranker,
		agentCfg:    agentCfg,
		runtimeCfg:  cfg.RuntimeCfg,
		mux:         http.NewServeMux(),
	}

	s.registerRoutes()
	return s, nil
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.mux)
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
	// Vector search
	vectorResults, err := s.vectorStore.Query(ctx, query, 5)
	if err != nil {
		return "", fmt.Errorf("vector search: %w", err)
	}

	// Graph search
	graphResults, err := s.graphDB.Search(ctx, query, 10)
	if err != nil {
		// Graph search failure is non-fatal
		graphResults = nil
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

// handleHealth returns a simple health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"agent":   s.agentCfg.Agent.Name,
		"vectors": s.vectorStore.Count(),
		"triples": s.graphDB.Count(),
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
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

	// Run hybrid search
	retrievedCtx, err := s.hybridSearch(ctx, userQuery)
	if err != nil {
		// Non-fatal: continue without retrieved context
		retrievedCtx = ""
	}

	// Build augmented messages with system prompt and context
	augmented := buildAugmentedMessages(s.agentCfg.Agent.SystemPrompt, retrievedCtx, req.Messages)

	if req.Stream {
		s.handleStreamingCompletion(w, r, req, augmented)
		return
	}

	// Non-streaming response
	response, err := s.llmClient.ChatWithContext(ctx, augmented, "")
	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusBadGateway)
		return
	}

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
		fmt.Fprintf(w, "data: {\"error\": \"%s\"}\n\n", err.Error())
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
