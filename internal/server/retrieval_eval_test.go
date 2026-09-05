package server

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/chunker"
	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/lexical"
	"github.com/akashicode/kash/internal/vector"
)

// This is a retrieval evaluation harness, not a unit test. It reproduces the
// reported failure — queries for specific dhāraṇās returning index tables and
// no scripture — against a fixture corpus shaped like the real one, and locks
// in recall@5 so fusion constants can be changed with evidence rather than
// hope.
//
// The vector route is stubbed rather than live: it replays the ranking
// inversion measured against the real agent, so the harness exercises fusion
// without needing an embedder. TestNegativeControlVectorOnly runs the same
// queries through that route alone — the behaviour before this work — so the
// two numbers can be compared directly.
//
// Note what this does and does not establish. It shows the lexical and
// exact-reference routes are load-bearing (0.40 -> 1.00). It does not isolate
// the noise penalty: this fixture has too few apparatus chunks for that
// constant to change the outcome, so do not read these numbers as evidence for
// its value.

// evalCorpus mirrors the structures that matter in the real corpus: a verse-
// per-heading scripture with commentary, a Sanskrit edition numbering the same
// techniques as dhāraṇās, and the ślokānukramaṇī concordance pages that were
// out-ranking both.
var evalCorpus = map[string]string{
	"vigyan-bhairava-tantra_FINAL_iast.md": `# Vigyan Bhairava Tantra

## THE MEDITATIONS

### Verse 25
Concentrate on the two places where the breath turns from inside to outside.
O Goddess, in this way the essential form of God is realized.

This meditation is a slight variation of the previous one. Instead of focussing
on the origin of the inbreath, focus on the turning of the breath.

### Verse 32
Meditate on the void in the skull. Fix the mind there with steady awareness.
Light will be revealed, and the form of Bhairava will appear to the seeker.

The skull meditation is practised seated with the eyes closed. Attention rests
at the crown, where the practitioner senses an opening into emptiness.

### Verse 72
From the pleasure of eating and drinking, one experiences joy blossoming.
One should become filled with that state of joy. Then great joy will be obtained.
`,

	"Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md": `# vijnanabhairava

## dhāraṇā-48
kaverdikaranam jnanam sarvatra paramesvarah .
bhavana paramanandamayi cittam samaviset ..71..

## dhāraṇā-49
gitadivisayasvadasamasaukhyaikatatmanah .
yoginastanmayatvena manorudhestadatmata ..72..

isa dharana mem sadhaka gita adi ke rasasvadana se utpanna sukha mem tanmaya
hokara usi rupa ko prapta kara leta hai . yaha ananda brahmananda ka hi rupa hai .

## dhāraṇā-50
yatra yatra manastuptirmanastatraiva dharayet .
tatra tatra paranandasvarupam sampravartate ..73..
`,

	"Tantra Sangraha 2 - Gopinath Kaviraj_FINAL_iast.md": `# Tantra Sangraha 2

## ślokānukramaṇī

| ślokāṃśa | anukramāṃka |
|----------|------------|
| bhuvaḥ śikhāyāṃ svaḥkāraṃ | 7 |
| bhūmigāḥ paripaśyanti | 18 |
| bhūmau śirasi cākāśe | 9 |
| yadrūpaṃ dhāma golokaṃ | 13 |
| bhūmau śūnye tathā mūrdhni | 49 |
| yadrūpaṃ brahmasadanaṃ | 12 |
| bhūrlokādi maheśāni | 2 |
| yantramadhye ca vṛttābhaṃ | 21 |
| dhyeyaṃ pañcākṣaraṃ mantrī | 49 |
| niṣiddhācaraṇaṃ pāpaṃ | 32 |
| nīlakaṇṭhasya yanmantraṃ | 49 |
| namo harāya namaskāraṃ | 25 |
| lakṣāntare tu deveśi | 49 |
| viparītakrameṇaiva | 32 |
`,

	"Merutantra - original.txt": `sthirāṇi vimavāgrāṇa rākra nasgaṇaḥ smarataḥ haṣyantyāmetrādityendvāṣvijyakara
ghoṭakāḥ devāḥ śeṣā rākṣasāḥ syuḥ sādhyasādhakayoḥ pham cadevayo phara śīghaṃ
vyastayoḥ kaṣṭataḥ phalam cedetyayomaṃnayāhācyastayo maraṇa bhavat davadānavayo
yuddhaṃ vyastayoniṣpharambhavet hopaymaha bhantaṣu śārācakara vicārathat vedā
rāmāścayo yugma yugma vadānta ṣaṭ ṣaḍagāśca tataḥ ṣaṭsu nāmvaṇastharānmanāḥ
`,

	// A bare verse listing: the numbering carries the reference and no heading
	// repeats it, so every marker but the first sits mid-body. Compiled
	// without (?m), only the first was ever tagged and the rest of the run was
	// unaddressable by number.
	"vbt-verse-listing_FINAL_iast.md": `# Vigyan Bhairava Tantra

## The Verses

104) Wherever the mind goes, whether outside or within, there is the state of
Shiva. Since Shiva is omnipresent, where can the mind go to escape him?

105) When one perceives a particular object, the perception of all other
objects becomes empty. Meditating on that emptiness, one is pacified.

106) The awareness of object and subject is common to all living beings.
However, the yogis have the distinction that they are always aware of the self.
`,

	// A non-Indic document with ordinary structural references. Reference
	// handling is domain-neutral by design, and this is what pins that: the
	// exact-reference route must work on a schedule of clauses exactly as it
	// works on a run of verses.
	"service-agreement.md": `# Service Agreement

## Schedule of Charges

The Provider shall levy the charges set out below. Amounts are exclusive of tax
and are reviewed annually in accordance with clause 22.

7) Handling charge, applied per consignment accepted for carriage.

8) Late payment charge, accruing daily on any sum outstanding after the due
date until payment is received in full.

9) Reissue charge, payable where a replacement document is requested by the
Customer after despatch.
`,
}

