package chunker

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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
	m, _ := CompileRefMatchersVerbose([]config.RefPattern{
		{Pattern: `(?i)(?:^|[^a-z])(?:verse|śloka|shloka|sloka)\s*[-–—]?\s*(\d+)`, MetaKey: MetaVerse},
		{Pattern: `(?i)(?:dh[aā]ra[nṇ][aā]|vidhi)\s*[-–—]?\s*(\d+)`, MetaKey: MetaDharana},
		// Bare "32)" numbering used by some English VBT editions
		{Pattern: `^\s*(\d{1,3})\)`, MetaKey: MetaVerse},
	})
	return m
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

// Consecutive pieces of a section overlap so a passage split across a chunk
// boundary reads whole in both. When two such pieces are packed into one chunk
// there is no boundary to bridge, and joining them verbatim stored the shared
// text twice — a sentence appearing once in the source appeared twice in what
// the reader was shown. On the corpus this was found on, removing this strip
// puts the duplication back: 0 affected chunks becomes 9 and 4 becomes 25.
//
// stripCarry is deliberately conservative. Text is removed only when the piece
// demonstrably begins with what was carried into it, because a corpus repeats
// lines for real reasons and those must survive.
func TestStripCarry(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		carry string
		want  string
	}{
		{
			name:  "removes the carried prefix",
			text:  "9) Know that to be insubstantial.\n\n10) The world is a dream.",
			carry: "9) Know that to be insubstantial.",
			want:  "10) The world is a dream.",
		},
		{
			name:  "a piece that is nothing but carry empties out",
			text:  "9) Know that to be insubstantial.",
			carry: "9) Know that to be insubstantial.",
			want:  "",
		},
		{
			name:  "tolerates blank lines on either side",
			text:  "\nfirst carried line\n\n\nsecond carried line\n\nnew material here",
			carry: "first carried line\n\nsecond carried line",
			want:  "new material here",
		},
		{
			// The rejected alternative — matching the seam by eye — would eat
			// this. A repeated line that was not carried must be left alone.
			name:  "leaves text that merely resembles the carry",
			text:  "Digitised by the archive.\n\nA different opening line.",
			carry: "A different opening line.",
			want:  "Digitised by the archive.\n\nA different opening line.",
		},
		{
			name:  "leaves text when the carry runs past the end of it",
			text:  "only one line here",
			carry: "only one line here\nand a second the piece never had",
			want:  "only one line here",
		},
		{
			name:  "no carry is a no-op",
			text:  "unchanged text",
			carry: "",
			want:  "unchanged text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripCarry(tt.text, tt.carry))
		})
	}
}

// A chunk should never show the same line twice when the source shows it once.
// This is a broad invariant over the packer rather than a guard on one change:
// it holds whether the repetition would have come from the overlap carry or
// from a piece that was nothing but carry.
func TestSplitStructuredDoesNotRepeatOverlapWithinAChunk(t *testing.T) {
	// A section whose body splits into pieces that then pack back into one
	// chunk — a wide overlap relative to the section is what makes the second
	// piece little more than a repeat of the first one's tail.
	var sb strings.Builder
	sb.WriteString("# Operations Manual\n\n## Escalation Procedure\n\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&sb, "Step %d: notify the duty officer and record the incident reference in the log.\n\n", i)
	}

	ck, err := NewChunker(Options{ChunkSize: 400, Overlap: 200})
	require.NoError(t, err)
	chunks, err := ck.SplitStructured(sb.String(), "manual.md", nil)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1, "fixture must split into several chunks")

	for i, c := range chunks {
		seen := map[string]bool{}
		for _, line := range strings.Split(c.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			assert.False(t, seen[line],
				"chunk %d repeats a line that occurs once in the source: %q", i, line)
			seen[line] = true
		}
	}
}

// Text that genuinely repeats must survive. Stripping the overlap by comparing
// the seam would eat a refrain, a repeated table header, or the digitisation
// footer that recurs on every page of a scanned book.
func TestSplitStructuredKeepsGenuinelyRepeatedText(t *testing.T) {
	doc := `# Field Notes

## Observations

Digitised by the archive. All rights reserved.

The specimen was measured at first light and again at dusk.

Digitised by the archive. All rights reserved.

A second specimen was recovered from the eastern slope.
`
	ck, err := NewChunker(Options{ChunkSize: 2000, Overlap: 400})
	require.NoError(t, err)
	chunks, err := ck.SplitStructured(doc, "notes.md", nil)
	require.NoError(t, err)
	require.Len(t, chunks, 1, "fixture is small enough to be one chunk")

	assert.Equal(t, 2, strings.Count(chunks[0].Content, "Digitised by the archive."),
		"a line the source really does repeat must not be stripped as overlap")
}

