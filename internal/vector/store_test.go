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
