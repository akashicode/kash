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

	"github.com/akashicode/kash/internal/chunker"
	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/reader"
	"github.com/akashicode/kash/internal/vector"
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

	// Ensure we're in a Kash agent project
	if _, err := os.Stat("agent.yaml"); os.IsNotExist(err) {
		return errors.New("agent.yaml not found — run 'kash init <name>' first")
	}
	if _, err := os.Stat("data"); os.IsNotExist(err) {
		return errors.New("data/ directory not found — run 'kash init <name>' first")
	}

	// Apply dimensions from agent.yaml (canonical source) before validation
	agentconfig.ApplyAgentYAMLDimensions(cfg, "agent.yaml")

	if err := agentconfig.ValidateBuild(cfg); err != nil {
		return err
	}

	display.Header("⚡ Kash Build Pipeline")
	fmt.Println()
	display.KeyValue("Embed Dimensions", cfg.Embedder.Dimensions, display.Bold+display.BrightYellow)
	display.KeyValue("LLM Model", cfg.LLM.Model, display.BrightMagenta)
	display.KeyValue("Embed Endpoint", cfg.Embedder.BaseURL, display.Dim+display.White)
	fmt.Println()

	// Step 1: Load documents
	display.Step(1, 5, "Loading documents from data/...")
	docs, err := reader.LoadDirectory("data")
	if err != nil {
		return fmt.Errorf("load documents: %w", err)
	}
	if len(docs) == 0 {
		return errors.New("no supported documents found in data/ (add .md, .txt, or .pdf files)")
	}
	display.StepResult("Loaded", fmt.Sprintf("%d document(s)", len(docs)))
	for _, doc := range docs {
		display.StepDetail("• " + doc.Name)
	}

	// Step 2: Chunk documents
	display.Step(2, 5, "Chunking documents...")

	// If max_tokens is set in agent.yaml, auto-tune chunk size
	maxTokens := agentconfig.AgentYAMLMaxTokens("agent.yaml")
	var chunkOpts chunker.Options
	if maxTokens > 0 {
		chunkOpts = chunker.OptionsFromMaxTokens(maxTokens)
		display.KeyValue("Embed Max Tokens", maxTokens, display.BrightYellow)
		display.KeyValue("Chunk Size (chars)", chunkOpts.ChunkSize, display.Dim+display.White)
	} else {
		chunkOpts = chunker.DefaultOptions()
	}

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
	display.StepResult("Created", fmt.Sprintf("%d chunk(s)", len(allChunks)))

	// Step 3: Build vector store
	display.Step(3, 5, "Building vector index (this may take a while)...")
	vectorPath := filepath.Join("data", "memory.chromem")
	if err := os.MkdirAll(vectorPath, 0755); err != nil {
		return fmt.Errorf("create vector store directory: %w", err)
	}

	vs, err := vector.NewPersistentStore(vectorPath, &cfg.Embedder)
	if err != nil {
		return fmt.Errorf("create vector store: %w", err)
	}

	if err := vs.AddChunks(ctx, allChunks, agentconfig.AgentYAMLParallelEmbedding("agent.yaml")); err != nil {
		return fmt.Errorf("add chunks to vector store: %w", err)
	}
	display.StepResult("Indexed", fmt.Sprintf("%d vectors", vs.Count()))

	// Step 4: Extract knowledge graph
	display.Step(4, 5, "Extracting knowledge graph triples...")
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

		var triples []llm.Triple
		var extractErr error
		maxRetries := 2
		for attempt := 0; attempt <= maxRetries; attempt++ {
			triples, extractErr = llmClient.ExtractTriples(ctx, combined.String())
			if extractErr == nil {
				break
			}
			if attempt < maxRetries {
				display.StepWarn(fmt.Sprintf("triple extraction failed for batch %d-%d (attempt %d/%d, retrying): %v", i, end, attempt+1, maxRetries+1, extractErr))
			}
		}
		if extractErr != nil {
			display.StepWarn(fmt.Sprintf("triple extraction failed for batch %d-%d after %d attempts: %v", i, end, maxRetries+1, extractErr))
			continue
		}

		if err := gdb.AddTriples(ctx, triples); err != nil {
			display.StepWarn(fmt.Sprintf("failed to add triples for batch %d-%d: %v", i, end, err))
			continue
		}

		totalTriples += int64(len(triples))
		display.StepDetail(fmt.Sprintf("Chunks %d-%d: +%d triples (total: %d)", i+1, end, len(triples), totalTriples))
	}
	display.StepResult("Knowledge graph", fmt.Sprintf("%d triples", gdb.Count()))

	// Step 5: Generate MCP descriptions
	display.Step(5, 5, "Generating optimized MCP tool descriptions...")
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
		display.StepWarn(fmt.Sprintf("MCP description generation failed: %v", err))
		mcpDesc = fmt.Sprintf("Search the %s expert knowledge base for relevant information.", agentName)
	}

	// Update agent.yaml with new MCP description
	if err := updateAgentYAMLMCPDescription("agent.yaml", agentName, mcpDesc); err != nil {
		display.StepWarn(fmt.Sprintf("failed to update agent.yaml: %v", err))
	} else {
		display.StepResult("Updated", "agent.yaml with MCP tool description")
	}

	fmt.Println()
	display.Success("Build complete!")
	fmt.Println()
	display.KeyValue("Vector index", fmt.Sprintf("%s (%d documents)", vectorPath, vs.Count()), display.BrightGreen)
	display.KeyValue("Graph store", fmt.Sprintf("%s (%d triples)", graphPath, gdb.Count()), display.BrightGreen)

	display.NextSteps([]string{
		"docker compose up --build",
	})

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