// A heading is whatever follows the hashes, so a document that numbers its
// passages by putting the passage in the heading produced a citation header
// hundreds of characters long — unquotable, and prefixed to every chunk of that
// section. The budget clamp meant to prevent that clamped the deduction rather
// than the header, so an oversized header both rendered in full and pushed the
// finished chunk past ChunkSize.
func TestContextHeaderCapsLongSegments(t *testing.T) {
	long := "4.2 The Provider shall ensure that all consignments accepted for carriage " +
		"are handled in accordance with the schedule of charges set out in this agreement"

	got := contextHeader(map[string]string{
		MetaBook:       "Service Agreement",
		MetaBreadcrumb: "Service Agreement > " + long,
		"clause":       "22",
	})

	assert.LessOrEqual(t, utf8.RuneCountInString(got), 160,
		"a citation header must stay short enough to quote; got %q", got)
	assert.Contains(t, got, "…", "a truncated segment should show it was cut")
	assert.Contains(t, got, "Clause 22", "the reference label must survive truncation")
	assert.Contains(t, got, "The Provider shall", "the start of the heading must be kept")
}

func TestSplitStructuredKeepsChunksWithinChunkSize(t *testing.T) {
	long := "The Provider shall ensure that all consignments accepted for carriage are " +
		"handled in accordance with the schedule of charges set out in this agreement"

	// Many short sections, each its own heading — the shape that packs several
	// units into one chunk and so fills the buffer to the limit before the
	// citation header is prefixed to it.
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Service Agreement\n\n## %s\n\n", long)
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&sb, "### Item %d\nCharges are reviewed annually and published in the schedule of fees.\n\n", i)
	}

	const size = 1000
	ck, err := NewChunker(Options{ChunkSize: size, Overlap: 100})
	require.NoError(t, err)
	chunks, err := ck.SplitStructured(sb.String(), "agreement.md", nil)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	for i, c := range chunks {
		assert.LessOrEqual(t, utf8.RuneCountInString(c.Content), size,
			"chunk %d is %d runes, over the %d-rune limit it was budgeted against",
			i, utf8.RuneCountInString(c.Content), size)
	}
}

// genericMatchers returns domain-neutral reference matchers — the shapes a
// contract, statute or specification uses. Kept separate from
// sanskritMatchers so the tagging rules can be tested without any assumption
// about what the corpus is about.
func genericMatchers() []refMatcher {
	m, _ := CompileRefMatchersVerbose([]config.RefPattern{
		{Pattern: `(?i)\b(?:section|article|part)\s*(\d[\d.]*)`, MetaKey: MetaSection},
		{Pattern: `(?i)\bclause\s+(\d[\d.]*)`, MetaKey: "clause"},
		// Bare "12)" numbering, anchored to the start of a line.
		{Pattern: `^\s*(\d{1,4})\)`, MetaKey: "item"},
	})
	return m
}

