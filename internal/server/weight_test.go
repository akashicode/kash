package server

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/vector"
)

func weightKey(s, p, o string) string { return graph.FoldKey(s, p, o) }

// A fact attested in many passages should outrank one seen once, even when the
// query matched them equally.
func TestWeightPromotesWellAttestedFacts(t *testing.T) {
	candidates := []graph.SearchResult{
		{Subject: "Shiva", Predicate: "manifests as", Object: "Rare", Score: 1.0, Source: "a.md"},
		{Subject: "Shiva", Predicate: "manifests as", Object: "Common", Score: 1.0, Source: "a.md"},
	}
	weights := map[string]float64{
		weightKey("Shiva", "manifests as", "Common"): 20,
		weightKey("Shiva", "manifests as", "Rare"):   1,
	}

	ranked := rankFactsByContext(candidates, nil, 10, weights)

	require.Len(t, ranked, 2)
	assert.Equal(t, "Common", ranked[0].Object, "the better-attested fact must lead")
}

// An index built before weights existed has none. Every fact then takes the
// same log1p(1) factor, which scales scores uniformly and leaves the order the
// query produced untouched.
func TestAbsentWeightsPreserveOrdering(t *testing.T) {
	candidates := []graph.SearchResult{
		{Subject: "A", Predicate: "rel", Object: "B", Score: 3.0, Source: "x.md"},
		{Subject: "C", Predicate: "rel", Object: "D", Score: 2.0, Source: "x.md"},
		{Subject: "E", Predicate: "rel", Object: "F", Score: 1.0, Source: "x.md"},
	}
	chunks := []vector.SearchResult{{ID: "x_md_0", Source: "other.md"}}

	ranked := rankFactsByContext(candidates, chunks, 10, nil)

	require.Len(t, ranked, 3)
	assert.Equal(t, "B", ranked[0].Object)
	assert.Equal(t, "D", ranked[1].Object)
	assert.Equal(t, "F", ranked[2].Object)
	// log1p(1), not zero — a missing weight must never erase a fact's score.
	assert.InDelta(t, 3.0*math.Log1p(1), ranked[0].Score, 1e-9)
}

// A zero weight is "not recorded", not "no evidence". Multiplying by log1p(0)
// would zero the score and sink the fact below everything else.
func TestZeroWeightIsTreatedAsOne(t *testing.T) {
	candidates := []graph.SearchResult{
		{Subject: "A", Predicate: "rel", Object: "B", Score: 2.0, Source: "x.md"},
	}
	chunks := []vector.SearchResult{{ID: "x_md_0", Source: "other.md"}}

	ranked := rankFactsByContext(candidates, chunks, 10, map[string]float64{
		weightKey("A", "rel", "B"): 0,
	})

	require.Len(t, ranked, 1)
	assert.Greater(t, ranked[0].Score, 0.0, "a zero weight must not erase the score")
	assert.InDelta(t, 2.0*math.Log1p(1), ranked[0].Score, 1e-9)
}

// Context boosts still decide between facts of equal weight — the weight is a
// corpus-time signal layered onto the query-time one, not a replacement.
func TestWeightDoesNotOverrideChunkContext(t *testing.T) {
	candidates := []graph.SearchResult{
		{Subject: "A", Predicate: "rel", Object: "InContext", Score: 1.0, ChunkID: "x_md_0", Source: "x.md"},
		{Subject: "A", Predicate: "rel", Object: "OutOfContext", Score: 1.0, Source: "y.md"},
	}
	chunks := []vector.SearchResult{{ID: "x_md_0", Source: "x.md"}}
	weights := map[string]float64{
		weightKey("A", "rel", "InContext"):    1,
		weightKey("A", "rel", "OutOfContext"): 3,
	}

	ranked := rankFactsByContext(candidates, chunks, 10, weights)

	require.Len(t, ranked, 2)
	assert.Equal(t, "InContext", ranked[0].Object,
		"a fact from a retrieved passage outranks a better-attested one the reader cannot see")
}
