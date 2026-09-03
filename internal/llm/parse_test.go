package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unusable response must not be reported as "no facts". The caller
// checkpoints an empty result as a completed batch and never revisits it, so
// conflating the two silently drops those passages from the graph forever.
func TestParseTriplesDistinguishesEmptyFromUnusable(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		wantLen int
	}{
		{
			name:    "explicit empty array means no facts",
			raw:     "[]",
			wantLen: 0,
		},
		{
			name:    "valid triples",
			raw:     `[{"subject":"Gorakhnath","predicate":"was student of","object":"Matsyendranath"}]`,
			wantLen: 1,
		},
		{
			name:    "markdown fenced",
			raw:     "```json\n[{\"subject\":\"a\",\"predicate\":\"is\",\"object\":\"b\"}]\n```",
			wantLen: 1,
		},
		{
			name:    "truncated mid-array is an error, not zero facts",
			raw:     `[{"subject":"a","predicate":"is","object":"b"},{"subject":"c"`,
			wantErr: true,
		},
		{
			name:    "refusal prose is an error, not zero facts",
			raw:     "I'm sorry, I can't help with that request.",
			wantErr: true,
		},
		{
			name:    "empty response is an error",
			raw:     "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTriples(tt.raw)
			if tt.wantErr {
				require.Error(t, err, "unusable response must surface an error so the batch is retried")
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantLen)
		})
	}
}

func TestParseTriplesDropsIncompleteEntries(t *testing.T) {
	got, err := parseTriples(`[{"subject":"a","predicate":"is","object":"b"},{"subject":"c","predicate":""}]`)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
