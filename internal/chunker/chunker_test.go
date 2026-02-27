package chunker

import (
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
