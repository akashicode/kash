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
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/vector"
)

// This reproduces the reported cross-book failure: asked what Sun Tzu and
// Machiavelli say about deception, the agent answered that The Prince says
// nothing on it. Retrieval was at fault. The question names each book by its
// author, and an author's name occurs in his own book only in apparatus — the
// title page and a translator's footnote — so every route that matches names
// lifted those chunks: BM25 on the literal name, graph provenance from the
// author entity, and a reranker that reads "Machiavelli" as relevance. They
// took The Prince's slots, and the chapter on keeping faith, which says
// "deceive" rather than "deception", never reached the slate.

var comparativeCorpus = map[string]string{
	"the-art-of-war.md": `# The Art of War

Sun Tzu. Translated from the Chinese by Lionel Giles (1910).

## Chapter I. Laying Plans

The art of war is of vital importance to the State. It is governed by five
constant factors: the moral law, heaven, earth, the commander, and method and
discipline. These are to be weighed in deliberation before any campaign.

The general who weighs the five factors before battle will conquer. All
warfare rests on deception: when able to attack, we must seem unable; when
near, we must make the enemy believe we are far away. Hold out baits to
entice him, feign disorder, and crush him.

## Chapter III. Attack by Stratagem

The highest form of generalship is to balk the enemy's plans; the next is to
prevent the junction of his forces. To subdue the enemy without fighting is
the acme of skill.

## Chapter X. Terrain

Ground may be accessible, entangling, temporising, narrow, precipitous, or
distant. Study the ground before committing the army to it.

## Chapter XIII. The Use of Spies

Foreknowledge cannot be had from spirits; it must be obtained from men who
know the enemy's situation. Spies are the most important element in war.
`,

	"the-prince.md": `# The Prince

Niccolo Machiavelli. Translated by W. K. Marriott.

## Dedication

To the Magnificent Lorenzo, a small volume offered as the most precious gift
its author holds: the knowledge of the actions of great men.

## Chapter XV. Things for which men, and especially princes, are praised or blamed

It is necessary for a prince wishing to hold his own to know how to do wrong,
and to make use of it or not according to necessity.

## Chapter XVII. Cruelty and clemency

It is much safer to be feared than loved, when one of the two must be
dispensed with, though a prince should avoid being hated. Cesare Borgia was
accounted cruel, yet his cruelty reconciled the Romagna.

## Chapter XVIII. Concerning the way in which princes should keep faith

A prudent lord cannot keep faith when doing so turns against him. He must
know how to play the fox, to be a great pretender and dissembler; men are so
simple that one who seeks to deceive will always find someone who allows
himself to be deceived.

## Chapter XX. Are fortresses useful

Fortresses are useful or not according to circumstances. The best fortress is
not to be hated by the people.

Note: a contemporary letter records that Nicolo Machiavelli, secretary to the
Ten, was to leave with the envoy at once.
`,
}

// contractCorpus is two agreements whose cover page names the party, and
// whose indemnity clause names it again. The party is a subject of the
// contract, not a byline: the clause that names it is the answer.
//
// The filenames share no title word on purpose. The work grouper would read
// "Supply Agreement" and "Services Agreement" as editions of one work.
var contractCorpus = map[string]string{
	"supply-contract.md": `# Supply Agreement

Between Acme Corporation, the Supplier, and the Buyer, dated 1 March.

## 1. Definitions

Goods means the products listed in the first schedule. Delivery Point means
the warehouse named in each purchase order. Business Day means a day other
than a weekend or public holiday in the place of delivery.

## 9. Indemnity

Acme shall provide an indemnity to the Buyer against every loss arising from
defective goods, including recall costs and reasonable legal fees.
`,

	"consulting-services.md": `# Services Agreement

Between Beta Limited, the Provider, and the Client, dated 4 May.

## 1. Definitions

Services means the work described in the statement of work. Deliverables
means anything produced under it. Working Hours means nine to five on any
weekday that is not a public holiday where the Client is based.

## 12. Indemnity

Beta shall provide an indemnity to the Client against third-party claims
arising from the services, capped at the fees paid in the prior year.
`,
}

