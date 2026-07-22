package server

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/vector"
)

// TestDiversifyBySource ensures a single source document cannot monopolize
// the selected context when other sources have relevant chunks.
func TestDiversifyBySource(t *testing.T) {
	ranked := []vector.SearchResult{}
	for i := 0; i < 6; i++ {
		ranked = append(ranked, vector.SearchResult{ID: fmt.Sprintf("a%d", i), Source: "bookA.pdf"})
	}
	ranked = append(ranked,
		vector.SearchResult{ID: "b0", Source: "bookB.pdf"},
		vector.SearchResult{ID: "c0", Source: "bookC.pdf"},
	)

	selected := diversifyBySource(ranked, 5)

	assert.Len(t, selected, 5)
	perSource := map[string]int{}
	for _, r := range selected {
		perSource[r.Source]++
	}
	// topK=5 → max 3 per source; bookB and bookC must both make it in
	assert.Equal(t, 3, perSource["bookA.pdf"])
	assert.Equal(t, 1, perSource["bookB.pdf"])
	assert.Equal(t, 1, perSource["bookC.pdf"])
}

// TestDiversifyBySourceBackfill ensures slots are backfilled from a single
// source when no other sources are available.
func TestDiversifyBySourceBackfill(t *testing.T) {
	ranked := []vector.SearchResult{}
	for i := 0; i < 8; i++ {
		ranked = append(ranked, vector.SearchResult{ID: fmt.Sprintf("a%d", i), Source: "onlybook.pdf"})
	}

	selected := diversifyBySource(ranked, 5)
	assert.Len(t, selected, 5)
}

// TestGraphContextBoostResolvesHomonyms models the reported saṃskāra failure:
// a query about the alchemical sense matched facts about the karmic sense
// equally well, because graph matching is purely lexical. Facts from documents
// that semantic retrieval actually selected must outrank the homonyms.
func TestGraphContextBoostResolvesHomonyms(t *testing.T) {
	// Semantic retrieval correctly surfaced the alchemical texts
	chunks := []vector.SearchResult{
		{ID: "r1", Source: "Rasarnavam_FINAL.md"},
		{ID: "r2", Source: "Rasa Hridaya Tantra.md"},
	}

	// The graph, matching only strings, scores both senses similarly —
	// and here the wrong sense even scores slightly higher.
	candidates := []graph.SearchResult{
		{Subject: "samskara", Predicate: "is", Object: "karmic residue", Source: "Anant ki Aur.md", Score: 6},
		{Subject: "samskara", Predicate: "ripens into", Object: "memory", Source: "Anant ki Aur.md", Score: 6},
		{Subject: "samskara", Predicate: "is a type of", Object: "mercury purification", Source: "Rasarnavam_FINAL.md", Score: 4},
		{Subject: "eighteen samskaras", Predicate: "purify", Object: "mercury", Source: "Rasa Hridaya Tantra.md", Score: 4},
	}

	ranked := rankFactsByContext(candidates, chunks, 4)

	require.Len(t, ranked, 4)
	// The alchemical facts must now lead
	assert.Equal(t, "Rasarnavam_FINAL.md", ranked[0].Source)
	assert.Equal(t, "Rasa Hridaya Tantra.md", ranked[1].Source)
	assert.Equal(t, "Anant ki Aur.md", ranked[2].Source)
}

// TestGraphContextBoostWithoutChunksIsPassthrough ensures the boost degrades
// gracefully when there is no semantic signal to disambiguate with.
func TestGraphContextBoostWithoutChunksIsPassthrough(t *testing.T) {
	candidates := []graph.SearchResult{
		{Subject: "a", Source: "x.md", Score: 3},
		{Subject: "b", Source: "y.md", Score: 1},
	}
	ranked := rankFactsByContext(candidates, nil, 10)
	assert.Equal(t, candidates, ranked)
}

func TestDiversifyBySourceFewerThanTopK(t *testing.T) {
	ranked := []vector.SearchResult{
		{ID: "a0", Source: "bookA.pdf"},
		{ID: "b0", Source: "bookB.pdf"},
	}
	selected := diversifyBySource(ranked, 5)
	assert.Equal(t, ranked, selected)
}
