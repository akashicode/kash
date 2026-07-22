package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clusterByKey(clusters []Cluster, key string) *Cluster {
	for i := range clusters {
		if clusters[i].Key == key {
			return &clusters[i]
		}
	}
	return nil
}

// TestBuildClustersMergesSpellingVariants covers the case that broke
// traversal: the same person under several transliterations.
func TestBuildClustersMergesSpellingVariants(t *testing.T) {
	triples := []SearchResult{
		{Subject: "Gorakhnath", Predicate: "authored", Object: "Kaya Bodha"},
		{Subject: "Gorakhnatha", Predicate: "was teacher of", Object: "Svatmarama"},
		{Subject: "Gorakhnātha", Predicate: "authored", Object: "Goraksha Samhita"},
		{Subject: "Matsyendranath", Predicate: "was teacher of", Object: "Gorakhnath"},
		{Subject: "Matsyendranatha", Predicate: "authored", Object: "Kaulajnana"},
	}

	clusters := BuildClusters(triples, DefaultResolveOptions())

	g := clusterByKey(clusters, "gorakhnath")
	require.NotNil(t, g, "Gorakhnath variants must cluster")
	assert.True(t, g.Approved, "proper-noun variants should auto-approve")
	assert.Len(t, g.Aliases, 2)
	// Canonical prefers the most diacriticized form
	assert.Equal(t, "Gorakhnātha", g.Canonical)

	m := clusterByKey(clusters, "matsyendranath")
	require.NotNil(t, m)
	assert.True(t, m.Approved)
}

// TestBuildClustersHoldsAmbiguousDiacritics is the safety property: Sanskrit
// diacritics distinguish real words, so a common-noun cluster formed only by
// folding them must NOT be applied automatically.
func TestBuildClustersHoldsAmbiguousDiacritics(t *testing.T) {
	triples := []SearchResult{
		// brahma (the absolute) vs brahmā (the creator god) — not the same
		{Subject: "brahma", Predicate: "is defined as", Object: "the absolute"},
		{Subject: "brahma", Predicate: "describes", Object: "reality"},
		{Subject: "brahmā", Predicate: "is a type of", Object: "deity"},
		{Subject: "brahmā", Predicate: "describes", Object: "creation"},
	}

	clusters := BuildClusters(triples, DefaultResolveOptions())
	c := clusterByKey(clusters, "brahm")
	require.NotNil(t, c, "the cluster should be surfaced for review")
	assert.False(t, c.Approved, "diacritic-only common-noun merges must not auto-apply")
	assert.Contains(t, c.Reason, "review")
}

// TestBuildClustersApprovesHonorifics covers titles, which never change which
// entity is meant.
func TestBuildClustersApprovesHonorifics(t *testing.T) {
	triples := []SearchResult{
		{Subject: "Kṣemarāja", Predicate: "commented on", Object: "Spanda"},
		{Subject: "śrī Kṣemarāja", Predicate: "describes", Object: "Pratyabhijna"},
		{Subject: "ācārya Kṣemarāja", Predicate: "describes", Object: "Shaivism"},
	}
	clusters := BuildClusters(triples, DefaultResolveOptions())
	require.Len(t, clusters, 1)
	assert.True(t, clusters[0].Approved)
	assert.Contains(t, clusters[0].Reason, "honorific")
	assert.Equal(t, "Kṣemarāja", clusters[0].Canonical)
}

// TestBuildClustersSkipsSingletons — entities appearing once cannot form a
// chain, so merging them changes nothing and only adds risk.
func TestBuildClustersSkipsSingletons(t *testing.T) {
	triples := []SearchResult{
		{Subject: "Rareone", Predicate: "describes", Object: "x"},
		{Subject: "Rareoné", Predicate: "describes", Object: "y"},
	}
	clusters := BuildClusters(triples, ResolveOptions{MinDegree: 5})
	assert.Empty(t, clusters, "low-degree entities must be skipped")
}

