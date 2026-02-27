package chunker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkDocument(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		chunkSize int
		wantMin   int // minimum expected chunks
		wantErr   bool
	}{
		{
			name:      "simple text",
			input:     "Hello world this is a test",
			chunkSize: 10,
			wantMin:   1,
			wantErr:   false,
		},
		{
			name:      "empty input",
			input:     "",
			chunkSize: 100,
			wantMin:   0,
			wantErr:   false,
		},
		{
			name:      "invalid chunk size",
			input:     "test",
			chunkSize: 0,
			wantMin:   0,
			wantErr:   true,
		},
		{
			name:      "chunk size larger than input",
			input:     "short",
			chunkSize: 1000,
			wantMin:   1,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := ChunkDocument(tt.input, tt.chunkSize)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(chunks), tt.wantMin)
		})
	}
}

func TestChunker_ChunkText(t *testing.T) {
	c, err := NewChunker(Options{ChunkSize: 100, Overlap: 20})
	require.NoError(t, err)

	text := "This is a long text that should be split into multiple chunks. " +
		"It contains enough content to span more than one chunk when the chunk size is small enough. " +
		"We want to test that overlapping works correctly."

	chunks, err := c.ChunkText(text, "test_doc")
	require.NoError(t, err)
	assert.NotEmpty(t, chunks)

	// Verify source is set
	for _, ch := range chunks {
		assert.Equal(t, "test_doc", ch.Source)
		assert.NotEmpty(t, ch.Content)
		assert.NotEmpty(t, ch.ID)
	}
}

func TestNewChunker_InvalidOptions(t *testing.T) {
	_, err := NewChunker(Options{ChunkSize: 0})
	require.Error(t, err)
	assert.Equal(t, ErrInvalidChunkSize, err)
}

func TestSplitBySentence_OversizedParagraph(t *testing.T) {
	// A single paragraph that is way bigger than ChunkSize and has no \n\n breaks.
	// This is the exact bug: previously it would emit this as one giant chunk.
	bigParagraph := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)
	// ~4500 chars, ChunkSize=500: must produce multiple chunks

	c, err := NewChunker(Options{ChunkSize: 500, Overlap: 100})
	require.NoError(t, err)

	chunks, err := c.SplitBySentence(bigParagraph, "test_oversized")
	require.NoError(t, err)
	assert.Greater(t, len(chunks), 1, "oversized paragraph must be split into multiple chunks")

	for _, ch := range chunks {
		assert.LessOrEqual(t, len(ch.Content), 500+100, // allow minor overlap margin
			"chunk %q exceeds ChunkSize", ch.ID)
		assert.NotEmpty(t, ch.Content)
	}
}

func TestSplitBySentence_CRLFLineEndings(t *testing.T) {
	// Simulate Windows-style \r\n\r\n paragraph breaks — the original bug.
	text := "First paragraph here.\r\n\r\nSecond paragraph here.\r\n\r\nThird paragraph."

	c, err := NewChunker(Options{ChunkSize: 100, Overlap: 20})
	require.NoError(t, err)

	chunks, err := c.SplitBySentence(text, "crlf_test")
	require.NoError(t, err)
	// Should recognize 3 paragraphs, possibly merged into 1-2 chunks
	assert.GreaterOrEqual(t, len(chunks), 1)
	// The key test: none of these chunks should contain \r
	for _, ch := range chunks {
		assert.NotContains(t, ch.Content, "\r", "CRLF should be normalized")
	}
}

func TestSplitBySentence_OversizedSentence(t *testing.T) {
	// A single sentence with no periods — so sentence splitting can't help.
	// Must fall back to ChunkText character-level splitting.
	hugeNoSentences := strings.Repeat("word ", 500) // ~2500 chars, no periods

	c, err := NewChunker(Options{ChunkSize: 300, Overlap: 50})
	require.NoError(t, err)

	chunks, err := c.SplitBySentence(hugeNoSentences, "no_sentences")
	require.NoError(t, err)
	assert.Greater(t, len(chunks), 1, "should fall back to character-level splitting")

	for _, ch := range chunks {
		assert.LessOrEqual(t, len(ch.Content), 300+50,
			"chunk should not exceed ChunkSize")
	}
}

func TestSplitBySentence_MixedSizes(t *testing.T) {
	// Mix of normal and oversized paragraphs
	small := "A short paragraph."
	big := strings.Repeat("This is a longer sentence that goes on. ", 50) // ~2000 chars
	text := small + "\n\n" + big + "\n\n" + small

	c, err := NewChunker(Options{ChunkSize: 500, Overlap: 100})
	require.NoError(t, err)

	chunks, err := c.SplitBySentence(text, "mixed")
	require.NoError(t, err)
	assert.Greater(t, len(chunks), 1)

	for _, ch := range chunks {
		assert.LessOrEqual(t, len(ch.Content), 500+100)
	}
}

func TestOptionsFromMaxTokens(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		wantChunk int
	}{
		{
			name:      "8192 token model",
			maxTokens: 8192,
			wantChunk: 29491, // int(8192 * 4 * 0.9)
		},
		{
			name:      "zero falls back to default",
			maxTokens: 0,
			wantChunk: 1000,
		},
		{
			name:      "negative falls back to default",
			maxTokens: -1,
			wantChunk: 1000,
		},
		{
			name:      "small model",
			maxTokens: 512,
			wantChunk: 1843, // int(512 * 4 * 0.9)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := OptionsFromMaxTokens(tt.maxTokens)
			assert.Equal(t, tt.wantChunk, opts.ChunkSize)
			if tt.maxTokens > 0 {
				assert.Equal(t, opts.ChunkSize/5, opts.Overlap)
			}
		})
	}
}
