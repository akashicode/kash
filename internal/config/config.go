package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ErrNilConfig is returned when a nil Config is provided.
var ErrNilConfig = errors.New("config is nil")

// ConfigDir returns the path to ~/.kash/.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".kash"), nil
}

// ConfigFilePath returns the full path to ~/.kash/config.yaml.
func ConfigFilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// ProviderConfig holds connection details for a single AI provider.
type ProviderConfig struct {
	BaseURL         string `mapstructure:"base_url"         yaml:"base_url"`
	APIKey          string `mapstructure:"api_key"          yaml:"api_key"`
	Model           string `mapstructure:"model"            yaml:"model"`
	Dimensions      int    `mapstructure:"dimensions"       yaml:"dimensions,omitempty"`
	ReasoningEffort string `mapstructure:"reasoning_effort" yaml:"reasoning_effort,omitempty"`
}

// Config holds the unified application configuration.
// Both build and serve commands use the same structure.
// Resolution order: environment variables first, then config.yaml fallback.
type Config struct {
	LLM      ProviderConfig `mapstructure:"llm"      yaml:"llm"`
	Embedder ProviderConfig `mapstructure:"embedder"  yaml:"embedder"`
	Reranker ProviderConfig `mapstructure:"reranker"  yaml:"reranker"`
	Port     int            `mapstructure:"port"      yaml:"port"`
}

// Load reads the unified config. Environment variables take priority over
// config.yaml values. This makes the same binary work for both CLI (config.yaml)
// and container (env vars) usage.
func Load() (*Config, error) {
	// 1. Read config.yaml via Viper (may be empty/missing — that's OK)
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 2. Override with environment variables where set
	applyEnv(&cfg.LLM.BaseURL, "LLM_BASE_URL")
	applyEnv(&cfg.LLM.APIKey, "LLM_API_KEY")
	applyEnv(&cfg.LLM.Model, "LLM_MODEL")
	applyEnv(&cfg.LLM.ReasoningEffort, "LLM_REASONING_EFFORT")

	applyEnv(&cfg.Embedder.BaseURL, "EMBED_BASE_URL")
	applyEnv(&cfg.Embedder.APIKey, "EMBED_API_KEY")
	applyEnv(&cfg.Embedder.Model, "EMBED_MODEL")

	// NOTE: Dimensions are NOT set from env vars.
	// agent.yaml is the canonical source for dimensions. The default of 1024
	// is applied in ApplyAgentYAMLDimensions() after agent.yaml is consulted.

	applyEnv(&cfg.Reranker.BaseURL, "RERANK_BASE_URL")
	applyEnv(&cfg.Reranker.APIKey, "RERANK_API_KEY")
	applyEnv(&cfg.Reranker.Model, "RERANK_MODEL")

	if portStr := os.Getenv("PORT"); portStr != "" {
		var p int
		if _, err := fmt.Sscanf(portStr, "%d", &p); err == nil && p > 0 {
			cfg.Port = p
		}
	}

	// Default port
	if cfg.Port == 0 {
		cfg.Port = 8000
	}

	return &cfg, nil
}

// applyEnv overwrites dst with the value of the environment variable if set.
func applyEnv(dst *string, envKey string) {
	if v := os.Getenv(envKey); v != "" {
		*dst = v
	}
}

// ValidateLLM checks that LLM provider settings are configured.
func ValidateLLM(cfg *Config) error {
	var missing []string
	if cfg.LLM.BaseURL == "" {
		missing = append(missing, "llm.base_url / LLM_BASE_URL")
	}
	if cfg.LLM.APIKey == "" {
		missing = append(missing, "llm.api_key / LLM_API_KEY")
	}
	if cfg.LLM.Model == "" {
		missing = append(missing, "llm.model / LLM_MODEL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config:\n  %s\n\nSet these in ~/.kash/config.yaml or as environment variables", strings.Join(missing, "\n  "))
	}
	return nil
}

// ValidateEmbedder checks that embedder provider settings are configured.
func ValidateEmbedder(cfg *Config) error {
	var missing []string
	if cfg.Embedder.BaseURL == "" {
		missing = append(missing, "embedder.base_url / EMBED_BASE_URL")
	}
	if cfg.Embedder.APIKey == "" {
		missing = append(missing, "embedder.api_key / EMBED_API_KEY")
	}
	// Model is optional when using an embedding router
	if len(missing) > 0 {
		return fmt.Errorf("missing required config:\n  %s\n\nSet these in ~/.kash/config.yaml or as environment variables", strings.Join(missing, "\n  "))
	}
	if cfg.Embedder.Dimensions <= 0 {
		return fmt.Errorf("embedder dimensions must be > 0 (got %d), set via runtime.embedder.dimensions in agent.yaml", cfg.Embedder.Dimensions)
	}
	return nil
}

