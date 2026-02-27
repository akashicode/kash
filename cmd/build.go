package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/agent-forge/agent-forge/internal/chunker"
	agentconfig "github.com/agent-forge/agent-forge/internal/config"
	"github.com/agent-forge/agent-forge/internal/graph"
	"github.com/agent-forge/agent-forge/internal/llm"
	"github.com/agent-forge/agent-forge/internal/reader"
	"github.com/agent-forge/agent-forge/internal/vector"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Compile documents into vector and graph databases",
	Long: `Reads documents from the data/ directory and builds the embedded databases:
  1. Chunks text documents
  2. Generates vector embeddings via the configured embedder
  3. Extracts knowledge graph triples via LLM
  4. Persists databases to data/memory.chromem/ and data/knowledge.cayley/
  5. Updates agent.yaml with optimized MCP tool descriptions`,
	RunE: runBuild,
}

var buildDir string

func init() {
	buildCmd.Flags().StringVarP(&buildDir, "dir", "d", ".", "Path to the agent project directory")
}

func runBuild(cmd *cobra.Command, args []string) error {
	// Change to project directory if specified
	if buildDir != "." {
		abs, err := filepath.Abs(buildDir)
		if err != nil {
			return fmt.Errorf("resolve directory %q: %w", buildDir, err)
		}
		if err := os.Chdir(abs); err != nil {
			return fmt.Errorf("change to directory %q: %w", abs, err)
		}
		fmt.Printf("Working directory: %s\n", abs)
	}

	ctx := context.Background()

	// Load unified config (env vars take priority over config.yaml)
	cfg, err := agentconfig.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := agentconfig.ValidateBuild(cfg); err != nil {
		return err
	}

	// Ensure we're in an agent-forge project
	if _, err := os.Stat("agent.yaml"); os.IsNotExist(err) {
		return errors.New("agent.yaml not found — run 'agentforge init <name>' first")
	}
	if _, err := os.Stat("data"); os.IsNotExist(err) {
		return errors.New("data/ directory not found — run 'agentforge init <name>' first")
	}

	// Apply dimensions from agent.yaml (canonical source) if not already set
	agentconfig.ApplyAgentYAMLDimensions(cfg, "agent.yaml")

	fmt.Println("Agent-Forge Build Pipeline")
	fmt.Println("==========================")
	fmt.Printf("Embedding dimensions: %d\n", cfg.Embedder.Dimensions)

	// Step 1: Load documents
	fmt.Println("\n[1/5] Loading documents from data/...")
	docs, err := reader.LoadDirectory("data")
	if err != nil {
		return fmt.Errorf("load documents: %w", err)
	}
	if len(docs) == 0 {
		return errors.New("no supported documents found in data/ (add .md, .txt, or .pdf files)")
	}
	fmt.Printf("      Loaded %d document(s)\n", len(docs))
	for _, doc := range docs {
		fmt.Printf("      - %s\n", doc.Name)
	}

	// Step 2: Chunk documents
	fmt.Println("\n[2/5] Chunking documents...")
	chunkOpts := chunker.DefaultOptions()
	ck, err := chunker.NewChunker(chunkOpts)
	if err != nil {
		return fmt.Errorf("create chunker: %w", err)
	}

	var allChunks []chunker.Chunk
	for _, doc := range docs {
		chunks, err := ck.SplitBySentence(doc.Content, doc.Name)
		if err != nil {
			return fmt.Errorf("chunk document %q: %w", doc.Name, err)
		}
		allChunks = append(allChunks, chunks...)
	}
	fmt.Printf("      Created %d chunk(s)\n", len(allChunks))

	// Step 3: Build vector store
	fmt.Println("\n[3/5] Building vector index (this may take a while)...")
	vectorPath := filepath.Join("data", "memory.chromem")
	if err := os.MkdirAll(vectorPath, 0755); err != nil {
		return fmt.Errorf("create vector store directory: %w", err)
	}

	vs, err := vector.NewPersistentStore(vectorPath, &cfg.Embedder)
	if err != nil {
		return fmt.Errorf("create vector store: %w", err)
	}

	if err := vs.AddChunks(ctx, allChunks); err != nil {
		return fmt.Errorf("add chunks to vector store: %w", err)
	}
	fmt.Printf("      Indexed %d vectors\n", vs.Count())

	// Step 4: Extract knowledge graph
	fmt.Println("\n[4/5] Extracting knowledge graph triples...")
	graphPath := filepath.Join("data", "knowledge.cayley")
	if err := os.MkdirAll(graphPath, 0755); err != nil {
		return fmt.Errorf("create graph store directory: %w", err)
	}

	gdb, err := graph.NewDBFromPath(graphPath)
	if err != nil {
		return fmt.Errorf("create graph store: %w", err)
	}
	defer gdb.Close()

	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		return fmt.Errorf("create LLM client: %w", err)
	}

	totalTriples := int64(0)
	// Process chunks in batches to extract triples
	batchSize := 10
	for i := 0; i < len(allChunks); i += batchSize {
		end := i + batchSize
		if end > len(allChunks) {
			end = len(allChunks)
		}
		batch := allChunks[i:end]

		// Combine batch into single text for efficiency
		var combined strings.Builder
		for _, ch := range batch {
			combined.WriteString(ch.Content)
			combined.WriteString("\n\n")
		}

		triples, err := llmClient.ExtractTriples(ctx, combined.String())
		if err != nil {
			fmt.Printf("      warning: triple extraction failed for batch %d-%d: %v\n", i, end, err)
			continue
		}

		if err := gdb.AddTriples(ctx, triples); err != nil {
			fmt.Printf("      warning: failed to add triples for batch %d-%d: %v\n", i, end, err)
			continue
		}

		totalTriples += int64(len(triples))
		fmt.Printf("      Processed chunks %d-%d: +%d triples (total: %d)\n", i+1, end, len(triples), totalTriples)
	}
	fmt.Printf("      Knowledge graph: %d triples\n", gdb.Count())

	// Step 5: Generate MCP descriptions
	fmt.Println("\n[5/5] Generating optimized MCP tool descriptions...")
	var sampleContent strings.Builder
	limit := 3
	if len(allChunks) < limit {
		limit = len(allChunks)
	}
	for i := 0; i < limit; i++ {
		sampleContent.WriteString(allChunks[i].Content)
		sampleContent.WriteString("\n\n")
	}

	agentYAMLData, err := os.ReadFile("agent.yaml")
	if err != nil {
		return fmt.Errorf("read agent.yaml: %w", err)
	}

	var agentConfig map[string]interface{}
	if err := yaml.Unmarshal(agentYAMLData, &agentConfig); err != nil {
		return fmt.Errorf("parse agent.yaml: %w", err)
	}

	agentName := "agent"
	if a, ok := agentConfig["agent"].(map[string]interface{}); ok {
		if name, ok := a["name"].(string); ok {
			agentName = strings.ToLower(strings.ReplaceAll(name, " ", "_"))
		}
	}

	mcpDesc, err := llmClient.GenerateMCPDescription(ctx, agentName, sampleContent.String())
	if err != nil {
		fmt.Printf("      warning: MCP description generation failed: %v\n", err)
		mcpDesc = fmt.Sprintf("Search the %s expert knowledge base for relevant information.", agentName)
	}

	// Update agent.yaml with new MCP description
	if err := updateAgentYAMLMCPDescription("agent.yaml", agentName, mcpDesc); err != nil {
		fmt.Printf("      warning: failed to update agent.yaml: %v\n", err)
	} else {
		fmt.Printf("      Updated agent.yaml with MCP tool description\n")
	}

	fmt.Println("\n==========================")
	fmt.Println("Build complete!")
	fmt.Printf("  Vector index: %s (%d documents)\n", vectorPath, vs.Count())
	fmt.Printf("  Graph store:  %s (%d triples)\n", graphPath, gdb.Count())
	fmt.Println("\nNext steps:")
	fmt.Println("  docker compose up --build")

	return nil
}

func updateAgentYAMLMCPDescription(path, agentName, description string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read agent.yaml: %w", err)
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse agent.yaml: %w", err)
	}

	// Update or create mcp.tools section
	mcpSection, _ := config["mcp"].(map[string]interface{})
	if mcpSection == nil {
		mcpSection = map[string]interface{}{}
	}

	tools := []map[string]interface{}{
		{
			"name":        "search_" + agentName + "_knowledge",
			"description": description,
		},
	}
	mcpSection["tools"] = tools
	config["mcp"] = mcpSection

	// Marshal back to YAML
	output, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal agent.yaml: %w", err)
	}

	return os.WriteFile(path, output, 0644)
}
