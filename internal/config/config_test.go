package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAgentYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestAgentYAMLChunkOptions(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantSize    int
		wantOverlap int
	}{
		{
			name:        "both set",
			yaml:        "build:\n  chunk_size: 1500\n  chunk_overlap: 300\n",
			wantSize:    1500,
			wantOverlap: 300,
		},
		{
			name:        "only size set",
			yaml:        "build:\n  chunk_size: 800\n",
			wantSize:    800,
			wantOverlap: 0,
		},
		{
			name:        "section absent",
			yaml:        "agent:\n  name: test\n",
			wantSize:    0,
			wantOverlap: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeAgentYAML(t, tt.yaml)
			size, overlap := AgentYAMLChunkOptions(path)
			assert.Equal(t, tt.wantSize, size)
			assert.Equal(t, tt.wantOverlap, overlap)
		})
	}
}

func TestAgentYAMLChunkOptions_MissingFile(t *testing.T) {
	size, overlap := AgentYAMLChunkOptions(filepath.Join(t.TempDir(), "nope.yaml"))
	assert.Equal(t, 0, size)
	assert.Equal(t, 0, overlap)
}