// ValidateBuild validates all config needed for the build command.
func ValidateBuild(cfg *Config) error {
	if err := ValidateLLM(cfg); err != nil {
		return err
	}
	return ValidateEmbedder(cfg)
}

// ValidateServe validates all config needed for the serve command.
func ValidateServe(cfg *Config) error {
	if err := ValidateLLM(cfg); err != nil {
		return err
	}
	return ValidateEmbedder(cfg)
}

// EnsureConfigFile creates ~/.kash/config.yaml with an empty skeleton
// if it does not already exist. Returns (created bool, error).
func EnsureConfigFile() (bool, error) {
	cfgPath, err := ConfigFilePath()
	if err != nil {
		return false, err
	}

	// Already exists — nothing to do
	if _, err := os.Stat(cfgPath); err == nil {
		return false, nil
	}

	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}

	skeleton := `# Kash Configuration
# Docs: https://github.com/akashicode/kash
#
# These values are used by 'kash build' and 'kash serve'.
# Environment variables (LLM_BASE_URL, etc.) take priority over this file,
# so you can leave this empty when running inside a Docker container.

# LLM provider (required) — must be OpenAI-compatible
llm:
  base_url: ""
  api_key: ""
  model: ""
  # reasoning_effort: "" # optional: low | medium | high (default: disabled)

# Embedding provider (required) — must be OpenAI-compatible
# Model is optional when using an embedding router.
# Dimensions must be consistent between build and serve.
embedder:
  base_url: ""
  api_key: ""
  model: ""       # optional — omit when using a router
  dimensions: 1024 # default: 1024

# Reranking provider (optional) — must be OpenAI-compatible
reranker:
  base_url: ""
  api_key: ""
  model: ""

# Server port (default: 8000)
port: 8000
`
	if err := os.WriteFile(cfgPath, []byte(skeleton), 0600); err != nil {
		return false, fmt.Errorf("write config file: %w", err)
	}
	return true, nil
}

// IsConfigured returns true if at least LLM + embedder are populated in the
// config file (ignoring env vars).
func IsConfigured() bool {
	cfgPath, err := ConfigFilePath()
	if err != nil {
		return false
	}
	v := viper.New()
	v.SetConfigFile(cfgPath)
	if err := v.ReadInConfig(); err != nil {
		return false
	}
	return v.GetString("llm.api_key") != "" && v.GetString("embedder.api_key") != ""
}

// AgentYAMLDimensions reads runtime.embedder.dimensions from an agent.yaml file.
// Returns 0 if the file doesn't exist or the field is not set.
func AgentYAMLDimensions(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var parsed struct {
		Runtime struct {
			Embedder struct {
				Dimensions int `yaml:"dimensions"`
			} `yaml:"embedder"`
		} `yaml:"runtime"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return 0
	}
	return parsed.Runtime.Embedder.Dimensions
}

// AgentYAMLChunkOptions reads build.chunk_size and build.chunk_overlap
// (both in characters) from an agent.yaml file. Returns 0 for unset fields
// or when the file doesn't exist.
func AgentYAMLChunkOptions(path string) (chunkSize, chunkOverlap int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var parsed struct {
		Build struct {
			ChunkSize    int `yaml:"chunk_size"`
			ChunkOverlap int `yaml:"chunk_overlap"`
		} `yaml:"build"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return 0, 0
	}
	return parsed.Build.ChunkSize, parsed.Build.ChunkOverlap
}

// AgentYAMLMaxTokens reads runtime.embedder.max_tokens from an agent.yaml file.
// Returns 0 if the file doesn't exist or the field is not set.
func AgentYAMLMaxTokens(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var parsed struct {
		Runtime struct {
			Embedder struct {
				MaxTokens int `yaml:"max_tokens"`
			} `yaml:"embedder"`
		} `yaml:"runtime"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return 0
	}
	return parsed.Runtime.Embedder.MaxTokens
}

// AgentYAMLParallelEmbedding reads runtime.embedder.parallel from an agent.yaml file.
// Returns false if the file doesn't exist or the field is not set.
func AgentYAMLParallelEmbedding(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var parsed struct {
		Runtime struct {
			Embedder struct {
				Parallel bool `yaml:"parallel"`
			} `yaml:"embedder"`
		} `yaml:"runtime"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return false
	}
	return parsed.Runtime.Embedder.Parallel
}

