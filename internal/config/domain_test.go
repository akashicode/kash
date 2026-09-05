package config

import (
	"os"
	"path/filepath"
	"regexp"
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
	assert.Equal(t, 1, d.Extraction.GleanRounds, "default gleaning rounds must be 1")
	assert.False(t, d.Chunker.StripTitleStemVowel,
		"title stem-vowel stripping must be opt-in")
	assert.NotEmpty(t, d.Chunker.RefPatterns,
		"default ref patterns should be provided")
	assert.NotEmpty(t, d.Chunker.TitleStopwords,
		"default title stopwords should be provided")
}

func TestLoadDomainConfigMissingFileFallsBack(t *testing.T) {
	d, _ := ResolveDomainConfig(nil, filepath.Join(t.TempDir(), "nope.yaml"))
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
	d, _ := ResolveDomainConfig(nil, path)
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
	d, _ := ResolveDomainConfig(nil, path)
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
	d, _ := ResolveDomainConfig(nil, path)
	assert.Equal(t, DefaultDomainConfig(), d)
}

func TestLoadDomainConfigRejectsBadDiacriticMode(t *testing.T) {
	path := writeYAML(t, "resolution:\n  fold_diacritics: klingon\n")
	d, _ := ResolveDomainConfig(nil, path)
	assert.Equal(t, DiacriticLatin, d.Resolution.FoldDiacritics,
		"an unrecognised mode must fall back to the default")
}

func TestLoadDomainConfigEmptyHonorificsIsMeaningful(t *testing.T) {
	path := writeYAML(t, "resolution:\n  honorifics: []\n")
	d, _ := ResolveDomainConfig(nil, path)
	assert.Empty(t, d.Resolution.Honorifics,
		"an explicitly empty list must disable honorific stripping")
}

func TestLoadDomainConfigChunkerOverrides(t *testing.T) {
	path := writeYAML(t, `
chunker:
  ref_patterns:
    - pattern: '(?i)article\s+(\d+)'
      meta_key: article
  title_stopwords:
    - "amended"
    - "restated"
  strip_title_stem_vowel: true
`)
	d, _ := ResolveDomainConfig(nil, path)
	require.Len(t, d.Chunker.RefPatterns, 1)
	assert.Equal(t, `(?i)article\s+(\d+)`, d.Chunker.RefPatterns[0].Pattern)
	assert.Equal(t, "article", d.Chunker.RefPatterns[0].MetaKey)
	assert.Equal(t, []string{"amended", "restated"}, d.Chunker.TitleStopwords)
	assert.True(t, d.Chunker.StripTitleStemVowel)
}

func TestLoadDomainConfigEntityDescription(t *testing.T) {
	// Defaults
	d := DefaultDomainConfig()
	assert.Equal(t, 2, d.EntityDescription.MinDegree)
	assert.Equal(t, 500, d.EntityDescription.MaxEntities)

	// Overrides
	path := writeYAML(t, `
entity_description:
  min_degree: 5
  max_entities: 100
`)
	loaded, _ := ResolveDomainConfig(nil, path)
	assert.Equal(t, 5, loaded.EntityDescription.MinDegree)
	assert.Equal(t, 100, loaded.EntityDescription.MaxEntities)
}

func TestLoadDomainConfigGleanRounds(t *testing.T) {
	path := writeYAML(t, `
extraction:
  glean_rounds: 0
`)
	d, _ := ResolveDomainConfig(nil, path)
	assert.Equal(t, 0, d.Extraction.GleanRounds, "glean_rounds: 0 must disable gleaning")

	path2 := writeYAML(t, `
extraction:
  glean_rounds: 3
`)
	d2, _ := ResolveDomainConfig(nil, path2)
	assert.Equal(t, 3, d2.Extraction.GleanRounds)
}

// The generic reference words are the structural divisions any document may
// carry, whatever it is about. A word naming the subject rather than the
// structure is detected per corpus instead, so this list stays domain-neutral.
func TestDefaultRefPatternsCoverGenericStructuralWords(t *testing.T) {
	re := regexp.MustCompile(defaultRefPatterns[0].Pattern)

	matches := map[string]string{
		"chapter 7":         "7",
		"Chapter 12 opens":  "12",
		"paragraph 4.2":     "4.2",
		"rule 11":           "11",
		"schedule 2":        "2",
		"annex 3":           "3",
		"appendix 5":        "5",
		"section 4.2":       "4.2",
		"clause 22":         "22",
		"article 5":         "5",
		"part 3":            "3",
		// A word boundary before a symbol needs a word character before it, so
		// the one non-letter marker in the list used to be unreadable.
		"§ 9":               "9",
		"§9":                "9",
		"see § 12.3":        "12.3",
	}
	for in, want := range matches {
		m := re.FindStringSubmatch(in)
		require.NotNil(t, m, "%q must yield a reference", in)
		assert.Equal(t, want, m[1], "input %q", in)
	}

	// Words that appear constantly in ordinary prose must not become
	// references, or every passage acquires a number that means nothing.
	for _, in := range []string{
		"the third step of the process", "item on the agenda",
		"page 45", "figure 3", "table 2", "note 7", "line 4",
	} {
		assert.Nil(t, re.FindStringSubmatch(in), "%q must not yield a reference", in)
	}
}
