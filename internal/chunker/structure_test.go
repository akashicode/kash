package chunker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/config"
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

// sanskritMatchers returns the ref matchers for the Sanskrit preset, matching
// verse/shloka/sutra/dharana/vidhi numbering patterns.
func sanskritMatchers() []refMatcher {
	return CompileRefMatchers([]config.RefPattern{
		{Pattern: `(?i)(?:^|[^a-z])(?:verse|śloka|shloka|sloka)\s*[-–—]?\s*(\d+)`, MetaKey: MetaVerse},
		{Pattern: `(?i)(?:dh[aā]ra[nṇ][aā]|vidhi)\s*[-–—]?\s*(\d+)`, MetaKey: MetaDharana},
		// Bare "32)" numbering used by some English VBT editions
		{Pattern: `^\s*(\d{1,3})\)`, MetaKey: MetaVerse},
	})
}

func newTestChunker(t *testing.T) *Chunker {
	t.Helper()
	ck, err := NewChunker(Options{ChunkSize: 2000, Overlap: 400})
	require.NoError(t, err)
	return ck
}

func TestSplitStructuredExtractsReferences(t *testing.T) {
	ck := newTestChunker(t)
	matchers := sanskritMatchers()

	chunks, err := ck.SplitStructured(vbtDoc, "vigyan-bhairava-tantra_FINAL_iast.md", matchers)
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
	matchers := sanskritMatchers()

	chunks, err := ck.SplitStructured(vbtDoc, "vbt.md", matchers)
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
	matchers := sanskritMatchers()

	chunks, err := ck.SplitStructured(vbtDoc, "vigyan-bhairava-tantra_FINAL_iast.md", matchers)
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

	chunks, err := ck.SplitStructured(sb.String(), "plain.txt", nil)
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
	matchers := sanskritMatchers()
	chunks, err := ck.SplitStructured(doc, "vigyan-bhairava-tantra_FINAL_iast.md", matchers)
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
	matchers := sanskritMatchers()
	chunks, err := ck.SplitStructured(doc, "treatise.md", matchers)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	assert.Equal(t, "5", chunks[0].Metadata[MetaVerse],
		"a heading-numbered section must not pick up list numbers as verses")
}

// When a table is split across chunk boundaries, continuation chunks must
// carry the table header forward so column context is preserved.
func TestSplitStructuredCarriesTableHeaderAcrossChunks(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# Policy Document\n\n## Fee Schedule\n\n")
	sb.WriteString("| Service Code | Description | Standard Fee | Copay |\n")
	sb.WriteString("|---|---|---|---|\n")
	for i := 1; i <= 60; i++ {
		sb.WriteString(fmt.Sprintf("| SVC-%03d | Detailed procedure description for item number %d | $%.2f | $20.00 |\n",
			i, i, float64(i)*15.5))
	}

	ck, err := NewChunker(Options{ChunkSize: 1000, Overlap: 100})
	require.NoError(t, err)

	chunks, err := ck.SplitStructured(sb.String(), "policy.md", nil)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1, "a 60-row table must split into multiple chunks")

	expectedHeader := "| Service Code | Description | Standard Fee | Copay |\n|---|---|---|---|"
	for i, c := range chunks {
		assert.Contains(t, c.Content, expectedHeader,
			"chunk %d of split table must contain the column header row", i)
		assert.Equal(t, ContentTable, c.Metadata[MetaContentType],
			"chunk %d must be classified as a table", i)
	}
}

// A reference pattern with no capture group used to compile happily and then
// panic on first use: FindAllStringSubmatch returns rows of length one and
// extractRefs indexes hit[1]. That was reachable only through a hand-written
// agent.yaml typo, but becomes a shippable crash once patterns are generated.
func TestCompileRefMatchersRejectsBadPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr string
	}{
		{"no capture group", `(?i)verse\s+\d+`, "one capture group"},
		{"two capture groups", `(?i)(verse)\s+(\d+)`, "one capture group"},
		{"invalid regex", `(?i)verse\s+(\d+`, "invalid ref_pattern"},
		{"over length limit", `(?i)` + strings.Repeat("a", MaxRefPatternLen) + `(\d+)`, "exceeds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := CompileRefMatchersVerbose([]config.RefPattern{
				{Pattern: tt.pattern, MetaKey: "verse"},
			})
			assert.Empty(t, got, "a rejected pattern must not be compiled in")
			require.Len(t, warnings, 1, "a rejected pattern must be reported, not dropped silently")
			assert.Contains(t, warnings[0], tt.wantErr)
		})
	}
}

func TestCompileRefMatchersAcceptsValidPattern(t *testing.T) {
	got, warnings := CompileRefMatchersVerbose([]config.RefPattern{
		{Pattern: `(?i)verse\s+(\d+)`, MetaKey: "verse"},
	})
	assert.Len(t, got, 1)
	assert.Empty(t, warnings)
}

// A pattern that survives compilation must be safe to run — this is the
// regression guard for the hit[1] panic.
func TestSplitStructuredSurvivesAllCompiledPatterns(t *testing.T) {
	matchers, _ := CompileRefMatchersVerbose([]config.RefPattern{
		{Pattern: `(?i)verse\s+\d+`, MetaKey: "verse"},   // rejected: no group
		{Pattern: `(?i)verse\s+(\d+)`, MetaKey: "verse"}, // kept
	})
	ck := newTestChunker(t)

	assert.NotPanics(t, func() {
		_, err := ck.SplitStructured("# Doc\n\n### Verse 12\nBody text here.\n", "d.md", matchers)
		require.NoError(t, err)
	})
}
