package cmd

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"
)

// These are set at build time via -ldflags. A plain `go build` leaves them at
// these defaults, and init fills what it can from the VCS stamp instead.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// commitTime is when the commit the binary was built from was made, if the Go
// toolchain recorded it.
var commitTime string

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of kash",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("kash %s\n", version)
		fmt.Printf("  commit:     %s\n", commit)
		if commitTime != "" {
			fmt.Printf("  committed:  %s\n", commitTime)
		}
		fmt.Printf("  built:      %s\n", builtAt())
		fmt.Printf("  go version: %s\n", runtime.Version())
		fmt.Printf("  os/arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	if info, ok := debug.ReadBuildInfo(); ok {
		version, commit, commitTime = stampFromVCS(version, commit, info.Settings)
	}
}

// stampFromVCS fills the version and commit that a plain `go build` leaves
// unset.
//
// Only `make build` and the release workflow pass -ldflags, so every other
// build reported "kash dev / commit none" — and wrote "dev" into each manifest
// and profile it produced, which traces back to no source at all. The Go
// toolchain records the git revision in every binary built inside a checkout,
// so that is used instead. Values set through -ldflags always win.
//
// The release tag cannot be recovered this way: Go derives the module version
// only from tags matching the module path, and v2+ tags would need a /v2
// suffix this module does not have. The version becomes dev-<commit> instead.
func stampFromVCS(version, commit string, settings []debug.BuildSetting) (string, string, string) {
	var revision, when string
	modified := false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return version, commit, when
	}

	short := revision
	if len(short) > 7 {
		short = short[:7]
	}
	if commit == "none" {
		commit = short
	}
	if version == "dev" {
		version = "dev-" + short
		if modified {
			version += "-dirty"
		}
	}
	return version, commit, when
}

// builtAt returns the build date set through -ldflags or, failing that, the
// time the binary file was written — which for a local `go build` is when it
// was built.
func builtAt() string {
	if buildDate != "unknown" {
		return buildDate
	}
	exe, err := os.Executable()
	if err != nil {
		return buildDate
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return buildDate
	}
	return fi.ModTime().UTC().Format(time.RFC3339) + " (binary file time)"
}
