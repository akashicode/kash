package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/profile"
)

var resolveCmd = &cobra.Command{
	Use:   "resolve-entities",
	Short: "Merge entity spelling variants in the knowledge graph",
	Long: `Finds entity surface forms that name the same thing — "Gorakhnath" and
"Gorakhnatha", "Ksemaraja" and "Kṣemarāja", "śrī Devī" and "Devī" — and writes
them to data/entity_aliases.json.

Merged entities share one node during graph traversal, so fact chains connect
across spelling variants instead of breaking at them.

Clusters differing only by case, honorific title or (where the corpus enables
it) stem vowel are approved automatically; those transformations cannot change
meaning. Clusters that required folding diacritics are approved only for proper
nouns, since in many languages diacritics distinguish real words (in Sanskrit,
brahma vs brahmā). The rest are written with "approved": false.

Pass --llm to have the model decide those remaining clusters, using each
entity's relations as context. It only touches undecided ones: approved merges,
clusters already adjudicated, and anything you have annotated in "note" are
left untouched, so hand edits always win. Verdicts are cached, so a later run
only adjudicates clusters it has not seen.

Rules come from agent.yaml (resolution.fold_diacritics, honorifics,
strip_final_vowel, proper_noun_predicates), so this works on any subject matter.

The file is plain JSON and safe to edit. Deleting it disables entity
resolution entirely — the agent runs normally without it.`,
	RunE: runResolve,
}

var (
	resolveDir       string
	resolveDryRun    bool
	resolveMinDegree int
	resolveShowAll   bool
	resolveUseLLM    bool
	resolveBatchSize int
)

// llmBatchSize is how many candidate clusters go into one adjudication call.
// Small batches keep the prompt focused and the JSON reliable.
const llmBatchSize = 15

func init() {
	resolveCmd.Flags().StringVarP(&resolveDir, "dir", "d", ".", "Path to the agent project directory")
	resolveCmd.Flags().BoolVar(&resolveDryRun, "dry-run", false, "Print the clusters without writing the file")
	resolveCmd.Flags().IntVar(&resolveMinDegree, "min-degree", 2, "Ignore entities appearing in fewer triples than this")
	resolveCmd.Flags().BoolVar(&resolveShowAll, "show-all", false, "List every cluster instead of a sample")
	resolveCmd.Flags().BoolVar(&resolveUseLLM, "llm", false, "Ask the LLM to decide the clusters deterministic rules could not settle")
	resolveCmd.Flags().IntVar(&resolveBatchSize, "llm-batch", llmBatchSize, "Clusters per LLM adjudication call")
	rootCmd.AddCommand(resolveCmd)
}

func runResolve(_ *cobra.Command, _ []string) error {
	if resolveDir != "." {
		abs, err := filepath.Abs(resolveDir)
		if err != nil {
			return fmt.Errorf("resolve directory %q: %w", resolveDir, err)
		}
		if err := os.Chdir(abs); err != nil {
			return fmt.Errorf("change to directory %q: %w", abs, err)
		}
	}

	graphPath := filepath.Join("data", "knowledge.cayley")
	if _, err := os.Stat(graphPath); os.IsNotExist(err) {
		return errors.New("no knowledge graph found at data/knowledge.cayley — run 'kash build' first")
	}
	aliasPath := filepath.Join("data", graph.AliasFileName)

	display.Header("⚡ Kash Entity Resolution")
	fmt.Println()

	gdb, err := graph.NewDBFromPath(graphPath)
	if err != nil {
		return fmt.Errorf("open graph: %w", err)
	}
	defer gdb.Close()

	ctx := context.Background()

	display.Step(1, 3, "Reading knowledge graph...")
	triples, err := gdb.Sample(ctx, 1_000_000)
	if err != nil {
		return fmt.Errorf("read graph: %w", err)
	}
	display.StepResult("Loaded", fmt.Sprintf("%d triples", len(triples)))

	display.Step(2, 3, "Grouping entity spelling variants...")
	// Entity resolution must cluster with the same rules the corpus was built
	// with, so it reads the same three layers the build does. Without the
	// profile it would use different honorifics and a different fold mode, and
	// silently under-merge.
	prof, profErr := profile.Load(filepath.Join("data", profile.FileName))
	if profErr != nil {
		display.StepWarn(fmt.Sprintf("ignoring corpus profile: %v", profErr))
	}
	domainCfg, _ := agentconfig.ResolveDomainConfig(prof.Overlay(), "agent.yaml")
	opts := graph.ResolveOptions{
		MinDegree:            resolveMinDegree,
		Honorifics:           domainCfg.Resolution.Honorifics,
		FoldDiacritics:       string(domainCfg.Resolution.FoldDiacritics),
		StripFinalVowel:      domainCfg.Resolution.StripFinalVowel,
		ProperNounPredicates: domainCfg.Resolution.ProperNounPredicates,
	}
	display.StepDetail(fmt.Sprintf("diacritics: %s · stem-vowel folding: %v · %d honorifics",
		opts.FoldDiacritics, opts.StripFinalVowel, len(opts.Honorifics)))
	fresh := graph.BuildClusters(triples, opts)

	existingFile, _, err := graph.LoadAliasFile(aliasPath)
	if err != nil {
		return err
	}
	merged := graph.MergeClusters(existingFile.Clusters, fresh)

	if resolveUseLLM {
		if err := adjudicateWithLLM(ctx, merged, triples); err != nil {
			return err
		}
	}

	approved, review, aliasCount := 0, 0, 0
	for _, c := range merged {
		if c.Approved {
			approved++
			aliasCount += len(c.Aliases)
		} else {
			review++
		}
	}
	display.StepResult("Clusters", fmt.Sprintf("%d total — %d approved (%d variants merged), %d held for review",
		len(merged), approved, aliasCount, review))

	// Show what will be applied
	if len(merged) > 0 {
		fmt.Println()
		display.Info("Approved merges:")
		shown := 0
		for _, c := range merged {
			if !c.Approved {
				continue
			}
			if !resolveShowAll && shown >= 12 {
				display.StepDetail(fmt.Sprintf("... and %d more (use --show-all)", approved-shown))
				break
			}
			display.StepDetail(fmt.Sprintf("%s  ←  %v", c.Canonical, c.Aliases))
			shown++
		}

		if review > 0 {
			fmt.Println()
			display.Warn(fmt.Sprintf("%d cluster(s) need review — NOT applied until you set \"approved\": true", review))
			shown = 0
			for _, c := range merged {
				if c.Approved {
					continue
				}
				if !resolveShowAll && shown >= 12 {
					display.StepDetail(fmt.Sprintf("... and %d more (use --show-all)", review-shown))
					break
				}
				display.StepDetail(fmt.Sprintf("%s  ?  %v", c.Canonical, c.Aliases))
				shown++
			}
		}
	}

	display.Step(3, 3, "Writing alias file...")
	if resolveDryRun {
		display.StepResult("Dry run", "nothing written")
		fmt.Println()
		display.Info("Re-run without --dry-run to write " + aliasPath)
		return nil
	}

	existingFile.Clusters = merged
	if err := existingFile.Save(aliasPath); err != nil {
		return err
	}
	display.StepResult("Wrote", aliasPath)

	fmt.Println()
	display.Success(fmt.Sprintf("Entity resolution ready — %d variants merged across %d entities", aliasCount, approved))
	fmt.Println()
	display.KeyValue("Alias file", aliasPath, display.BrightGreen)
	display.NextSteps([]string{
		"Review " + aliasPath + " (edit canonical / approved by hand as needed)",
		"kash serve   — traversal now connects merged variants",
		"Delete the file at any time to disable entity resolution",
	})
	return nil
}

