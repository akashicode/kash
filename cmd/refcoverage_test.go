package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentconfig "github.com/akashicode/kash/internal/config"
)

// A reference pattern that tags nothing is invisible at query time: the
// exact-reference route returns an empty result and retrieval silently falls
// back to similarity. Profiles outlive the corpus they were derived from, so
// this is the common case, not a corner one.
func TestUnusedRefKeys(t *testing.T) {
	tests := []struct {
		name     string
		patterns []agentconfig.RefPattern
		coverage map[string]int
		want     []string
	}{
		{
			name: "reports only keys that tagged nothing",
			patterns: []agentconfig.RefPattern{
				{Pattern: `(?i)verse\s+(\d+)`, MetaKey: "verse"},
				{Pattern: `(?i)clause\s+(\d+)`, MetaKey: "clause"},
			},
			coverage: map[string]int{"verse": 201},
			want:     []string{"clause"},
		},
		{
			// Several patterns may write one key. The key is only unused when
			// every pattern writing it matched nothing, and it is reported once.
			name: "a key shared by two patterns is reported once",
			patterns: []agentconfig.RefPattern{
				{Pattern: `(?i)section\s+(\d+)`, MetaKey: "section"},
				{Pattern: `^\s*(\d+)\.`, MetaKey: "section"},
			},
			coverage: map[string]int{},
			want:     []string{"section"},
		},
		{
			name: "a key covered by either of its patterns is not reported",
			patterns: []agentconfig.RefPattern{
				{Pattern: `(?i)section\s+(\d+)`, MetaKey: "section"},
				{Pattern: `^\s*(\d+)\.`, MetaKey: "section"},
			},
			coverage: map[string]int{"section": 3},
			want:     nil,
		},
		{
			name: "unfilled template entries are not reported",
			patterns: []agentconfig.RefPattern{
				{Pattern: "", MetaKey: ""},
				{Pattern: `(?i)article\s+(\d+)`, MetaKey: ""},
			},
			coverage: map[string]int{},
			want:     nil,
		},
		{
			name:     "everything covered",
			patterns: []agentconfig.RefPattern{{Pattern: `(?i)verse\s+(\d+)`, MetaKey: "verse"}},
			coverage: map[string]int{"verse": 1},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, unusedRefKeys(tt.patterns, tt.coverage))
		})
	}
}
