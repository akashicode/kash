package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/config"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	p := New()
	p.Complete = true
	p.KashVersion = "v1.2.3"
	mode := config.DiacriticIAST
	p.Config.Resolution.FoldDiacritics = &mode
	p.AddSignal("resolution.fold_diacritics", "iast", DecidedDetected, "IAST marks in 41/61 docs")

	require.NoError(t, p.Save(path))

	got, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, FormatVersion, got.Version)
	assert.Equal(t, Note, got.Note, "the file must explain itself")
	assert.True(t, got.Complete)
	assert.Equal(t, "v1.2.3", got.KashVersion)
	require.NotNil(t, got.Config.Resolution.FoldDiacritics)
	assert.Equal(t, config.DiacriticIAST, *got.Config.Resolution.FoldDiacritics)

	sig, ok := got.SignalFor("resolution.fold_diacritics")
	require.True(t, ok, "provenance must survive the round trip")
	assert.Equal(t, DecidedDetected, sig.DecidedBy)
	assert.Contains(t, sig.Evidence, "41/61")
}

// No profile is a valid state — the agent falls back to generic defaults.
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Nil(t, got.Overlay(), "a nil profile contributes no config layer")
}

func TestLoadCorruptFileReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse profile")
}

// A profile written by a newer kash must be refused rather than
// half-understood, since unknown fields would silently vanish.
func TestLoadRefusesNewerFormatVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	require.NoError(t, os.WriteFile(path, []byte(`{"version": 99}`), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade kash")
}

// The overlay must feed straight into the config resolver, since that is the
// entire point of mirroring agent.yaml's shape.
func TestOverlayResolvesThroughConfigLayers(t *testing.T) {
	p := New()
	mode := config.DiacriticIAST
	p.Config.Resolution.FoldDiacritics = &mode

	cfg, notes := config.ResolveDomainConfig(p.Overlay(), filepath.Join(t.TempDir(), "none.yaml"))

	assert.Equal(t, config.DiacriticIAST, cfg.Resolution.FoldDiacritics)
	require.NotEmpty(t, notes)
	assert.Equal(t, "profile", notes[0].Layer)
}

// The fingerprint must not depend on directory walk order.
func TestFingerprintIsOrderIndependent(t *testing.T) {
	a := Fingerprint([]string{"a.md", "b.md", "c.md"}, []int64{1, 2, 3})
	b := Fingerprint([]string{"c.md", "a.md", "b.md"}, []int64{3, 1, 2})

	assert.Equal(t, a.Hash, b.Hash)
	assert.Equal(t, 3, a.Documents)
	assert.EqualValues(t, 6, a.Bytes)
}

func TestFingerprintChangesWithCorpus(t *testing.T) {
	a := Fingerprint([]string{"a.md"}, []int64{10})
	b := Fingerprint([]string{"a.md", "b.md"}, []int64{10, 5})
	assert.NotEqual(t, a.Hash, b.Hash)
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	require.NoError(t, New().Save(path))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file may be left behind, got %v", entries)
}
