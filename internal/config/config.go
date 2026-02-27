package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// ErrNilConfig is returned when a nil Config is provided.
var ErrNilConfig = errors.New("config is nil")

// ConfigDir returns the path to ~/.agentforge/.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".agentforge"), nil
}

// ConfigFilePath returns the full path to ~/.agentforge/config.yaml.
func ConfigFilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// ProviderConfig holds connection details for a single AI provider.
type ProviderConfig struct {
	BaseURL    string `mapstructure:"base_url"    yaml:"base_url"`
	APIKey     string `mapstructure:"api_key"     yaml:"api_key"`
	Model      string `mapstructure:"model"       yaml:"model"`
	Dimensions int    `mapstructure:"dimensions"  yaml:"dimensions,omitempty"`
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

	applyEnv(&cfg.Embedder.BaseURL, "EMBED_BASE_URL")
	applyEnv(&cfg.Embedder.APIKey, "EMBED_API_KEY")
	applyEnv(&cfg.Embedder.Model, "EMBED_MODEL")
	applyEnvInt(&cfg.Embedder.Dimensions, "EMBED_DIMENSIONS")

	// Default embedding dimensions
	if cfg.Embedder.Dimensions == 0 {
		cfg.Embedder.Dimensions = 1024
	}

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

// applyEnvInt overwrites dst with the int value of the environment variable if set.
func applyEnvInt(dst *int, envKey string) {
	if v := os.Getenv(envKey); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			*dst = n
		}
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
		return fmt.Errorf("missing required config:\n  %s\n\nSet these in ~/.agentforge/config.yaml or as environment variables", strings.Join(missing, "\n  "))
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
		return fmt.Errorf("missing required config:\n  %s\n\nSet these in ~/.agentforge/config.yaml or as environment variables", strings.Join(missing, "\n  "))
	}
	if cfg.Embedder.Dimensions <= 0 {
		return fmt.Errorf("embedder dimensions must be > 0 (got %d), set via embedder.dimensions or EMBED_DIMENSIONS", cfg.Embedder.Dimensions)
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

// EnsureConfigFile creates ~/.agentforge/config.yaml with an empty skeleton
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

	skeleton := `# Agent-Forge Configuration
# Docs: https://github.com/agent-forge/agent-forge
#
# These values are used by 'agentforge build' and 'agentforge serve'.
# Environment variables (LLM_BASE_URL, etc.) take priority over this file,
# so you can leave this empty when running inside a Docker container.

# LLM provider (required) — must be OpenAI-compatible
llm:
  base_url: ""
  api_key: ""
  model: ""

# Embedding provider (required) — must be OpenAI-compatible
embedder:
  base_url: ""
  api_key: ""
  model: ""

# Reranking provider (optional) — must be OpenAI-compatible
reranker:
  base_url: ""
  api_key: ""
  model: ""

# Server port (default: 8000)
port: 8000
`
	if err := os.WriteFile(cfgPath, []byte(skeleton), 0644); err != nil {
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
