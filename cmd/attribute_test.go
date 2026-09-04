package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/llm"
)

func testBatch() []chunker.Chunk {
	return []chunker.Chunk{
		{ID: "doc_md_0", Content: "A discussion of hatha yoga postures and breath."},
		{ID: "doc_md_1", Content: "Abhinavagupta composed the Tantraloka in Kashmir."},
		{ID: "doc_md_2", Content: "Later commentators expanded on ritual worship."},
	}
}

func newTestEvidence() *graph.EvidenceChecker {
	return graph.NewEvidenceChecker(config.DiacriticLatin, false)
}

// The extractor's passage index used to become the chunk ID with no check at
// all, so a misreported index printed a passage citation on text that does not
// support the fact — and took the chunk-level ranking boost with it.
func TestAttributeChunkRejectsAWrongPassageClaim(t *testing.T) {
	got := attributeChunk(newTestEvidence(), testBatch(), llm.Triple{
		Subject:   "Abhinavagupta",
		Predicate: "composed",
		Object:    "Tantraloka",
		Passage:   1, // claims passage 1, which is about hatha yoga
	})

	assert.Equal(t, "doc_md_1", got, "the claim must lose to the passage that actually supports the fact")
}

// A correct claim is still preferred, and preferred directly: the model knows
// which passage it read, and searching would only find the same one.
func TestAttributeChunkHonoursACorrectClaim(t *testing.T) {
	got := attributeChunk(newTestEvidence(), testBatch(), llm.Triple{
		Subject:   "Abhinavagupta",
		Predicate: "composed",
		Object:    "Tantraloka",
		Passage:   2,
	})

	assert.Equal(t, "doc_md_1", got)
}

// No passage mentioning either endpoint means unknown provenance. Falling back
// to the first chunk would invent it.
func TestAttributeChunkLeavesUnknownProvenanceEmpty(t *testing.T) {
	got := attributeChunk(newTestEvidence(), testBatch(), llm.Triple{
		Subject:   "Gorakhnath",
		Predicate: "founded",
		Object:    "Nath Sampradaya",
		Passage:   1,
	})

	assert.Empty(t, got, "a fact no passage supports must not borrow one")
}

// An out-of-range or absent index is not an error, just no claim — the batch is
// searched as it always was.
func TestAttributeChunkSearchesWhenThereIsNoClaim(t *testing.T) {
	ev := newTestEvidence()
	batch := testBatch()

	for _, passage := range []int{0, 99, -3} {
		got := attributeChunk(ev, batch, llm.Triple{
			Subject:   "Abhinavagupta",
			Predicate: "composed",
			Object:    "Tantraloka",
			Passage:   passage,
		})
		assert.Equal(t, "doc_md_1", got, "passage %d should fall through to the search", passage)
	}
}

// A passage naming both endpoints beats one naming only a single endpoint.
func TestAttributeChunkPrefersStrongerEvidence(t *testing.T) {
	batch := []chunker.Chunk{
		{ID: "weak", Content: "Kashmir was a centre of learning."},
		{ID: "strong", Content: "Abhinavagupta lived in Kashmir."},
	}

	got := attributeChunk(newTestEvidence(), batch, llm.Triple{
		Subject:   "Abhinavagupta",
		Predicate: "located in",
		Object:    "Kashmir",
	})

	assert.Equal(t, "strong", got)
}

func TestAttributeChunkOnEmptyBatch(t *testing.T) {
	assert.Empty(t, attributeChunk(newTestEvidence(), nil, llm.Triple{Subject: "a", Object: "b"}))
}
