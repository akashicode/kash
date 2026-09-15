package manifest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrNew_MissingFile(t *testing.T) {
	m, err := LoadOrNew(filepath.Join(t.TempDir(), "does-not-exist.json"))
	require.NoError(t, err)
	assert.Equal(t, 0, m.Version)
	assert.NotNil(t, m.Documents)
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	m := New()
	m.Version = 2
	m.EmbedModel = "voyage-3"
	m.EmbedDimensions = 1024
	m.ChunkSize = 2000
	m.ChunkOverlap = 400
	m.Documents["book1.pdf"] = &DocState{
		SHA256:           "abc123",
		Chunks:           42,
		Triples:          17,
		VectorDone:       true,
		GraphBatchesDone: 5,
		GraphDone:        true,
		CompletedAt:      time.Now().UTC(),
	}
	m.Documents["book2.pdf"] = &DocState{
		SHA256:           "def456",
		VectorDone:       true,
		GraphBatchesDone: 2, // interrupted mid-extraction
	}
	require.NoError(t, m.Save(path))

	loaded, err := LoadOrNew(path)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.Version)
	assert.Equal(t, "voyage-3", loaded.EmbedModel)
	assert.Equal(t, 1024, loaded.EmbedDimensions)

	done := loaded.Documents["book1.pdf"]
	require.NotNil(t, done)
	assert.True(t, done.Done())
	assert.Equal(t, 42, done.Chunks)

	partial := loaded.Documents["book2.pdf"]
	require.NotNil(t, partial)
	assert.False(t, partial.Done())
	assert.Equal(t, 2, partial.GraphBatchesDone)
}

func TestNeedsFinalize(t *testing.T) {
	doc := func() map[string]*DocState {
		return map[string]*DocState{"book.md": {SHA256: "abc", VectorDone: true, GraphDone: true}}
	}
	tests := []struct {
		name string
		m    *Manifest
		want bool
	}{
		{
			name: "fresh manifest",
			m:    New(),
			want: false,
		},
		{
			name: "finished build",
			m:    &Manifest{Version: 3, Documents: doc()},
			want: false,
		},
		{
			name: "flag set by an interrupted build",
			m:    &Manifest{Version: 3, PendingFinalize: true, Documents: doc()},
			want: true,
		},
		{
			// Written before the flag existed: every document is done, yet the
			// build that did them never reached the version bump at its end.
			name: "legacy manifest with documents at version 0",
			m:    &Manifest{Version: 0, Documents: doc()},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.m.NeedsFinalize())
		})
	}
}

func TestPendingFinalizeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	m := New()
	m.Version = 1
	m.PendingFinalize = true
	require.NoError(t, m.Save(path))

	loaded, err := LoadOrNew(path)
	require.NoError(t, err)
	assert.True(t, loaded.PendingFinalize)
}

func TestHashContent(t *testing.T) {
	h1 := HashContent("some book text")
	h2 := HashContent("some book text")
	h3 := HashContent("different text")

	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
	assert.Len(t, h1, 64)
}
