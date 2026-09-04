package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Kash writes its outputs into the same directory it reads documents from, so
// without a skip list every build walked its own embedded stores and reported
// the manifest plus one line per gob file as "not indexed".
func TestLoadDirectorySkipsBuildArtifacts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.md"), []byte("# Real\n\nBody."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "build.manifest.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entity_aliases.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lexical.idx"), []byte("gob"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".manifest-123.tmp"), []byte("x"), 0o644))

	store := filepath.Join(dir, "memory.chromem", "9b16b44d")
	require.NoError(t, os.MkdirAll(store, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(store, "0001.gob"), []byte("gob"), 0o644))

	docs, rejected, err := LoadDirectory(dir)
	require.NoError(t, err)

	require.Len(t, docs, 1, "only the real document should be loaded")
	assert.Equal(t, "real.md", docs[0].Name)
	assert.Empty(t, rejected, "build artifacts must not be reported as failed documents, got %v", rejected)
}

// A genuinely unsupported source file must still be reported — the skip list
// must not become a way to lose documents silently.
func TestLoadDirectoryStillReportsUnsupportedSources(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.docx"), []byte("x"), 0o644))

	docs, rejected, err := LoadDirectory(dir)
	require.NoError(t, err)
	assert.Empty(t, docs)
	require.Len(t, rejected, 1)
	assert.Contains(t, rejected[0].Reason, "unsupported format")
}
