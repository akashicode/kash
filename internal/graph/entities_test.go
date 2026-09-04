package graph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntityFacts(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	triples := []Triple{
		{Subject: "Abhinavagupta", Predicate: "authored", Object: "Tantraloka"},
		{Subject: "Abhinavagupta", Predicate: "was disciple of", Object: "Laksmanagupta"},
		{Subject: "Abhinavagupta", Predicate: "systematized", Object: "Kashmir Shaivism"},
		{Subject: "Gorakhnath", Predicate: "founded", Object: "Nath tradition"},
		{Subject: "Gorakhnath", Predicate: "was disciple of", Object: "Matsyendranath"},
		{Subject: "SingletonEntity", Predicate: "mentioned in", Object: "Passing"},
	}
	require.NoError(t, db.AddTriples(ctx, triples, "source.md"))

	// minDegree = 2 should include Abhinavagupta and Gorakhnath, but exclude SingletonEntity
	facts := db.EntityFacts(ctx, 2)
	require.NotEmpty(t, facts)

	byName := map[string]EntityFacts{}
	for _, f := range facts {
		byName[f.Name] = f
	}

	require.Contains(t, byName, "Abhinavagupta")
	assert.GreaterOrEqual(t, byName["Abhinavagupta"].Degree, 3)
	assert.Contains(t, byName["Abhinavagupta"].Facts, "authored Tantraloka")
	assert.Contains(t, byName["Abhinavagupta"].Facts, "was disciple of Laksmanagupta")

	require.Contains(t, byName, "Gorakhnath")
	assert.GreaterOrEqual(t, byName["Gorakhnath"].Degree, 2)
	assert.Contains(t, byName["Gorakhnath"].Facts, "founded Nath tradition")

	assert.NotContains(t, byName, "SingletonEntity", "degree 1 entity must be excluded when minDegree is 2")
}

func TestSearchWithSeeds(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	triples := []Triple{
		{Subject: "Abhinavagupta", Predicate: "authored", Object: "Tantraloka"},
		{Subject: "Tantraloka", Predicate: "describes", Object: "Kaula ritual"},
		{Subject: "Kaula ritual", Predicate: "involves", Object: "Cakra puja"},
	}
	require.NoError(t, db.AddTriples(ctx, triples, "tantra.md"))

	// Query that does NOT match text "Abhinavagupta", but has "Abhinavagupta" as seed entity
	results, err := db.SearchWithSeeds(ctx, "Kaula", []string{"Abhinavagupta"}, nil, 5, 1)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	foundDirect := false
	for _, r := range results {
		if r.Subject == "Abhinavagupta" {
			foundDirect = true
			assert.Equal(t, 0, r.Hop)
		}
	}
	assert.True(t, foundDirect, "seed entity facts must be included")

	// Query with seedTriples (from relationship vector retrieval)
	seedRel := []Triple{
		{Subject: "Kaula ritual", Predicate: "involves", Object: "Cakra puja"},
	}
	relResults, err := db.SearchWithSeeds(ctx, "", nil, seedRel, 5, 1)
	require.NoError(t, err)
	require.NotEmpty(t, relResults)
	assert.Equal(t, "Kaula ritual", relResults[0].Subject)
	assert.Equal(t, 0, relResults[0].Hop)
}