// evalDomainConfig returns the domain configuration for the fixture corpus.
// It carries both Indic and ordinary structural reference patterns, because
// reference handling is domain-neutral and the harness should prove it.
func evalDomainConfig() agentconfig.DomainConfig {
	return agentconfig.DomainConfig{
		Resolution: agentconfig.ResolutionConfig{
			FoldDiacritics:  agentconfig.DiacriticIAST,
			StripFinalVowel: true,
		},
		Chunker: agentconfig.ChunkerConfig{
			RefPatterns: []agentconfig.RefPattern{
				{Pattern: `(?i)(?:^|[^a-z])(?:verse|śloka|shloka|sloka|sutra|sūtra)\s*[-–—.]?\s*(\d{1,3})\b`, MetaKey: "verse"},
				{Pattern: `(?i)(?:dh[aā]ra[nṇ][aā]|vidhi)\s*[-–—.]?\s*(\d+)`, MetaKey: "dharana"},
				{Pattern: `(?:\.\.|\|\||॥)\s*(\d+)\s*(?:\.\.|\|\||॥)`, MetaKey: "verse"},
				{Pattern: `^\s*(\d{1,3})\)`, MetaKey: "verse"},
				{Pattern: `(?i)\bclause\s+(\d[\d.]*)`, MetaKey: "clause"},
			},
			TitleStopwords: []string{
				"the", "of", "and", "with", "by", "for", "vol", "volume", "part",
				"final", "iast", "ocr", "original", "hindi", "english", "sanskrit",
				"translation", "commentary", "tika", "tantra", "tantram", "tantras",
				"paddhati", "paddhat", "samhita", "samhit",
			},
			StripTitleStemVowel: true,
		},
	}
}

// evalIndex builds the fixture corpus exactly as `kash build` would.
func evalIndex(t *testing.T) (*lexical.Index, map[string]vector.SearchResult) {
	t.Helper()

	// Use Sanskrit domain config for this corpus (VBT-based eval fixtures).
	dc := evalDomainConfig()
	refMatchers, _ := chunker.CompileRefMatchersVerbose(dc.Chunker.RefPatterns)

	ck, err := chunker.NewChunker(chunker.Options{ChunkSize: 2000, Overlap: 400})
	require.NoError(t, err)

	ix := lexical.NewWithFold(dc.Resolution.FoldDiacritics)
	byID := map[string]vector.SearchResult{}

	for name, body := range evalCorpus {
		chunks, err := ck.SplitStructured(body, name, refMatchers)
		require.NoError(t, err)
		for _, c := range chunks {
			meta := map[string]string{"source": c.Source}
			for k, v := range c.Metadata {
				meta[k] = v
			}
			ix.Add(c.ID, c.Content, meta)
			byID[c.ID] = vector.SearchResult{
				ID: c.ID, Content: c.Content, Source: c.Source, Metadata: meta,
			}
		}
	}
	ix.Finalize()
	return ix, byID
}

