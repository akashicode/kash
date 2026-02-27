package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/akashicode/kash/internal/config"
)

// ErrNilRerankConfig is returned when nil rerank config is provided.
var ErrNilRerankConfig = errors.New("reranker config is nil")

// RerankResult represents a reranked document.
type RerankResult struct {
	Index          int
	RelevanceScore float64
	Content        string
}

// Reranker reranks documents using a Cohere-compatible reranking API.
type Reranker struct {
	endpoint string // fully-resolved POST URL, e.g. https://api.cohere.ai/v1/rerank
	apiKey   string
	model    string
	client   *http.Client
}

// NewReranker creates a new Reranker from a ProviderConfig.
// Returns nil, nil if the config has no model or base URL (reranker is optional).
//
// Endpoint resolution order:
//  1. RERANK_ENDPOINT env var (full URL override)
//  2. If base_url already contains "/rerank", use it as the full endpoint
//  3. Otherwise append "/rerank" to base_url
func NewReranker(cfg *config.ProviderConfig) (*Reranker, error) {
	if cfg == nil {
		return nil, ErrNilRerankConfig
	}
	// Reranker is optional
	if cfg.Model == "" || cfg.BaseURL == "" {
		return nil, nil
	}

	// Resolve the full rerank endpoint URL
	endpoint := os.Getenv("RERANK_ENDPOINT")
	if endpoint == "" {
		base := strings.TrimSuffix(cfg.BaseURL, "/")
		if strings.Contains(base, "/rerank") {
			// base_url already points directly at the rerank endpoint
			endpoint = base
		} else {
			endpoint = base + "/rerank"
		}
	}

	return &Reranker{
		endpoint: endpoint,
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		client:   &http.Client{},
	}, nil
}

// rerankRequest is the Cohere-compatible rerank request body.
type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

// rerankResponse is the Cohere-compatible rerank response body.
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank reorders documents by relevance to the query using the configured API.
// If the Reranker is nil (not configured), returns documents in original order.
func (r *Reranker) Rerank(ctx context.Context, query string, docs []string) ([]RerankResult, error) {
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

	reqBody := rerankRequest{
		Model:     r.model,
		Query:     query,
		Documents: docs,
		TopN:      len(docs),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal rerank request: %w", err)
	}

	url := r.endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rerank response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var rerankResp rerankResponse
	if err := json.Unmarshal(respBody, &rerankResp); err != nil {
		return nil, fmt.Errorf("unmarshal rerank response: %w", err)
	}

	if len(rerankResp.Results) == 0 {
		return nil, errors.New("rerank API returned no results")
	}

	// Sort by relevance score descending
	sort.Slice(rerankResp.Results, func(i, j int) bool {
		return rerankResp.Results[i].RelevanceScore > rerankResp.Results[j].RelevanceScore
	})

	results := make([]RerankResult, len(rerankResp.Results))
	for i, r := range rerankResp.Results {
		results[i] = RerankResult{
			Index:          r.Index,
			RelevanceScore: r.RelevanceScore,
			Content:        docs[r.Index],
		}
	}
	return results, nil
}