// biographyCorpus is two lives under neutral filenames, so their subjects are
// on the title page but not in every chunk header. A subject is named
// throughout his own biography and must never be treated as a byline.
var biographyCorpus = map[string]string{
	"frontier-president.md": `# A Life

Abraham Lincoln, sixteenth president of the United States.

## Youth

Lincoln grew up on the frontier in Kentucky and Indiana, splitting rails and
reading every book he could borrow from neighbours, often walking miles to
fetch one and reading it by firelight after the day's work was done.

## Law

Lincoln rode the eighth judicial circuit as a lawyer for two decades, trying
cases in county courthouses across the prairie and building the reputation
for plain honesty that later carried him back into national politics.

## War

Lincoln held the Union together through four years of civil war, replacing
generals until he found ones who would fight, and framing the struggle as a
test of whether government by the people could endure at all.
`,

	"corsican-emperor.md": `# A Life

Napoleon Bonaparte, emperor of the French.

## Youth

Napoleon trained as an artillery officer at the military school in Brienne,
a Corsican outsider among the sons of French nobility who mocked his accent
and his poverty, and who mostly lived to serve under him.

## Italy

Napoleon won his first campaign in Italy by speed and concentration of force,
marching his ragged army faster than the Austrians thought possible and
beating their divided columns one at a time before they could combine.

## Exile

Napoleon spent his last years on Saint Helena in the South Atlantic, dictating
his memoirs to a small circle of companions and shaping the legend that would
outlive both his empire and the dynasty he had hoped to found.
`,
}

// filler is n ordinary chapters with none of the words the tests ask about.
// A real book runs to hundreds of chunks, and the share of them naming the
// author is what tells a byline from a subject; three chapters would make an
// author look like the topic.
func filler(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "\n## Part %d. Ledger %d\n\n", i+100, i)
		fmt.Fprintf(&b, "The ledger for year %d records grain, timber and salt moved along the river\n", 1500+i)
		b.WriteString("roads, counted twice each season by the clerks so that no shortfall passes\n")
		fmt.Fprintf(&b, "unnoticed before the stores of granary %d are sealed for winter.\n", i)
	}
	return b.String()
}

// fixtureIndex chunks a fixture corpus exactly as `kash build` would, after
// padding every document with pad chapters of filler.
func fixtureIndex(t *testing.T, corpus map[string]string, pad int) (*lexical.Index, map[string]vector.SearchResult) {
	t.Helper()

	ck, err := chunker.NewChunker(chunker.Options{ChunkSize: 400, Overlap: 0})
	require.NoError(t, err)

	ix := lexical.NewWithFold(agentconfig.DiacriticLatin)
	byID := map[string]vector.SearchResult{}

	names := make([]string, 0, len(corpus))
	for name := range corpus {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		chunks, err := ck.SplitStructured(corpus[name]+filler(pad), name, nil)
		require.NoError(t, err)
		for _, c := range chunks {
			meta := map[string]string{"source": c.Source}
			for k, v := range c.Metadata {
				meta[k] = v
			}
			ix.Add(c.ID, c.Content, meta)
			byID[c.ID] = vector.SearchResult{ID: c.ID, Content: c.Content, Source: c.Source, Metadata: meta}
		}
	}
	ix.Finalize()
	return ix, byID
}

// comparativeIndex is the two-book corpus at a realistic length: the author
// is named in under a tenth of his own book, as on the demo agent.
func comparativeIndex(t *testing.T) (*lexical.Index, map[string]vector.SearchResult) {
	t.Helper()
	return fixtureIndex(t, comparativeCorpus, 20)
}

// chunkWith returns the ID of the fixture chunk containing text.
func chunkWith(t *testing.T, byID map[string]vector.SearchResult, text string) string {
	t.Helper()
	for id, r := range byID {
		if strings.Contains(r.Content, text) {
			return id
		}
	}
	require.FailNow(t, "fixture has no chunk containing "+text)
	return ""
}

