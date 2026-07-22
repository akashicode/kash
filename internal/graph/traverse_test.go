package graph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchWithHopsFindsLineageChain is the motivating case: a query naming
// only the first person in a lineage should surface the rest of the chain,
// which a flat term match can never reach because the downstream facts do not
// contain the query terms at all.
func TestSearchWithHopsFindsLineageChain(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Matsyendranath", Predicate: "was teacher of", Object: "Gorakhnath"},
		{Subject: "Gorakhnath", Predicate: "authored", Object: "Goraksha Samhita"},
		{Subject: "Gorakhnath", Predicate: "founded", Object: "Nath Sampradaya"},
		// unrelated, must not appear
		{Subject: "Mercury", Predicate: "is a type of", Object: "rasa"},
	}, "nath.md"))

	// Flat search: only the fact literally mentioning Matsyendranath
	flat, err := db.Search(ctx, "Matsyendranath", 10)
	require.NoError(t, err)
	require.Len(t, flat, 1)
	assert.Equal(t, "Gorakhnath", flat[0].Object)

	// Traversal: the chain beyond Gorakhnath becomes reachable
	hops, err := db.SearchWithHops(ctx, "Matsyendranath", 10, 1)
	require.NoError(t, err)
	require.Greater(t, len(hops), 1, "traversal must reach beyond the direct match")

	var objects []string
	for _, r := range hops {
		objects = append(objects, r.Object)
	}
	assert.Contains(t, objects, "Goraksha Samhita")
	assert.Contains(t, objects, "Nath Sampradaya")
	assert.NotContains(t, objects, "rasa", "unconnected facts must not be pulled in")

	// Direct matches always outrank traversed ones
	assert.Equal(t, 0, hops[0].Hop)
	for _, r := range hops[1:] {
		assert.Equal(t, 1, r.Hop)
		assert.Equal(t, "Gorakhnath", r.Via, "hop facts must name their connecting entity")
	}
}

// TestSearchWithHopsSuppressesHubs guards against a query touching a
// high-degree entity flooding the results with everything attached to it.
func TestSearchWithHopsSuppressesHubs(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	triples := []Triple{{Subject: "Kundalini", Predicate: "rises through", Object: "Sushumna"}}
	// Make Sushumna a hub well past maxHubDegree
	for i := 0; i < maxHubDegree+50; i++ {
		triples = append(triples, Triple{
			Subject:   "Sushumna",
			Predicate: "describes",
			Object:    fmt.Sprintf("detail %d", i),
		})
	}
	require.NoError(t, db.AddTriples(ctx, triples, "yoga.md"))

	hops, err := db.SearchWithHops(ctx, "Kundalini", 10, 1)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(hops), 10)

	// The hub must not have been expanded through
	for _, r := range hops {
		if r.Hop == 1 {
			assert.NotEqual(t, "Sushumna", r.Via, "expansion through a hub entity must be suppressed")
		}
	}
}

// TestSearchWithHopsCapsPerEntity ensures one connecting entity cannot
// monopolise the expansion budget.
func TestSearchWithHopsCapsPerEntity(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	triples := []Triple{{Subject: "Abhinavagupta", Predicate: "authored", Object: "Tantraloka"}}
	for i := 0; i < 20; i++ {
		triples = append(triples, Triple{
			Subject:   "Tantraloka",
			Predicate: "contains",
			Object:    fmt.Sprintf("chapter %d", i),
		})
	}
	require.NoError(t, db.AddTriples(ctx, triples, "tantraloka.pdf"))

	hops, err := db.SearchWithHops(ctx, "Abhinavagupta", 20, 1)
	require.NoError(t, err)

	via := 0
	for _, r := range hops {
		if r.Hop == 1 && r.Via == "Tantraloka" {
			via++
		}
	}
	assert.LessOrEqual(t, via, maxPerEntity, "per-entity expansion must be capped")
}

// TestSearchWithHopsZeroIsFlat verifies the traversal is opt-in.
func TestSearchWithHopsZeroIsFlat(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "A", Predicate: "was teacher of", Object: "Bxyz"},
		{Subject: "Bxyz", Predicate: "authored", Object: "Cxyz"},
	}, "x.md"))

	flat, err := db.SearchWithHops(ctx, "teacher", 10, 0)
	require.NoError(t, err)
	for _, r := range flat {
		assert.Equal(t, 0, r.Hop)
	}
}

// TestIndexInvalidatedOnMutation ensures the traversal cache does not serve
// stale adjacency after the graph changes.
func TestIndexInvalidatedOnMutation(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Alpha", Predicate: "was teacher of", Object: "Beta"},
	}, "a.md"))

	// Warm the cache
	_, err = db.SearchWithHops(ctx, "Alpha", 10, 1)
	require.NoError(t, err)

	// Add a fact extending the chain
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Beta", Predicate: "authored", Object: "Gamma"},
	}, "a.md"))

	hops, err := db.SearchWithHops(ctx, "Alpha", 10, 1)
	require.NoError(t, err)
	var found bool
	for _, r := range hops {
		if r.Object == "Gamma" {
			found = true
		}
	}
	assert.True(t, found, "index must be rebuilt after a mutation")
}

func TestFormatResultsMarksHops(t *testing.T) {
	out := FormatResults([]SearchResult{
		{Subject: "Matsyendranath", Predicate: "was teacher of", Object: "Gorakhnath", Source: "nath.md"},
		{Subject: "Gorakhnath", Predicate: "founded", Object: "Nath Sampradaya", Source: "nath.md", Hop: 1, Via: "Gorakhnath"},
	})
	assert.Contains(t, out, "- Matsyendranath was teacher of Gorakhnath (source: nath.md)")
	assert.Contains(t, out, "↳ Gorakhnath founded Nath Sampradaya")
	assert.Contains(t, out, "[connected via Gorakhnath]")
	assert.Equal(t, 1, strings.Count(out, "↳"))
}
