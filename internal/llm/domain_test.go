package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The vocabulary is closed and unmatched facts are dropped, so losing a default
// predicate silently discards a whole class of relations. Suggestions extend,
// never replace.
func TestMergePredicatesNeverDropsDefaults(t *testing.T) {
	defaults := []string{"contains", "is a type of", "causes"}
	got := MergePredicates(defaults, []string{"was teacher of", "commented on"})

	for _, d := range defaults {
		assert.Contains(t, got, d, "a default predicate must survive merging")
	}
	assert.Contains(t, got, "was teacher of")
	assert.Len(t, got, 5)
}

func TestMergePredicatesDedupesCaseInsensitively(t *testing.T) {
	got := MergePredicates([]string{"contains"}, []string{"Contains", "CONTAINS", "authored"})
	assert.Equal(t, []string{"contains", "authored"}, got)
}

func TestMergePredicatesRespectsCap(t *testing.T) {
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, string(rune('a'+i%26))+"-relation")
	}
	got := MergePredicates([]string{"contains"}, many)
	assert.LessOrEqual(t, len(got), maxPredicates,
		"an unbounded vocabulary stops constraining the extractor")
}

// The sample is untrusted corpus text and the output becomes configuration, so
// the model may filter a mined list but never extend it.
func TestFilterToCandidatesRejectsInventedValues(t *testing.T) {
	candidates := []string{"sri ", "swami ", "dr "}
	got := FilterToCandidates([]string{"sri ", "rm -rf / ", "swami "}, candidates)

	assert.Equal(t, []string{"sri ", "swami "}, got)
	assert.NotContains(t, got, "rm -rf / ", "a value not offered must not survive")
}

func TestFilterToCandidatesIsCaseInsensitive(t *testing.T) {
	got := FilterToCandidates([]string{"SRI "}, []string{"sri "})
	assert.Equal(t, []string{"SRI "}, got)
}

func TestSubsetOfDropsNonMembers(t *testing.T) {
	got := SubsetOf([]string{"authored", "invented"}, []string{"authored", "contains"})
	assert.Equal(t, []string{"authored"}, got)
}

func TestExtractJSONDistinguishesFailureModes(t *testing.T) {
	tests := []struct {
		name, raw, wantErr string
	}{
		{"empty", "   ", "empty response"},
		{"truncated", `{"a": 1`, "truncated"},
		{"close without open", `"a": 1}`, "malformed"},
		{"no json at all", "I cannot help with that", "no JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractJSONObject(tt.raw)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExtractJSONStripsFences(t *testing.T) {
	got, err := extractJSONObject("```json\n{\"a\": 1}\n```")
	assert.NoError(t, err)
	assert.Equal(t, `{"a": 1}`, got)
}
