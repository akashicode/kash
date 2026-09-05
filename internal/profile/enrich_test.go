package profile

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/llm"
)

type stubSuggester struct {
	out llm.DomainSuggestion
	err error
}

func (s stubSuggester) SuggestDomainConfig(context.Context, llm.DomainEvidence) (llm.DomainSuggestion, error) {
	return s.out, s.err
}

// A failed model call must keep everything detection established and mark the
// profile incomplete, so the next build retries only the missing half. Marking
// it complete would extract a whole corpus with generic vocabulary and never
// revisit it.
func TestEnrichFailureKeepsDetectionAndStaysIncomplete(t *testing.T) {
	docs := iastDocs(8)
	p := Derive(docs, Options{})
	require.NotNil(t, p.Config.Resolution.FoldDiacritics)
	detected := *p.Config.Resolution.FoldDiacritics

	Enrich(context.Background(), p, docs, stubSuggester{err: errors.New("429 rate limited")})

	assert.False(t, p.Complete, "a failed call must not produce a complete profile")
	assert.Contains(t, p.LLMStatus, "429")
	require.NotNil(t, p.Config.Resolution.FoldDiacritics)
	assert.Equal(t, detected, *p.Config.Resolution.FoldDiacritics,
		"measurements must survive a model failure")
	assert.Nil(t, p.Config.Extraction.Predicates, "no vocabulary should be invented on failure")
}

func TestEnrichWithoutSuggesterIsNotFatal(t *testing.T) {
	docs := iastDocs(8)
	p := Derive(docs, Options{})
	Enrich(context.Background(), p, docs, nil)

	assert.False(t, p.Complete)
	assert.Contains(t, p.LLMStatus, "no model")
}

func TestEnrichUnionsPredicatesAndCompletes(t *testing.T) {
	docs := iastDocs(8)
	p := Derive(docs, Options{})

	Enrich(context.Background(), p, docs, stubSuggester{out: llm.DomainSuggestion{
		Predicates: []string{"was teacher of", "commented on"},
		Priorities: []string{"Lineage relations first."},
	}})

	assert.True(t, p.Complete)
	require.NotNil(t, p.Config.Extraction.Predicates)
	got := *p.Config.Extraction.Predicates
	assert.Contains(t, got, "was teacher of")
	assert.Contains(t, got, "contains", "generic predicates must survive the union")
}

// The sample is untrusted corpus text and the output becomes configuration, so
// an honorific the model was not offered must not reach the profile.
func TestEnrichRejectsUnofferedHonorifics(t *testing.T) {
	docs := iastDocs(8)
	p := Derive(docs, Options{})

	Enrich(context.Background(), p, docs, stubSuggester{out: llm.DomainSuggestion{
		Honorifics: []string{"definitely-not-mined "},
	}})

	if p.Config.Resolution.Honorifics != nil {
		assert.NotContains(t, *p.Config.Resolution.Honorifics, "definitely-not-mined ")
	}
}

// A meta_key that collided with the chunker's own structural fields would
// corrupt a chunk's citation header.
func TestEnrichRejectsReservedMetaKeys(t *testing.T) {
	docs := iastDocs(8)
	p := Derive(docs, Options{})
	require.NotNil(t, p.Config.Chunker.RefPatterns)

	Enrich(context.Background(), p, docs, stubSuggester{out: llm.DomainSuggestion{
		MetaKeys: map[string]string{"dhāraṇā": "heading"},
	}})

	for _, rp := range *p.Config.Chunker.RefPatterns {
		assert.NotEqual(t, "heading", rp.MetaKey)
		assert.NotEqual(t, "breadcrumb", rp.MetaKey)
	}
}

// The evidence sample must not be the opening of each document: a title page
// describes publishing, not subject matter.
func TestEvidenceSampleReachesBeyondDocumentOpenings(t *testing.T) {
	body := "OPENING TITLE PAGE COPYRIGHT NOTICE. " +
		strings_Repeat("the substantive discussion of the practice continues here. ", 200)
	docs := []Doc{{Name: "book.md", Content: body}}

	got := EvidenceSample(docs, 4000)
	assert.Contains(t, got, "substantive discussion",
		"the sample must include text from the middle of a document")
}

func strings_Repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// Every numbering scheme but one is named by the word beside its number, so the
// pattern detection builds contains that word and a substring test finds it.
// The bare "48)" form has no word at all — its pattern is punctuation and a
// digit class — so the substring test excluded the one scheme that could not
// name itself, and it stayed filed under the generic section key. A corpus
// writing both "Verse 51" and "97)" then filed one reference under two names.
func TestMetaKeyRenamingReachesTheWordlessScheme(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		label   string
		key     string
		want    string
	}{
		{
			name:    "bare-number scheme is renamed",
			pattern: ParenPattern,
			label:   ParenLabel,
			key:     "verse",
			want:    "verse",
		},
		{
			// A contract would answer differently; the key comes from the
			// corpus, not from this package.
			name:    "the name comes from the model, not from us",
			pattern: ParenPattern,
			label:   ParenLabel,
			key:     "clause",
			want:    "clause",
		},
		{
			name:    "a label-derived pattern is still matched by its word",
			pattern: `(?i)(?:^|[^\p{L}])articles?\s*[-–—.]?\s*(\d[\d.]*)`,
			label:   "article",
			key:     "article",
			want:    "article",
		},
		{
			name:    "an unrelated label renames nothing",
			pattern: ParenPattern,
			label:   "sloka",
			key:     "sloka",
			want:    chunker.MetaSection,
		},
		{
			name:    "a key that is not a usable metadata name is refused",
			pattern: ParenPattern,
			label:   ParenLabel,
			key:     "Verse Number!",
			want:    chunker.MetaSection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns := []config.RefPattern{{Pattern: tt.pattern, MetaKey: chunker.MetaSection}}
			p := &Profile{}
			p.Config.Chunker.RefPatterns = &patterns

			applyMetaKeyNames(p, map[string]string{tt.label: tt.key})

			assert.Equal(t, tt.want, (*p.Config.Chunker.RefPatterns)[0].MetaKey)
		})
	}
}
