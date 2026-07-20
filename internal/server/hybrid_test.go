package server

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/akashicode/kash/internal/vector"
)

// TestDiversifyBySource ensures a single source document cannot monopolize
// the selected context when other sources have relevant chunks.
func TestDiversifyBySource(t *testing.T) {
	ranked := []vector.SearchResult{}
	for i := 0; i < 6; i++ {
		ranked = append(ranked, vector.SearchResult{ID: fmt.Sprintf("a%d", i), Source: "bookA.pdf"})
	}
	ranked = append(ranked,
		vector.SearchResult{ID: "b0", Source: "bookB.pdf"},
		vector.SearchResult{ID: "c0", Source: "bookC.pdf"},
	)

	selected := diversifyBySource(ranked, 5)

	assert.Len(t, selected, 5)
	perSource := map[string]int{}
	for _, r := range selected {
		perSource[r.Source]++
	}
	// topK=5 → max 3 per source; bookB and bookC must both make it in
	assert.Equal(t, 3, perSource["bookA.pdf"])
	assert.Equal(t, 1, perSource["bookB.pdf"])
	assert.Equal(t, 1, perSource["bookC.pdf"])
}

// TestDiversifyBySourceBackfill ensures slots are backfilled from a single
// source when no other sources are available.
func TestDiversifyBySourceBackfill(t *testing.T) {
	ranked := []vector.SearchResult{}
	for i := 0; i < 8; i++ {
		ranked = append(ranked, vector.SearchResult{ID: fmt.Sprintf("a%d", i), Source: "onlybook.pdf"})
	}

	selected := diversifyBySource(ranked, 5)
	assert.Len(t, selected, 5)
}

func TestDiversifyBySourceFewerThanTopK(t *testing.T) {
	ranked := []vector.SearchResult{
		{ID: "a0", Source: "bookA.pdf"},
		{ID: "b0", Source: "bookB.pdf"},
	}
	selected := diversifyBySource(ranked, 5)
	assert.Equal(t, ranked, selected)
}
