package server

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/vector"
)

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

	selected := selectDiverse(cands(specs...), 5)

	assert.Len(t, selected, 5)
	g := newWorkGrouper()
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

	selected := selectDiverse(cands(specs...), 5)

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
	g := newWorkGrouper()
	keys := map[string]bool{}
	for _, e := range editions {
		keys[g.Key(vector.SearchResult{Source: e})] = true
	}
	assert.Len(t, keys, 1, "editions of one work must share a key, got %v", keys)
}

func TestSelectDiverseFewerThanTopK(t *testing.T) {
	in := cands([2]string{"a0", "bookA.pdf"}, [2]string{"b0", "bookB.pdf"})
	assert.Equal(t, in, selectDiverse(in, 5))
}
