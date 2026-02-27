package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	agentconfig "github.com/agent-forge/agent-forge/internal/config"
	"github.com/agent-forge/agent-forge/internal/server"
)

var (
	servePort      int
	serveAgentYAML string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Agent-Forge runtime server",
	Long: `Starts the runtime HTTP server on port 8000 (or $PORT).
Requires compiled databases in data/memory.chromem/ and data/knowledge.cayley/.

Exposes three interfaces:
  POST /v1/chat/completions  - OpenAI-compatible REST API
  GET  /mcp                  - Model Context Protocol over HTTP SSE
  POST /rpc/agent            - A2A JSON-RPC endpoint

Runtime API keys must be provided via environment variables:
  LLM_BASE_URL, LLM_API_KEY, LLM_MODEL
  EMBED_BASE_URL, EMBED_API_KEY, EMBED_MODEL`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 8000, "Port to listen on")
	serveCmd.Flags().StringVar(&serveAgentYAML, "agent", "agent.yaml", "Path to agent.yaml")
	rootCmd.AddCommand(serveCmd)
}

func runServe(_ *cobra.Command, _ []string) error {
	// Load runtime config from environment
	runtimeCfg := agentconfig.LoadRuntime()

	if err := validateRuntimeConfig(runtimeCfg); err != nil {
		return fmt.Errorf("runtime config error: %w", err)
	}

	// Use PORT env variable if set (container environments)
	if envPort := os.Getenv("PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", &servePort)
	}

	cfg := server.Config{
		VectorStorePath: "data/memory.chromem",
		GraphDBPath:     "data/knowledge.cayley",
		AgentYAMLPath:   serveAgentYAML,
		RuntimeCfg:      runtimeCfg,
	}

	srv, err := server.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize server: %w", err)
	}

	addr := fmt.Sprintf(":%d", servePort)
	fmt.Printf("Agent-Forge Runtime Server\n")
	fmt.Printf("==========================\n")
	fmt.Printf("Listening on http://0.0.0.0%s\n\n", addr)
	fmt.Printf("Endpoints:\n")
	fmt.Printf("  REST  POST http://0.0.0.0%s/v1/chat/completions\n", addr)
	fmt.Printf("  MCP   GET  http://0.0.0.0%s/mcp\n", addr)
	fmt.Printf("  A2A   POST http://0.0.0.0%s/rpc/agent\n", addr)
	fmt.Printf("  Health GET http://0.0.0.0%s/health\n\n", addr)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	return httpServer.ListenAndServe()
}

func validateRuntimeConfig(cfg *agentconfig.RuntimeConfig) error {
	if cfg.LLM.BaseURL == "" {
		return fmt.Errorf("LLM_BASE_URL environment variable is required")
	}
	if cfg.LLM.APIKey == "" {
		return fmt.Errorf("LLM_API_KEY environment variable is required")
	}
	if cfg.LLM.Model == "" {
		return fmt.Errorf("LLM_MODEL environment variable is required")
	}
	if cfg.Embedder.BaseURL == "" {
		return fmt.Errorf("EMBED_BASE_URL environment variable is required")
	}
	if cfg.Embedder.APIKey == "" {
		return fmt.Errorf("EMBED_API_KEY environment variable is required")
	}
	if cfg.Embedder.Model == "" {
		return fmt.Errorf("EMBED_MODEL environment variable is required")
	}
	return nil
}
