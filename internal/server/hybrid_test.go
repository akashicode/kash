package server

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/vector"
)

// defaultTestFusionCfg returns a fusionConfig with generic defaults for tests.
// Tests that don't care about Sanskrit folding or custom stopwords use this.
func defaultTestFusionCfg() *fusionConfig {
	return buildFusionConfig(agentconfig.DefaultDomainConfig())
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

func TestGraphContextBoostChunkLevel(t *testing.T) {
	// Retrieved chunk is specifically chunk "doc_1" from "big_book.md"
	chunks := []vector.SearchResult{
		{ID: "doc_1", Source: "big_book.md"},
	}

	candidates := []graph.SearchResult{
		// Fact from same book but another chunk (only gets contextDocBoost 2.5)
		{Subject: "general fact", Source: "big_book.md", ChunkID: "doc_99", Score: 2.0},
		// Fact from exact retrieved chunk (gets contextChunkBoost 4.0)
		{Subject: "precise fact", Source: "big_book.md", ChunkID: "doc_1", Score: 1.5},
		// Fact from unrelated book (no boost)
		{Subject: "other fact", Source: "other.md", ChunkID: "other_1", Score: 3.0},
	}

	ranked := rankFactsByContext(candidates, chunks, 3)
	require.Len(t, ranked, 3)

	// Precise fact: 1.5 * 4.0 = 6.0
	// General fact: 2.0 * 2.5 = 5.0
	// Other fact: 3.0 * 1.0 = 3.0
	assert.Equal(t, "precise fact", ranked[0].Subject)
	assert.Equal(t, "general fact", ranked[1].Subject)
	assert.Equal(t, "other fact", ranked[2].Subject)
}

func cands(specs ...[2]string) []*candidate {
	var out []*candidate
	for _, sp := range specs {
		out = append(out, &candidate{
			id: sp[0],
			result: vector.SearchResult{
				ID:       sp[0],
				Source:   sp[1],
				Content:  "content of " + sp[0],
				Metadata: map[string]string{"source": sp[1]},
			},
		})
	}
	return out
}

// TestSelectDiverseSpreadsAcrossWorks keeps the original guarantee: when the
// evidence really is spread across books, one book must not monopolize.
func TestSelectDiverseSpreadsAcrossWorks(t *testing.T) {
	var specs [][2]string
	for i := 0; i < 4; i++ {
		specs = append(specs, [2]string{fmt.Sprintf("a%d", i), "Rasarnavam.md"})
	}
	specs = append(specs,
		[2]string{"b0", "Yogini Tantra.md"}, [2]string{"b1", "Yogini Tantra.md"},
		[2]string{"c0", "Goraksh Paddhati.md"}, [2]string{"c1", "Goraksh Paddhati.md"},
		[2]string{"d0", "Sambodhi.md"}, [2]string{"d1", "Sambodhi.md"},
	)

	fc := defaultTestFusionCfg()
	selected := selectDiverse(cands(specs...), 5, fc)

	assert.Len(t, selected, 5)
	g := fc.grouper()
	perWork := map[string]int{}
	for _, c := range selected {
		perWork[g.Key(c.result)]++
	}
	assert.LessOrEqual(t, perWork[g.Key(selected[0].result)], 3)
	assert.Greater(t, len(perWork), 1, "a spread result set must stay spread")
}

// TestSelectDiverseAllowsDominantWork covers the regression this replaced: for
// a focused question about one text, the per-source cap discarded the
// best-ranked chunks in favour of lower-ranked ones from unrelated books.
func TestSelectDiverseAllowsDominantWork(t *testing.T) {
	var specs [][2]string
	for i := 0; i < 9; i++ {
		specs = append(specs, [2]string{fmt.Sprintf("v%d", i), "vigyan-bhairava-tantra.md"})
	}
	specs = append(specs, [2]string{"x0", "Merutantra.txt"})

	fc := defaultTestFusionCfg()
	selected := selectDiverse(cands(specs...), 5, fc)

	require.Len(t, selected, 5)
	for _, c := range selected {
		assert.Equal(t, "vigyan-bhairava-tantra.md", c.result.Source,
			"a question concentrated in one text must be answered from that text")
	}
}

// TestWorkKeyGroupsEditions is the mechanism behind the reported symptom: six
// editions of one text under six filenames were treated as six independent
// books, so they competed for the same slots.
func TestWorkKeyGroupsEditions(t *testing.T) {
	editions := []string{
		"vigyan-bhairava-tantra_FINAL_iast.md",
		"vigyan-bhairav-tantra-hindi_FINAL_iast.md",
		"VijnanaBhairava-khemraj_FINAL_iast.md",
	}
	fc := defaultTestFusionCfg()
	g := fc.grouper()
	keys := map[string]bool{}
	for _, e := range editions {
		keys[g.Key(vector.SearchResult{Source: e})] = true
	}
	assert.Len(t, keys, 1, "editions of one work must share a key, got %v", keys)
}

func TestSelectDiverseFewerThanTopK(t *testing.T) {
	in := cands([2]string{"a0", "bookA.pdf"}, [2]string{"b0", "bookB.pdf"})
	fc := defaultTestFusionCfg()
	assert.Equal(t, in, selectDiverse(in, 5, fc))
}

func TestGraphRRFPromotion(t *testing.T) {
	// Chunk c1 is ranked lower in vector search, but is top-ranked in graph search.
	// Chunk c2 is top-ranked in vector search, but absent in graph search.
	lists := map[string][]string{
		"vector": {"c2", "c1", "c3"},
		"graph":  {"c1"},
	}

	cands := fuseRankLists(lists)
	ranked := rankCandidates(cands)

	require.NotEmpty(t, ranked)
	// c1 receives votes from both vector (rank 1) and graph (rank 0), so it outranks c2:
	// c1: 1/(60+1) + 1/(60+0) = 0.01639 + 0.01667 = 0.03306
	// c2: 1/(60+0) = 0.01667
	assert.Equal(t, "c1", ranked[0].id)
	assert.Contains(t, ranked[0].routes, "graph")
	assert.Contains(t, ranked[0].routes, "vector")
}
