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

// A triple whose originating chunk could not be identified must degrade to a
// document-level citation rather than borrow another chunk's identity. The
// build's findBestChunk returns "" when no chunk in the batch shows evidence
// for the fact; inventing an ID there would print a passage citation the
// passage does not support and hand the fact a chunk-level ranking boost.
func TestChunkIDAbsentDegradesToSourceCitation(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Gorakhnath", Predicate: "founded", Object: "Nath Sampradaya"},
	}, "nath.md"))

	results, err := db.Search(ctx, "Gorakhnath Nath Sampradaya", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	assert.Equal(t, "nath.md", results[0].Source, "source must survive without a chunk ID")
	assert.Empty(t, results[0].ChunkID, "unknown provenance must stay unknown")

	// With no chunk ID there is nothing to match against the passage map, so
	// the citation falls back to the document alone.
	formatted := FormatResultsWithPassages(results, map[string]int{"other_chunk_0": 1})
	assert.Contains(t, formatted, "(source: nath.md)")
	assert.NotContains(t, formatted, "passage")
}

// A chunk ID is an internal identifier, not a citation. When the supporting
// chunk was not among the retrieved passages there is nothing for a reader to
// look up, so the fact must degrade to its document rather than print an ID
// that looks like a reference to a model instructed to cite inline.
func TestUnretrievedChunkDoesNotLeakItsID(t *testing.T) {
	results := []SearchResult{{
		Subject:   "Gorakhnath",
		Predicate: "founded",
		Object:    "Nath Sampradaya",
		Source:    "nath.md",
		ChunkID:   "nath_md_312",
	}}

	// The passage map holds a different chunk, as it will whenever a graph fact
	// is supported by a chunk that did not make top_k.
	formatted := FormatResultsWithPassages(results, map[string]int{"other_md_7": 1})

	assert.Contains(t, formatted, "(source: nath.md)")
	assert.NotContains(t, formatted, "nath_md_312", "an internal chunk ID must never reach the prompt")
	assert.NotContains(t, formatted, "passage", "an unretrieved chunk cannot be cited as a passage")
}

// The passage citation is the whole point of chunk-level provenance: it points
// a claim at text the reader can actually see.
func TestRetrievedChunkCitesItsPassageNumber(t *testing.T) {
	results := []SearchResult{{
		Subject:   "Abhinavagupta",
		Predicate: "commented on",
		Object:    "Tantraloka",
		Source:    "tantra.md",
		ChunkID:   "tantra_md_4",
	}}

	formatted := FormatResultsWithPassages(results, map[string]int{"tantra_md_4": 3})

	assert.Contains(t, formatted, "(source: tantra.md [passage 3])")
	assert.NotContains(t, formatted, "tantra_md_4")
}

// Graphs built before chunk-level provenance stored a bare source as the quad
// label. Those labels have no separator and must still parse as a source, so an
// existing corpus keeps working without a rebuild.
func TestLegacyLabelParsesAsSourceOnly(t *testing.T) {
	source, chunkID := parseLabel("tantraloka_vol1.md")
	assert.Equal(t, "tantraloka_vol1.md", source)
	assert.Empty(t, chunkID)

	source, chunkID = parseLabel("tantraloka_vol1.md|tantraloka_vol1_md_42")
	assert.Equal(t, "tantraloka_vol1.md", source)
	assert.Equal(t, "tantraloka_vol1_md_42", chunkID)
}

// AllTriples feeds the build's relationship-embedding backfill, so it must
// carry provenance through — otherwise relationships rebuilt from an existing
// graph would silently lose their chunk IDs.
func TestAllTriplesCarriesChunkID(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Matsyendranath", Predicate: "was teacher of", Object: "Gorakhnath", ChunkID: "nath_md_7"},
	}, "nath.md"))

	all := db.AllTriples(ctx)
	require.Len(t, all, 1)
	assert.Equal(t, "nath.md", all[0].Source)
	assert.Equal(t, "nath_md_7", all[0].ChunkID)
}