// Reference patterns are matched against a whole multi-line chunk body, so a
// leading ^ has to mean start-of-line. Compiled without (?m) it meant
// start-of-body instead, and every marker but the first in a chunk went
// untagged — on the corpus this was found on, 22 of 112 references were
// silently unaddressable while sitting in plain text.
//
// This is deliberately written with generic references: the rule is about
// where a marker sits in a chunk, not about what kind of document it is.
func TestSplitStructuredTagsReferencesAwayFromChunkStart(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		metaKey string
		want    []string
	}{
		{
			// The regression: markers on their own lines, none of them first.
			name: "anchored pattern mid-body",
			doc: "# Schedule of Fees\n\n## Listing\n\n" +
				"7) handling charge\n\n8) late payment charge\n\n9) reissue charge\n",
			metaKey: "item",
			want:    []string{"7", "8", "9"},
		},
		{
			// The control: an unanchored pattern always worked.
			name:    "unanchored pattern mid-body",
			doc:     "# Agreement\n\n## Terms\n\nThe parties agree.\n\nSee clause 14 and clause 15 below.\n",
			metaKey: "clause",
			want:    []string{"14", "15"},
		},
		{
			// A heading answers the key, so the body is not consulted for it.
			// An ordinary numbered list must not become reference numbering.
			name: "heading wins over body list",
			doc: "# Policy\n\n### Section 4\nThe applicant must supply the following.\n\n" +
				"1) proof of address\n2) proof of income\n",
			metaKey: MetaSection,
			want:    []string{"4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ck := newTestChunker(t)
			chunks, err := ck.SplitStructured(tt.doc, "doc.md", genericMatchers())
			require.NoError(t, err)
			require.NotEmpty(t, chunks)

			got := map[string]bool{}
			for _, c := range chunks {
				for _, v := range strings.Split(c.Metadata[tt.metaKey], ",") {
					if v = strings.TrimSpace(v); v != "" {
						got[v] = true
					}
				}
			}
			for _, w := range tt.want {
				assert.True(t, got[w],
					"reference %q must be addressable under key %q, got %v", w, tt.metaKey, got)
			}
		})
	}
}

// The chunker and the retrieval layer compile the same configured patterns for
// different inputs — chunk bodies and queries. When they compiled them
// differently, a query could name a reference the index had never recorded.
// Both now go through CompileRefPattern; this pins the flag it applies.
func TestCompileRefPatternIsLineAnchored(t *testing.T) {
	re, err := CompileRefPattern(config.RefPattern{
		Pattern: `^\s*(\d{1,4})\)`, MetaKey: "item",
	})
	require.NoError(t, err)

	hits := re.FindAllStringSubmatch("intro line\n7) first\n8) second\n", -1)
	require.Len(t, hits, 2, "^ must match at each line start, not only at the start of the string")
	assert.Equal(t, "7", hits[0][1])
	assert.Equal(t, "8", hits[1][1])
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

// A reference pattern ends in (\d[\d.]*) so it can capture a dotted number like
// "4.2". That class also captures the full stop ending a sentence, so a clause
// cited mid-prose was stored as "22." — indexed, and unreachable by every query
// asking for 22.
func TestNormalizeRefValueDropsAccidentalPunctuation(t *testing.T) {
	tests := []struct{ in, want string }{
		{"22.", "22"},
		{" 22. ", "22"},
		{"4.2", "4.2"},
		{"4.2.", "4.2"},
		{"7", "7"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, NormalizeRefValue(tt.in), "input %q", tt.in)
	}
}

// Heading precedence is per section, not per chunk. A section whose heading
// already numbers it must not take numbers from its own body — but a different
// section packed into the same chunk has to be judged on its own evidence.
//
// Reading the whole chunk at once let one numbered heading silence every other
// section's numbering. It stayed invisible while a bare listing wrote a
// different metadata key from the heading, and became a real loss as soon as
// both named the same key: a listing of 31) and 32) beside a "Clause 33"
// heading lost both of its numbers.
func TestHeadingPrecedenceIsPerSectionNotPerChunk(t *testing.T) {
	doc := `# Operations Manual

### Clause 33
The duty officer records each incident reference in the log.

## Schedule of Charges

31) Handling charge, applied per consignment accepted for carriage.

32) Late payment charge, accruing daily on any sum outstanding.
`
	matchers, _ := CompileRefMatchersVerbose([]config.RefPattern{
		{Pattern: `^\s*(\d{1,4})\)`, MetaKey: "clause"},
		{Pattern: `(?i)\bclauses?\s*(\d[\d.]*)`, MetaKey: "clause"},
	})
	ck, err := NewChunker(Options{ChunkSize: 2000, Overlap: 200})
	require.NoError(t, err)
	chunks, err := ck.SplitStructured(doc, "manual.md", matchers)
	require.NoError(t, err)
	require.Len(t, chunks, 1, "fixture must pack both sections into one chunk")

	got := map[string]bool{}
	for _, v := range strings.Split(chunks[0].Metadata["clause"], ",") {
		got[strings.TrimSpace(v)] = true
	}
	for _, want := range []string{"31", "32", "33"} {
		assert.True(t, got[want],
			"clause %s must be addressable; a neighbouring numbered heading must not "+
				"suppress this section's own numbering. got %v", want, got)
	}
}
