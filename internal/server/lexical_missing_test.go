package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/lexical"
)

// A missing lexical index loads as an empty one, so a corpus whose build
// stopped before indexing serves on vector search alone. Startup must say so.
func TestLexicalIndexMissing(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, lexical.FileName)
	require.NoError(t, os.WriteFile(present, []byte("x"), 0o644))
	absent := filepath.Join(dir, "missing", lexical.FileName)

	tests := []struct {
		name    string
		path    string
		vectors int
		want    bool
	}{
		{name: "corpus with chunks and no index", path: absent, vectors: 45392, want: true},
		{name: "corpus with chunks and an index", path: present, vectors: 45392, want: false},
		{name: "empty corpus has nothing to index", path: absent, vectors: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, lexicalIndexMissing(tt.path, tt.vectors))
		})
	}
}
