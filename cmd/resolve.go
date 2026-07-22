package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/display"
	"github.com/akashicode/kash/internal/graph"
)

var resolveCmd = &cobra.Command{
	Use:   "resolve-entities",
	Short: "Merge entity spelling variants in the knowledge graph",
	Long: `Finds entity surface forms that name the same thing — "Gorakhnath" and
"Gorakhnatha", "Ksemaraja" and "Kṣemarāja", "śrī Devī" and "Devī" — and writes
them to data/entity_aliases.json.

Merged entities share one node during graph traversal, so fact chains connect
across spelling variants instead of breaking at them.

Clusters differing only by case, honorific title or Sanskrit stem vowel are
approved automatically; those transformations cannot change meaning. Clusters
that required folding diacritics are approved only for proper nouns, since
diacritics distinguish real Sanskrit words (brahma vs brahmā). The rest are
written with "approved": false for you to review by hand.

The file is plain JSON and safe to edit. Deleting it disables entity
resolution entirely — the agent runs normally without it.`,
	RunE: runResolve,
}

var (
	resolveDir       string
	resolveDryRun    bool
	resolveMinDegree int
	resolveShowAll   bool
)

func init() {
	resolveCmd.Flags().StringVarP(&resolveDir, "dir", "d", ".", "Path to the agent project directory")
	resolveCmd.Flags().BoolVar(&resolveDryRun, "dry-run", false, "Print the clusters without writing the file")
	resolveCmd.Flags().IntVar(&resolveMinDegree, "min-degree", 2, "Ignore entities appearing in fewer triples than this")
	resolveCmd.Flags().BoolVar(&resolveShowAll, "show-all", false, "List every cluster instead of a sample")
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
	domainCfg := agentconfig.LoadDomainConfig("agent.yaml")
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
