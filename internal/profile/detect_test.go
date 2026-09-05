package profile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/config"
)

// iastStems are Sanskrit words that appear both bare and with a trailing stem
// vowel — the co-occurrence DetectStemVowel actually measures.
var iastStems = []string{
	"saṃskāra", "bhāvanā", "prakāśa", "kṣetra", "māhātmya", "pariṇāma",
	"ātmatattva", "vimarśa", "spanda", "kaula", "mudrā", "cakra",
	"nāḍī", "bindu", "kalā", "tattva", "mantra", "yantra", "maṇḍala",
	"dīkṣā", "sādhana", "upāya", "śakti", "bhairava", "krama", "pīṭha",
	"vidyā", "smaraṇa", "dhyāna", "japa", "homa", "nyāsa",
}

// iastDocs builds a transliterated Sanskrit corpus with stem-vowel variants.
func iastDocs(n int) []Doc {
	var docs []Doc
	for i := 0; i < n; i++ {
		var b strings.Builder
		for v := 1; v <= 40; v++ {
			fmt.Fprintf(&b, "## Dhāraṇā %d\n\n", v)
			for _, stem := range iastStems {
				// Both the bare stem and a vowel-suffixed form, the way a real
				// Sanskrit text carries them.
				fmt.Fprintf(&b, "%s %sm ", stem, stem)
			}
			b.WriteString("\n\n")
		}
		docs = append(docs, Doc{Name: fmt.Sprintf("tantra-vol%d_FINAL_iast.md", i), Content: b.String()})
	}
	return docs
}

// englishDocs builds a plain ASCII corpus with no numbering in headings.
func englishDocs(n int) []Doc {
	var docs []Doc
	for i := 0; i < n; i++ {
		var b strings.Builder
		for s := 0; s < 25; s++ {
			fmt.Fprintf(&b, "## Overview of operations\n\nThe department reviews each request "+
				"and records the outcome in the register for later audit.\n\n")
		}
		docs = append(docs, Doc{Name: fmt.Sprintf("handbook-%d.md", i), Content: b.String()})
	}
	return docs
}

func TestDetectDiacriticsIAST(t *testing.T) {
	mode, evidence := DetectDiacritics(iastDocs(10))
	assert.Equal(t, config.DiacriticIAST, mode)
	assert.Contains(t, evidence, "IAST marks")
}

func TestDetectDiacriticsPlainASCII(t *testing.T) {
	mode, _ := DetectDiacritics(englishDocs(10))
	assert.Equal(t, config.DiacriticNone, mode,
		"a pure ASCII corpus needs no folding at all")
}

// "ñ" is in both the Latin and IAST fold tables, so counting it toward either
// would make a Spanish corpus read as Sanskrit.
func TestDetectDiacriticsSpanishIsNotSanskrit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("El niño y la señora enseñaban mañana en la montaña. ")
	}
	docs := []Doc{
		{Name: "a.md", Content: b.String()},
		{Name: "b.md", Content: b.String()},
	}

	mode, evidence := DetectDiacritics(docs)
	assert.NotEqual(t, config.DiacriticIAST, mode,
		"Spanish must not be detected as Sanskrit; evidence: %s", evidence)
}

func TestDetectStemVowelOnIAST(t *testing.T) {
	ev := DetectStemVowel(iastDocs(12))
	assert.True(t, ev.Decided)
	assert.True(t, ev.Value, "co-occurring stem-vowel variants must enable folding: %s", ev.Narrative)
}

// The Sanskrit-only rules must stay off for an English corpus. Dropping a final
// vowel there changes the word (plasma/plasm, corona/coron).
func TestDetectStemVowelOffForEnglish(t *testing.T) {
	ev := DetectStemVowel(englishDocs(12))
	assert.True(t, ev.Decided)
	assert.False(t, ev.Value, "%s", ev.Narrative)
}

func TestDetectRefPatternsFindsNumberedHeadings(t *testing.T) {
	cands, evidence := DetectRefPatterns(iastDocs(6))
	require.NotEmpty(t, cands, "evidence: %s", evidence)

	var keys []string
	for _, c := range cands {
		keys = append(keys, c.MetaKey)
	}
	assert.Contains(t, keys, "dharana",
		"a transliterated label must be folded into a typeable metadata key")
}

// The monotonicity rejection: prose whose headings merely contain ascending
// page numbers is not a numbering scheme. This is the test that stops false
// positives, and it is the one that matters — coverage alone would accept it.
func TestDetectRefPatternsRejectsPageNumbers(t *testing.T) {
	var docs []Doc
	for i := 0; i < 6; i++ {
		var b strings.Builder
		// Page numbers appear, but never in ascending runs within a heading
		// label, and they restart arbitrarily.
		for _, p := range []int{412, 87, 903, 55, 671, 128, 744, 39, 560, 210, 888, 44} {
			fmt.Fprintf(&b, "## Commentary page %d\n\nDiscussion of the preceding passage.\n\n", p)
		}
		docs = append(docs, Doc{Name: fmt.Sprintf("commentary-%d.md", i), Content: b.String()})
	}

	cands, evidence := DetectRefPatterns(docs)
	for _, c := range cands {
		assert.NotEqual(t, "page", c.Label,
			"page numbers must not be mistaken for a numbering scheme: %s", evidence)
	}
}

