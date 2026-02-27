package config

import (
	"errors"

	"github.com/spf13/viper"
)

// ErrNilConfig is returned when a nil Config is provided.
var ErrNilConfig = errors.New("config is nil")

// Config holds the full application configuration.
type Config struct {
	BuildProviders BuildProviders `mapstructure:"build_providers"`
}

// BuildProviders holds configuration for all build-time AI providers.
type BuildProviders struct {
	LLM      ProviderConfig `mapstructure:"llm"`
	Embedder ProviderConfig `mapstructure:"embedder"`
	Reranker ProviderConfig `mapstructure:"reranker,omitempty"`
}

// ProviderConfig holds connection details for a single AI provider.
type ProviderConfig struct {
	BaseURL string `mapstructure:"base_url"`
	APIKey  string `mapstructure:"api_key"`
	Model   string `mapstructure:"model"`
}

// RuntimeConfig holds environment-provided runtime configuration.
type RuntimeConfig struct {
	LLM      ProviderConfig
	Embedder ProviderConfig
	Reranker ProviderConfig
}

// Load reads the Viper-populated config into a Config struct.
func Load() (*Config, error) {
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, errors.New("unmarshal config: " + err.Error())
	}
	return &cfg, nil
}

// LoadRuntime reads runtime provider settings from environment variables.
func LoadRuntime() *RuntimeConfig {
	return &RuntimeConfig{
		LLM: ProviderConfig{
			BaseURL: viper.GetString("LLM_BASE_URL"),
			APIKey:  viper.GetString("LLM_API_KEY"),
			Model:   viper.GetString("LLM_MODEL"),
		},
		Embedder: ProviderConfig{
			BaseURL: viper.GetString("EMBED_BASE_URL"),
			APIKey:  viper.GetString("EMBED_API_KEY"),
			Model:   viper.GetString("EMBED_MODEL"),
		},
		Reranker: ProviderConfig{
			BaseURL: viper.GetString("RERANK_BASE_URL"),
			APIKey:  viper.GetString("RERANK_API_KEY"),
			Model:   viper.GetString("RERANK_MODEL"),
		},
	}
}
