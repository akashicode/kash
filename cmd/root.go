package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "agentforge",
	Short: "The Static Site Generator for AI Minds",
	Long: `Agent-Forge compiles raw documents into embedded, pure-Go GraphRAG databases,
packaged into ultra-lightweight (~50MB) Docker containers.

Compile your knowledge into a microchip.`,
}

// Execute is the entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.agentforge/config.yaml)")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(buildCmd)
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not determine home directory:", err)
			return
		}
		viper.AddConfigPath(filepath.Join(home, ".agentforge"))
		viper.SetConfigType("yaml")
		viper.SetConfigName("config")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		// Silence the warning — config.yaml is optional when env vars are set
	}
}
