package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/profile"
	"github.com/akashicode/kash/internal/vector"
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Audit the provenance chain from graph facts back to source passages",
	Long: `Checks that the knowledge graph is traceable to the text it came from.

Every triple records the chunk it was extracted from. This walks the graph,
fetches each fact's chunk from the vector store, and reports whether that
passage actually mentions the fact's subject and object.

It answers a question a knowledge graph cannot answer about itself: how much of
it can be shown to a reader, and how much is only asserted. A fact with no
chunk, or one whose chunk no longer exists, still cites its document — it just
cannot be traced to a passage.

The check is lexical, so it is a floor rather than a verdict. It cannot confirm
that a passage asserts the relation, only that the passage mentions both things
being related. Unsupported means the wording diverged too far to match, which
for a transliterated corpus is often entity resolution's job rather than an
error.`,
	RunE: runVerify,
}

var (
	verifyDir    string
	verifyShow   int
	verifySample int
)

func init() {
	verifyCmd.Flags().StringVarP(&verifyDir, "dir", "d", ".", "Path to the agent project directory")
	verifyCmd.Flags().IntVar(&verifyShow, "show", 5, "Examples to print per finding")
	verifyCmd.Flags().IntVar(&verifySample, "sample", 0, "Check at most this many facts (0 = all)")
	rootCmd.AddCommand(verifyCmd)
}

// verifyReport counts how a corpus's facts trace back to their passages.
type verifyReport struct {
	total       int
	noChunk     int // extraction could not identify a passage
	chunkGone   int // the chunk id no longer resolves in the vector store
	supported   int // the passage mentions both endpoints
	partial     int // the passage mentions one endpoint
	unsupported int // the passage mentions neither

	unsupportedEx []string
	goneEx        []string
	noChunkEx     []string
}

func runVerify(_ *cobra.Command, _ []string) error {
	if verifyDir != "." {
		abs, err := filepath.Abs(verifyDir)
		if err != nil {
			return fmt.Errorf("resolve directory %q: %w", verifyDir, err)
		}
		if err := os.Chdir(abs); err != nil {
			return fmt.Errorf("change to directory %q: %w", abs, err)
		}
	}

	graphPath := filepath.Join("data", "knowledge.cayley")
	storePath := filepath.Join("data", "memory.chromem")
	if _, err := os.Stat(graphPath); os.IsNotExist(err) {
		return errors.New("no knowledge graph found — run 'kash build' first")
	}

	display.Header("🔍 Provenance Audit")
	fmt.Println()

	cfg, err := agentconfig.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	agentconfig.ApplyAgentYAMLDimensions(cfg, "agent.yaml")

	gdb, err := graph.NewDBFromPath(graphPath)
	if err != nil {
		return fmt.Errorf("open graph: %w", err)
	}
	defer gdb.Close()

	vs, err := vector.NewStoreFromPath(storePath, &cfg.Embedder)
	if err != nil {
		return fmt.Errorf("open vector store: %w", err)
	}

	// The audit has to fold the way the corpus was built, or it would report
	// correct provenance as unsupported across an entire transliterated corpus.
	prof, profErr := profile.Load(filepath.Join("data", profile.FileName))
	if profErr != nil {
		display.StepWarn(fmt.Sprintf("ignoring corpus profile: %v", profErr))
	}
	domainCfg, _ := agentconfig.ResolveDomainConfig(prof.Overlay(), "agent.yaml")
	ev := graph.NewEvidenceChecker(domainCfg.Resolution.FoldDiacritics, domainCfg.Resolution.StripFinalVowel)

	ctx := context.Background()
	facts := gdb.AllTriples(ctx)
	if len(facts) == 0 {
		return errors.New("the knowledge graph is empty")
	}
	if verifySample > 0 && verifySample < len(facts) {
		facts = facts[:verifySample]
	}

	display.KeyValue("Facts", len(facts), display.Bold+display.BrightYellow)
	display.KeyValue("Fold mode", string(domainCfg.Resolution.FoldDiacritics), display.BrightMagenta)
	fmt.Println()

	rep := auditFacts(ctx, ev, vs, facts)
	printVerifyReport(rep)
	return nil
}

