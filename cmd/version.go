package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// These are set at build time via -ldflags.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of kash",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("kash %s\n", version)
		fmt.Printf("  commit:     %s\n", commit)
		fmt.Printf("  built:      %s\n", buildDate)
		fmt.Printf("  go version: %s\n", runtime.Version())
		fmt.Printf("  os/arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