// nameAnchoredRoutes replays the dense and graph routes as measured on the
// demo agent for this question. The reranker placed the Art of War title page
// and the Machiavelli footnote above the chapter on keeping faith, and graph
// provenance from the two author entities led with the chunks naming them,
// reaching the chapter on keeping faith only fifth.
func nameAnchoredRoutes(t *testing.T, byID map[string]vector.SearchResult) map[string][]string {
	t.Helper()
	aowFront := chunkWith(t, byID, "Lionel Giles")
	princeFront := chunkWith(t, byID, "Translated by W. K. Marriott")
	footnote := chunkWith(t, byID, "secretary to the\nTen")
	deception := chunkWith(t, byID, "All\nwarfare rests on deception")
	keepFaith := chunkWith(t, byID, "seeks to deceive")
	stratagem := chunkWith(t, byID, "balk the enemy")

	vec := []string{aowFront, deception, footnote, stratagem, princeFront, keepFaith}
	seen := map[string]bool{}
	for _, id := range vec {
		seen[id] = true
	}
	var rest []string
	for id := range byID {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)

	return map[string][]string{
		"vector": append(vec, rest...),
		"graph":  {princeFront, footnote, aowFront, deception, keepFaith},
	}
}

// comparativeRetrieve runs fusion and slate selection the way retrieve() does,
// with the dense and graph routes replayed and dq standing in for the query
// decomposition.
func comparativeRetrieve(t *testing.T, query string, dq llm.DecomposedQuery, topK int) []vector.SearchResult {
	t.Helper()
	ix, byID := comparativeIndex(t)
	fc := buildFusionConfig(agentconfig.DefaultDomainConfig())

	lists := nameAnchoredRoutes(t, byID)
	lex, _ := lexicalRoutes(ix, fc.router, query, 200)
	lists["lexical"] = lex

	cands := fuseRankLists(lists)
	for id, c := range cands {
		c.result = byID[id]
		applyNoisePenalty(c)
	}

	var out []vector.SearchResult
	for _, c := range selectSlate(cands, topK, fc, ix, dq) {
		out = append(out, c.result)
	}
	return out
}

func TestComparativeQuerySurfacesEveryNamedBook(t *testing.T) {
	const query = "What do Sun Tzu and Machiavelli say about deception?"

	concepts := []string{"deception", "strategy", "military tactics"}
	tests := []struct {
		name string
		dq   llm.DecomposedQuery
		topK int
	}{
		{
			name: "authors named",
			dq:   llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Machiavelli"}, BroadConcepts: concepts},
			topK: 5,
		},
		{
			name: "authors named, small slate",
			dq:   llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Machiavelli"}, BroadConcepts: concepts},
			topK: 3,
		},
		{
			// "Niccolo" is on the title page but the footnote spells it
			// "Nicolo", so only the title page carries this byline.
			name: "full name on the title page only",
			dq:   llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Niccolo Machiavelli"}, BroadConcepts: concepts},
			topK: 5,
		},
		{
			name: "no concepts decomposed",
			dq:   llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Machiavelli"}},
			topK: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := comparativeRetrieve(t, query, tt.dq, tt.topK)
			require.Len(t, got, tt.topK)

			var ids []string
			var keepFaith, deception bool
			for _, r := range got {
				ids = append(ids, r.ID)
				keepFaith = keepFaith || strings.Contains(r.Content, "seeks to deceive")
				deception = deception || strings.Contains(r.Content, "warfare rests on deception")
				assert.NotContains(t, r.Content, "Translated by W. K. Marriott",
					"The Prince's title page took a slot; slate %v", ids)
			}
			assert.True(t, keepFaith, "The Prince's chapter on keeping faith is missing; slate %v", ids)
			assert.True(t, deception, "The Art of War's passage on deception is missing; slate %v", ids)
		})
	}
}