func TestMergeClustersPreservesManualEdits(t *testing.T) {
	fresh := []Cluster{
		{Key: "brahm", Canonical: "brahmā", Aliases: []string{"brahma"}, Approved: false, Reason: "auto"},
		{Key: "devi", Canonical: "Devī", Aliases: []string{"devi"}, Approved: true},
	}
	existing := []Cluster{
		// User reviewed this one and rejected the merge, with a note
		{Key: "brahm", Canonical: "brahmā", Aliases: []string{"brahma"}, Approved: false,
			Note: "keep separate: brahma != Brahmā", Reason: "manually reviewed"},
	}

	merged := MergeClusters(existing, fresh)
	b := clusterByKey(merged, "brahm")
	require.NotNil(t, b)
	assert.Equal(t, "keep separate: brahma != Brahmā", b.Note, "hand-written notes must survive")
	assert.Equal(t, "manually reviewed", b.Reason)
	assert.False(t, b.Approved)
}

func TestAliasSetResolvesAndIgnoresUnapproved(t *testing.T) {
	set := NewAliasSet([]Cluster{
		{Canonical: "Gorakhnātha", Aliases: []string{"Gorakhnath", "Gorakhnatha"}, Approved: true},
		{Canonical: "brahmā", Aliases: []string{"brahma"}, Approved: false},
	})

	assert.Equal(t, normalizeSurface("Gorakhnātha"), set.Resolve("Gorakhnath"))
	assert.Equal(t, normalizeSurface("Gorakhnātha"), set.Resolve("Gorakhnatha"))
	assert.Equal(t, "Gorakhnātha", set.Display("Gorakhnath"))

	// Unapproved clusters must have no effect
	assert.Equal(t, "brahma", set.Resolve("brahma"))
	assert.Equal(t, "brahma", set.Display("brahma"))
}

// TestNilAliasSetIsIdentity is the "works without the file" guarantee.
func TestNilAliasSetIsIdentity(t *testing.T) {
	var set *AliasSet
	assert.Equal(t, 0, set.Len())
	assert.Equal(t, "gorakhnath", set.Resolve("Gorakhnath"))
	assert.Equal(t, "Gorakhnath", set.Display("Gorakhnath"))
}

// TestLoadAliasFileMissingIsNotAnError is the other half of that guarantee:
// deleting the file must leave the agent fully functional.
func TestLoadAliasFileMissingIsNotAnError(t *testing.T) {
	f, set, err := LoadAliasFile(filepath.Join(t.TempDir(), "nope.json"))
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NotNil(t, set)
	assert.Equal(t, 0, set.Len())
	assert.Empty(t, f.Clusters)
}

func TestAliasFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), AliasFileName)
	f := &AliasFile{Clusters: []Cluster{
		{Key: "gorakhnath", Canonical: "Gorakhnātha", Aliases: []string{"Gorakhnath"}, Approved: true},
	}}
	require.NoError(t, f.Save(path))

	loaded, set, err := LoadAliasFile(path)
	require.NoError(t, err)
	require.Len(t, loaded.Clusters, 1)
	assert.Equal(t, 1, set.Len())
	assert.NotEmpty(t, loaded.Note, "file should carry hand-editing instructions")
}

// TestTraversalConnectsAcrossAliases is the payoff: with variants merged, a
// chain that previously broke at the spelling boundary now traverses.
func TestTraversalConnectsAcrossAliases(t *testing.T) {
	db, err := NewDB()
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.AddTriples(ctx, []Triple{
		{Subject: "Matsyendranath", Predicate: "was teacher of", Object: "Gorakhnath"},
		// Same person, different transliteration — the chain breaks here
		{Subject: "Gorakhnatha", Predicate: "was teacher of", Object: "Svatmarama"},
	}, "nath.md"))

	// Without aliases the second fact is unreachable
	before, err := db.SearchWithHops(ctx, "Matsyendranath", 10, 1)
	require.NoError(t, err)
	assert.False(t, containsObject(before, "Svatmarama"),
		"chain should break at the spelling variant before resolution")

	db.SetAliases(NewAliasSet([]Cluster{
		{Canonical: "Gorakhnath", Aliases: []string{"Gorakhnatha"}, Approved: true},
	}))

	after, err := db.SearchWithHops(ctx, "Matsyendranath", 10, 1)
	require.NoError(t, err)
	assert.True(t, containsObject(after, "Svatmarama"),
		"chain must traverse across merged spelling variants")
}

func containsObject(rs []SearchResult, obj string) bool {
	for _, r := range rs {
		if r.Object == obj {
			return true
		}
	}
	return false
}
