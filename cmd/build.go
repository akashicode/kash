package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/akashicode/kash/internal/chunker"
	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/manifest"
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
  5. Updates agent.yaml with optimized MCP tool descriptions

Builds are incremental and resumable: a build manifest (data/build.manifest.json)
tracks each document's content hash and progress. Unchanged documents are
skipped, new or modified documents are (re)processed, and an interrupted build
resumes where it left off. Every build that changes the corpus bumps its
version (v1, v2, ...).`,
	RunE: runBuild,
}

var (
	buildDir     string
	buildRebuild bool
	buildPrune   bool
)

func init() {
	buildCmd.Flags().StringVarP(&buildDir, "dir", "d", ".", "Path to the agent project directory")
	buildCmd.Flags().BoolVar(&buildRebuild, "rebuild", false, "Discard existing databases and manifest, rebuild the corpus from scratch")
	buildCmd.Flags().BoolVar(&buildPrune, "prune", false, "Remove data for documents that no longer exist in data/")
}

// buildReason describes why a document needs processing.
type buildReason string

const (
	reasonNew     buildReason = "new"
	reasonChanged buildReason = "changed"
	reasonResume  buildReason = "resume"
)

// docPlan pairs a loaded document with its build decision.
type docPlan struct {
	Doc    reader.Document
	Hash   string
	Reason buildReason
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

	vectorPath := filepath.Join("data", "memory.chromem")
	graphPath := filepath.Join("data", "knowledge.cayley")
	manifestPath := filepath.Join("data", manifest.FileName)

	if buildRebuild {
		display.StepWarn("--rebuild: discarding existing databases and manifest")
		for _, p := range []string{vectorPath, graphPath, manifestPath} {
			if err := os.RemoveAll(p); err != nil {
				return fmt.Errorf("remove %q: %w", p, err)
			}
		}
	}

	m, err := manifest.LoadOrNew(manifestPath)
	if err != nil {
		return err
	}

	// Chunk options: auto-tune from max_tokens if set in agent.yaml
	maxTokens := agentconfig.AgentYAMLMaxTokens("agent.yaml")
	var chunkOpts chunker.Options
	if maxTokens > 0 {
		chunkOpts = chunker.OptionsFromMaxTokens(maxTokens)
	} else {
		chunkOpts = chunker.DefaultOptions()
	}

	// An existing corpus must have been built with the same embedder —
	// vectors from different models/dimensions are not comparable.
	optsChanged := false
	if len(m.Documents) > 0 {
		if m.EmbedModel != cfg.Embedder.Model || m.EmbedDimensions != cfg.Embedder.Dimensions {
			return fmt.Errorf(
				"existing corpus was built with embedder %q (%d dims) but config now specifies %q (%d dims) — run 'kash build --rebuild' to rebuild from scratch",
				m.EmbedModel, m.EmbedDimensions, cfg.Embedder.Model, cfg.Embedder.Dimensions,
			)
		}
		if m.ChunkSize != chunkOpts.ChunkSize || m.ChunkOverlap != chunkOpts.Overlap {
			optsChanged = true
			display.StepWarn("chunking options changed since last build — existing documents keep their old chunking (run 'kash build --rebuild' for consistency)")
		}
	}
	m.EmbedModel = cfg.Embedder.Model
	m.EmbedDimensions = cfg.Embedder.Dimensions
	m.ChunkSize = chunkOpts.ChunkSize
	m.ChunkOverlap = chunkOpts.Overlap

	display.Header("⚡ Kash Build Pipeline")
	fmt.Println()
	display.KeyValue("Corpus Version", m.Version, display.Bold+display.BrightYellow)
	display.KeyValue("Embed Dimensions", cfg.Embedder.Dimensions, display.Bold+display.BrightYellow)
	display.KeyValue("LLM Model", cfg.LLM.Model, display.BrightMagenta)
	display.KeyValue("Embed Endpoint", cfg.Embedder.BaseURL, display.Dim+display.White)
	display.KeyValue("Chunk Size (chars)", chunkOpts.ChunkSize, display.Dim+display.White)
	fmt.Println()

	// Step 1: Load documents
	display.Step(1, 4, "Loading documents from data/...")
	docs, err := reader.LoadDirectory("data")
	if err != nil {
		return fmt.Errorf("load documents: %w", err)
	}
	if len(docs) == 0 {
		return errors.New("no supported documents found in data/ (add .md, .txt, or .pdf files)")
	}
	display.StepResult("Loaded", fmt.Sprintf("%d document(s)", len(docs)))

	// Step 2: Plan — decide per document whether to skip, add, replace, or resume
	display.Step(2, 4, "Planning incremental build...")
	var pending []docPlan
	unchanged := 0
	docNames := map[string]bool{}
	for _, doc := range docs {
		docNames[doc.Name] = true
		hash := manifest.HashContent(doc.Content)
		state := m.Documents[doc.Name]
		switch {
		case state == nil:
			pending = append(pending, docPlan{Doc: doc, Hash: hash, Reason: reasonNew})
		case state.SHA256 != hash:
			pending = append(pending, docPlan{Doc: doc, Hash: hash, Reason: reasonChanged})
		case !state.Done():
			// Resuming requires identical chunking; if options changed the
			// chunk/batch boundaries shift, so redo the document instead.
			reason := reasonResume
			if optsChanged {
				reason = reasonChanged
			}
			pending = append(pending, docPlan{Doc: doc, Hash: hash, Reason: reason})
		default:
			unchanged++
		}
	}

	// Documents recorded in the manifest but no longer present in data/
	var removed []string
	for name := range m.Documents {
		if !docNames[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)

	for _, p := range pending {
		display.StepDetail(fmt.Sprintf("• %s (%s)", p.Doc.Name, p.Reason))
	}
	display.StepResult("Plan", fmt.Sprintf("%d to process, %d unchanged, %d removed", len(pending), unchanged, len(removed)))

	if len(removed) > 0 && !buildPrune {
		display.StepWarn(fmt.Sprintf("%d document(s) in the corpus no longer exist in data/ (kept — run with --prune to remove): %s",
			len(removed), strings.Join(removed, ", ")))
	}

	if len(pending) == 0 && (len(removed) == 0 || !buildPrune) {
		fmt.Println()
		display.Success(fmt.Sprintf("Corpus up to date (version %d) — nothing to build", m.Version))
		return nil
	}

	// Step 3: Process documents (embed + extract triples per document)
	display.Step(3, 4, "Building documents (embeddings + knowledge graph)...")

	vs, err := vector.NewPersistentStore(vectorPath, &cfg.Embedder)
	if err != nil {
		return fmt.Errorf("create vector store: %w", err)
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

	corpusChanged := false

	// Prune removed documents first
	if buildPrune {
		for _, name := range removed {
			if err := vs.DeleteBySource(ctx, name); err != nil {
				display.StepWarn(fmt.Sprintf("prune vectors for %s: %v", name, err))
			}
			if err := gdb.DeleteBySource(ctx, name); err != nil {
				display.StepWarn(fmt.Sprintf("prune triples for %s: %v", name, err))
			}
			delete(m.Documents, name)
			corpusChanged = true
			display.StepDetail(fmt.Sprintf("Pruned %s", name))
		}
		if corpusChanged {
			if err := m.Save(manifestPath); err != nil {
				return fmt.Errorf("save manifest: %w", err)
			}
		}
	}

	ck, err := chunker.NewChunker(chunkOpts)
	if err != nil {
		return fmt.Errorf("create chunker: %w", err)
	}

	parallelEmbed := agentconfig.AgentYAMLParallelEmbedding("agent.yaml")
	batchSize := 10
	incomplete := []string{}
	var sampleChunks []chunker.Chunk

	for _, p := range pending {
		name := p.Doc.Name

		chunks, err := ck.SplitBySentence(p.Doc.Content, name)
		if err != nil {
			return fmt.Errorf("chunk document %q: %w", name, err)
		}
		if len(sampleChunks) == 0 {
			limit := 3
			if len(chunks) < limit {
				limit = len(chunks)
			}
			sampleChunks = chunks[:limit]
		}

		state := m.Documents[name]
		if p.Reason == reasonChanged {
			// Replace: clear the document's old vectors and triples first
			if err := vs.DeleteBySource(ctx, name); err != nil {
				return fmt.Errorf("delete old vectors for %q: %w", name, err)
			}
			if err := gdb.DeleteBySource(ctx, name); err != nil {
				return fmt.Errorf("delete old triples for %q: %w", name, err)
			}
			state = nil
		}
		if state == nil {
			state = &manifest.DocState{SHA256: p.Hash}
			m.Documents[name] = state
			if err := m.Save(manifestPath); err != nil {
				return fmt.Errorf("save manifest: %w", err)
			}
		}

		// Phase 1: embeddings
		if !state.VectorDone {
			if p.Reason == reasonResume {
				// Clear any partially embedded chunks from the interrupted run
				if err := vs.DeleteBySource(ctx, name); err != nil {
					return fmt.Errorf("clear partial vectors for %q: %w", name, err)
				}
			}
			if err := vs.AddChunks(ctx, chunks, parallelEmbed); err != nil {
				return fmt.Errorf("add chunks for %q to vector store: %w", name, err)
			}
			state.Chunks = len(chunks)
			state.VectorDone = true
			if err := m.Save(manifestPath); err != nil {
				return fmt.Errorf("save manifest: %w", err)
			}
			display.StepDetail(fmt.Sprintf("%s: embedded %d chunk(s)", name, len(chunks)))
		} else {
			display.StepDetail(fmt.Sprintf("%s: embeddings already done (%d chunks)", name, state.Chunks))
		}

		// Phase 2: knowledge graph triples, resumable per batch
		if !state.GraphDone {
			if state.GraphBatchesDone > 0 {
				display.StepDetail(fmt.Sprintf("%s: resuming triple extraction from batch %d", name, state.GraphBatchesDone+1))
			}

			docComplete := true
			for i := state.GraphBatchesDone * batchSize; i < len(chunks); i += batchSize {
				end := i + batchSize
				if end > len(chunks) {
					end = len(chunks)
				}
				batch := chunks[i:end]

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
						display.StepWarn(fmt.Sprintf("triple extraction failed for %s chunks %d-%d (attempt %d/%d, retrying): %v", name, i+1, end, attempt+1, maxRetries+1, extractErr))
					}
				}
				if extractErr != nil {
					// Leave the manifest at the last completed batch so the
					// next build resumes exactly here.
					display.StepWarn(fmt.Sprintf("triple extraction failed for %s chunks %d-%d after %d attempts — will resume on next build: %v", name, i+1, end, maxRetries+1, extractErr))
					docComplete = false
					break
				}

				if err := gdb.AddTriples(ctx, triples, name); err != nil {
					return fmt.Errorf("add triples for %q: %w", name, err)
				}

				state.Triples += len(triples)
				state.GraphBatchesDone++
				if err := m.Save(manifestPath); err != nil {
					return fmt.Errorf("save manifest: %w", err)
				}
				display.StepDetail(fmt.Sprintf("%s: chunks %d-%d: +%d triples (doc total: %d)", name, i+1, end, len(triples), state.Triples))
			}

			if docComplete {
				state.GraphDone = true
				state.CompletedAt = time.Now().UTC()
				if err := m.Save(manifestPath); err != nil {
					return fmt.Errorf("save manifest: %w", err)
				}
			} else {
				incomplete = append(incomplete, name)
			}
		}

		corpusChanged = true
	}

	display.StepResult("Indexed", fmt.Sprintf("%d vectors, %d triples total", vs.Count(), gdb.Count()))

	// Step 4: Generate MCP tool description (only when the corpus changed)
	display.Step(4, 4, "Generating optimized MCP tool descriptions...")
	var sampleContent strings.Builder
	for _, ch := range sampleChunks {
		sampleContent.WriteString(ch.Content)
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

	if sampleContent.Len() > 0 {
		mcpDesc, err := llmClient.GenerateMCPDescription(ctx, agentName, sampleContent.String())
		if err != nil {
			display.StepWarn(fmt.Sprintf("MCP description generation failed: %v", err))
			mcpDesc = fmt.Sprintf("Search the %s expert knowledge base for relevant information.", agentName)
		}
		if err := updateAgentYAMLMCPDescription("agent.yaml", agentName, mcpDesc); err != nil {
			display.StepWarn(fmt.Sprintf("failed to update agent.yaml: %v", err))
		} else {
			display.StepResult("Updated", "agent.yaml with MCP tool description")
		}
	} else {
		display.StepResult("Skipped", "no new content to describe")
	}

	// Bump the corpus version on any successful change
	if corpusChanged {
		m.Version++
		if err := m.Save(manifestPath); err != nil {
			return fmt.Errorf("save manifest: %w", err)
		}
	}

	fmt.Println()
	if len(incomplete) > 0 {
		display.StepWarn(fmt.Sprintf("build finished with %d incomplete document(s): %s — run 'kash build' again to resume", len(incomplete), strings.Join(incomplete, ", ")))
	}
	display.Success(fmt.Sprintf("Build complete! Corpus version: %d", m.Version))
	fmt.Println()
	display.KeyValue("Vector index", fmt.Sprintf("%s (%d documents)", vectorPath, vs.Count()), display.BrightGreen)
	display.KeyValue("Graph store", fmt.Sprintf("%s (%d triples)", graphPath, gdb.Count()), display.BrightGreen)
	display.KeyValue("Manifest", manifestPath, display.BrightGreen)

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