// auditFacts walks every fact and classifies its provenance. Chunk lookups are
// cached because a well-populated passage backs many facts.
func auditFacts(ctx context.Context, ev *graph.EvidenceChecker, vs *vector.Store, facts []graph.SearchResult) verifyReport {
	rep := verifyReport{total: len(facts)}
	cache := map[string]string{}
	missing := map[string]bool{}

	for i, f := range facts {
		if i%500 == 0 {
			display.Progress(fmt.Sprintf("checking fact %d/%d", i+1, len(facts)))
		}
		if f.ChunkID == "" {
			rep.noChunk++
			if len(rep.noChunkEx) < verifyShow {
				rep.noChunkEx = append(rep.noChunkEx, factLine(f))
			}
			continue
		}

		text, ok := cache[f.ChunkID]
		if !ok && !missing[f.ChunkID] {
			r, err := vs.GetByID(ctx, f.ChunkID)
			if err != nil {
				missing[f.ChunkID] = true
			} else {
				text = r.Content
				cache[f.ChunkID] = text
			}
		}
		if missing[f.ChunkID] {
			rep.chunkGone++
			if len(rep.goneEx) < verifyShow {
				rep.goneEx = append(rep.goneEx, fmt.Sprintf("%s → %s", factLine(f), f.ChunkID))
			}
			continue
		}

		switch ev.Check(text, f.Subject, f.Object) {
		case graph.EvidenceBoth:
			rep.supported++
		case graph.EvidencePartial:
			rep.partial++
		default:
			rep.unsupported++
			if len(rep.unsupportedEx) < verifyShow {
				rep.unsupportedEx = append(rep.unsupportedEx, fmt.Sprintf("%s → %s", factLine(f), f.ChunkID))
			}
		}
	}
	display.ProgressDone()
	return rep
}

func factLine(f graph.SearchResult) string {
	return strings.TrimSpace(fmt.Sprintf("%s %s %s", f.Subject, f.Predicate, f.Object))
}

func printVerifyReport(rep verifyReport) {
	pct := func(n int) string {
		if rep.total == 0 {
			return "0.0%"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(rep.total))
	}

	traceable := rep.supported + rep.partial
	display.Info("Traceable to a passage:")
	display.StepDetail(fmt.Sprintf("both endpoints found     %6d  %s", rep.supported, pct(rep.supported)))
	display.StepDetail(fmt.Sprintf("one endpoint found       %6d  %s", rep.partial, pct(rep.partial)))
	fmt.Println()

	display.Info("Not traceable:")
	display.StepDetail(fmt.Sprintf("passage found neither    %6d  %s", rep.unsupported, pct(rep.unsupported)))
	display.StepDetail(fmt.Sprintf("chunk no longer exists   %6d  %s", rep.chunkGone, pct(rep.chunkGone)))
	display.StepDetail(fmt.Sprintf("no chunk recorded        %6d  %s", rep.noChunk, pct(rep.noChunk)))
	fmt.Println()

	printExamples("Facts whose passage mentions neither endpoint", rep.unsupportedEx)
	printExamples("Facts whose chunk is missing from the vector store", rep.goneEx)
	printExamples("Facts extraction could not place in a passage", rep.noChunkEx)

	// A dangling chunk ID is the one finding that is unambiguously wrong: the
	// graph and the vector store disagree about what exists, which means they
	// were built from different runs.
	if rep.chunkGone > 0 {
		display.Warn(fmt.Sprintf("%s of facts cite a chunk the vector store does not have — "+
			"the graph and the vector store are out of step; rebuild with 'kash build --rebuild'", pct(rep.chunkGone)))
		return
	}
	display.Success(fmt.Sprintf("%s of facts can be shown to a reader in the passage they came from", pct(traceable)))
}

func printExamples(title string, ex []string) {
	if len(ex) == 0 {
		return
	}
	display.Info(title + ":")
	sort.Strings(ex)
	for _, e := range ex {
		display.StepDetail("• " + e)
	}
	fmt.Println()
}
