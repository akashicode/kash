package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// TestDefaultsAreDomainNeutral guards the property that made this configurable:
// no Sanskrit assumption may be active out of the box.
func TestDefaultsAreDomainNeutral(t *testing.T) {
	d := DefaultDomainConfig()
	assert.False(t, d.Resolution.StripFinalVowel,
		"the Sanskrit stem-vowel rule must be opt-in")
	assert.Equal(t, DiacriticLatin, d.Resolution.FoldDiacritics)
	assert.NotEmpty(t, d.Extraction.Predicates)
	assert.NotContains(t, d.Extraction.Predicates, "was disciple of",
		"default vocabulary must not assume a scholarly-lineage corpus")
}

func TestLoadDomainConfigMissingFileFallsBack(t *testing.T) {
	d := LoadDomainConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	assert.Equal(t, DefaultDomainConfig(), d)
}

// TestLoadDomainConfigAerospace is the whole point: a different subject can
// declare its own vocabulary and resolution rules.
func TestLoadDomainConfigAerospace(t *testing.T) {
	path := writeYAML(t, `
extraction:
  predicates:
    - "manufactured by"
    - "launched on"
    - "powered by"
  priorities:
    - "Engineering relations"
resolution:
  honorifics: ["dr. ", "prof. "]
  fold_diacritics: latin
  strip_final_vowel: false
  proper_noun_predicates: ["manufactured by", "designed by"]
`)
	d := LoadDomainConfig(path)
	assert.Equal(t, []string{"manufactured by", "launched on", "powered by"}, d.Extraction.Predicates)
	assert.Equal(t, []string{"Engineering relations"}, d.Extraction.Priorities)
	assert.Equal(t, DiacriticLatin, d.Resolution.FoldDiacritics)
	assert.False(t, d.Resolution.StripFinalVowel)
	assert.Equal(t, []string{"manufactured by", "designed by"}, d.Resolution.ProperNounPredicates)
}

// TestLoadDomainConfigSanskrit covers the opposite end: the Indic preset.
func TestLoadDomainConfigSanskrit(t *testing.T) {
	path := writeYAML(t, `
extraction:
  predicates: ["authored", "was disciple of", "commented on"]
resolution:
  honorifics: ["śrī ", "ācārya "]
  fold_diacritics: iast
  strip_final_vowel: true
`)
	d := LoadDomainConfig(path)
	assert.Equal(t, DiacriticIAST, d.Resolution.FoldDiacritics)
	assert.True(t, d.Resolution.StripFinalVowel)
	assert.Equal(t, []string{"śrī ", "ācārya "}, d.Resolution.Honorifics)
	// Unset sections keep their defaults
	assert.NotEmpty(t, d.Extraction.Priorities)
	assert.NotEmpty(t, d.Resolution.ProperNounPredicates)
}

// TestLoadDomainConfigPartialKeepsDefaults — an agent.yaml written before
// these sections existed must keep working.
func TestLoadDomainConfigPartialKeepsDefaults(t *testing.T) {
	path := writeYAML(t, "agent:\n  name: legacy\n")
	d := LoadDomainConfig(path)
	assert.Equal(t, DefaultDomainConfig(), d)
}

func TestLoadDomainConfigRejectsBadDiacriticMode(t *testing.T) {
	path := writeYAML(t, "resolution:\n  fold_diacritics: klingon\n")
	d := LoadDomainConfig(path)
	assert.Equal(t, DiacriticLatin, d.Resolution.FoldDiacritics,
		"an unrecognised mode must fall back to the default")
}

func TestLoadDomainConfigEmptyHonorificsIsMeaningful(t *testing.T) {
	path := writeYAML(t, "resolution:\n  honorifics: []\n")
	d := LoadDomainConfig(path)
	assert.Empty(t, d.Resolution.Honorifics,
		"an explicitly empty list must disable honorific stripping")
}
