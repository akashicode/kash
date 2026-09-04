package vector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/httpx"
)

// warnTruncation ensures the dimension-truncation warning is printed once per
// process rather than once per embedded chunk.
var warnTruncation sync.Once

// ErrNilConfig is returned when a nil config is provided.
var ErrNilConfig = errors.New("vector store config is nil")

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

// EntityDesc represents an entity node and its description to embed.
type EntityDesc struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Degree      int      `json:"degree,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// EntitySearchResult represents a single result from an entity vector search.
type EntitySearchResult struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Similarity  float32           `json:"similarity"`
	Degree      int               `json:"degree,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// RelationshipDoc represents a graph relationship/triple and its description to embed.
type RelationshipDoc struct {
	Subject     string  `json:"subject"`
	Predicate   string  `json:"predicate"`
	Object      string  `json:"object"`
	Description string  `json:"description,omitempty"`
	Source      string  `json:"source,omitempty"`
	ChunkID     string  `json:"chunk_id,omitempty"`
	Weight      float64 `json:"weight,omitempty"`
}

// RelationshipSearchResult represents a single result from a relationship vector search.
type RelationshipSearchResult struct {
	Subject     string            `json:"subject"`
	Predicate   string            `json:"predicate"`
	Object      string            `json:"object"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source,omitempty"`
	ChunkID     string            `json:"chunk_id,omitempty"`
	Similarity  float32           `json:"similarity"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Store wraps a chromem-go database for vector operations.
type Store struct {
	db                      *chromem.DB
	collection              *chromem.Collection
	entitiesCollection      *chromem.Collection
	relationshipsCollection *chromem.Collection
	embedCfg                *config.ProviderConfig
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

	// GetOrCreateCollection preserves documents already loaded from disk.
	// CreateCollection must NOT be used here: it unconditionally replaces the
	// loaded collection with an empty one (see NewPersistentStore).
	collection, err := db.GetOrCreateCollection("documents", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get or create collection: %w", err)
	}

	entitiesCollection, err := db.GetOrCreateCollection("entities", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get or create entities collection: %w", err)
	}

	relCollection, err := db.GetOrCreateCollection("relationships", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get or create relationships collection: %w", err)
	}

	return &Store{
		db:                      db,
		collection:              collection,
		entitiesCollection:      entitiesCollection,
		relationshipsCollection: relCollection,
		embedCfg:                embedCfg,
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

	// CRITICAL: chromem's CreateCollection never errors on an existing
	// collection — it silently replaces it with an empty one, discarding every
	// document NewPersistentDB just loaded from disk. That made incremental
	// builds report 0 vectors and left DeleteBySource unable to see (and so
	// unable to remove) a changed document's stale chunks. Always get-or-create.
	collection, err := db.GetOrCreateCollection("documents", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get or create collection: %w", err)
	}

	entitiesCollection, err := db.GetOrCreateCollection("entities", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get or create entities collection: %w", err)
	}

	relCollection, err := db.GetOrCreateCollection("relationships", nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get or create relationships collection: %w", err)
	}

	return &Store{
		db:                      db,
		collection:              collection,
		entitiesCollection:      entitiesCollection,
		relationshipsCollection: relCollection,
		embedCfg:                embedCfg,
	}, nil
}

// chunkMetadata builds the stored metadata for a chunk: the structural fields
// the chunker derived (book, breadcrumb, verse, content type, noise score) plus
// the source and index every chunk carries. Structure is stored because a query
// naming a verse number is an exact match against metadata but carries almost no
// signal in a dense embedding.
func chunkMetadata(ch chunker.Chunk) map[string]string {
	meta := make(map[string]string, len(ch.Metadata)+2)
	for k, v := range ch.Metadata {
		if v != "" {
			meta[k] = v
		}
	}
	meta["source"] = ch.Source
	meta["index"] = fmt.Sprintf("%d", ch.Index)
	return meta
}

// AddChunks adds a batch of document chunks to the vector store.
// When parallel is true, all documents are embedded concurrently using all CPU
// cores (ideal for local embedders). When false, documents are added in small
// sequential batches with retry/backoff (safe for hosted APIs with rate limits).
func (s *Store) AddChunks(ctx context.Context, chunks []chunker.Chunk, parallel bool) error {
	if len(chunks) == 0 {
		return nil
	}

	if parallel {
		return s.addChunksParallel(ctx, chunks)
	}
	return s.addChunksSequential(ctx, chunks)
}

// addChunksParallel adds all chunks concurrently using runtime.NumCPU().
func (s *Store) addChunksParallel(ctx context.Context, chunks []chunker.Chunk) error {
	docs := make([]chromem.Document, len(chunks))
	for i, ch := range chunks {
		docs[i] = chromem.Document{
			ID:       ch.ID,
			Content:  ch.Content,
			Metadata: chunkMetadata(ch),
		}
	}
	if err := s.collection.AddDocuments(ctx, docs, runtime.NumCPU()); err != nil {
		return fmt.Errorf("add documents to collection: %w", err)
	}
	return nil
}

// addChunksSequential adds chunks in small batches with concurrency=1 and
// retries with exponential backoff on 429 rate-limit errors.
func (s *Store) addChunksSequential(ctx context.Context, chunks []chunker.Chunk) error {
	const batchSize = 20
	const maxRetries = 5

	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}

		docs := make([]chromem.Document, end-i)
		for j, ch := range chunks[i:end] {
			docs[j] = chromem.Document{
				ID:       ch.ID,
				Content:  ch.Content,
				Metadata: chunkMetadata(ch),
			}
		}

		var err error
		for attempt := 0; attempt < maxRetries; attempt++ {
			err = s.collection.AddDocuments(ctx, docs, 1)
			if err == nil {
				break
			}
			if isRateLimitError(err) {
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				select {
				case <-time.After(backoff):
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			break
		}
		if err != nil {
			return fmt.Errorf("add documents to collection: %w", err)
		}
	}
	return nil
}

// isRateLimitError checks if an error message indicates a 429 rate limit.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "429") || strings.Contains(msg, "Too Many Requests") || strings.Contains(msg, "rate limit")
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

// GetByID fetches a single stored chunk. Lexical search returns chunk IDs
// without content, so fusion uses this to materialize hits that keyword search
// found but vector search missed — which is the whole point of running both.
func (s *Store) GetByID(ctx context.Context, id string) (SearchResult, error) {
	doc, err := s.collection.GetByID(ctx, id)
	if err != nil {
		return SearchResult{}, fmt.Errorf("get chunk %q: %w", id, err)
	}
	return SearchResult{
		ID:       doc.ID,
		Content:  doc.Content,
		Source:   doc.Metadata["source"],
		Metadata: doc.Metadata,
	}, nil
}

// Count returns the number of documents in the store.
func (s *Store) Count() int {
	return s.collection.Count()
}

// DeleteBySource removes all chunks originating from the given source
// document. Used by incremental builds to replace a changed document's data.
func (s *Store) DeleteBySource(ctx context.Context, source string) error {
	if source == "" {
		return errors.New("source cannot be empty")
	}
	if err := s.collection.Delete(ctx, map[string]string{"source": source}, nil); err != nil {
		return fmt.Errorf("delete chunks for source %q: %w", source, err)
	}
	return nil
}

// AddEntityDescriptions adds or updates entity descriptions in the entities collection.
func (s *Store) AddEntityDescriptions(ctx context.Context, entities []EntityDesc) error {
	if len(entities) == 0 {
		return nil
	}

	docs := make([]chromem.Document, len(entities))
	for i, e := range entities {
		content := e.Name
		if len(e.Aliases) > 0 {
			content += fmt.Sprintf("\nAlso known as: %s", strings.Join(e.Aliases, ", "))
		}
		if e.Description != "" {
			content += "\n" + e.Description
		}

		meta := map[string]string{
			"name":         e.Name,
			"content_type": "entity_description",
			"degree":       fmt.Sprintf("%d", e.Degree),
		}
		if len(e.Aliases) > 0 {
			meta["aliases"] = strings.Join(e.Aliases, ", ")
		}

		docs[i] = chromem.Document{
			ID:       "entity:" + e.Name,
			Content:  content,
			Metadata: meta,
		}
	}

	if err := s.entitiesCollection.AddDocuments(ctx, docs, runtime.NumCPU()); err != nil {
		return fmt.Errorf("add entities to collection: %w", err)
	}
	return nil
}

// QueryEntities performs a semantic similarity search against the entity descriptions.
func (s *Store) QueryEntities(ctx context.Context, query string, topK int) ([]EntitySearchResult, error) {
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if topK <= 0 {
		topK = 5
	}
	if s.entitiesCollection == nil || s.entitiesCollection.Count() == 0 {
		return nil, nil
	}

	results, err := s.entitiesCollection.Query(ctx, query, topK, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query entities: %w", err)
	}

	out := make([]EntitySearchResult, len(results))
	for i, r := range results {
		degree := 0
		if dStr := r.Metadata["degree"]; dStr != "" {
			fmt.Sscanf(dStr, "%d", &degree)
		}
		var aliases []string
		if aStr := r.Metadata["aliases"]; aStr != "" {
			for _, a := range strings.Split(aStr, ", ") {
				if a = strings.TrimSpace(a); a != "" {
					aliases = append(aliases, a)
				}
			}
		}
		name := r.Metadata["name"]
		if name == "" {
			name = strings.TrimPrefix(r.ID, "entity:")
		}

		desc := r.Content
		lines := strings.Split(r.Content, "\n")
		var descLines []string
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "Also known as:") {
				continue
			}
			descLines = append(descLines, line)
		}
		if len(descLines) > 0 {
			desc = strings.TrimSpace(strings.Join(descLines, "\n"))
		}

		out[i] = EntitySearchResult{
			Name:        name,
			Description: desc,
			Similarity:  r.Similarity,
			Degree:      degree,
			Aliases:     aliases,
			Metadata:    r.Metadata,
		}
	}
	return out, nil
}

// EntityCount returns the number of entity descriptions stored.
func (s *Store) EntityCount() int {
	if s.entitiesCollection == nil {
		return 0
	}
	return s.entitiesCollection.Count()
}

// ClearEntityDescriptions removes all entity descriptions from the entities collection.
func (s *Store) ClearEntityDescriptions(ctx context.Context) error {
	if s.entitiesCollection == nil || s.entitiesCollection.Count() == 0 {
		return nil
	}
	if err := s.entitiesCollection.Delete(ctx, map[string]string{"content_type": "entity_description"}, nil); err != nil {
		return fmt.Errorf("clear entity descriptions: %w", err)
	}
	return nil
}

// AddRelationships adds or updates relationship descriptions in the relationships collection.
func (s *Store) AddRelationships(ctx context.Context, rels []RelationshipDoc) error {
	if len(rels) == 0 {
		return nil
	}

	docs := make([]chromem.Document, len(rels))
	for i, r := range rels {
		content := fmt.Sprintf("%s %s %s", r.Subject, r.Predicate, r.Object)
		if r.Description != "" && r.Description != content {
			content += "\n" + r.Description
		}

		meta := map[string]string{
			"subject":      r.Subject,
			"predicate":    r.Predicate,
			"object":       r.Object,
			"source":       r.Source,
			"content_type": "relationship",
		}
		if r.ChunkID != "" {
			meta["chunk_id"] = r.ChunkID
		}
		if r.Description != "" {
			meta["description"] = r.Description
		}

		h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s", r.Subject, r.Predicate, r.Object)))
		id := "rel:" + hex.EncodeToString(h[:12])

		docs[i] = chromem.Document{
			ID:       id,
			Content:  content,
			Metadata: meta,
		}
	}

	if err := s.relationshipsCollection.AddDocuments(ctx, docs, runtime.NumCPU()); err != nil {
		return fmt.Errorf("add relationships to collection: %w", err)
	}
	return nil
}

// QueryRelationships performs a semantic similarity search against relationship descriptions.
func (s *Store) QueryRelationships(ctx context.Context, query string, topK int) ([]RelationshipSearchResult, error) {
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if topK <= 0 {
		topK = 5
	}
	if s.relationshipsCollection == nil || s.relationshipsCollection.Count() == 0 {
		return nil, nil
	}

	results, err := s.relationshipsCollection.Query(ctx, query, topK, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query relationships: %w", err)
	}

	out := make([]RelationshipSearchResult, len(results))
	for i, r := range results {
		desc := r.Metadata["description"]
		if desc == "" {
			lines := strings.Split(r.Content, "\n")
			if len(lines) > 1 {
				desc = strings.TrimSpace(strings.Join(lines[1:], "\n"))
			}
		}

		out[i] = RelationshipSearchResult{
			Subject:     r.Metadata["subject"],
			Predicate:   r.Metadata["predicate"],
			Object:      r.Metadata["object"],
			Description: desc,
			Source:      r.Metadata["source"],
			ChunkID:     r.Metadata["chunk_id"],
			Similarity:  r.Similarity,
			Metadata:    r.Metadata,
		}
	}
	return out, nil
}

// RelationshipCount returns the number of relationship descriptions stored.
func (s *Store) RelationshipCount() int {
	if s.relationshipsCollection == nil {
		return 0
	}
	return s.relationshipsCollection.Count()
}

// ClearRelationships removes all relationship descriptions from the relationships collection.
func (s *Store) ClearRelationships(ctx context.Context) error {
	if s.relationshipsCollection == nil || s.relationshipsCollection.Count() == 0 {
		return nil
	}
	if err := s.relationshipsCollection.Delete(ctx, map[string]string{"content_type": "relationship"}, nil); err != nil {
		return fmt.Errorf("clear relationships: %w", err)
	}
	return nil
}

// DeleteRelationshipsBySource removes all relationships originating from the given source document.
func (s *Store) DeleteRelationshipsBySource(ctx context.Context, source string) error {
	if source == "" {
		return errors.New("source cannot be empty")
	}
	if s.relationshipsCollection == nil || s.relationshipsCollection.Count() == 0 {
		return nil
	}
	if err := s.relationshipsCollection.Delete(ctx, map[string]string{"source": source}, nil); err != nil {
		return fmt.Errorf("delete relationships for source %q: %w", source, err)
	}
	return nil
}

// embedRequest is the request body for OpenAI-compatible embeddings.
// Input is sent as an array for maximum compatibility across providers/gateways.
type embedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model,omitempty"`
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
	// A build makes one of these calls per chunk, tens of thousands of times.
	// An unbounded client turns a single stalled request into a dead build.
	client := httpx.Provider(httpx.EmbedTimeout)

	return func(ctx context.Context, text string) ([]float32, error) {
		// Sanitize: trim whitespace, replace null bytes
		text = strings.TrimSpace(text)
		text = strings.ReplaceAll(text, "\x00", "")
		if text == "" {
			return nil, errors.New("empty text after sanitization")
		}

		reqBody := embedRequest{
			Input: []string{text}, // array format for broad API compatibility
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

		// Truncate to the configured dimension.
		//
		// chromem re-normalizes vectors on both add and query, so the magnitude
		// is handled downstream. What it cannot fix is meaning: truncation is
		// only safe for Matryoshka-trained models, which are explicitly trained
		// so that a prefix of the vector is a usable embedding. For any other
		// model, dropping the tail silently degrades every similarity score in
		// the corpus — so say so once rather than never.
		if cfg.Dimensions > 0 && len(v) > cfg.Dimensions {
			warnTruncation.Do(func() {
				fmt.Fprintf(os.Stderr,
					"warning: embedder returned %d dimensions but agent.yaml requests %d; "+
						"truncating. This is only safe for Matryoshka-capable models "+
						"(voyage-3/4, text-embedding-3). For any other model, set "+
						"runtime.embedder.dimensions to the model's native size.\n",
					len(v), cfg.Dimensions)
			})
			v = v[:cfg.Dimensions]
		}

		return v, nil
	}
}
