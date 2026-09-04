package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v3"

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/lexical"
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
	// Retrieval tunes how much context is injected. Both were compile-time
	// constants, so no deployment could widen retrieval without a rebuild.
	Retrieval struct {
		// TopK is the number of chunks injected as context.
		TopK int `yaml:"top_k"`
		// GraphFacts is the number of knowledge-graph facts injected.
		GraphFacts int `yaml:"graph_facts"`
	} `yaml:"retrieval"`
}

// Server is the Kash runtime HTTP server.
type Server struct {
	vectorStore   *vector.Store
	lexicalIndex  *lexical.Index
	graphDB       *graph.DB
	llmClient     *llm.Client
	reranker      *llm.Reranker
	agentCfg      *AgentConfig
	appCfg        *agentconfig.Config
	fusionCfg     *fusionConfig // compiled corpus-specific fusion settings
	mux           *http.ServeMux
	log           *slog.Logger
	apiKey        string             // optional API key for auth; empty = open access
	corpusVersion int                // 0 = unknown (no build manifest found)
	buildManifest *manifest.Manifest // nil when no manifest is present
	entityAliases int                // 0 = entity resolution not in use
}

// Config holds the runtime server configuration.
type Config struct {
	VectorStorePath string
	GraphDBPath     string
	AgentYAMLPath   string
	// ManifestPath is the optional build manifest path; when present the
	// corpus version is exposed on /health.
	ManifestPath string
	// AliasPath is the optional entity resolution map. When absent, entity
	// spelling variants are simply not merged.
	AliasPath string
	// LexicalIndexPath is the optional BM25 index. When absent, retrieval
	// falls back to vector search alone — which is what a corpus built before
	// the lexical index existed will do until it is rebuilt.
	LexicalIndexPath string
	AppCfg           *agentconfig.Config
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

	// Entity resolution is optional: a missing alias file simply means no
	// spelling variants are merged.
	aliasCount := 0
	if cfg.AliasPath != "" {
		_, aliases, aliasErr := graph.LoadAliasFile(cfg.AliasPath)
		if aliasErr != nil {
			// A malformed alias file must not take the agent down
			slog.Warn("ignoring entity alias file", "error", aliasErr, "path", cfg.AliasPath)
		} else if aliases.Len() > 0 {
			gdb.SetAliases(aliases)
			aliasCount = aliases.Len()
		}
	}

	// Lexical index is optional: a corpus built before it existed still serves,
	// with vector search alone. A corrupt index must not take the agent down.
	domainCfg := agentconfig.LoadDomainConfig(cfg.AgentYAMLPath)
	lexIndex := lexical.NewWithFold(domainCfg.Resolution.FoldDiacritics)
	if cfg.LexicalIndexPath != "" {
		if ix, lexErr := lexical.Load(cfg.LexicalIndexPath); lexErr != nil {
			slog.Warn("ignoring lexical index", "error", lexErr, "path", cfg.LexicalIndexPath)
		} else {
			ix.SetFold(domainCfg.Resolution.FoldDiacritics)
			lexIndex = ix
		}
	}

	// Initialize LLM client
	llmClient, err := llm.NewClient(&cfg.AppCfg.LLM)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}

	// Initialize reranker (optional — skip if not configured)
	var reranker *llm.Reranker
	if rerankerConfigured(cfg.AppCfg) {
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
		lexicalIndex:  lexIndex,
		graphDB:       gdb,
		llmClient:     llmClient,
		reranker:      reranker,
		agentCfg:      agentCfg,
		appCfg:        cfg.AppCfg,
		fusionCfg:     buildFusionConfig(domainCfg),
		mux:           http.NewServeMux(),
		log:           logger,
		apiKey:        apiKey,
		corpusVersion: corpusVersion,
		buildManifest: buildManifest,
		entityAliases: aliasCount,
	}

	logger.Info("server initialized",
		"agent", agentCfg.Agent.Name,
		"vectors", vs.Count(),
		"entities", vs.EntityCount(),
		"relationships", vs.RelationshipCount(),
		"lexical", lexIndex.Len(),
		"triples", gdb.Count(),
		"entity_aliases", aliasCount,
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
		AgentName:         s.agentCfg.Agent.Name,
		AgentDescription:  s.agentCfg.Agent.Description,
		AgentVersion:      s.agentCfg.Agent.Version,
		VectorCount:       s.vectorStore.Count(),
		EntityCount:       s.vectorStore.EntityCount(),
		RelationshipCount: s.vectorStore.RelationshipCount(),
		TripleCount:       s.graphDB.Count(),
		MCPTools:          len(s.agentCfg.MCP.Tools),
		EmbedDimensions:   s.appCfg.Embedder.Dimensions,
		EmbedModel:        s.appCfg.Embedder.Model,
		EmbedBaseURL:      s.appCfg.Embedder.BaseURL,
		LLMModel:          s.appCfg.LLM.Model,
		LLMBaseURL:        s.appCfg.LLM.BaseURL,
		RerankModel:       s.appCfg.Reranker.Model,
		RerankBaseURL:     s.appCfg.Reranker.BaseURL,
		Port:              s.appCfg.Port,
		AuthEnabled:       s.apiKey != "",
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

// rerankerConfigured reports whether a reranker can actually be built.
//
// llm.NewReranker requires both a base URL and a model, and returns (nil, nil)
// otherwise. Gating construction on the base URL alone meant setting
// RERANK_BASE_URL without RERANK_MODEL silently disabled reranking while
// /health went on reporting it as enabled.
func rerankerConfigured(cfg *agentconfig.Config) bool {
	return cfg.Reranker.BaseURL != "" && cfg.Reranker.Model != ""
}

// topKOrDefault returns the configured chunk count when the caller did not
// specify one.
func (s *Server) topKOrDefault(topK int) int {
	if topK > 0 {
		return topK
	}
	if s.agentCfg != nil && s.agentCfg.Retrieval.TopK > 0 {
		return s.agentCfg.Retrieval.TopK
	}
	return defaultTopK
}

// graphFactLimit returns the number of graph facts to inject.
func (s *Server) graphFactLimit() int {
	if s.agentCfg != nil && s.agentCfg.Retrieval.GraphFacts > 0 {
		return s.agentCfg.Retrieval.GraphFacts
	}
	return defaultGraphFactLimit
}

// defaultTopK is the number of chunks injected as context when the caller
// does not specify one.
const defaultTopK = 5

// retrievalResult bundles structured hybrid search output.
type retrievalResult struct {
	Chunks        []vector.SearchResult             `json:"chunks"`
	Entities      []vector.EntitySearchResult       `json:"entities,omitempty"`
	Relationships []vector.RelationshipSearchResult `json:"relationships,omitempty"`
	Facts         []graph.SearchResult              `json:"facts"`
}

// retrieve performs both vector and graph search and returns structured results.
//
// Retrieval is two-stage: a wide candidate pool is pulled from the vector
// store (so relevant chunks from any document can surface), then narrowed to
// topK — by the reranker when configured, by similarity order otherwise —
// with a per-source cap so a single document cannot monopolize the context.
func (s *Server) retrieve(ctx context.Context, query string, topK int) (retrievalResult, error) {
	topK = s.topKOrDefault(topK)
	s.log.Debug("hybrid search starting", "query", query, "top_k", topK)

	candidateK := candidateDepth(topK, s.vectorStore.Count())

	var vectorResults []vector.SearchResult
	if candidateK > 0 {
		var err error
		vectorResults, err = s.vectorStore.Query(ctx, query, candidateK)
		if err != nil {
			s.log.Error("vector search failed", "error", err, "query", query)
			return retrievalResult{}, fmt.Errorf("vector search: %w", err)
		}
	}

	// Dense entity retrieval: find key entities matching query semantically
	var entityResults []vector.EntitySearchResult
	if s.vectorStore.EntityCount() > 0 {
		var err error
		entityResults, err = s.vectorStore.QueryEntities(ctx, query, 5)
		if err != nil {
			s.log.Warn("entity vector search failed (non-fatal)", "error", err, "query", query)
		}
	}

	var seedEntities []string
	for _, e := range entityResults {
		if e.Similarity > 0.4 {
			seedEntities = append(seedEntities, e.Name)
		}
	}

	// Dense relationship retrieval: find key relationships matching query semantically
	var relResults []vector.RelationshipSearchResult
	if s.vectorStore.RelationshipCount() > 0 {
		var err error
		relResults, err = s.vectorStore.QueryRelationships(ctx, query, 5)
		if err != nil {
			s.log.Warn("relationship vector search failed (non-fatal)", "error", err, "query", query)
		}
	}

	var seedTriples []graph.Triple
	for _, r := range relResults {
		if r.Similarity > 0.4 {
			seedTriples = append(seedTriples, graph.Triple{
				Subject:   r.Subject,
				Predicate: r.Predicate,
				Object:    r.Object,
			})
		}
	}

	// Rerank before fusion so the semantic route contributes its best ordering.
	vectorResults = s.rerank(ctx, query, vectorResults)

	// Routes 2 and 3: BM25, and exact structural lookup. Dense embeddings
	// cannot match an exact token, so a query naming a verse number is
	// unanswerable by similarity alone.
	lexIDs, exact := lexicalRoutes(s.lexicalIndex, s.fusionCfg.router, query, candidateK)

	s.log.Info("routes completed", "query", query,
		"vector", len(vectorResults), "lexical", len(lexIDs), "exact", len(exact),
		"entities", len(entityResults), "relationships", len(relResults))

	lists := map[string][]string{}
	byID := map[string]vector.SearchResult{}

	vecIDs := make([]string, 0, len(vectorResults))
	for _, r := range vectorResults {
		vecIDs = append(vecIDs, r.ID)
		byID[r.ID] = r
	}
	lists["vector"] = vecIDs

	lists["lexical"] = lexIDs
	if len(exact) > 0 {
		lists["exact"] = exact
	}

	cands := fuseRankLists(lists)

	// Materialize any candidate that only a non-vector route found.
	for id, c := range cands {
		if r, ok := byID[id]; ok {
			c.result = r
			continue
		}
		r, err := s.vectorStore.GetByID(ctx, id)
		if err != nil {
			s.log.Debug("could not materialize chunk", "id", id, "error", err)
			delete(cands, id)
			continue
		}
		c.result = r
	}

	for _, c := range cands {
		if containsRoute(c.routes, "exact") {
			c.score += exactRefBoost
		}
		applyNoisePenalty(c)
	}

	ranked := rankCandidates(cands)
	ranked = dedupeNearDuplicates(ranked, nearDuplicateThreshold)
	selected := selectDiverse(ranked, topK, s.fusionCfg)

	chunks := make([]vector.SearchResult, 0, len(selected))
	for _, c := range selected {
		chunks = append(chunks, c.result)
	}

	// Graph search runs last so it can be disambiguated by the vector layer.
	// Graph matching is seeded with text query tokens, semantic entity matches, and semantic relationship matches.
	graphResults := s.searchGraphInContext(ctx, query, seedEntities, seedTriples, chunks, s.graphFactLimit())

	return retrievalResult{
		Chunks:        chunks,
		Entities:      entityResults,
		Relationships: relResults,
		Facts:         graphResults,
	}, nil
}

// nearDuplicateThreshold is the shingle overlap above which two chunks are
// treated as the same passage. Chunks carry a build-time overlap tail, so
// adjacent chunks are genuinely near-identical.
const nearDuplicateThreshold = 0.8

// candidateDepth sizes the candidate pool.
//
// The previous fixed ceiling of 40 was the hard recall limit for the whole
// system: no chunk outside the top-40 cosine neighbours could ever be reranked
// or fused in, so top_k=1000 and top_k=10 fetched exactly the same candidates.
// Depth now scales with the request and with corpus size.
func candidateDepth(topK, corpusSize int) int {
	depth := topK * 20
	if depth < minCandidateDepth {
		depth = minCandidateDepth
	}
	if depth > maxCandidateDepth {
		depth = maxCandidateDepth
	}
	if corpusSize > 0 && depth > corpusSize {
		depth = corpusSize
	}
	return depth
}

const (
	minCandidateDepth = 200
	maxCandidateDepth = 2000
)

// rerank reorders candidates with the configured reranker, falling back to
// similarity order on any failure.
func (s *Server) rerank(ctx context.Context, query string, in []vector.SearchResult) []vector.SearchResult {
	if s.reranker == nil || len(in) == 0 {
		return in
	}

	docs := make([]string, len(in))
	for i, r := range in {
		docs[i] = r.Content
	}

	out, err := s.reranker.Rerank(ctx, query, docs)
	if err != nil {
		s.log.Warn("reranker failed (using similarity order)", "error", err)
		return in
	}
	if len(out) == 0 {
		s.log.Warn("reranker returned no results (using similarity order)")
		return in
	}

	ranked := make([]vector.SearchResult, 0, len(out))
	for _, r := range out {
		// A provider echoing an index from a different document set would
		// otherwise panic the request, and there is no recover middleware.
		if r.Index < 0 || r.Index >= len(in) {
			s.log.Warn("reranker returned out-of-range index", "index", r.Index, "candidates", len(in))
			continue
		}
		ranked = append(ranked, in[r.Index])
	}
	if len(ranked) == 0 {
		return in
	}

	s.log.Info("reranker completed", "candidates", len(ranked),
		"top_score", fmt.Sprintf("%.3f", out[0].RelevanceScore))
	return ranked
}

func containsRoute(routes []string, want string) bool {
	for _, r := range routes {
		if r == want {
			return true
		}
	}
	return false
}

// defaultGraphFactLimit is the number of knowledge-graph facts injected as
// context when agent.yaml does not override it.
const defaultGraphFactLimit = 10

// graphHops is how far retrieval traverses from a directly matching fact.
// 1 surfaces connected chains without letting the neighbourhood explode.
const graphHops = 1

// graphHopSharePct reserves a share of the injected facts for traversed
// results, which would otherwise always be truncated away by their decayed score.
const graphHopSharePct = 30

// contextBoost multiplies the score of a fact whose source document also
// surfaced in semantic retrieval.
const contextBoost = 2.5

// searchGraphInContext runs a graph search and re-ranks the results using the
// source documents that semantic retrieval selected, resolving homonyms that
// pure string matching cannot.
func (s *Server) searchGraphInContext(ctx context.Context, query string, seedEntities []string, seedTriples []graph.Triple, chunks []vector.SearchResult, limit int) []graph.SearchResult {
	// Pull a wider candidate pool so promotion has something to promote from.
	// One hop of traversal surfaces connected chains (A taught B, B founded C)
	// that a flat term match would never reach. Seed entities and triples from
	// dense vector retrieval seed graph traversal even when query terms differ.
	candidates, err := s.graphDB.SearchWithSeeds(ctx, query, seedEntities, seedTriples, limit*4, graphHops)
	if err != nil {
		s.log.Warn("graph search failed (non-fatal)", "error", err, "query", query)
		return nil
	}

	ranked := rankFactsByContext(candidates, chunks, limit)
	s.log.Info("graph search completed", "results", len(ranked),
		"candidates", len(candidates), "query", query)
	return ranked
}

// rankFactsByContext promotes facts whose source document also surfaced in
// semantic retrieval, then truncates to limit. This is what separates the
// alchemical sense of a term from its karmic sense: both match the query
// string, but only one appears in the documents the embedder selected.
func rankFactsByContext(candidates []graph.SearchResult, chunks []vector.SearchResult, limit int) []graph.SearchResult {
	// No semantic signal to disambiguate with — return as-is
	if len(chunks) == 0 {
		if len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}

	inContext := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		if c.Source != "" {
			inContext[c.Source] = true
		}
	}

	ranked := make([]graph.SearchResult, len(candidates))
	copy(ranked, candidates)
	for i := range ranked {
		if ranked[i].Source != "" && inContext[ranked[i].Source] {
			ranked[i].Score *= contextBoost
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})
	if len(ranked) <= limit {
		return ranked
	}

	// Traversed facts carry a decayed score by construction, so a plain
	// truncation would always drop them. Reserve a small quota so connected
	// chains survive into the context, and backfill if either side is short.
	hopQuota := limit * graphHopSharePct / 100
	direct := make([]graph.SearchResult, 0, limit)
	hops := make([]graph.SearchResult, 0, hopQuota)
	for _, r := range ranked {
		if r.Hop > 0 {
			if len(hops) < hopQuota {
				hops = append(hops, r)
			}
		} else if len(direct) < limit-hopQuota {
			direct = append(direct, r)
		}
	}
	out := append(direct, hops...)
	if len(out) < limit {
		for _, r := range ranked {
			if len(out) >= limit {
				break
			}
			if !containsFact(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// containsFact reports whether a fact is already present in the slice.
func containsFact(list []graph.SearchResult, r graph.SearchResult) bool {
	for _, x := range list {
		if x.Subject == r.Subject && x.Predicate == r.Predicate && x.Object == r.Object {
			return true
		}
	}
	return false
}

// hybridSearch runs retrieve and formats the results as a context string for
// injection into LLM prompts.
func (s *Server) hybridSearch(ctx context.Context, query string, topK int) (string, error) {
	result, err := s.retrieve(ctx, query, topK)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	if len(result.Entities) > 0 {
		sb.WriteString("## Relevant Entities\n\n")
		for _, e := range result.Entities {
			fmt.Fprintf(&sb, "- **%s**: %s\n", e.Name, e.Description)
		}
		sb.WriteString("\n")
	}

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

// handleHealth returns a detailed health status including all key metrics.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"status":           "ok",
		"agent":            s.agentCfg.Agent.Name,
		"version":          s.agentCfg.Agent.Version,
		"corpus_version":   s.corpusVersion,
		"vectors":          s.vectorStore.Count(),
		"entities":         s.vectorStore.EntityCount(),
		"relationships":    s.vectorStore.RelationshipCount(),
		"triples":          s.graphDB.Count(),
		"mcp_tools":        len(s.agentCfg.MCP.Tools),
		"embed_dimensions": s.appCfg.Embedder.Dimensions,
		"llm_model":        s.appCfg.LLM.Model,
		"embed_model":      s.appCfg.Embedder.Model,
		"reranker_enabled": s.reranker != nil,
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
