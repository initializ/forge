// Package cmd implements the forge CLI commands.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	cfgFile       string
	verbose       bool
	outputDir     string
	themeOverride string

	appVersion = "dev"
	appCommit  = "none"
)

var rootCmd = &cobra.Command{
	Use:   "forge",
	Short: "Forge — scaffold, build, and deploy AI agents",
	Long:  "Forge is a CLI tool for initializing, building, validating, and deploying AI agent projects.",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "forge.yaml", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
	rootCmd.PersistentFlags().StringVarP(&outputDir, "output-dir", "o", ".", "output directory")
	rootCmd.PersistentFlags().StringVar(&themeOverride, "theme", "", "TUI color theme: dark, light, or auto")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(tryCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(toolCmd)
	rootCmd.AddCommand(compressionCmd)
	rootCmd.AddCommand(packageCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(channelCmd)
	rootCmd.AddCommand(skillsCmd)
	rootCmd.AddCommand(secretCmd)
	rootCmd.AddCommand(keyCmd)
	rootCmd.AddCommand(scheduleCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(uiCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(guardrailsCmd)
}

// SetVersionInfo sets the version and commit for display.
func SetVersionInfo(version, commit string) {
	appVersion = version
	appCommit = commit
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(fmt.Sprintf("forge %s (commit: %s)\n", version, commit))
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
