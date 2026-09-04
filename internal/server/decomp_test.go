package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/llm"
)

func TestQueryDecompCacheBasic(t *testing.T) {
	cache := newQueryDecompCache(2)
	require.NotNil(t, cache)
	assert.Equal(t, 0, cache.Len())

	dq1 := llm.DecomposedQuery{
		SpecificEntities: []string{"Abhinavagupta"},
		BroadConcepts:    []string{"Kashmir Shaivism"},
	}

	// Miss
	val, ok := cache.Get("Who was Abhinavagupta?")
	assert.False(t, ok)
	assert.Empty(t, val.SpecificEntities)

	// Put
	cache.Put("Who was Abhinavagupta?", dq1)
	assert.Equal(t, 1, cache.Len())

	// Hit with case and whitespace variance
	got, ok := cache.Get("  who was abhinavagupta?  ")
	assert.True(t, ok)
	assert.Equal(t, dq1.SpecificEntities, got.SpecificEntities)
	assert.Equal(t, dq1.BroadConcepts, got.BroadConcepts)
}

func TestQueryDecompCacheEviction(t *testing.T) {
	cache := newQueryDecompCache(2)

	dqA := llm.DecomposedQuery{SpecificEntities: []string{"A"}}
	dqB := llm.DecomposedQuery{SpecificEntities: []string{"B"}}
	dqC := llm.DecomposedQuery{SpecificEntities: []string{"C"}}

	cache.Put("query A", dqA)
	cache.Put("query B", dqB)
	assert.Equal(t, 2, cache.Len())

	// Access A to make B the oldest
	_, ok := cache.Get("query A")
	require.True(t, ok)

	// Insert C, which should evict B
	cache.Put("query C", dqC)
	assert.Equal(t, 2, cache.Len())

	_, okA := cache.Get("query A")
	assert.True(t, okA, "query A should still be in cache")

	_, okB := cache.Get("query B")
	assert.False(t, okB, "query B should have been evicted")

	_, okC := cache.Get("query C")
	assert.True(t, okC, "query C should be in cache")
}

func TestDecomposeQueryBypassAndFallback(t *testing.T) {
	s := &Server{
		decompCache: newQueryDecompCache(100),
		llmClient:   nil, // offline / test mode without LLM
	}

	ctx := context.Background()

	// Test empty query
	emptyRes := s.decomposeQuery(ctx, "   ")
	assert.Empty(t, emptyRes.SpecificEntities)
	assert.Empty(t, emptyRes.BroadConcepts)

	// Test short query (<= 2 words) bypasses decomposition
	shortRes := s.decomposeQuery(ctx, "Gorakhnath")
	require.Len(t, shortRes.SpecificEntities, 1)
	assert.Equal(t, "Gorakhnath", shortRes.SpecificEntities[0])
	assert.Empty(t, shortRes.BroadConcepts)

	// Test short query (<= 15 chars) bypasses decomposition
	shortRes2 := s.decomposeQuery(ctx, "section 4.2")
	require.Len(t, shortRes2.SpecificEntities, 1)
	assert.Equal(t, "section 4.2", shortRes2.SpecificEntities[0])

	// Test longer conversational query with nil llmClient falls back gracefully to raw query
	longQuery := "Can you explain the philosophical doctrine of non-dualism in Kashmir Shaivism?"
	fallbackRes := s.decomposeQuery(ctx, longQuery)
	require.Len(t, fallbackRes.SpecificEntities, 1)
	assert.Equal(t, longQuery, fallbackRes.SpecificEntities[0])
	assert.Empty(t, fallbackRes.BroadConcepts)

	// Pre-populate cache and verify hit without calling LLM
	cachedQuery := "What is the relationship between Gorakhnath and Matsyendranath?"
	s.decompCache.Put(cachedQuery, llm.DecomposedQuery{
		SpecificEntities: []string{"Gorakhnath", "Matsyendranath"},
		BroadConcepts:    []string{"Nath Sampradaya", "Guru-shishya"},
	})

	cachedRes := s.decomposeQuery(ctx, cachedQuery)
	assert.Equal(t, []string{"Gorakhnath", "Matsyendranath"}, cachedRes.SpecificEntities)
	assert.Equal(t, []string{"Nath Sampradaya", "Guru-shishya"}, cachedRes.BroadConcepts)
}

func TestQueryDecompCacheUpdate(t *testing.T) {
	cache := newQueryDecompCache(5)
	q := "test query"

	cache.Put(q, llm.DecomposedQuery{SpecificEntities: []string{"v1"}})
	got, ok := cache.Get(q)
	require.True(t, ok)
	assert.Equal(t, []string{"v1"}, got.SpecificEntities)

	// Update existing entry
	cache.Put(q, llm.DecomposedQuery{SpecificEntities: []string{"v2"}})
	assert.Equal(t, 1, cache.Len())

	got2, ok := cache.Get(q)
	require.True(t, ok)
	assert.Equal(t, []string{"v2"}, got2.SpecificEntities)
}
