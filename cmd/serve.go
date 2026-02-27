package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	agentconfig "github.com/agent-forge/agent-forge/internal/config"
	"github.com/agent-forge/agent-forge/internal/server"
)

var (
	serveAgentYAML string
	serveDir       string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Agent-Forge runtime server",
	Long: `Starts the runtime HTTP server on port 8000 (or config/PORT env).
Requires compiled databases in data/memory.chromem/ and data/knowledge.cayley/.

Exposes three interfaces:
  POST /v1/chat/completions  - OpenAI-compatible REST API
  GET  /mcp                  - Model Context Protocol over HTTP SSE
  POST /rpc/agent            - A2A JSON-RPC endpoint

Provider config is resolved from environment variables first,
then falls back to ~/.agentforge/config.yaml.`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAgentYAML, "agent", "agent.yaml", "Path to agent.yaml")
	serveCmd.Flags().StringVarP(&serveDir, "dir", "d", ".", "Path to the agent project directory")
	rootCmd.AddCommand(serveCmd)
}

func runServe(_ *cobra.Command, _ []string) error {
	// Change to project directory if specified
	if serveDir != "." {
		abs, err := filepath.Abs(serveDir)
		if err != nil {
			return fmt.Errorf("resolve directory %q: %w", serveDir, err)
		}
		if err := os.Chdir(abs); err != nil {
			return fmt.Errorf("change to directory %q: %w", abs, err)
		}
		fmt.Printf("Working directory: %s\n", abs)
	}

	// Load unified config (env vars take priority over config.yaml)
	cfg, err := agentconfig.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := agentconfig.ValidateServe(cfg); err != nil {
		return err
	}

	srvCfg := server.Config{
		VectorStorePath: "data/memory.chromem",
		GraphDBPath:     "data/knowledge.cayley",
		AgentYAMLPath:   serveAgentYAML,
		AppCfg:          cfg,
	}

	srv, err := server.New(srvCfg)
	if err != nil {
		return fmt.Errorf("initialize server: %w", err)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	fmt.Printf("Agent-Forge Runtime Server\n")
	fmt.Printf("==========================\n")
	fmt.Printf("Listening on http://0.0.0.0%s\n\n", addr)
	fmt.Printf("Endpoints:\n")
	fmt.Printf("  REST  POST http://0.0.0.0%s/v1/chat/completions\n", addr)
	fmt.Printf("  MCP   GET  http://0.0.0.0%s/mcp\n", addr)
	fmt.Printf("  A2A   POST http://0.0.0.0%s/rpc/agent\n", addr)
	fmt.Printf("  Health GET http://0.0.0.0%s/health\n\n", addr)

	if cfg.Reranker.BaseURL != "" {
		fmt.Printf("Reranker: enabled (%s)\n\n", cfg.Reranker.Model)
	}

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	return httpServer.ListenAndServe()
}
