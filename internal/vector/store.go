package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"

	chromem "github.com/philippgille/chromem-go"

	"github.com/agent-forge/agent-forge/internal/chunker"
	"github.com/agent-forge/agent-forge/internal/config"
)

// ErrNilConfig is returned when a nil config is provided.
var ErrNilConfig = errors.New("vector store config is nil")

// ErrNotFound is returned when a query returns no results.
var ErrNotFound = errors.New("no results found")

// Document represents a document stored in the vector store.
type Document struct {
	ID       string
	Content  string
	Source   string
	Metadata map[string]string
}

// SearchResult represents a single vector search result.
type SearchResult struct {
	ID         string
	Content    string
	Source     string
	Similarity float32
	Metadata   map[string]string
}

// Store wraps a chromem-go database for vector operations.
type Store struct {
	db         *chromem.DB
	collection *chromem.Collection
	embedCfg   *config.ProviderConfig
}

// NewStore creates a new vector Store backed by an in-memory chromem-go database.
func NewStore(embedCfg *config.ProviderConfig) (*Store, error) {
	if embedCfg == nil {
		return nil, ErrNilConfig
	}

	db := chromem.NewDB()

	embeddingFunc := newEmbeddingFuncWithDimensions(embedCfg)

	collection, err := db.CreateCollection("documents", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("create collection: %w", err)
	}

	return &Store{
		db:         db,
		collection: collection,
		embedCfg:   embedCfg,
	}, nil
}

// NewStoreFromPath loads a persisted chromem-go database from disk.
func NewStoreFromPath(path string, embedCfg *config.ProviderConfig) (*Store, error) {
	if embedCfg == nil {
		return nil, ErrNilConfig
	}

	db, err := chromem.NewPersistentDB(path, false)
	if err != nil {
		return nil, fmt.Errorf("open persistent db at %q: %w", path, err)
	}

	embeddingFunc := newEmbeddingFuncWithDimensions(embedCfg)

	collection := db.GetCollection("documents", embeddingFunc)
	if collection == nil {
		// Create it if it doesn't exist yet
		collection, err = db.CreateCollection("documents", nil, embeddingFunc)
		if err != nil {
			return nil, fmt.Errorf("create collection: %w", err)
		}
	}

	return &Store{
		db:         db,
		collection: collection,
		embedCfg:   embedCfg,
	}, nil
}

// NewPersistentStore creates a Store backed by a persistent on-disk chromem-go database.
func NewPersistentStore(path string, embedCfg *config.ProviderConfig) (*Store, error) {
	if embedCfg == nil {
		return nil, ErrNilConfig
	}

	db, err := chromem.NewPersistentDB(path, false)
	if err != nil {
		return nil, fmt.Errorf("create persistent db at %q: %w", path, err)
	}

	embeddingFunc := newEmbeddingFuncWithDimensions(embedCfg)

	collection, err := db.CreateCollection("documents", nil, embeddingFunc)
	if err != nil {
		// Collection may already exist
		existing := db.GetCollection("documents", embeddingFunc)
		if existing == nil {
			return nil, fmt.Errorf("get or create collection: %w", err)
		}
		collection = existing
	}

	return &Store{
		db:         db,
		collection: collection,
		embedCfg:   embedCfg,
	}, nil
}

// AddChunks adds a batch of document chunks to the vector store.
func (s *Store) AddChunks(ctx context.Context, chunks []chunker.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	docs := make([]chromem.Document, len(chunks))
	for i, ch := range chunks {
		docs[i] = chromem.Document{
			ID:      ch.ID,
			Content: ch.Content,
			Metadata: map[string]string{
				"source": ch.Source,
				"index":  fmt.Sprintf("%d", ch.Index),
			},
		}
	}

	if err := s.collection.AddDocuments(ctx, docs, runtime.NumCPU()); err != nil {
		return fmt.Errorf("add documents to collection: %w", err)
	}
	return nil
}

// Query performs a semantic similarity search against the vector store.
func (s *Store) Query(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if topK <= 0 {
		topK = 5
	}

	results, err := s.collection.Query(ctx, query, topK, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("vector query: %w", err)
	}

	searchResults := make([]SearchResult, len(results))
	for i, r := range results {
		searchResults[i] = SearchResult{
			ID:         r.ID,
			Content:    r.Content,
			Source:     r.Metadata["source"],
			Similarity: r.Similarity,
			Metadata:   r.Metadata,
		}
	}
	return searchResults, nil
}

// Count returns the number of documents in the store.
func (s *Store) Count() int {
	return s.collection.Count()
}

// embedRequest is the request body for OpenAI-compatible embeddings.
type embedRequest struct {
	Input string `json:"input"`
	Model string `json:"model,omitempty"`
}

// embedResponse is the response body from an OpenAI-compatible embeddings API.
type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// newEmbeddingFuncWithDimensions returns a chromem-go EmbeddingFunc that calls
// an OpenAI-compatible embeddings API. The configured dimensions are used only
// for local truncation — not sent in the API request. It is the user's
// responsibility to pick a model whose native output matches agent.yaml dimensions.
// If Model is empty it is omitted from the request (router-friendly).
func newEmbeddingFuncWithDimensions(cfg *config.ProviderConfig) chromem.EmbeddingFunc {
	client := &http.Client{}

	return func(ctx context.Context, text string) ([]float32, error) {
		reqBody := embedRequest{
			Input: text,
		}
		if cfg.Model != "" {
			reqBody.Model = cfg.Model
		}

		body, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal embedding request: %w", err)
		}

		url := cfg.BaseURL + "/embeddings"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create embedding request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("embedding request: %w", err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read embedding response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(respBody))
		}

		var embedResp embedResponse
		if err := json.Unmarshal(respBody, &embedResp); err != nil {
			return nil, fmt.Errorf("unmarshal embedding response: %w", err)
		}

		if len(embedResp.Data) == 0 || len(embedResp.Data[0].Embedding) == 0 {
			return nil, errors.New("embedding API returned no embeddings")
		}

		v := embedResp.Data[0].Embedding

		// Truncate or validate dimension
		if cfg.Dimensions > 0 && len(v) > cfg.Dimensions {
			v = v[:cfg.Dimensions]
		}

		return v, nil
	}
}
