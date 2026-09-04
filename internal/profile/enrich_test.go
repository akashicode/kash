package profile

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
