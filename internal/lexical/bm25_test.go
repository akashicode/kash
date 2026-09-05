package lexical

import (
	"path/filepath"
	"testing"

	"github.com/akashicode/kash/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildTestIndex(t *testing.T) *Index {
	t.Helper()
	ix := New()
	ix.Add("vbt_275", "[Vijnanabhairava > dhāraṇā-49]\n\ngītādiviṣayāsvādāsamasaukhyaikatātmanaḥ",
		map[string]string{"dharana": "49", "verse": "72", "source": "vbt.md"})
	ix.Add("vbt_100", "[Vigyan Bhairava Tantra > Verse 25]\n\nConcentrate on the two places where the breath turns",
		map[string]string{"verse": "25", "source": "vbt-en.md"})
	ix.Add("ts_900", "ślokānukramaṇī index table 45 12 33 page numbers listing",
		map[string]string{"source": "tantra-sangraha.md", "content_type": "index"})
	ix.Finalize()
	return ix
}

func TestSearchMatchesExactNumberedTerm(t *testing.T) {
	ix := buildTestIndex(t)

	got := ix.Search("dharana 49", 5)
	require.NotEmpty(t, got, "a keyword+number query must match lexically")
	assert.Equal(t, "vbt_275", got[0].ID,
		"the chunk headed dhāraṇā-49 must outrank an index table for 'dharana 49'")
}

func TestSearchRanksProseOverIndexTable(t *testing.T) {
	ix := buildTestIndex(t)

	got := ix.Search("breath turns concentrate", 5)
	require.NotEmpty(t, got)
	assert.Equal(t, "vbt_100", got[0].ID)
}

func TestTokenizeKeepsNumbersAndShortTerms(t *testing.T) {
	got := Tokenize("Dharana 49 om AI")
	assert.Equal(t, []string{"dharana", "49", "om", "ai"}, got,
		"numbers and two-rune terms must survive tokenization")
}

func TestTokenizeDropsSingleRuneTerms(t *testing.T) {
	assert.Equal(t, []string{"ab"}, Tokenize("a ab"))
}

func TestFindByRefMatchesCommaSeparatedLists(t *testing.T) {
	ix := New()
	ix.Add("c1", "text", map[string]string{"verse": "24,25,26"})
	ix.Finalize()

	assert.Len(t, ix.FindByRef("verse", "25"), 1, "a chunk spanning verses must match any of them")
	assert.Empty(t, ix.FindByRef("verse", "27"))
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ix := buildTestIndex(t)
	path := filepath.Join(t.TempDir(), FileName)
	require.NoError(t, ix.Save(path))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ix.Len(), loaded.Len())

	got := loaded.Search("dharana 49", 3)
	require.NotEmpty(t, got)
	assert.Equal(t, "vbt_275", got[0].ID)
}

func TestLoadMissingFileYieldsEmptyIndex(t *testing.T) {
	ix, err := Load(filepath.Join(t.TempDir(), "absent.idx"))
	require.NoError(t, err, "a corpus built before the lexical index existed must still serve")
	assert.Equal(t, 0, ix.Len())
	assert.Nil(t, ix.Search("anything", 5))
}

// The index must tokenise queries the way it tokenised its own documents. When
// the fold mode lived only in configuration, a corpus built with IAST folding
// and served with a different config tokenised queries differently from the
// index — keyword search returned nothing, with no error and no log line.
func TestFoldModeSurvivesSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	ix := NewWithFold(config.DiacriticIAST)
	ix.Add("c1", "dhāraṇā 49 gītādiviṣayāsvādā", map[string]string{"dharana": "49"})
	ix.Finalize()
	require.NoError(t, ix.Save(path))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.DiacriticIAST, loaded.FoldMode,
		"the index must remember the fold it was built with")

	// The reader types ASCII; the text is transliterated. They only meet if the
	// loaded index folds the way the original did.
	got := loaded.Search("dharana 49", 5)
	require.NotEmpty(t, got, "an IAST-folded index must still match an ASCII query after reload")
	assert.Equal(t, "c1", got[0].ID)
}

// An index written before FoldMode existed decodes with an empty mode and must
// keep working on the Latin default it was actually built with.
func TestLoadDefaultsFoldModeForLegacyIndex(t *testing.T) {
	ix := New()
	assert.Equal(t, config.DiacriticLatin, ix.FoldMode)
	assert.Equal(t, "resume", ix.Fold("résumé"), "the default fold must still apply")
}

// A reference lookup has two sides that name the key independently: the chunker
// names it from the pattern that matched the document, the query router from
// the pattern that matched the query. When they disagree, the number is indexed
// and still unreachable. FindByAnyRef is the fallback for that case.
func TestFindByAnyRefIgnoresTheKeyName(t *testing.T) {
	ix := New()
	ix.Add("a", "Handling charge, applied per consignment.", map[string]string{
		"source": "agreement.md", "section": "7",
	})
	ix.Add("b", "Concentrate on the gap between two breaths.", map[string]string{
		"source": "vbt.md", "verse": "24,25,26",
	})
	// Infrastructure metadata holds prose, not references. A heading that
	// happens to contain the number must not match.
	ix.Add("c", "Unrelated passage.", map[string]string{
		"source": "other.md", "heading": "Chapter 7 of the manual", "breadcrumb": "Book > 7",
	})
	ix.Finalize()

	assert.Empty(t, ix.FindByRef("clause", "7"), "the named key genuinely holds nothing")

	got := ix.FindByAnyRef("7")
	require.Len(t, got, 1, "the value must be found under whatever key holds it")
	assert.Equal(t, "a", got[0].ID)

	assert.Len(t, ix.FindByAnyRef("25"), 1, "a comma-joined list must still match")
	assert.Empty(t, ix.FindByAnyRef("999"), "a value nothing carries must not match")
}
