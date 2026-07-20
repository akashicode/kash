package graph

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchReachesLateTriples guards against early-exit bias: a strongly
// matching triple must surface even when hundreds of weakly matching triples
// from earlier documents precede it in storage order.
func TestSearchReachesLateTriples(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	triples := []Triple{}
	for i := 0; i < 200; i++ {
		triples = append(triples, Triple{
			Subject:   fmt.Sprintf("early book concept %d", i),
			Predicate: "relates to",
			Object:    "yoga practice",
		})
	}
	// The most relevant triple is added last
	triples = append(triples, Triple{
		Subject:   "kundalini yoga",
		Predicate: "awakens",
		Object:    "chakra energy",
	})
	require.NoError(t, db.AddTriples(ctx, triples))

	results, err := db.Search(ctx, "kundalini chakra awakening", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// The late, strongly matching triple must rank first
	assert.Equal(t, "kundalini yoga", results[0].Subject)
}

func TestScoreMatchPrefersWholeWords(t *testing.T) {
	terms := tokenize("art history")

	wholeWord := scoreMatch(terms, "art", "is part of", "history")
	substring := scoreMatch(terms, "particular", "is part of", "prehistoric")

	assert.Greater(t, wholeWord, substring)
}
