package server

import (
	"container/list"
	"context"
	"strings"
	"sync"

	"github.com/akashicode/kash/internal/llm"
)

type cacheEntry struct {
	key string
	val llm.DecomposedQuery
}

// queryDecompCache is a thread-safe, bounded LRU cache for query keyword decompositions.
type queryDecompCache struct {
	mu      sync.Mutex
	maxSize int
	items   map[string]*list.Element
	evict   *list.List
}

// newQueryDecompCache creates a new bounded cache with the given capacity.
func newQueryDecompCache(maxSize int) *queryDecompCache {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &queryDecompCache{
		maxSize: maxSize,
		items:   make(map[string]*list.Element),
		evict:   list.New(),
	}
}

// Get looks up a cached decomposition by normalized query string.
func (c *queryDecompCache) Get(query string) (llm.DecomposedQuery, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := strings.ToLower(strings.TrimSpace(query))
	if elem, ok := c.items[key]; ok {
		c.evict.MoveToFront(elem)
		return elem.Value.(*cacheEntry).val, true
	}
	return llm.DecomposedQuery{}, false
}

// Put inserts or updates a decomposition in the cache.
func (c *queryDecompCache) Put(query string, val llm.DecomposedQuery) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := strings.ToLower(strings.TrimSpace(query))
	if elem, ok := c.items[key]; ok {
		c.evict.MoveToFront(elem)
		elem.Value.(*cacheEntry).val = val
		return
	}

	if c.evict.Len() >= c.maxSize {
		oldest := c.evict.Back()
		if oldest != nil {
			c.evict.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}

	elem := c.evict.PushFront(&cacheEntry{key: key, val: val})
	c.items[key] = elem
}

// Len returns the current number of cached items.
func (c *queryDecompCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evict.Len()
}

// decomposeQuery preprocesses a user query into specific entities and broad concepts.
// It bypasses short queries, checks cache, calls LLM, and gracefully falls back on error.
func (s *Server) decomposeQuery(ctx context.Context, query string) llm.DecomposedQuery {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return llm.DecomposedQuery{}
	}

	// Short query bypass: queries of 1 or 2 words, or <= 15 chars (e.g. "Abhinavagupta", "dharana 49")
	words := strings.Fields(trimmed)
	if len(words) <= 2 || len(trimmed) <= 15 {
		return llm.DecomposedQuery{
			SpecificEntities: []string{trimmed},
		}
	}

	// Check cache
	if s.decompCache != nil {
		if cached, ok := s.decompCache.Get(trimmed); ok {
			return cached
		}
	}

	// Fallback if LLM client is nil (offline/testing)
	if s.llmClient == nil {
		return llm.DecomposedQuery{
			SpecificEntities: []string{trimmed},
		}
	}

	// Call LLM for keyword decomposition
	dq, err := s.llmClient.DecomposeQuery(ctx, trimmed)
	if err != nil {
		if s.log != nil {
			s.log.Warn("query keyword decomposition failed (falling back)", "error", err, "query", trimmed)
		}
		return llm.DecomposedQuery{
			SpecificEntities: []string{trimmed},
		}
	}

	// If the LLM returned nothing, fallback to the trimmed query
	if len(dq.SpecificEntities) == 0 && len(dq.BroadConcepts) == 0 {
		dq.SpecificEntities = []string{trimmed}
	}

	// Cache successful decomposition
	if s.decompCache != nil {
		s.decompCache.Put(trimmed, dq)
	}

	return dq
}
