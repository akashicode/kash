package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/akashicode/kash/internal/chunker"
	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/lexical"
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/manifest"
	"github.com/akashicode/kash/internal/profile"
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
	buildDir            string
	buildRebuild        bool
	buildPrune          bool
	buildRefreshProfile bool
)

func init() {
	buildCmd.Flags().StringVarP(&buildDir, "dir", "d", ".", "Path to the agent project directory")
	buildCmd.Flags().BoolVar(&buildRebuild, "rebuild", false, "Discard existing databases and manifest, rebuild the corpus from scratch")
	buildCmd.Flags().BoolVar(&buildPrune, "prune", false, "Remove data for documents that no longer exist in data/")
	buildCmd.Flags().BoolVar(&buildRefreshProfile, "refresh-profile", false, "Re-derive the corpus profile instead of reusing the existing one")
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
	lexicalPath := filepath.Join("data", lexical.FileName)
	profilePath := filepath.Join("data", profile.FileName)

	if buildRebuild {
		display.StepWarn("--rebuild: discarding existing databases and manifest")
		for _, p := range []string{vectorPath, graphPath, manifestPath, lexicalPath} {
			if err := os.RemoveAll(p); err != nil {
				return fmt.Errorf("remove %q: %w", p, err)
			}
		}
	}

	m, err := manifest.LoadOrNew(manifestPath)
	if err != nil {
		return err
	}

	// Chunk options — priority: explicit build.chunk_size in agent.yaml,
	// then auto-tune from runtime.embedder.max_tokens, then defaults.
	chunkSize, chunkOverlap := agentconfig.AgentYAMLChunkOptions("agent.yaml")
	maxTokens := agentconfig.AgentYAMLMaxTokens("agent.yaml")
	var chunkOpts chunker.Options
	switch {
	case chunkSize > 0:
		if chunkOverlap <= 0 {
			chunkOverlap = chunkSize / 5
		}
		chunkOpts = chunker.Options{ChunkSize: chunkSize, Overlap: chunkOverlap}
		// Never exceed what the embedding model can actually accept
		if maxTokens > 0 {
			modelLimit := int(float64(maxTokens) * 4 * 0.9)
			if chunkOpts.ChunkSize > modelLimit {
				display.StepWarn(fmt.Sprintf("build.chunk_size %d exceeds the embedder's ~%d-char limit (max_tokens: %d) — capping to %d", chunkOpts.ChunkSize, modelLimit, maxTokens, modelLimit))
				chunkOpts.ChunkSize = modelLimit
			}
		}
		if chunkOpts.ChunkSize > chunker.MaxRetrievalChunkSize {
			display.StepWarn(fmt.Sprintf("build.chunk_size %d is large — chunks over ~%d chars usually reduce retrieval precision", chunkOpts.ChunkSize, chunker.MaxRetrievalChunkSize))
		}
	case maxTokens > 0:
		chunkOpts = chunker.OptionsFromMaxTokens(maxTokens)
	default:
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
	m.KashVersion = version

	display.Header("⚡ Kash Build Pipeline")
	fmt.Println()
	display.KeyValue("Corpus Version", m.Version, display.Bold+display.BrightYellow)
	display.KeyValue("Embed Dimensions", cfg.Embedder.Dimensions, display.Bold+display.BrightYellow)
	display.KeyValue("LLM Model", cfg.LLM.Model, display.BrightMagenta)
	display.KeyValue("Embed Endpoint", cfg.Embedder.BaseURL, display.Dim+display.White)
	display.KeyValue("Chunk Size (chars)", chunkOpts.ChunkSize, display.Dim+display.White)
	display.KeyValue("Chunk Overlap (chars)", chunkOpts.Overlap, display.Dim+display.White)
	fmt.Println()

	// Step 1: Load documents
	display.Step(1, 7, "Loading documents from data/...")
	docs, rejected, err := reader.LoadDirectory("data")
	if err != nil {
		return fmt.Errorf("load documents: %w", err)
	}
	if len(docs) == 0 {
		return errors.New("no supported documents found in data/ (add .md, .txt, or .pdf files)")
	}
	display.StepResult("Loaded", fmt.Sprintf("%d document(s)", len(docs)))

	// Rejections are reported, never swallowed: a document that silently fails
	// to load is indistinguishable from one that was never added.
	for _, r := range rejected {
		display.StepWarn(fmt.Sprintf("not indexed: %s — %s", r.Path, r.Reason))
	}

	// Step 2: Derive the corpus profile.
	//
	// This must run before the "corpus up to date" early return below: an
	// existing agent has a full manifest and zero pending documents, so placing
	// it later would mean it never acquires a profile and serves on generic
	// defaults forever.
	display.Step(2, 7, "Deriving corpus profile...")

	profDocs := make([]profile.Doc, 0, len(docs))
	names := make([]string, 0, len(docs))
	sizes := make([]int64, 0, len(docs))
	for _, d := range docs {
		profDocs = append(profDocs, profile.Doc{Name: d.Name, Content: d.Content})
		names = append(names, d.Name)
		sizes = append(sizes, int64(len(d.Content)))
	}

	prof, profStatus, profErr := profile.LoadOrDerive(profilePath, profDocs, profile.Options{
		Refresh:     buildRefreshProfile,
		KashVersion: version,
	})
	if profErr != nil {
		display.StepWarn(fmt.Sprintf("could not read existing profile (regenerating): %v", profErr))
	}
	if profStatus != profile.StatusLoaded {
		prof.Corpus = profile.Fingerprint(names, sizes)
		if err := prof.Save(profilePath); err != nil {
			return fmt.Errorf("save corpus profile: %w", err)
		}
	}

	domainCfg, layers := agentconfig.ResolveDomainConfig(prof.Overlay(), "agent.yaml")

	refMatchers, refWarnings := chunker.CompileRefMatchersVerbose(domainCfg.Chunker.RefPatterns)
	for _, w := range refWarnings {
		display.StepWarn("chunker: " + w)
	}

	display.StepResult(string(profStatus), fmt.Sprintf("%s (%d field(s) measured)", profilePath, len(prof.Signals)))
	for _, sig := range prof.Signals {
		if sig.DecidedBy == profile.DecidedDetected {
			display.StepDetail(fmt.Sprintf("• %s = %s — %s", sig.Field, sig.Value, sig.Evidence))
		}
	}
	for _, l := range layers {
		if l.Layer == "agent.yaml" {
			display.StepDetail(fmt.Sprintf("• %s overridden in agent.yaml: %s", l.Field, l.Value))
		}
	}

	// Reference patterns and the fold mode are baked into chunk metadata at
	// build time. If they change under an existing corpus, previously indexed
	// documents keep their old metadata while the lexical index — rebuilt
	// wholesale below — gets the new one, and the two disagree about the same
	// chunk. That is a rebuild, not a warning.
	sig := profile.Signature(domainCfg)
	if len(m.Documents) > 0 && m.DomainSignature != "" && m.DomainSignature != sig {
		return fmt.Errorf("corpus was indexed with different structural rules " +
			"(reference patterns or diacritic folding changed) — run 'kash build --rebuild' to re-chunk consistently")
	}
	m.DomainSignature = sig

	if ps := profile.PredicateSignature(domainCfg); len(m.Documents) > 0 && m.PredicateSignature != "" && m.PredicateSignature != ps {
		display.StepWarn("extraction vocabulary changed — existing triples keep the old predicates; " +
			"run 'kash build --rebuild' to re-extract the whole corpus")
	} else if m.PredicateSignature == "" {
		m.PredicateSignature = profile.PredicateSignature(domainCfg)
	}

	// Step 3: Plan — decide per document whether to skip, add, replace, or resume
	display.Step(3, 7, "Planning incremental build...")
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
	display.Step(4, 7, "Building documents (embeddings + knowledge graph)...")

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
			if err := vs.DeleteRelationshipsBySource(ctx, name); err != nil {
				display.StepWarn(fmt.Sprintf("prune relationships for %s: %v", name, err))
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
	extractSpec := llm.ExtractionSpec{
		Predicates: domainCfg.Extraction.Predicates,
		Priorities: domainCfg.Extraction.Priorities,
	}
	batchSize := 10
	incomplete := []string{}

	for _, p := range pending {
		name := p.Doc.Name

		chunks, err := ck.SplitStructured(p.Doc.Content, name, refMatchers)
		if err != nil {
			return fmt.Errorf("chunk document %q: %w", name, err)
		}

		state := m.Documents[name]
		if p.Reason == reasonChanged {
			// Replace: clear the document's old vectors, relationships, and triples first
			if err := vs.DeleteBySource(ctx, name); err != nil {
				return fmt.Errorf("delete old vectors for %q: %w", name, err)
			}
			if err := vs.DeleteRelationshipsBySource(ctx, name); err != nil {
				return fmt.Errorf("delete old relationships for %q: %w", name, err)
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

		// The two phases are independent — embeddings call the embedder API,
		// triple extraction calls the LLM API — so they run concurrently and
		// each document takes ~max(embed, extract) time instead of their sum.
		// commit serializes manifest mutation + save between the goroutines.
		var manifestMu sync.Mutex
		commit := func(mutate func()) error {
			manifestMu.Lock()
			defer manifestMu.Unlock()
			mutate()
			if err := m.Save(manifestPath); err != nil {
				return fmt.Errorf("save manifest: %w", err)
			}
			return nil
		}

		var wg sync.WaitGroup
		var vecErr error

		// Phase 1: embeddings (background goroutine)
		if !state.VectorDone {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if p.Reason == reasonResume {
					// Clear any partially embedded chunks from the interrupted run
					if err := vs.DeleteBySource(ctx, name); err != nil {
						vecErr = fmt.Errorf("clear partial vectors for %q: %w", name, err)
						return
					}
				}
				if err := vs.AddChunks(ctx, chunks, parallelEmbed); err != nil {
					vecErr = fmt.Errorf("add chunks for %q to vector store: %w", name, err)
					return
				}
				if err := commit(func() { state.Chunks = len(chunks); state.VectorDone = true }); err != nil {
					vecErr = err
					return
				}
				display.StepDetail(fmt.Sprintf("%s: embedded %d chunk(s)", name, len(chunks)))
			}()
		} else {
			display.StepDetail(fmt.Sprintf("%s: embeddings already done (%d chunks)", name, state.Chunks))
		}

		// Phase 2: knowledge graph triples, resumable per batch (this goroutine)
		docComplete := true
		if !state.GraphDone {
			if state.GraphBatchesDone > 0 {
				display.StepDetail(fmt.Sprintf("%s: resuming triple extraction from batch %d", name, state.GraphBatchesDone+1))
			}

			for i := state.GraphBatchesDone * batchSize; i < len(chunks); i += batchSize {
				end := i + batchSize
				if end > len(chunks) {
					end = len(chunks)
				}
				batch := chunks[i:end]

				// Delimit passages explicitly. Concatenating raw chunks let the
				// extractor bind facts across unrelated excerpts — most damagingly
				// title-page credits (translator, editor) onto texts merely
				// mentioned further down. The prompt forbids crossing these markers.
				var combined strings.Builder
				for j, ch := range batch {
					fmt.Fprintf(&combined, "--- PASSAGE %d ---\n%s\n\n", j+1, ch.Content)
				}

				var triples []llm.Triple
				var extractErr error
				maxRetries := 2
				for attempt := 0; attempt <= maxRetries; attempt++ {
					triples, extractErr = llmClient.ExtractTriples(ctx, combined.String(), extractSpec)
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

				for ti := range triples {
					pIdx := triples[ti].Passage - 1
					if pIdx >= 0 && pIdx < len(batch) {
						triples[ti].ChunkID = batch[pIdx].ID
					} else {
						triples[ti].ChunkID = findBestChunk(batch, triples[ti].Subject, triples[ti].Object)
					}
				}

				if err := gdb.AddTriples(ctx, triples, name); err != nil {
					wg.Wait()
					return fmt.Errorf("add triples for %q: %w", name, err)
				}

				if len(triples) > 0 {
					relDocs := make([]vector.RelationshipDoc, len(triples))
					for ti, t := range triples {
						relDocs[ti] = vector.RelationshipDoc{
							Subject:     t.Subject,
							Predicate:   t.Predicate,
							Object:      t.Object,
							Description: t.Description,
							Source:      name,
							ChunkID:     t.ChunkID,
						}
					}
					if err := vs.AddRelationships(ctx, relDocs); err != nil {
						display.StepWarn(fmt.Sprintf("failed to embed relationships for %s chunks %d-%d (non-fatal): %v", name, i+1, end, err))
					}
				}

				if err := commit(func() { state.Triples += len(triples); state.GraphBatchesDone++ }); err != nil {
					wg.Wait()
					return err
				}
				display.StepDetail(fmt.Sprintf("%s: chunks %d-%d: +%d triples (doc total: %d)", name, i+1, end, len(triples), state.Triples))
			}

			if docComplete {
				if err := commit(func() { state.GraphDone = true }); err != nil {
					wg.Wait()
					return err
				}
			} else {
				incomplete = append(incomplete, name)
			}
		}

		// Join the embedding goroutine before moving to the next document
		wg.Wait()
		if vecErr != nil {
			return vecErr
		}
		if state.Done() && state.CompletedAt.IsZero() {
			if err := commit(func() { state.CompletedAt = time.Now().UTC() }); err != nil {
				return err
			}
		}

		corpusChanged = true
	}

	// Backfill relationship descriptions if graph has triples but relationship collection is empty
	if vs.RelationshipCount() == 0 && gdb.Count() > 0 {
		allTriples := gdb.AllTriples(ctx)
		if len(allTriples) > 0 {
			relDocs := make([]vector.RelationshipDoc, len(allTriples))
			for i, t := range allTriples {
				relDocs[i] = vector.RelationshipDoc{
					Subject:   t.Subject,
					Predicate: t.Predicate,
					Object:    t.Object,
					Source:    t.Source,
					ChunkID:   t.ChunkID,
				}
			}
			if err := vs.AddRelationships(ctx, relDocs); err != nil {
				display.StepWarn(fmt.Sprintf("failed to embed relationships (non-fatal): %v", err))
			}
		}
	}

	display.StepResult("Indexed", fmt.Sprintf("%d vectors, %d relationships, %d triples total", vs.Count(), vs.RelationshipCount(), gdb.Count()))

	// Build the lexical index over the whole corpus.
	//
	// It is rebuilt from every document rather than updated incrementally:
	// chunking is local and takes a few seconds for a corpus this size, whereas
	// a partial lexical index would silently answer keyword queries from only
	// the documents that happened to change in the last build.
	if corpusChanged || !fileExists(lexicalPath) {
		display.Step(5, 7, "Building lexical index...")
		lx := lexical.NewWithFold(domainCfg.Resolution.FoldDiacritics)
		for _, doc := range docs {
			chunks, err := ck.SplitStructured(doc.Content, doc.Name, refMatchers)
			if err != nil {
				return fmt.Errorf("chunk document %q for lexical index: %w", doc.Name, err)
			}
			for _, c := range chunks {
				lx.Add(c.ID, c.Content, chunkLexicalMeta(c))
			}
		}
		lx.Finalize()
		if err := lx.Save(lexicalPath); err != nil {
			return fmt.Errorf("save lexical index: %w", err)
		}
		display.StepResult("Indexed", fmt.Sprintf("%d chunks for keyword search", lx.Len()))
	}

	// Step 5: Generate and embed entity descriptions
	//
	// Dense embeddings over entity descriptions allow queries to find entities
	// conceptually even when exact keywords don't match, and seed knowledge-graph
	// traversal with relevant starting points.
	if corpusChanged || vs.EntityCount() == 0 {
		display.Step(6, 7, "Generating entity descriptions & embeddings...")
		minDegree := domainCfg.EntityDescription.MinDegree
		if minDegree <= 0 {
			minDegree = 2
		}
		maxEntities := domainCfg.EntityDescription.MaxEntities
		if maxEntities <= 0 {
			maxEntities = 500
		}

		// Also check if an alias file exists to resolve spelling variants
		aliasPath := filepath.Join("data", graph.AliasFileName)
		if fileExists(aliasPath) {
			if _, aliases, err := graph.LoadAliasFile(aliasPath); err == nil && aliases.Len() > 0 {
				gdb.SetAliases(aliases)
			}
		}

		entityFacts := gdb.EntityFacts(ctx, minDegree)
		if len(entityFacts) > maxEntities && maxEntities > 0 {
			entityFacts = entityFacts[:maxEntities]
		}

		if len(entityFacts) > 0 {
			// Clear stale entity descriptions before rebuilding them
			if err := vs.ClearEntityDescriptions(ctx); err != nil {
				display.StepWarn(fmt.Sprintf("failed to clear old entity descriptions: %v", err))
			}

			// Batch entities for LLM description generation (15 per call)
			const descBatchSize = 15
			var entityDescs []vector.EntityDesc

			for i := 0; i < len(entityFacts); i += descBatchSize {
				end := i + descBatchSize
				if end > len(entityFacts) {
					end = len(entityFacts)
				}
				batchFacts := entityFacts[i:end]

				toDescribe := make([]llm.EntityToDescribe, len(batchFacts))
				for j, ef := range batchFacts {
					toDescribe[j] = llm.EntityToDescribe{
						Name:    ef.Name,
						Aliases: ef.Aliases,
						Facts:   ef.Facts,
					}
				}

				llmResults, err := llmClient.DescribeEntities(ctx, toDescribe)
				descMap := map[string]string{}
				if err == nil {
					for _, r := range llmResults {
						descMap[r.Name] = r.Description
					}
				} else {
					display.StepWarn(fmt.Sprintf("LLM entity description generation failed for batch %d-%d (using fallback): %v", i+1, end, err))
				}

				for _, ef := range batchFacts {
					desc := descMap[ef.Name]
					if desc == "" {
						desc = llm.DeterministicDescription(ef.Name, ef.Facts)
					}
					entityDescs = append(entityDescs, vector.EntityDesc{
						Name:        ef.Name,
						Description: desc,
						Degree:      ef.Degree,
						Aliases:     ef.Aliases,
					})
				}
			}

			if err := vs.AddEntityDescriptions(ctx, entityDescs); err != nil {
				display.StepWarn(fmt.Sprintf("failed to embed entity descriptions: %v", err))
			} else {
				display.StepResult("Embedded", fmt.Sprintf("%d entity descriptions into vector store", len(entityDescs)))
			}
		} else {
			display.StepResult("Skipped", "no entities met degree threshold")
		}
	} else {
		display.StepDetail(fmt.Sprintf("entity descriptions already up to date (%d entities)", vs.EntityCount()))
	}

	// Step 6: Generate MCP tool description (only when the corpus changed)
	//
	// The sample must span the WHOLE corpus, not the documents this run happened
	// to process. Sampling the first pending document made an incremental build
	// that touched one book rewrite the description to be about only that book —
	// a 61-document Tantra corpus advertised itself as being about a single text,
	// which is the only description an MCP client ever sees.
	display.Step(7, 7, "Generating optimized MCP tool descriptions...")
	sampleContent := buildCorpusSample(docs, mcpSampleBudget)

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

	if sampleContent != "" {
		mcpDesc, err := llmClient.GenerateMCPDescription(ctx, agentName, sampleContent)
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
	display.KeyValue("Vector index", fmt.Sprintf("%s (%d documents, %d entities, %d relationships)", vectorPath, vs.Count(), vs.EntityCount(), vs.RelationshipCount()), display.BrightGreen)
	display.KeyValue("Graph store", fmt.Sprintf("%s (%d triples)", graphPath, gdb.Count()), display.BrightGreen)
	if fileExists(lexicalPath) {
		display.KeyValue("Lexical index", lexicalPath, display.BrightGreen)
	}
	display.KeyValue("Manifest", manifestPath, display.BrightGreen)

	display.NextSteps([]string{
		"docker compose up --build",
	})

	return nil
}

// fileExists reports whether a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// chunkLexicalMeta copies the metadata the lexical index needs for exact-match
// lookups: structural references plus the source.
func chunkLexicalMeta(c chunker.Chunk) map[string]string {
	meta := make(map[string]string, len(c.Metadata)+1)
	for k, v := range c.Metadata {
		if v != "" {
			meta[k] = v
		}
	}
	meta["source"] = c.Source
	return meta
}

// mcpSampleBudget caps the corpus excerpt handed to the description generator.
const mcpSampleBudget = 12000

// buildCorpusSample builds a description sample that spans every document in the
// corpus. Each document contributes its title and a short opening excerpt, with
// the per-document share derived from the total budget so a large corpus still
// yields a representative spread rather than a deep look at one book.
func buildCorpusSample(docs []reader.Document, budget int) string {
	if len(docs) == 0 || budget <= 0 {
		return ""
	}

	perDoc := budget / len(docs)
	if perDoc < 120 {
		perDoc = 120
	}

	var sb strings.Builder
	for _, doc := range docs {
		if sb.Len() >= budget {
			break
		}
		excerpt := documentExcerpt(doc.Content, perDoc)
		if excerpt == "" {
			continue
		}
		fmt.Fprintf(&sb, "--- %s ---\n%s\n\n", doc.Name, excerpt)
	}
	return sb.String()
}

// documentExcerpt returns up to n runes of meaningful text from the start of a
// document, skipping blank and separator-only lines so the excerpt is prose
// rather than front-matter rules.
func documentExcerpt(content string, n int) string {
	var sb strings.Builder
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Trim(line, "-=_*# ") == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(line)
		if utf8.RuneCountInString(sb.String()) >= n {
			break
		}
	}

	runes := []rune(sb.String())
	if len(runes) > n {
		runes = runes[:n]
	}
	return strings.TrimSpace(string(runes))
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

// findBestChunk finds the chunk in batch that best matches the subject and object of a triple.
func findBestChunk(batch []chunker.Chunk, subject, object string) string {
	if len(batch) == 0 {
		return ""
	}
	sLow := strings.ToLower(subject)
	oLow := strings.ToLower(object)
	for _, ch := range batch {
		cLow := strings.ToLower(ch.Content)
		if sLow != "" && oLow != "" && strings.Contains(cLow, sLow) && strings.Contains(cLow, oLow) {
			return ch.ID
		}
	}
	for _, ch := range batch {
		cLow := strings.ToLower(ch.Content)
		if (sLow != "" && strings.Contains(cLow, sLow)) || (oLow != "" && strings.Contains(cLow, oLow)) {
			return ch.ID
		}
	}

	// No evidence for any chunk in this batch. Returning batch[0] here would
	// invent provenance: the fact would print as "[passage 1]" and take the
	// chunk-level context boost, both on a passage that does not support it.
	// This path is common rather than rare — the extractor is asked for the
	// shortest unambiguous entity name, so it emits "Gorakhnath" where an IAST
	// source reads "gorakhanātha" and neither Contains pass can match. An empty
	// ID degrades correctly: the fact keeps its document citation and the
	// document-level boost.
	return ""
}