func TestDetectRefPatternsEmptyCorpus(t *testing.T) {
	cands, evidence := DetectRefPatterns(nil)
	assert.Empty(t, cands)
	assert.Contains(t, evidence, "no numbering scheme")
}

// Detection must never emit a pattern the chunker would reject, since that
// would be an inert rule nobody notices.
func TestDetectedPatternsAlwaysCompile(t *testing.T) {
	cands, _ := DetectRefPatterns(iastDocs(6))
	require.NotEmpty(t, cands)
	for _, c := range cands {
		assert.True(t, validPattern(c.Pattern), "emitted an uncompilable pattern: %s", c.Pattern)
	}
}

func TestDetectTitleStopwordsNeedsEnoughDocuments(t *testing.T) {
	got, evidence := DetectTitleStopwords(iastDocs(3), nil)
	assert.Nil(t, got)
	assert.Contains(t, evidence, "too small")
}

func TestDetectTitleStopwordsFindsSharedGenreWords(t *testing.T) {
	docs := []Doc{
		{Name: "Rudra Yamala Tantra - Mishra.md"}, {Name: "Yogini Tantra - Sharma.md"},
		{Name: "Meru Tantra - Narayan.md"}, {Name: "Gyanarnava Tantra - Kaviraj.md"},
		{Name: "Svacchanda Tantra - Mishra.md"}, {Name: "Malini Tantra - Mishra.md"},
		{Name: "Kularnava Tantra - Sharma.md"}, {Name: "Netra Tantra - Kaviraj.md"},
		{Name: "Brahma Yamala.md"}, {Name: "Siddha Paddhati.md"},
	}

	got, evidence := DetectTitleStopwords(docs, []string{"the", "of"})
	require.NotNil(t, got)
	assert.Contains(t, got, "tantra",
		"a word in most titles cannot distinguish works: %s", evidence)
	assert.Contains(t, got, "the", "defaults must be preserved")
}

// A metadata key must never collide with the chunker's own structural fields —
// writing to one would corrupt the chunk's citation header.
func TestSanitizeMetaKeyRefusesReservedNames(t *testing.T) {
	for _, reserved := range []string{"book", "heading", "breadcrumb", "content_type", "noise_score"} {
		assert.Equal(t, "section", sanitizeMetaKey(reserved),
			"%q is structural metadata and must not be used as a reference key", reserved)
	}
}

func TestSanitizeMetaKeyFoldsTransliteration(t *testing.T) {
	assert.Equal(t, "dharana", sanitizeMetaKey("dhāraṇā"))
	assert.Equal(t, "sloka", sanitizeMetaKey("śloka"))
	assert.Equal(t, "verse", sanitizeMetaKey("Verses"))
}

// A numbering scheme is a property of the work that uses it. One work citing
// another's numbering in passing is not evidence against that numbering, but
// averaging the per-document scores let the passing mention outvote the real
// one: a scripture numbering its own verses scored 0.84, a commentary citing
// nine of them scored 0.49, and the mean of 0.66 described neither.
func TestBestSequenceScoreIgnoresAPassingMention(t *testing.T) {
	strong := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	passing := []int{46, 47, 48}

	both := map[string][]int{"scripture.md": strong, "commentary.md": passing}
	only := map[string][]int{"scripture.md": strong}

	assert.InDelta(t, bestSequenceScore(only), bestSequenceScore(both), 0.001,
		"a commentary citing three numbers must not lower the scripture's own score")
}

// Requiring the first value to be 1, 2 or 3 assumed every work is quoted whole.
// An anthology quoting ślokas 7 to 102 begins at the beginning of what it
// quotes; scoring it as though it began nowhere was enough on its own to sink a
// real scheme carrying 41 headings and 40 distinct numbers.
func TestSequenceScoreAcceptsNumberingThatStartsPartWayIn(t *testing.T) {
	extract := []int{7, 9, 10, 11, 13, 14, 15, 34, 35, 36, 60, 61, 63, 100, 102}
	pages := []int{200, 210, 220, 230, 240, 250, 260, 270, 280, 290, 300, 400}

	assert.Greater(t, sequenceScore(extract), sequenceScore(pages),
		"an extract starting a short way into its range must outscore numbering that starts halfway in")
	assert.Greater(t, sequenceScore(extract), 0.7,
		"a monotonic extract is numbering, not noise")
}
