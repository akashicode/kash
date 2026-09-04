package graph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The chain is entity -> chunk id -> raw passage text. An entity description is
// synthesised from facts and quotes nothing, so without the chunk ids a
// semantic hit on that description has no way back to text a reader can check.
func TestEntityFactsCarryChunkProvenance(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Abhinavagupta", Predicate: "composed", Object: "Tantraloka", ChunkID: "tantra_md_4"},
		{Subject: "Abhinavagupta", Predicate: "disciple of", Object: "Lakshmanagupta", ChunkID: "tantra_md_9"},
	}, "tantra.md"))

	facts := db.EntityFacts(ctx, 1)
	require.NotEmpty(t, facts)

	var abhi *EntityFacts
	for i := range facts {
		if facts[i].Name == "Abhinavagupta" {
			abhi = &facts[i]
		}
	}
	require.NotNil(t, abhi, "the entity must be present")
	assert.ElementsMatch(t, []string{"tantra_md_4", "tantra_md_9"}, abhi.ChunkIDs)
}

// A well-connected entity appears in hundreds of passages; listing them all
// would swamp retrieval with weak signal.
func TestEntityChunkProvenanceIsBounded(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	var triples []Triple
	for i := 0; i < maxChunkIDsPerEntity*3; i++ {
		triples = append(triples, Triple{
			Subject:   "Shiva",
			Predicate: "manifests as",
			Object:    "form" + string(rune('A'+i)),
			ChunkID:   "doc_md_" + string(rune('a'+i)),
		})
	}
	require.NoError(t, db.AddTriples(ctx, triples, "doc.md"))

	facts := db.EntityFacts(ctx, 1)
	for _, f := range facts {
		assert.LessOrEqual(t, len(f.ChunkIDs), maxChunkIDsPerEntity,
			"entity %q carries unbounded provenance", f.Name)
	}
}

// Triples extracted before chunk-level provenance have no chunk id. That must
// read as "unknown", never as a fabricated link.
func TestEntityFactsWithoutChunkIDsCarryNone(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Gorakhnath", Predicate: "founded", Object: "Nath Sampradaya"},
	}, "nath.md"))

	facts := db.EntityFacts(ctx, 1)
	require.NotEmpty(t, facts)
	for _, f := range facts {
		assert.Empty(t, f.ChunkIDs, "unknown provenance must stay unknown")
	}
}