// ApplyAgentYAMLDimensions reads dimensions from agent.yaml and applies them
// to the config. Priority (highest to lowest):
//  1. agent.yaml runtime.embedder.dimensions
//  2. config.yaml embedder.dimensions
//  3. Default: 1024
func ApplyAgentYAMLDimensions(cfg *Config, agentYAMLPath string) {
	if d := AgentYAMLDimensions(agentYAMLPath); d > 0 {
		cfg.Embedder.Dimensions = d
	}
	// Final default
	if cfg.Embedder.Dimensions == 0 {
		cfg.Embedder.Dimensions = 1024
	}
}

// NormalizeReasoningEffort validates and canonicalizes a reasoning effort string.
// Allowed values (case-insensitive): "low", "medium" (or "med"), "high".
// Empty string or "none"/"off"/"disabled" returns "" (disabled).
// Any other value returns an error.
func NormalizeReasoningEffort(val string) (string, error) {
	val = strings.ToLower(strings.TrimSpace(val))
	switch val {
	case "", "none", "off", "disabled":
		return "", nil
	case "low":
		return "low", nil
	case "med", "medium":
		return "medium", nil
	case "high":
		return "high", nil
	default:
		return "", fmt.Errorf("invalid reasoning effort %q: must be \"low\", \"medium\", or \"high\"", val)
	}
}

// AgentYAMLReasoningEffort reads reasoning_effort from an agent.yaml file.
// Supports runtime.llm.reasoning_effort, runtime.reasoning_effort, llm.reasoning_effort,
// agent.reasoning_effort, and their .reasoning aliases.
// Returns (effort, specified, error). When specified is true but effort is "",
// reasoning effort was explicitly disabled (e.g. "none" or "off").
func AgentYAMLReasoningEffort(path string) (effort string, specified bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return "", false, nil
	}

	var raw struct {
		Runtime struct {
			LLM struct {
				ReasoningEffort string `yaml:"reasoning_effort"`
				Reasoning       string `yaml:"reasoning"`
			} `yaml:"llm"`
			ReasoningEffort string `yaml:"reasoning_effort"`
			Reasoning       string `yaml:"reasoning"`
		} `yaml:"runtime"`
		LLM struct {
			ReasoningEffort string `yaml:"reasoning_effort"`
			Reasoning       string `yaml:"reasoning"`
		} `yaml:"llm"`
		Agent struct {
			ReasoningEffort string `yaml:"reasoning_effort"`
			Reasoning       string `yaml:"reasoning"`
		} `yaml:"agent"`
		ReasoningEffort string `yaml:"reasoning_effort"`
		Reasoning       string `yaml:"reasoning"`
	}

	if unmarshalErr := yaml.Unmarshal(data, &raw); unmarshalErr != nil {
		return "", false, unmarshalErr
	}

	candidates := []string{
		raw.Runtime.LLM.ReasoningEffort,
		raw.Runtime.LLM.Reasoning,
		raw.Runtime.ReasoningEffort,
		raw.Runtime.Reasoning,
		raw.LLM.ReasoningEffort,
		raw.LLM.Reasoning,
		raw.Agent.ReasoningEffort,
		raw.Agent.Reasoning,
		raw.ReasoningEffort,
		raw.Reasoning,
	}

	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			norm, normErr := NormalizeReasoningEffort(c)
			if normErr != nil {
				return "", true, normErr
			}
			return norm, true, nil
		}
	}

	return "", false, nil
}

// ApplyAgentYAMLReasoningEffort reads reasoning effort from agent.yaml and applies it
// to the config. Priority:
//  1. agent.yaml (explicit value, or explicit disable)
//  2. config.yaml / env var LLM_REASONING_EFFORT (already populated in cfg.LLM.ReasoningEffort)
//  3. Default: "" (disabled)
func ApplyAgentYAMLReasoningEffort(cfg *Config, agentYAMLPath string) error {
	if cfg == nil {
		return ErrNilConfig
	}
	effort, specified, err := AgentYAMLReasoningEffort(agentYAMLPath)
	if err != nil {
		return fmt.Errorf("agent.yaml reasoning_effort: %w", err)
	}
	if specified {
		cfg.LLM.ReasoningEffort = effort
		return nil
	}
	if cfg.LLM.ReasoningEffort != "" {
		normalized, normErr := NormalizeReasoningEffort(cfg.LLM.ReasoningEffort)
		if normErr != nil {
			return fmt.Errorf("llm.reasoning_effort: %w", normErr)
		}
		cfg.LLM.ReasoningEffort = normalized
	}
	return nil
}
