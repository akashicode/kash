package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-forge/agent-forge/internal/config"
)

// ErrNilRerankConfig is returned when nil rerank config is provided.
var ErrNilRerankConfig = errors.New("reranker config is nil")

// RerankResult represents a reranked document.
type RerankResult struct {
	Index          int
	RelevanceScore float64
	Content        string
}

// Reranker reranks documents using an OpenAI-compatible reranking API.
type Reranker struct {
	baseURL string
	apiKey  string
	model   string
}

// NewReranker creates a new Reranker from a ProviderConfig.
// Returns nil, nil if the config has no model (optional reranker).
func NewReranker(cfg *config.ProviderConfig) (*Reranker, error) {
	if cfg == nil {
		return nil, ErrNilRerankConfig
	}
	// Reranker is optional
	if cfg.Model == "" || cfg.BaseURL == "" {
		return nil, nil
	}
	return &Reranker{
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
	}, nil
}

// Rerank reorders documents by relevance to the query.
// If the Reranker is nil (not configured), returns documents in original order.
func (r *Reranker) Rerank(_ context.Context, query string, docs []string) ([]RerankResult, error) {
	if r == nil {
		// No reranker configured; return original order
		results := make([]RerankResult, len(docs))
		for i, doc := range docs {
			results[i] = RerankResult{
				Index:          i,
				RelevanceScore: 1.0,
				Content:        doc,
			}
		}
		return results, nil
	}

	// TODO: Call actual reranking API when available
	// For now, return original order (reranker endpoint varies by provider)
	_ = query
	_ = fmt.Sprintf("rerank via %s/%s", r.baseURL, r.model)

	results := make([]RerankResult, len(docs))
	for i, doc := range docs {
		results[i] = RerankResult{
			Index:          i,
			RelevanceScore: 1.0 - float64(i)*0.01,
			Content:        doc,
		}
	}
	return results, nil
}
