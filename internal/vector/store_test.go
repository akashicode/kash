package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
)

// stubEmbedder serves an OpenAI-compatible /embeddings endpoint returning
// deterministic unit vectors, so store tests need no network or API key.
func stubEmbedder(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		// Derive a stable vector from the text so distinct texts differ
		var seed float32
		if len(req.Input) > 0 {
			for _, c := range req.Input[0] {
				seed += float32(c)
			}
		}
		vec := make([]float32, 4)
		vec[0] = 1 // non-zero so normalization never divides by zero
		vec[1] = seed / 100000

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"embedding":[%f,%f,0,0]}]}`, vec[0], vec[1])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testChunks(source string, n int) []chunker.Chunk {
	chunks := make([]chunker.Chunk, n)
	for i := range chunks {
		chunks[i] = chunker.Chunk{
			ID:      fmt.Sprintf("%s_%d", source, i),
			Content: fmt.Sprintf("content of %s chunk %d", source, i),
			Source:  source,
			Index:   i,
		}
	}
	return chunks
}

// TestPersistentStoreReopenPreservesDocuments is the regression test for the
// bug where reopening a persisted store reported 0 vectors: chromem's
// CreateCollection silently replaces an existing collection with an empty one,
// discarding everything loaded from disk.
func TestPersistentStoreReopenPreservesDocuments(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	dir := t.TempDir()
	ctx := context.Background()

	// First build: add 10 chunks
	vs, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	require.NoError(t, vs.AddChunks(ctx, testChunks("book-a.pdf", 10), false))
	require.Equal(t, 10, vs.Count())

	// Second build: reopening must see the previously persisted documents
	vs2, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 10, vs2.Count(), "reopened store must retain persisted documents")

	// Adding a second document accumulates rather than replacing
	require.NoError(t, vs2.AddChunks(ctx, testChunks("book-b.pdf", 5), false))
	assert.Equal(t, 15, vs2.Count())

	// Serve path must see the same corpus
	vs3, err := NewStoreFromPath(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 15, vs3.Count(), "serve path must see all persisted documents")
}

// TestDeleteBySourceAfterReopen guards the incremental-build path: replacing a
// changed document must actually remove its old chunks, which is only possible
// if the reopened collection contains them.
func TestDeleteBySourceAfterReopen(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	dir := t.TempDir()
	ctx := context.Background()

	vs, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	require.NoError(t, vs.AddChunks(ctx, testChunks("old.pdf", 8), false))
	require.NoError(t, vs.AddChunks(ctx, testChunks("keep.pdf", 3), false))
	require.Equal(t, 11, vs.Count())

	// Reopen (as an incremental build does) and delete one document's chunks
	vs2, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	require.NoError(t, vs2.DeleteBySource(ctx, "old.pdf"))
	assert.Equal(t, 3, vs2.Count(), "stale chunks must be removed on reopen")

	// And the deletion must survive another reopen
	vs3, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 3, vs3.Count(), "deletion must persist to disk")
}

func TestDeleteBySourceRequiresSource(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	vs, err := NewPersistentStore(t.TempDir(), cfg)
	require.NoError(t, err)
	assert.Error(t, vs.DeleteBySource(context.Background(), ""))
}

func TestEntityDescriptions(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	dir := t.TempDir()
	ctx := context.Background()

	vs, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)

	// Initially empty
	assert.Equal(t, 0, vs.EntityCount())

	entities := []EntityDesc{
		{
			Name:        "Abhinavagupta",
			Description: "10th-century philosopher and master of Kashmir Shaivism.",
			Degree:      8,
			Aliases:     []string{"Abhinava", "Acarya Abhinavagupta"},
		},
		{
			Name:        "Gorakhnath",
			Description: "Spiritual master and legendary founder of the Nath tradition.",
			Degree:      5,
			Aliases:     []string{"Gorakhnatha"},
		},
	}

	require.NoError(t, vs.AddEntityDescriptions(ctx, entities))
	assert.Equal(t, 2, vs.EntityCount())
	// Regular document collection must be unaffected
	assert.Equal(t, 0, vs.Count())

	// Query entities
	results, err := vs.QueryEntities(ctx, "philosopher of Kashmir Shaivism", 2)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Verify fields populated
	found := false
	for _, r := range results {
		if r.Name == "Abhinavagupta" {
			found = true
			assert.Equal(t, 8, r.Degree)
			assert.Contains(t, r.Aliases, "Abhinava")
			assert.Contains(t, r.Description, "10th-century philosopher")
			assert.Greater(t, r.Similarity, float32(0))
		}
	}
	assert.True(t, found, "Abhinavagupta must be returned")

	// Empty query returns error
	_, err = vs.QueryEntities(ctx, "", 2)
	assert.Error(t, err)

	// Clear entity descriptions
	require.NoError(t, vs.ClearEntityDescriptions(ctx))
	assert.Equal(t, 0, vs.EntityCount())
}

func TestEntityDescriptionsReopenPreserves(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	dir := t.TempDir()
	ctx := context.Background()

	vs, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)

	entities := []EntityDesc{
		{
			Name:        "Siva",
			Description: "Supreme deity representing supreme consciousness.",
			Degree:      20,
		},
	}
	require.NoError(t, vs.AddEntityDescriptions(ctx, entities))
	assert.Equal(t, 1, vs.EntityCount())

	// Reopen persistent store
	vs2, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, vs2.EntityCount(), "reopened persistent store must retain entities")

	// Reopen with NewStoreFromPath (runtime serve path)
	vs3, err := NewStoreFromPath(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, vs3.EntityCount(), "serve path must retain entities")

	results, err := vs3.QueryEntities(ctx, "supreme deity", 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Siva", results[0].Name)
}

func TestRelationships(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	dir := t.TempDir()
	ctx := context.Background()

	vs, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)

	assert.Equal(t, 0, vs.RelationshipCount())

	rels := []RelationshipDoc{
		{
			Subject:     "Gorakhnath",
			Predicate:   "founded",
			Object:      "Nath tradition",
			Description: "Established the Nath sampradaya and yogic discipline.",
			Source:      "nath.md",
		},
		{
			Subject:     "Gorakhnath",
			Predicate:   "was disciple of",
			Object:      "Matsyendranath",
			Description: "Studied under the master Matsyendranath.",
			Source:      "nath.md",
		},
		{
			Subject:   "Abhinavagupta",
			Predicate: "authored",
			Object:    "Tantraloka",
			Source:    "tantra.md",
		},
	}

	require.NoError(t, vs.AddRelationships(ctx, rels))
	assert.Equal(t, 3, vs.RelationshipCount())
	// Document and Entity counts must remain isolated
	assert.Equal(t, 0, vs.Count())
	assert.Equal(t, 0, vs.EntityCount())

	// Query relationships
	results, err := vs.QueryRelationships(ctx, "founder of Nath tradition", 2)
	require.NoError(t, err)
	require.Len(t, results, 2)

	found := false
	for _, r := range results {
		if r.Subject == "Gorakhnath" && r.Predicate == "founded" && r.Object == "Nath tradition" {
			found = true
			assert.Equal(t, "nath.md", r.Source)
			assert.Equal(t, "Established the Nath sampradaya and yogic discipline.", r.Description)
			assert.Greater(t, r.Similarity, float32(0))
		}
	}
	assert.True(t, found, "matching relationship must be returned")

	// Empty query returns error
	_, err = vs.QueryRelationships(ctx, "", 2)
	assert.Error(t, err)

	// Delete by source
	require.NoError(t, vs.DeleteRelationshipsBySource(ctx, "nath.md"))
	assert.Equal(t, 1, vs.RelationshipCount())

	// Clear remaining
	require.NoError(t, vs.ClearRelationships(ctx))
	assert.Equal(t, 0, vs.RelationshipCount())
}

func TestRelationshipsReopenPreserves(t *testing.T) {
	srv := stubEmbedder(t)
	cfg := &config.ProviderConfig{BaseURL: srv.URL, APIKey: "test", Model: "test-embed"}
	dir := t.TempDir()
	ctx := context.Background()

	vs, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)

	rels := []RelationshipDoc{
		{
			Subject:   "Kundalini",
			Predicate: "pierces",
			Object:    "Chakras",
			Source:    "yoga.md",
		},
	}
	require.NoError(t, vs.AddRelationships(ctx, rels))
	assert.Equal(t, 1, vs.RelationshipCount())

	// Reopen persistent store
	vs2, err := NewPersistentStore(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, vs2.RelationshipCount(), "reopened persistent store must retain relationships")

	// Reopen with NewStoreFromPath (serve path)
	vs3, err := NewStoreFromPath(dir, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, vs3.RelationshipCount(), "serve path must retain relationships")

	results, err := vs3.QueryRelationships(ctx, "kundalini chakras", 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Kundalini", results[0].Subject)
}
