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

func TestParseTriplesPassageField(t *testing.T) {
	raw := `[{"subject":"Gorakhnath","predicate":"was student of","object":"Matsyendranath","passage":2}]`
	got, err := parseTriples(raw)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 2, got[0].Passage)
	assert.Equal(t, "Gorakhnath", got[0].Subject)
}

func TestParseDecomposedQuery(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantEntities []string
		wantConcepts []string
		wantErr      bool
	}{
		{
			name: "clean json",
			raw: `{
				"specific_entities": ["Gorakhnath", "Matsyendranath"],
				"broad_concepts": ["Hatha Yoga", "Lineage"]
			}`,
			wantEntities: []string{"Gorakhnath", "Matsyendranath"},
			wantConcepts: []string{"Hatha Yoga", "Lineage"},
		},
		{
			name: "markdown code fences with prose",
			raw: "Here is the extracted keyword decomposition:\n```json\n" +
				"{\n  \"specific_entities\": [\"Abhinavagupta\", \"Tantraloka\"],\n  \"broad_concepts\": [\"Kashmir Shaivism\"]\n}\n" +
				"```\nHope this helps!",
			wantEntities: []string{"Abhinavagupta", "Tantraloka"},
			wantConcepts: []string{"Kashmir Shaivism"},
		},
		{
			name: "trims whitespace, ignores blanks and dedupes case-insensitively",
			raw: `{
				"specific_entities": ["  Gorakhnath  ", "", "   ", "gorakhnath"],
				"broad_concepts": [" Yoga ", "yoga", "   "]
			}`,
			wantEntities: []string{"Gorakhnath"},
			wantConcepts: []string{"Yoga"},
		},
		{
			name: "empty lists",
			raw:  `{"specific_entities": [], "broad_concepts": []}`,
		},
		{
			name:    "empty string error",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "malformed json without brackets error",
			raw:     "I cannot perform this operation.",
			wantErr: true,
		},
		{
			name:    "truncated json error",
			raw:     `{"specific_entities": ["Gorakhnath"`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDecomposedQuery(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEntities, got.SpecificEntities)
			assert.Equal(t, tt.wantConcepts, got.BroadConcepts)
		})
	}
}
