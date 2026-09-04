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

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: "", want: ""},
		{name: "none", input: "none", want: ""},
		{name: "off", input: "off", want: ""},
		{name: "disabled", input: "disabled", want: ""},
		{name: "low", input: "low", want: "low"},
		{name: "low uppercase", input: "LOW", want: "low"},
		{name: "medium", input: "medium", want: "medium"},
		{name: "medium mixed case", input: "Medium", want: "medium"},
		{name: "med alias", input: "med", want: "medium"},
		{name: "med whitespace", input: "  med  ", want: "medium"},
		{name: "high", input: "high", want: "high"},
		{name: "high uppercase", input: "HIGH", want: "high"},
		{name: "invalid ultra", input: "ultra", wantErr: true},
		{name: "invalid number", input: "100", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeReasoningEffort(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAgentYAMLReasoningEffort(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantEffort    string
		wantSpecified bool
		wantErr       bool
	}{
		{
			name:          "runtime.llm.reasoning_effort",
			yaml:          "runtime:\n  llm:\n    reasoning_effort: low\n",
			wantEffort:    "low",
			wantSpecified: true,
		},
		{
			name:          "runtime.llm.reasoning med",
			yaml:          "runtime:\n  llm:\n    reasoning: med\n",
			wantEffort:    "medium",
			wantSpecified: true,
		},
		{
			name:          "runtime.reasoning_effort",
			yaml:          "runtime:\n  reasoning_effort: high\n",
			wantEffort:    "high",
			wantSpecified: true,
		},
		{
			name:          "llm.reasoning_effort",
			yaml:          "llm:\n  reasoning_effort: medium\n",
			wantEffort:    "medium",
			wantSpecified: true,
		},
		{
			name:          "explicitly disabled none",
			yaml:          "runtime:\n  llm:\n    reasoning_effort: none\n",
			wantEffort:    "",
			wantSpecified: true,
		},
		{
			name:          "section absent",
			yaml:          "agent:\n  name: test\n",
			wantEffort:    "",
			wantSpecified: false,
		},
		{
			name:          "invalid value",
			yaml:          "runtime:\n  llm:\n    reasoning_effort: maximum\n",
			wantEffort:    "",
			wantSpecified: true,
			wantErr:       true,
		},
		{
			name:    "malformed yaml syntax",
			yaml:    "runtime:\n  llm: [unclosed\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeAgentYAML(t, tt.yaml)
			effort, specified, err := AgentYAMLReasoningEffort(path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEffort, effort)
			assert.Equal(t, tt.wantSpecified, specified)
		})
	}
}

func TestApplyAgentYAMLReasoningEffort(t *testing.T) {
	t.Run("agent.yaml overrides config env", func(t *testing.T) {
		path := writeAgentYAML(t, "runtime:\n  llm:\n    reasoning_effort: high\n")
		cfg := &Config{
			LLM: ProviderConfig{ReasoningEffort: "low"},
		}
		err := ApplyAgentYAMLReasoningEffort(cfg, path)
		require.NoError(t, err)
		assert.Equal(t, "high", cfg.LLM.ReasoningEffort)
	})

	t.Run("agent.yaml explicit none disables config env", func(t *testing.T) {
		path := writeAgentYAML(t, "runtime:\n  llm:\n    reasoning_effort: none\n")
		cfg := &Config{
			LLM: ProviderConfig{ReasoningEffort: "high"},
		}
		err := ApplyAgentYAMLReasoningEffort(cfg, path)
		require.NoError(t, err)
		assert.Equal(t, "", cfg.LLM.ReasoningEffort)
	})

	t.Run("fallback to config when agent.yaml has no reasoning", func(t *testing.T) {
		path := writeAgentYAML(t, "agent:\n  name: test\n")
		cfg := &Config{
			LLM: ProviderConfig{ReasoningEffort: "med"},
		}
		err := ApplyAgentYAMLReasoningEffort(cfg, path)
		require.NoError(t, err)
		assert.Equal(t, "medium", cfg.LLM.ReasoningEffort)
	})

	t.Run("disabled by default when nothing is set", func(t *testing.T) {
		path := writeAgentYAML(t, "agent:\n  name: test\n")
		cfg := &Config{}
		err := ApplyAgentYAMLReasoningEffort(cfg, path)
		require.NoError(t, err)
		assert.Equal(t, "", cfg.LLM.ReasoningEffort)
	})

	t.Run("nil config returns error", func(t *testing.T) {
		err := ApplyAgentYAMLReasoningEffort(nil, "agent.yaml")
		assert.ErrorIs(t, err, ErrNilConfig)
	})
}
