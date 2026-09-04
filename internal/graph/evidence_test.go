package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/akashicode/kash/internal/config"
)

func TestEvidenceGradesSupport(t *testing.T) {
	e := NewEvidenceChecker(config.DiacriticLatin, false)
	passage := "Abhinavagupta composed the Tantraloka in Kashmir."

	tests := []struct {
		name    string
		subject string
		object  string
		want    Evidence
	}{
		{"both endpoints present", "Abhinavagupta", "Tantraloka", EvidenceBoth},
		{"one endpoint present", "Abhinavagupta", "Bhagavad Gita", EvidencePartial},
		{"neither present", "Gorakhnath", "Hatha Yoga", EvidenceNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, e.Check(passage, tt.subject, tt.object))
		})
	}
}

// The extractor is told to use the shortest unambiguous name, so it writes
// "Siddhasana" where an IAST source reads "siddhasana" with macrons. Comparing
// raw strings would reject correct provenance across a whole corpus, so the
// check folds and stems exactly the way the corpus's own index does.
func TestEvidenceFoldsLikeTheCorpus(t *testing.T) {
	passage := "The teachings of gorakhanātha describe the siddhāsana posture."

	unfolded := NewEvidenceChecker(config.DiacriticLatin, false)
	assert.Equal(t, EvidenceNone, unfolded.Check(passage, "Siddhasana", "gorakhanatha"),
		"Latin folding leaves IAST macrons in place, so neither name matches")

	folded := NewEvidenceChecker(config.DiacriticIAST, true)
	assert.Equal(t, EvidenceBoth, folded.Check(passage, "Siddhasana", "gorakhanatha"),
		"IAST folding plus the stem-vowel convention resolves both names")
}

// Folding closes the diacritic and stem-vowel gap and nothing more. A genuine
// transliteration variant differing by a syllable is entity resolution's job,
// and this check reports it as unsupported rather than pretending otherwise.
func TestEvidenceDoesNotResolveSpellingVariants(t *testing.T) {
	e := NewEvidenceChecker(config.DiacriticIAST, true)
	passage := "The teachings of gorakhanātha are recorded here."

	assert.Equal(t, EvidenceNone, e.Check(passage, "Gorakhnatha", "Hatha Yoga"),
		"gorakhanatha and gorakhnatha differ by a syllable, not a diacritic")
}

// A two-word entity must not be evidenced by its commonest half.
func TestEvidenceRequiresEveryTokenOfAName(t *testing.T) {
	e := NewEvidenceChecker(config.DiacriticLatin, false)
	passage := "The nath tradition spread across northern India."

	assert.Equal(t, EvidenceNone, e.Check(passage, "Nath Sampradaya", "Kashmir"),
		"\"nath\" alone does not evidence \"Nath Sampradaya\"")
}

// Short tokens match everywhere and prove nothing.
func TestEvidenceIgnoresVeryShortTokens(t *testing.T) {
	e := NewEvidenceChecker(config.DiacriticLatin, false)
	assert.Equal(t, EvidenceNone, e.Check("a text about of the in", "Om", "It"))
}

func TestEvidenceOnEmptyPassage(t *testing.T) {
	e := NewEvidenceChecker(config.DiacriticLatin, false)
	assert.Equal(t, EvidenceNone, e.Check("", "Shiva", "Bhairava"))
}
