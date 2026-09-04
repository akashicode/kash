package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/akashicode/kash/internal/config"
)

func TestGenerateAgentYAMLIsValidYAML(t *testing.T) {
	yamlStr := generateAgentYAML("test-agent")

	// Ensure no forbidden control characters exist in the scaffolded agent.yaml
	for i := 0; i < len(yamlStr); i++ {
		b := yamlStr[i]
		if (b < 32 && b != '\n' && b != '\r' && b != '\t') || b == 127 {
			line := strings.Count(yamlStr[:i], "\n") + 1
			t.Fatalf("unexpected control character 0x%02x at byte %d (line %d)", b, i, line)
		}
	}

	// Must unmarshal cleanly with yaml.v3 without syntax or control character errors
	var parsed map[string]any
	err := yaml.Unmarshal([]byte(yamlStr), &parsed)
	require.NoError(t, err, "generated agent.yaml must unmarshal cleanly")

	// Verify the config readers parse it without errors
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "agent.yaml")
	err = writeFile(path, yamlStr)
	require.NoError(t, err)

	effort, specified, err := config.AgentYAMLReasoningEffort(path)
	require.NoError(t, err)
	assert.False(t, specified)
	assert.Empty(t, effort)

	dim := config.AgentYAMLDimensions(path)
	assert.Equal(t, 1024, dim)

	chunkSize, overlap := config.AgentYAMLChunkOptions(path)
	assert.Equal(t, 1000, chunkSize)
	assert.Equal(t, 200, overlap)
}
