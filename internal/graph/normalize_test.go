package graph

import (
	"context"
	"testing"

	"github.com/cayleygraph/quad"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addRawForTest inserts quads bypassing AddTriples' normalization, simulating
// a graph that was built before normalization existed.
func (db *DB) addRawForTest(triples ...[3]string) error {
	quads := make([]quad.Quad, 0, len(triples))
	for _, t := range triples {
		quads = append(quads, quad.Make(t[0], t[1], t[2], "legacy.pdf"))
	}
	return db.store.AddQuadSet(quads)
}

func TestCanonicalPredicate(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		// "has X" and "contains X" were stored as two separate facts
		{"has", "contains"},
		{"contains", "contains"},
		{"Includes", "contains"},
		{"consists of", "contains"},
		// authorship, same direction
		{"wrote", "authored"},
		{"is the author of", "authored"},
		// inverse direction must NOT collapse into the above
		{"was written by", "was written by"},
		// lineage
		{"was a disciple of", "was disciple of"},
		{"studied under", "was disciple of"},
		{"guru of", "was teacher of"},
		// unknown predicates stay open, just normalized
		{"  Venerates  ", "venerates"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, CanonicalPredicate(tt.in), "predicate %q", tt.in)
	}
}

func TestCanonicalPredicateKeepsDirectionDistinct(t *testing.T) {
	// Collapsing these would silently reverse subject and object
	assert.NotEqual(t, CanonicalPredicate("authored"), CanonicalPredicate("was written by"))
}

func TestNormalizeEntity(t *testing.T) {
	assert.Equal(t, "Abhinavagupta", NormalizeEntity("  Abhinavagupta.  "))
	assert.Equal(t, "Rudra Yamala", NormalizeEntity("Rudra   Yamala"))
	assert.Equal(t, "Tantraloka", NormalizeEntity(`"Tantraloka"`))
	// Diacritics and case are meaning-bearing and must survive
	assert.Equal(t, "Kṣemarāja", NormalizeEntity("Kṣemarāja"))
}

func TestFoldKeyCollapsesCaseAndWording(t *testing.T) {
	// The two reported duplicate pairs
	a := FoldKey("Rasārṇava", "contains", "Aṣṭādaśaḥ paṭalaḥ")
	b := FoldKey("Rasārṇava", "contains", "aṣṭādaśaḥ paṭalaḥ")
	assert.Equal(t, a, b, "case-only variants must fold together")

	c := FoldKey("Svacchanda Tantra", "has", "ṣaṣṭhaḥ paṭalaḥ")
	d := FoldKey("Svacchanda Tantra", "contains", "ṣaṣṭhaḥ paṭalaḥ")
	assert.Equal(t, c, d, "has/contains must fold together")
}

// TestAddTriplesDeduplicates covers the reported case where four separate
// triples all asserted that Abhinavagupta wrote the Tantraloka.
func TestAddTriplesDeduplicates(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	dupes := []Triple{
		{Subject: "Abhinavagupta", Predicate: "wrote", Object: "Tantraloka"},
		{Subject: "Abhinavagupta", Predicate: "authored", Object: "Tantraloka"},
		{Subject: "abhinavagupta", Predicate: "is the author of", Object: "Tantraloka"},
		{Subject: "Abhinavagupta", Predicate: "composed", Object: "tantraloka."},
	}
	require.NoError(t, db.AddTriples(ctx, dupes, "tantraloka.pdf"))

	assert.EqualValues(t, 1, db.Count(), "four wordings of one fact must collapse to a single quad")
}

// TestAddTriplesFiltersNoise covers "Randhir Book Sales distributes ..."
func TestAddTriplesFiltersNoise(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	triples := []Triple{
		{Subject: "Randhir Book Sales", Predicate: "distributes", Object: "Rudra Yamala"},
		{Subject: "Chaukhamba Publishers", Predicate: "published", Object: "Meru Tantram"},
		{Subject: "Rasarnava", Predicate: "priced at", Object: "250 INR"},
		{Subject: "1998", Predicate: "is", Object: "2000"},
		{Subject: "Ksemaraja", Predicate: "was disciple of", Object: "Abhinavagupta"},
	}
	require.NoError(t, db.AddTriples(ctx, triples, "book.pdf"))

	results, err := db.Search(ctx, "Ksemaraja Abhinavagupta disciple", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Ksemaraja", results[0].Subject)
	assert.EqualValues(t, 1, db.Count(), "only the lineage fact should survive filtering")
}

// TestSearchFoldsDuplicatesInExistingGraph verifies that near-duplicates
// already stored in a graph collapse at query time, without a rebuild.
func TestSearchFoldsDuplicatesInExistingGraph(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	// Insert pre-normalization variants directly as raw quads, simulating a
	// graph built before normalization existed.
	require.NoError(t, db.addRawForTest(
		[3]string{"Svacchanda Tantra", "has", "ṣaṣṭhaḥ paṭalaḥ"},
		[3]string{"Svacchanda Tantra", "contains", "ṣaṣṭhaḥ paṭalaḥ"},
	))

	results, err := db.Search(ctx, "Svacchanda paṭalaḥ", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1, "wording variants must fold to one result at query time")
}
