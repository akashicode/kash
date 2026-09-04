package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeOverlayYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func ptr[T any](v T) *T { return &v }

// The regression this whole overlay exists for. DomainConfig cannot express
// "absent", so with a plain value type an agent.yaml that merely omits
// strip_final_vowel would zero a profile that detected it — and after the init
// template change, omitting it is the normal case.
func TestAbsentBoolDoesNotZeroProfileValue(t *testing.T) {
	var prof DomainOverlay
	prof.Resolution.StripFinalVowel = ptr(true)
	prof.Chunker.StripTitleStemVowel = ptr(true)

	path := writeOverlayYAML(t, "agent:\n  name: test\n")

	cfg, _ := ResolveDomainConfig(&prof, path)

	assert.True(t, cfg.Resolution.StripFinalVowel,
		"agent.yaml omitting the key must leave the profile value intact")
	assert.True(t, cfg.Chunker.StripTitleStemVowel)
}

func TestExplicitFalseInAgentYAMLBeatsProfile(t *testing.T) {
	var prof DomainOverlay
	prof.Resolution.StripFinalVowel = ptr(true)

	path := writeOverlayYAML(t, "resolution:\n  strip_final_vowel: false\n")

	cfg, _ := ResolveDomainConfig(&prof, path)
	assert.False(t, cfg.Resolution.StripFinalVowel,
		"an explicit false must override the profile")
}

func TestLayerPrecedence(t *testing.T) {
	var prof DomainOverlay
	prof.Resolution.FoldDiacritics = ptr(DiacriticIAST)
	prof.Extraction.Predicates = ptr([]string{"was disciple of", "commented on"})
	prof.Chunker.TitleStopwords = ptr([]string{"tantra"})

	path := writeOverlayYAML(t, `
resolution:
  fold_diacritics: latin
`)

	cfg, notes := ResolveDomainConfig(&prof, path)

	assert.Equal(t, DiacriticLatin, cfg.Resolution.FoldDiacritics, "agent.yaml wins")
	assert.Equal(t, []string{"was disciple of", "commented on"}, cfg.Extraction.Predicates,
		"profile wins where agent.yaml is silent")
	assert.Equal(t, []string{"tantra"}, cfg.Chunker.TitleStopwords)

	// The notes must attribute the contested field to agent.yaml.
	var foldLayer string
	for _, n := range notes {
		if n.Field == "resolution.fold_diacritics" {
			foldLayer = n.Layer
		}
	}
	assert.Equal(t, layerAgentYAML, foldLayer)
}

func TestDefaultsSurviveWhenNoLayerSetsAField(t *testing.T) {
	cfg, notes := ResolveDomainConfig(nil, writeOverlayYAML(t, "agent:\n  name: test\n"))
	assert.Equal(t, DefaultDomainConfig(), cfg)
	assert.Empty(t, notes, "nothing overridden means nothing to explain")
}

// An explicitly empty list stays meaningful for the three fields where "none"
// is a legitimate choice.
func TestExplicitEmptyListsRemainMeaningful(t *testing.T) {
	path := writeOverlayYAML(t, `
resolution:
  honorifics: []
chunker:
  ref_patterns: []
  title_stopwords: []
`)
	cfg, _ := ResolveDomainConfig(nil, path)

	assert.Empty(t, cfg.Resolution.Honorifics)
	assert.Empty(t, cfg.Chunker.RefPatterns)
	assert.Empty(t, cfg.Chunker.TitleStopwords)
}

// The predicate vocabulary is closed, so an empty list would drop every fact in
// the corpus with no error and no output. That is refused rather than obeyed.
func TestEmptyPredicatesIsRefused(t *testing.T) {
	path := writeOverlayYAML(t, "extraction:\n  predicates: []\n")

	cfg, notes := ResolveDomainConfig(nil, path)

	assert.Equal(t, DefaultDomainConfig().Extraction.Predicates, cfg.Extraction.Predicates,
		"an empty predicate list must not be applied")
	require.NotEmpty(t, notes)
	assert.Contains(t, notes[0].Value, "ignored")
}

func TestProfileAppliesWithoutAgentYAML(t *testing.T) {
	var prof DomainOverlay
	prof.Resolution.FoldDiacritics = ptr(DiacriticIAST)

	cfg, notes := ResolveDomainConfig(&prof, filepath.Join(t.TempDir(), "absent.yaml"))

	assert.Equal(t, DiacriticIAST, cfg.Resolution.FoldDiacritics)
	require.NotEmpty(t, notes)
	assert.Equal(t, layerProfile, notes[0].Layer)
}

func TestInvalidDiacriticModeIsIgnored(t *testing.T) {
	var prof DomainOverlay
	prof.Resolution.FoldDiacritics = ptr(DiacriticIAST)

	path := writeOverlayYAML(t, "resolution:\n  fold_diacritics: klingon\n")

	cfg, _ := ResolveDomainConfig(&prof, path)
	assert.Equal(t, DiacriticIAST, cfg.Resolution.FoldDiacritics,
		"an invalid mode must not clobber a valid lower layer")
}

func TestGleanRoundsZeroBeatsDefault(t *testing.T) {
	path := writeOverlayYAML(t, `
extraction:
  glean_rounds: 0
`)
	cfg, notes := ResolveDomainConfig(nil, path)
	assert.Equal(t, 0, cfg.Extraction.GleanRounds, "glean_rounds: 0 must override default 1")
	var found bool
	for _, n := range notes {
		if n.Field == "extraction.glean_rounds" {
			found = true
			assert.Equal(t, layerAgentYAML, n.Layer)
			assert.Equal(t, "0", n.Value)
		}
	}
	assert.True(t, found, "layer note must attribute extraction.glean_rounds to agent.yaml")
}