// adjudicateWithLLM asks the model to settle clusters the deterministic rules
// could not. It mutates clusters in place, and only ever touches undecided
// ones: approved merges, previously adjudicated clusters and anything a human
// annotated are left exactly as they are.
func adjudicateWithLLM(ctx context.Context, clusters []graph.Cluster, triples []graph.SearchResult) error {
	cfg, err := agentconfig.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := agentconfig.ValidateLLM(cfg); err != nil {
		return err
	}
	if err := agentconfig.ApplyAgentYAMLReasoningEffort(cfg, "agent.yaml"); err != nil {
		return err
	}
	client, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		return fmt.Errorf("create LLM client: %w", err)
	}

	// Index a few sample relations per surface form — this context is what
	// lets the model separate homonyms from genuine spelling variants.
	sampleCtx := map[string][]string{}
	for _, t := range triples {
		for _, e := range []string{t.Subject, t.Object} {
			k := strings.ToLower(strings.TrimSpace(e))
			if len(sampleCtx[k]) >= 3 {
				continue
			}
			sampleCtx[k] = append(sampleCtx[k], fmt.Sprintf("%s %s %s", t.Subject, t.Predicate, t.Object))
		}
	}

	// Collect the undecided clusters, remembering where each one lives
	idxByKey := map[string]int{}
	var pending []llm.EntityGroup
	for i := range clusters {
		if clusters[i].Settled() {
			continue
		}
		idxByKey[clusters[i].Key] = i

		var lines []string
		for _, form := range append([]string{clusters[i].Canonical}, clusters[i].Aliases...) {
			for _, s := range sampleCtx[strings.ToLower(form)] {
				lines = append(lines, s)
			}
		}
		if len(lines) > 6 {
			lines = lines[:6]
		}
		pending = append(pending, llm.EntityGroup{
			Key:       clusters[i].Key,
			Canonical: clusters[i].Canonical,
			Aliases:   clusters[i].Aliases,
			Context:   lines,
		})
	}

	if len(pending) == 0 {
		display.StepResult("LLM review", "nothing undecided — all clusters already settled")
		return nil
	}

	batch := resolveBatchSize
	if batch <= 0 {
		batch = llmBatchSize
	}
	calls := (len(pending) + batch - 1) / batch
	display.StepDetail(fmt.Sprintf("adjudicating %d undecided cluster(s) in %d call(s) using %s",
		len(pending), calls, cfg.LLM.Model))

	sameCount, diffCount, failed := 0, 0, 0
	for i := 0; i < len(pending); i += batch {
		end := min(i+batch, len(pending))

		verdicts, err := client.AdjudicateEntities(ctx, pending[i:end])
		if err != nil {
			// A failed batch must not lose the whole run; those clusters stay
			// undecided and are retried on the next --llm invocation.
			display.StepWarn(fmt.Sprintf("batch %d-%d failed (left undecided): %v", i+1, end, err))
			failed += end - i
			continue
		}

		for _, v := range verdicts {
			idx, ok := idxByKey[v.Key]
			if !ok || clusters[idx].Settled() {
				continue
			}
			clusters[idx].Approved = v.SameEntity
			clusters[idx].DecidedBy = "llm"
			clusters[idx].Reason = "LLM: " + strings.TrimSpace(v.Reason)
			if v.SameEntity {
				sameCount++
			} else {
				diffCount++
			}
		}
		display.StepDetail(fmt.Sprintf("  batch %d/%d done (%d merged, %d kept separate so far)",
			(i/batch)+1, calls, sameCount, diffCount))
	}

	msg := fmt.Sprintf("%d merged, %d kept separate", sameCount, diffCount)
	if failed > 0 {
		msg += fmt.Sprintf(", %d left undecided (re-run --llm to retry)", failed)
	}
	display.StepResult("LLM review", msg)
	return nil
}
