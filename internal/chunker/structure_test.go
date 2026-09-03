package chunker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const vbtDoc = `# Vigyan Bhairava Tantra

## THE MEDITATIONS

### Verse 25
Concentrate on the two places where the breath turns from inside to outside.

This meditation is a slight variation of the previous one.

### Verse 26
At the center where the breath does not enter or go out, all thoughts disappear.

## dhāraṇā-49
gītādiviṣayāsvādāsamasaukhyaikatātmanaḥ .
yoginastanmayatvena manorūḍhestadātmatā ..72..
`

func newTestChunker(t *testing.T) *Chunker {
	t.Helper()
	ck, err := NewChunker(Options{ChunkSize: 2000, Overlap: 400})
	require.NoError(t, err)
	return ck
}

func TestSplitStructuredExtractsReferences(t *testing.T) {
	ck := newTestChunker(t)

	chunks, err := ck.SplitStructured(vbtDoc, "vigyan-bhairava-tantra_FINAL_iast.md")
	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	refs := map[string]string{} // verse/dharana -> content
	for _, c := range chunks {
		if v := c.Metadata[MetaVerse]; v != "" {
			refs["v"+v] = c.Content
		}
		if d := c.Metadata[MetaDharana]; d != "" {
			refs["d"+d] = c.Content
		}
	}

	assert.Contains(t, refs, "v25", "a numbered verse must be addressable by its number")
	assert.Contains(t, refs, "d49", "a numbered dhāraṇā must be addressable by its number")
	assert.Contains(t, refs["d49"], "gītādiviṣayāsvādā")
}

// Packing keeps a verse with its commentary, but must not merge two numbered
// verses into one chunk — that would make neither addressable by number, which
// is the failure this metadata exists to fix.
func TestSplitStructuredKeepsNumberedSectionsSeparate(t *testing.T) {
	ck := newTestChunker(t)

	chunks, err := ck.SplitStructured(vbtDoc, "vbt.md")
	require.NoError(t, err)

	for _, c := range chunks {
		verses := strings.Split(c.Metadata[MetaVerse], ",")
		if len(verses) > 1 && c.Metadata[MetaVerse] != "" {
			assert.Failf(t, "verses merged",
				"chunk %d covers verses %v; numbered sections must not be packed together",
				c.Index, verses)
		}
	}
}

func TestSplitStructuredAttachesBreadcrumb(t *testing.T) {
	ck := newTestChunker(t)

	chunks, err := ck.SplitStructured(vbtDoc, "vigyan-bhairava-tantra_FINAL_iast.md")
	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	assert.Contains(t, chunks[0].Metadata[MetaBreadcrumb], "THE MEDITATIONS")
	assert.Equal(t, "Vigyan Bhairava Tantra", chunks[0].Metadata[MetaBook])
	assert.True(t, strings.HasPrefix(chunks[0].Content, "["),
		"a chunk must carry its citation header so the answering model can cite it")
}

// A document with no headings must still chunk, rather than producing nothing
// or one enormous chunk — this is how plain .txt sources are handled.
func TestSplitStructuredWithoutHeadings(t *testing.T) {
	ck, err := NewChunker(Options{ChunkSize: 300, Overlap: 50})
	require.NoError(t, err)

	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString("This is an ordinary paragraph of running prose without any heading.\n\n")
	}

	chunks, err := ck.SplitStructured(sb.String(), "plain.txt")
	require.NoError(t, err)
	assert.Greater(t, len(chunks), 1, "an unstructured document must still be split")
	for _, c := range chunks {
		assert.LessOrEqual(t, len([]rune(c.Content)), 700,
			"chunks must stay near the configured size")
	}
}

func TestClassifyDetectsApparatus(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantProse bool
	}{
		{
			name: "concordance table",
			content: `| bhuvaḥ śikhāyāṃ svaḥkāraṃ | 7 |
| bhūmigāḥ paripaśyanti | 18 |
| bhūmau śirasi cākāśe | 9 |
| yadrūpaṃ dhāma golokaṃ | 13 |`,
			wantProse: false,
		},
		{
			name: "running prose",
			content: `Concentrate on the two places where the breath turns from inside to
outside. This meditation is a slight variation of the previous one, and it
should be practised seated with the eyes closed.`,
			wantProse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctype, noise := classify(tt.content)
			if tt.wantProse {
				assert.Equal(t, ContentProse, ctype)
				assert.Less(t, noise, 0.3)
			} else {
				assert.NotEqual(t, ContentProse, ctype)
				assert.Greater(t, noise, 0.3, "apparatus must carry a noise score that can down-rank it")
			}
		})
	}
}

func TestBookTitleStripsPipelineSuffixes(t *testing.T) {
	assert.Equal(t, "Vigyan Bhairava Tantra", bookTitle("vigyan-bhairava-tantra_FINAL_iast.md"))
	assert.Equal(t, "Merutantra Original", bookTitle("Merutantra - original.txt"))
}

// Editions do not agree on how they number things. The English Vijñāna
// Bhairava marks most of its 112 techniques as a bare "32)" rather than
// "Verse 32"; recognising only the latter left those techniques unaddressable
// by number even though their text was fully present and correctly indexed.
func TestSplitStructuredRecognisesParenNumbering(t *testing.T) {
	doc := `# Vigyan Bhairava Tantra

### 31) Concentrate without thoughts on a point between and just above the eyebrows.
The Divine Energy breaks out and rises above to the crown of the head.

### 32) Meditate on the five voids in the form of five colored circles on a peacock's tail.
When the circles dissolve, one will enter into the Supreme Void within.
`
	ck := newTestChunker(t)
	chunks, err := ck.SplitStructured(doc, "vigyan-bhairava-tantra_FINAL_iast.md")
	require.NoError(t, err)

	got := map[string]bool{}
	for _, c := range chunks {
		for _, v := range strings.Split(c.Metadata[MetaVerse], ",") {
			got[v] = true
		}
	}
	assert.True(t, got["31"], "a technique numbered \"31)\" must be addressable by number")
	assert.True(t, got["32"], "a technique numbered \"32)\" must be addressable by number")
}

// An ordinary numbered list is not verse numbering. The paren form is only
// consulted when nothing else numbered the chunk, so prose that happens to
// contain a list does not acquire bogus verse metadata.
func TestSplitStructuredIgnoresListsWhenAlreadyNumbered(t *testing.T) {
	doc := `# Some Treatise

### Verse 5
The practitioner should observe the following.

1) first item
2) second item
`
	ck := newTestChunker(t)
	chunks, err := ck.SplitStructured(doc, "treatise.md")
	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	assert.Equal(t, "5", chunks[0].Metadata[MetaVerse],
		"a heading-numbered section must not pick up list numbers as verses")
}