// pathologicalVectorRoute replays the dense-retrieval behaviour actually
// measured against the live agent: concordance and table-of-contents pages
// scored 0.64–0.66 on these queries while genuine prose scored 0.56, so the
// apparatus outranked the scripture. Ranking apparatus first reproduces that
// inversion, which is what the noise penalty and exact-reference route exist to
// correct. Without this the harness would pass on BM25 alone and prove nothing
// about the ranking fixes.
func pathologicalVectorRoute(byID map[string]vector.SearchResult) []string {
	var apparatus, prose []string
	for id, r := range byID {
		if r.Metadata[chunker.MetaContentType] != chunker.ContentProse {
			apparatus = append(apparatus, id)
		} else {
			prose = append(prose, id)
		}
	}
	sort.Strings(apparatus)
	sort.Strings(prose)
	return append(apparatus, prose...)
}

// evalRetrieve runs the vector, lexical, exact-reference and fusion stages —
// the same code path retrieve() uses, with the vector route stubbed to the
// measured failure mode.
func evalRetrieve(ix *lexical.Index, byID map[string]vector.SearchResult, query string, topK int) []vector.SearchResult {
	dc := evalDomainConfig()
	fc := buildFusionConfig(dc)

	lists := map[string][]string{
		"vector": pathologicalVectorRoute(byID),
	}

	var lexIDs []string
	for _, r := range ix.Search(query, 200) {
		lexIDs = append(lexIDs, r.ID)
	}
	lists["lexical"] = lexIDs

	var exact []string
	for _, ref := range fc.router.queryRefs(query) {
		for _, r := range ix.FindByRef(ref.Field, ref.Value) {
			exact = append(exact, r.ID)
		}
	}
	if len(exact) > 0 {
		lists["exact"] = exact
	}

	cands := fuseRankLists(lists)
	for id, c := range cands {
		r, ok := byID[id]
		if !ok {
			delete(cands, id)
			continue
		}
		c.result = r
		if containsRoute(c.routes, "exact") {
			c.score += exactRefBoost
		}
		applyNoisePenalty(c)
	}

	ranked := dedupeNearDuplicates(rankCandidates(cands), nearDuplicateThreshold)
	out := make([]vector.SearchResult, 0, topK)
	for _, c := range selectDiverse(ranked, topK, fc) {
		out = append(out, c.result)
	}
	return out
}

func TestRetrievalRecallOnReportedFailures(t *testing.T) {
	ix, byID := evalIndex(t)

	tests := []struct {
		name       string
		query      string
		wantSource string
		wantIn     string // substring the winning chunk must contain
		// wantRefKey/wantRefVal, when set, assert the winning chunk was
		// actually tagged with the reference the query names. Retrieval alone
		// is a weak assertion here: BM25 finds a number in the body text
		// whether or not it was ever indexed as a reference, so without this
		// the exact-reference route could rot unnoticed.
		wantRefKey string
		wantRefVal string
	}{
		{
			// Reported failure: returned four ślokānukramaṇī tables and one
			// page of OCR noise, and no Vijñāna Bhairava content at all.
			name:       "dharana by number",
			query:      "dharana 49",
			wantSource: "Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md",
			wantIn:     "dhāraṇā-49",
		},
		{
			// Reported failure: returned table-of-contents pages from other
			// books at higher similarity than genuine prose.
			name:       "verse by number",
			query:      "Verse 32 Vigyan Bhairava Tantra",
			wantSource: "vigyan-bhairava-tantra_FINAL_iast.md",
			wantIn:     "void in the skull",
		},
		{
			// The marker sits mid-body in a run of them, which is the case
			// reference patterns compiled without (?m) could not tag: the
			// number was in the text and still unaddressable.
			name:       "verse numbered away from the chunk start",
			query:      "Verse 106 Vigyan Bhairava Tantra",
			wantSource: "vbt-verse-listing_FINAL_iast.md",
			wantIn:     "awareness of object and subject",
			wantRefKey: "verse",
			wantRefVal: "106",
		},
		{
			// Nothing about the exact-reference route is Indic. A schedule of
			// clauses must resolve by number exactly as a run of verses does.
			name:       "ordinary clause reference",
			query:      "service agreement clause 22",
			wantSource: "service-agreement.md",
			wantIn:     "reviewed annually",
		},
		{
			name:       "semantic phrase still works",
			query:      "pleasure of eating and drinking joy blossoming",
			wantSource: "vigyan-bhairava-tantra_FINAL_iast.md",
			wantIn:     "eating and drinking",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evalRetrieve(ix, byID, tt.query, 5)
			require.NotEmpty(t, got, "query %q returned nothing", tt.query)

			var sources []string
			var winner *vector.SearchResult
			for i, r := range got {
				sources = append(sources, r.Source)
				if winner == nil && r.Source == tt.wantSource && strings.Contains(r.Content, tt.wantIn) {
					winner = &got[i]
				}
			}
			require.NotNil(t, winner,
				"expected a chunk from %s containing %q in the top %d; got sources %v",
				tt.wantSource, tt.wantIn, len(got), sources)

			if tt.wantRefKey != "" {
				assert.Contains(t, splitRefs(winner.Metadata[tt.wantRefKey]), tt.wantRefVal,
					"the retrieved chunk must be tagged %s=%s, not merely contain the number in its text; got %q",
					tt.wantRefKey, tt.wantRefVal, winner.Metadata[tt.wantRefKey])
			}
		})
	}
}

