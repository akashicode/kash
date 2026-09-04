package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestFilterToCandidatesReturnsTheCandidateSpelling(t *testing.T) {
	// Matching ignores case and surrounding space, but the value that survives
	// is the one from the candidate list, never the model's echo of it.
	got := FilterToCandidates([]string{"SRI"}, []string{"sri "})
	assert.Equal(t, []string{"sri "}, got)
}

func TestFilterToCandidatesKeepsHonorificTrailingSpace(t *testing.T) {
	// The reply is trimmed before it reaches here, so a filter that echoed the
	// model would yield "sri" — and stripHonorific cuts a plain prefix, so
	// "sri" would eat the front of "sristi" and every word like it.
	candidates := []string{"sri ", "dr ", "swami "}
	got := FilterToCandidates(cleanStringSlice([]string{"sri", "dr"}), candidates)

	require.Len(t, got, 2)
	for _, h := range got {
		assert.True(t, strings.HasSuffix(h, " "), "honorific %q must keep its trailing space", h)
	}
}

func TestSubsetOfDropsNonMembers(t *testing.T) {
	got := SubsetOf([]string{"authored", "invented"}, []string{"authored", "contains"})
	assert.Equal(t, []string{"authored"}, got)
}

func TestSubsetOfReturnsTheAllowedSpelling(t *testing.T) {
	// proper_noun_predicates is looked up against predicates by exact string,
	// so the subset must be spelled the way the superset is.
	got := SubsetOf([]string{"Authored "}, []string{"authored"})
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
