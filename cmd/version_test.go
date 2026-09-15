package cmd

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A binary built without -ldflags must still say which source it came from:
// its version string is written into every manifest and profile it builds.
func TestStampFromVCS(t *testing.T) {
	vcs := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "1a984b74581e569e81d5372ecd66f83244bcccb7"},
			{Key: "vcs.time", Value: "2026-09-05T06:20:41Z"},
			{Key: "vcs.modified", Value: modified},
		}
	}

	tests := []struct {
		name        string
		version     string
		commit      string
		settings    []debug.BuildSetting
		wantVersion string
		wantCommit  string
		wantTime    string
	}{
		{
			name:        "clean checkout",
			version:     "dev",
			commit:      "none",
			settings:    vcs("false"),
			wantVersion: "dev-1a984b7",
			wantCommit:  "1a984b7",
			wantTime:    "2026-09-05T06:20:41Z",
		},
		{
			name:        "uncommitted changes",
			version:     "dev",
			commit:      "none",
			settings:    vcs("true"),
			wantVersion: "dev-1a984b7-dirty",
			wantCommit:  "1a984b7",
			wantTime:    "2026-09-05T06:20:41Z",
		},
		{
			name:        "ldflags values win",
			version:     "v2.1.0",
			commit:      "abcdef0",
			settings:    vcs("false"),
			wantVersion: "v2.1.0",
			wantCommit:  "abcdef0",
			wantTime:    "2026-09-05T06:20:41Z",
		},
		{
			// go run, go test, or a source tree without .git.
			name:        "no VCS stamp",
			version:     "dev",
			commit:      "none",
			settings:    nil,
			wantVersion: "dev",
			wantCommit:  "none",
			wantTime:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVersion, gotCommit, gotTime := stampFromVCS(tt.version, tt.commit, tt.settings)
			assert.Equal(t, tt.wantVersion, gotVersion)
			assert.Equal(t, tt.wantCommit, gotCommit)
			assert.Equal(t, tt.wantTime, gotTime)
		})
	}
}