// TestApparatusDoesNotDominate is the precision half of the same failure: the
// concordance tables are legitimate content and stay indexed, but must not fill
// the slate ahead of scripture.
func TestApparatusDoesNotDominate(t *testing.T) {
	ix, byID := evalIndex(t)

	got := evalRetrieve(ix, byID, "dharana 49", 5)
	require.NotEmpty(t, got)

	apparatus := 0
	for _, r := range got {
		if r.Metadata[chunker.MetaContentType] != chunker.ContentProse {
			apparatus++
		}
	}
	assert.Less(t, apparatus, len(got),
		"index tables filled the entire result slate: %d of %d", apparatus, len(got))
	assert.Equal(t, "Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md", got[0].Source,
		"the passage headed dhāraṇā-49 must rank first for 'dharana 49'")
}

// TestRecallAtK reports recall@k over the fixture set so a change in fusion
// constants shows up as a number rather than a guess.
func TestRecallAtK(t *testing.T) {
	ix, byID := evalIndex(t)

	cases := []struct{ query, wantSource string }{
		{"dharana 49", "Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md"},
		{"dharana 50", "Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md"},
		{"verse 25 breath turns inside outside", "vigyan-bhairava-tantra_FINAL_iast.md"},
		{"verse 32 void in the skull", "vigyan-bhairava-tantra_FINAL_iast.md"},
		{"joy blossoming from eating", "vigyan-bhairava-tantra_FINAL_iast.md"},
	}

	hits := 0
	for _, c := range cases {
		for _, r := range evalRetrieve(ix, byID, c.query, 5) {
			if r.Source == c.wantSource {
				hits++
				break
			}
		}
	}

	recall := float64(hits) / float64(len(cases))
	fmt.Printf("recall@5 = %.2f (%d/%d)\n", recall, hits, len(cases))
	assert.Equal(t, len(cases), hits, "every fixture query must retrieve its source within the top 5")
}

// splitRefs unpacks the comma-joined reference values stored in chunk
// metadata, so an assertion on "106" cannot be satisfied by "1060".
func splitRefs(meta string) []string {
	var out []string
	for _, v := range strings.Split(meta, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}



// TestNegativeControlVectorOnly measures the pipeline as it behaved before this
// work: dense retrieval alone, with no lexical or exact-reference route. It is
// the baseline TestRecallAtK is compared against, and it is what makes that
// test meaningful rather than merely green.
func TestNegativeControlVectorOnly(t *testing.T) {
	_, byID := evalIndex(t)

	cases := []struct{ query, wantSource string }{
		{"dharana 49", "Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md"},
		{"dharana 50", "Vijnanabhairava - Vrajvallabha Dwivedi_FINAL_iast.md"},
		{"verse 25 breath turns inside outside", "vigyan-bhairava-tantra_FINAL_iast.md"},
		{"verse 32 void in the skull", "vigyan-bhairava-tantra_FINAL_iast.md"},
		{"joy blossoming from eating", "vigyan-bhairava-tantra_FINAL_iast.md"},
	}

	fc := buildFusionConfig(evalDomainConfig())
	hits := 0
	for _, c := range cases {
		cands := fuseRankLists(map[string][]string{"vector": pathologicalVectorRoute(byID)})
		for id, cd := range cands {
			cd.result = byID[id]
		}
		ranked := dedupeNearDuplicates(rankCandidates(cands), nearDuplicateThreshold)
		for _, cd := range selectDiverse(ranked, 5, fc) {
			if cd.result.Source == c.wantSource {
				hits++
				break
			}
		}
	}

	recall := float64(hits) / float64(len(cases))
	fmt.Printf("vector-only recall@5 = %.2f (%d/%d)\n", recall, hits, len(cases))
	assert.Less(t, recall, 1.0,
		"the baseline must fail some queries, otherwise TestRecallAtK proves nothing")
}