func TestNameWorks(t *testing.T) {
	type fixture struct {
		ix   *lexical.Index
		byID map[string]vector.SearchResult
	}
	load := func(corpus map[string]string, pad int) fixture {
		ix, byID := fixtureIndex(t, corpus, pad)
		return fixture{ix: ix, byID: byID}
	}
	books := load(comparativeCorpus, 20)
	contracts := load(contractCorpus, 20)
	lives := load(biographyCorpus, 8)
	shortBooks := load(comparativeCorpus, 0)

	tests := []struct {
		name      string
		fx        fixture
		dq        llm.DecomposedQuery
		wantWorks int
		// wantBylines are text fragments locating the chunks that must be
		// demoted; every other chunk must not be.
		wantBylines []string
	}{
		{
			name:        "authors are bylines",
			fx:          books,
			dq:          llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Machiavelli"}, BroadConcepts: []string{"deception"}},
			wantWorks:   2,
			wantBylines: []string{"Lionel Giles", "Translated by W. K. Marriott", "secretary to the\nTen"},
		},
		{
			// The footnote holds "secretary", so it matches the question on
			// its own terms and keeps its rank.
			name:        "a byline chunk that also holds a concept is spared",
			fx:          books,
			dq:          llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Machiavelli"}, BroadConcepts: []string{"secretary"}},
			wantWorks:   2,
			wantBylines: []string{"Lionel Giles", "Translated by W. K. Marriott"},
		},
		{
			// Every chunk carries its book title in its header, so a title
			// names its work without making any chunk a byline.
			name:      "titles name works but are not bylines",
			fx:        books,
			dq:        llm.DecomposedQuery{SpecificEntities: []string{"The Art of War", "The Prince"}},
			wantWorks: 2,
		},
		{
			// Borgia occurs only in The Prince but not on its title page: a
			// subject of the book, not its author.
			name:        "subject off the title page is not a byline",
			fx:          books,
			dq:          llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Cesare Borgia"}},
			wantWorks:   2,
			wantBylines: []string{"Lionel Giles"},
		},
		{
			// The party is on the cover and rare enough to pass for a byline,
			// but the indemnity clause also matches the concept asked about.
			// Only the cover pages go down.
			name:        "contract party in the answering clause is spared",
			fx:          contracts,
			dq:          llm.DecomposedQuery{SpecificEntities: []string{"Acme", "Beta"}, BroadConcepts: []string{"indemnity terms"}},
			wantWorks:   2,
			wantBylines: []string{"Between Acme Corporation", "Between Beta Limited"},
		},
		{
			// A biography names its subject on the title page and on a third
			// of its pages: the subject, not a byline.
			name:      "biography subjects are not bylines",
			fx:        lives,
			dq:        llm.DecomposedQuery{SpecificEntities: []string{"Lincoln", "Napoleon"}, BroadConcepts: []string{"leadership"}},
			wantWorks: 2,
		},
		{
			// In a book only a few chunks long, an author named twice is a
			// large share of it. Too little evidence to call it a byline, so
			// nothing moves.
			name:      "short works are left alone",
			fx:        shortBooks,
			dq:        llm.DecomposedQuery{SpecificEntities: []string{"Sun Tzu", "Machiavelli"}, BroadConcepts: []string{"deception"}},
			wantWorks: 2,
		},
		{
			// One named work is not a comparison; nothing is demoted.
			name:      "single work",
			fx:        books,
			dq:        llm.DecomposedQuery{SpecificEntities: []string{"Machiavelli", "deceive"}},
			wantWorks: 1,
		},
		{
			name:      "name found nowhere",
			fx:        books,
			dq:        llm.DecomposedQuery{SpecificEntities: []string{"Clausewitz", "Machiavelli"}},
			wantWorks: 1,
		},
		{
			// "know" appears in both books, so it selects neither.
			name:      "name spread across works",
			fx:        books,
			dq:        llm.DecomposedQuery{SpecificEntities: []string{"know"}},
			wantWorks: 0,
		},
	}

	fc := buildFusionConfig(agentconfig.DefaultDomainConfig())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nw := nameWorks(tt.fx.ix, fc.grouper(), tt.dq)
			assert.Len(t, nw.works, tt.wantWorks)

			var want []string
			for _, text := range tt.wantBylines {
				want = append(want, chunkWith(t, tt.fx.byID, text))
			}
			sort.Strings(want)

			var got []string
			for id := range nw.bylines {
				got = append(got, id)
			}
			sort.Strings(got)
			assert.Equal(t, want, got)
		})
	}
}
