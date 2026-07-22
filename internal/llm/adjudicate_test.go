package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{
			name: "plain array",
			raw:  `[{"key":"brahm","same_entity":false,"reason":"distinct words"}]`,
			want: 1,
		},
		{
			name: "markdown fenced",
			raw:  "```json\n[{\"key\":\"gorakhnath\",\"same_entity\":true,\"reason\":\"transliteration variant\"}]\n```",
			want: 1,
		},
		{
			name: "surrounded by prose",
			raw:  "Here are my decisions:\n[{\"key\":\"a\",\"same_entity\":true,\"reason\":\"x\"},{\"key\":\"b\",\"same_entity\":false,\"reason\":\"y\"}]\nHope that helps.",
			want: 2,
		},
		{
			name: "entries without a key are dropped",
			raw:  `[{"key":"","same_entity":true},{"key":"ok","same_entity":true}]`,
			want: 1,
		},
		{
			name:    "no array at all",
			raw:     "I cannot decide this.",
			wantErr: true,
		},
		{
			name:    "malformed json",
			raw:     `[{"key": broken}]`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVerdicts(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.want)
		})
	}
}

func TestParseVerdictsPreservesDecision(t *testing.T) {
	got, err := parseVerdicts(`[{"key":"brahm","same_entity":false,"reason":"absolute vs creator god"}]`)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "brahm", got[0].Key)
	assert.False(t, got[0].SameEntity)
	assert.Equal(t, "absolute vs creator god", got[0].Reason)
}

func TestAdjudicateEntitiesEmptyIsNoOp(t *testing.T) {
	// No groups must not attempt a network call
	var c *Client
	got, err := c.AdjudicateEntities(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
