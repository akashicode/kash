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
	require.NoError(t, db.AddTriples(ctx, triples, "early-book.pdf"))

	// The most relevant triple comes from a document added last
	late := []Triple{{
		Subject:   "kundalini yoga",
		Predicate: "awakens",
		Object:    "chakra energy",
	}}
	require.NoError(t, db.AddTriples(ctx, late, "late-book.pdf"))

	results, err := db.Search(ctx, "kundalini chakra awakening", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// The late, strongly matching triple must rank first — and cite its source
	assert.Equal(t, "kundalini yoga", results[0].Subject)
	assert.Equal(t, "late-book.pdf", results[0].Source)
}

// TestTripleProvenance ensures sources round-trip through the graph and show
// up as citations in formatted results, while unlabeled triples stay clean.
func TestTripleProvenance(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	labeled := []Triple{{Subject: "Abhinavagupta", Predicate: "authored", Object: "Tantraloka"}}
	require.NoError(t, db.AddTriples(ctx, labeled, "tantraloka-intro.pdf"))

	unlabeled := []Triple{{Subject: "Kashmir Shaivism", Predicate: "flourished in", Object: "Kashmir"}}
	require.NoError(t, db.AddTriples(ctx, unlabeled, ""))

	results, err := db.Search(ctx, "Abhinavagupta Tantraloka Kashmir", 10)
	require.NoError(t, err)
	require.Len(t, results, 2)

	bySubject := map[string]SearchResult{}
	for _, r := range results {
		bySubject[r.Subject] = r
	}
	assert.Equal(t, "tantraloka-intro.pdf", bySubject["Abhinavagupta"].Source)
	assert.Equal(t, "", bySubject["Kashmir Shaivism"].Source)

	formatted := FormatResults(results)
	assert.Contains(t, formatted, "(source: tantraloka-intro.pdf)")
	assert.NotContains(t, formatted, "(source: )")
}

// TestDeleteBySource ensures incremental builds can surgically remove one
// document's triples while leaving other documents untouched.
func TestDeleteBySource(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	keep := []Triple{{Subject: "Gorakhnath", Predicate: "founded", Object: "Nath tradition"}}
	require.NoError(t, db.AddTriples(ctx, keep, "nath-book.pdf"))

	drop := []Triple{
		{Subject: "old fact", Predicate: "belongs to", Object: "stale edition"},
		{Subject: "another old fact", Predicate: "belongs to", Object: "stale edition"},
	}
	require.NoError(t, db.AddTriples(ctx, drop, "stale-book.pdf"))

	require.EqualValues(t, 3, db.Count())
	require.NoError(t, db.DeleteBySource(ctx, "stale-book.pdf"))
	assert.EqualValues(t, 1, db.Count())

	results, err := db.Search(ctx, "Gorakhnath Nath tradition", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "nath-book.pdf", results[0].Source)
}

func TestScoreMatchPrefersWholeWords(t *testing.T) {
	terms := tokenize("art history")

	wholeWord := scoreMatch(terms, "art", "is part of", "history")
	substring := scoreMatch(terms, "particular", "is part of", "prehistoric")

	assert.Greater(t, wholeWord, substring)
}

func TestAllTriples(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	triples := []Triple{
		{Subject: "A", Predicate: "knows", Object: "B"},
		{Subject: "B", Predicate: "knows", Object: "C"},
	}
	require.NoError(t, db.AddTriples(ctx, triples, "doc1.txt"))

	all := db.AllTriples(ctx)
	require.Len(t, all, 2)
	assert.Equal(t, "A", all[0].Subject)
	assert.Equal(t, "doc1.txt", all[0].Source)
}

func TestChunkIDProvenance(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	triples := []Triple{
		{Subject: "Abhinavagupta", Predicate: "authored", Object: "Tantraloka", ChunkID: "tantraloka_0"},
		{Subject: "Abhinavagupta", Predicate: "systematized", Object: "Kashmir Shaivism", ChunkID: "shaivism_1"},
	}
	require.NoError(t, db.AddTriples(ctx, triples, "tantra.md"))

	results, err := db.Search(ctx, "Abhinavagupta Tantraloka", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	assert.Equal(t, "tantra.md", results[0].Source)
	assert.Equal(t, "tantraloka_0", results[0].ChunkID)

	// Format with passage map
	passages := map[string]int{
		"tantraloka_0": 1,
	}
	formatted := FormatResultsWithPassages(results, passages)
	assert.Contains(t, formatted, "(source: tantra.md [passage 1])")

	// Verify DeleteBySource works with chunk ID labels
	require.NoError(t, db.DeleteBySource(ctx, "tantra.md"))
	assert.EqualValues(t, 0, db.Count())
}
