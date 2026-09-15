package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every document being built is not the same as the build being finished. A
// build stopped after its last document left the lexical index, entity
// descriptions and MCP description missing, and each later run reported the
// corpus up to date without producing them.
func TestUpToDate(t *testing.T) {
	tests := []struct {
		name          string
		pending       int
		removed       int
		prune         bool
		needsFinalize bool
		want          bool
	}{
		{
			name: "nothing pending, nothing removed",
			want: true,
		},
		{
			name:    "documents pending",
			pending: 2,
			want:    false,
		},
		{
			name:    "removed documents kept without --prune",
			removed: 1,
			want:    true,
		},
		{
			name:    "removed documents with --prune",
			removed: 1,
			prune:   true,
			want:    false,
		},
		{
			name:          "every document done but the final steps never ran",
			needsFinalize: true,
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, upToDate(tt.pending, tt.removed, tt.prune, tt.needsFinalize))
		})
	}
}
