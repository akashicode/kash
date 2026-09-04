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
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/profile"
	"github.com/akashicode/kash/internal/reader"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Inspect or re-derive the corpus profile",
	Long: `Shows the domain configuration Kash derived from your documents.

Extraction vocabulary, entity-resolution rules and structural reference
patterns all depend on what a corpus actually contains, so 'kash build'
measures them and writes data/domain.profile.json. This command shows what was
measured and why, and can re-derive it.

The profile is the middle of three configuration layers:

    built-in defaults  <  data/domain.profile.json  <  agent.yaml

agent.yaml always wins, so anything here can be overridden by hand. Nothing
needs to be: the profile exists so a corpus works well without one.`,
	RunE: runProfile,
}

var (
	profileDir     string
	profileDryRun  bool
	profileRefresh bool
	profileNoLLM   bool
)

func init() {
	profileCmd.Flags().StringVarP(&profileDir, "dir", "d", ".", "Path to the agent project directory")
	profileCmd.Flags().BoolVar(&profileDryRun, "dry-run", false, "Print the derived profile without writing it")
	profileCmd.Flags().BoolVar(&profileRefresh, "refresh", false, "Re-derive over an existing profile")
	profileCmd.Flags().BoolVar(&profileNoLLM, "no-llm", false, "Measure only; skip the model-derived extraction vocabulary")
	rootCmd.AddCommand(profileCmd)
}

func runProfile(_ *cobra.Command, _ []string) error {
	if profileDir != "." {
		abs, err := filepath.Abs(profileDir)
		if err != nil {
			return fmt.Errorf("resolve directory %q: %w", profileDir, err)
		}
		if err := os.Chdir(abs); err != nil {
			return fmt.Errorf("change to directory %q: %w", abs, err)
		}
	}
	if _, err := os.Stat("data"); os.IsNotExist(err) {
		return errors.New("data/ directory not found — run 'kash init <name>' first")
	}

	path := filepath.Join("data", profile.FileName)

	display.Header("🧭 Corpus Profile")
	fmt.Println()

	docs, rejected, err := reader.LoadDirectory("data")
	if err != nil {
		return fmt.Errorf("load documents: %w", err)
	}
	if len(docs) == 0 {
		return errors.New("no supported documents found in data/")
	}
	for _, r := range rejected {
		display.StepWarn(fmt.Sprintf("not indexed: %s — %s", r.Path, r.Reason))
	}

	profDocs := make([]profile.Doc, 0, len(docs))
	names := make([]string, 0, len(docs))
	sizes := make([]int64, 0, len(docs))
	for _, d := range docs {
		profDocs = append(profDocs, profile.Doc{Name: d.Name, Content: d.Content})
		names = append(names, d.Name)
		sizes = append(sizes, int64(len(d.Content)))
	}

	// A dry run always re-derives: showing a cached file would answer a
	// different question than the one being asked.
	prof, status, loadErr := profile.LoadOrDerive(path, profDocs, profile.Options{
		Refresh:     profileRefresh || profileDryRun,
		KashVersion: version,
	})
	if loadErr != nil {
		display.StepWarn(fmt.Sprintf("could not read existing profile: %v", loadErr))
	}
	if !profileNoLLM && (status != profile.StatusLoaded || !prof.Complete) {
		cfg, cfgErr := agentconfig.Load()
		if cfgErr == nil {
			if client, clientErr := llm.NewClient(&cfg.LLM); clientErr == nil {
				profile.Enrich(context.Background(), prof, profDocs, client)
			} else {
				prof.LLMStatus = "skipped — no model configured"
			}
		}
	}
	prof.Corpus = profile.Fingerprint(names, sizes)

	display.KeyValue("Documents", len(docs), display.Bold+display.BrightYellow)
	display.KeyValue("Status", string(status), display.BrightMagenta)
	display.KeyValue("Profile", path, display.Dim+display.White)
	fmt.Println()

	display.Info("Measured from your corpus:")
	for _, s := range prof.Signals {
		marker := "•"
		if s.DecidedBy == profile.DecidedDefault {
			marker = "-"
		}
		display.StepDetail(fmt.Sprintf("%s %s = %s", marker, s.Field, s.Value))
		display.StepDetail(fmt.Sprintf("    %s", s.Evidence))
	}
	fmt.Println()

	// Show what agent.yaml overrides, since that is the layer a reader
	// controls and the one that explains a surprising value.
	_, layers := agentconfig.ResolveDomainConfig(prof.Overlay(), "agent.yaml")
	var overrides int
	for _, l := range layers {
		if l.Layer == "agent.yaml" {
			if overrides == 0 {
				display.Info("Overridden in agent.yaml:")
			}
			overrides++
			display.StepDetail(fmt.Sprintf("• %s = %s", l.Field, l.Value))
		}
	}
	if overrides > 0 {
		fmt.Println()
	}

	if profileDryRun {
		display.Success("Dry run — nothing written")
		return nil
	}
	if status == profile.StatusLoaded {
		display.Success("Existing profile is unchanged (use --refresh to re-derive)")
		return nil
	}
	if err := prof.Save(path); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}
	display.Success(fmt.Sprintf("Profile written to %s", path))
	return nil
}
