package server

import (
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

// comparativeIndex chunks the fixture exactly as `kash build` would.
func comparativeIndex(t *testing.T) (*lexical.Index, map[string]vector.SearchResult) {
	t.Helper()

	ck, err := chunker.NewChunker(chunker.Options{ChunkSize: 400, Overlap: 0})
	require.NoError(t, err)

	ix := lexical.NewWithFold(agentconfig.DiacriticLatin)
	byID := map[string]vector.SearchResult{}

	names := make([]string, 0, len(comparativeCorpus))
	for name := range comparativeCorpus {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		chunks, err := ck.SplitStructured(comparativeCorpus[name], name, nil)
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
// provenance from the two author entities led with the chunks naming them.
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
		"graph":  {princeFront, footnote, aowFront, deception},
	}
}

// comparativeRetrieve runs fusion and slate selection the way retrieve() does,
// with the dense and graph routes replayed and entities standing in for the
// query decomposition.
func comparativeRetrieve(t *testing.T, query string, entities []string, topK int) []vector.SearchResult {
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
	for _, c := range selectSlate(cands, topK, fc, ix, entities) {
		out = append(out, c.result)
	}
	return out
}

func TestComparativeQuerySurfacesEveryNamedBook(t *testing.T) {
	const query = "What do Sun Tzu and Machiavelli say about deception?"

	tests := []struct {
		name     string
		entities []string
		topK     int
	}{
		{name: "authors named", entities: []string{"Sun Tzu", "Machiavelli"}, topK: 5},
		{name: "authors named, small slate", entities: []string{"Sun Tzu", "Machiavelli"}, topK: 3},
		{
			// "Niccolo" is on the title page but the footnote spells it
			// "Nicolo", so only the title page carries this byline.
			name: "full name on the title page only", entities: []string{"Sun Tzu", "Niccolo Machiavelli"}, topK: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := comparativeRetrieve(t, query, tt.entities, tt.topK)
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
	ix, byID := comparativeIndex(t)
	fc := buildFusionConfig(agentconfig.DefaultDomainConfig())

	aowFront := chunkWith(t, byID, "Lionel Giles")
	princeFront := chunkWith(t, byID, "Translated by W. K. Marriott")
	footnote := chunkWith(t, byID, "secretary to the\nTen")

	tests := []struct {
		name        string
		entities    []string
		wantWorks   int
		wantBylines []string
	}{
		{
			name:        "authors are bylines",
			entities:    []string{"Sun Tzu", "Machiavelli"},
			wantWorks:   2,
			wantBylines: []string{aowFront, footnote, princeFront},
		},
		{
			// Every chunk carries its book title in its header, so a title
			// names its work without making any chunk a byline.
			name:      "titles name works but are not bylines",
			entities:  []string{"The Art of War", "The Prince"},
			wantWorks: 2,
		},
		{
			// Borgia occurs only in The Prince but not on its title page: a
			// subject of the book, not its author.
			name:        "subject off the title page is not a byline",
			entities:    []string{"Sun Tzu", "Cesare Borgia"},
			wantWorks:   2,
			wantBylines: []string{aowFront},
		},
		{
			// One named work is not a comparison; nothing is demoted.
			name:      "single work",
			entities:  []string{"Machiavelli", "deceive"},
			wantWorks: 1,
		},
		{
			name:      "name found nowhere",
			entities:  []string{"Clausewitz", "Machiavelli"},
			wantWorks: 1,
		},
		{
			// "know" appears in both books, so it selects neither.
			name:      "name spread across works",
			entities:  []string{"know"},
			wantWorks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nw := nameWorks(ix, fc.grouper(), tt.entities)
			assert.Len(t, nw.works, tt.wantWorks)

			var bylines []string
			for id := range nw.bylines {
				bylines = append(bylines, id)
			}
			sort.Strings(bylines)
			want := append([]string(nil), tt.wantBylines...)
			sort.Strings(want)
			assert.Equal(t, want, bylines)
		})
	}
}
