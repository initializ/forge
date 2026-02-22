package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-cli/config"
	cliskills "github.com/initializ/forge/forge-cli/skills"
	coreskills "github.com/initializ/forge/forge-core/skills"
	"github.com/spf13/cobra"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Manage and inspect agent skills",
}

var skillsValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate skills file and check requirements",
	RunE:  runSkillsValidate,
}

func init() {
	skillsCmd.AddCommand(skillsValidateCmd)
}

func runSkillsValidate(cmd *cobra.Command, args []string) error {
	// Determine skills file path
	skillsPath := "skills.md"

	cfgPath := cfgFile
	if !filepath.IsAbs(cfgPath) {
		wd, _ := os.Getwd()
		cfgPath = filepath.Join(wd, cfgPath)
	}
	cfg, err := config.LoadForgeConfig(cfgPath)
	if err == nil && cfg.Skills.Path != "" {
		skillsPath = cfg.Skills.Path
	}

	if !filepath.IsAbs(skillsPath) {
		wd, _ := os.Getwd()
		skillsPath = filepath.Join(wd, skillsPath)
	}

	// Parse with metadata
	entries, _, err := cliskills.ParseFileWithMetadata(skillsPath)
	if err != nil {
		return fmt.Errorf("parsing skills file: %w", err)
	}

	fmt.Printf("Skills file: %s\n", skillsPath)
	fmt.Printf("Entries:     %d\n\n", len(entries))

	// Aggregate requirements
	reqs := coreskills.AggregateRequirements(entries)

	hasErrors := false

	// Check binaries
	if len(reqs.Bins) > 0 {
		fmt.Println("Binaries:")
		binDiags := coreskills.BinDiagnostics(reqs.Bins)
		diagMap := make(map[string]string)
		for _, d := range binDiags {
			diagMap[d.Var] = d.Level
		}
		for _, bin := range reqs.Bins {
			if _, missing := diagMap[bin]; missing {
				fmt.Printf("  %-20s MISSING\n", bin)
			} else {
				fmt.Printf("  %-20s ok\n", bin)
			}
		}
		fmt.Println()
	}

	// Build env resolver from OS env + .env file
	osEnv := envFromOS()
	dotEnv := map[string]string{}
	envFilePath := filepath.Join(filepath.Dir(skillsPath), ".env")
	if f, fErr := os.Open(envFilePath); fErr == nil {
		// Simple line-based .env parsing
		defer func() { _ = f.Close() }()
		// Use the runtime's LoadEnvFile indirectly — just check OS env for now
	}

	resolver := coreskills.NewEnvResolver(osEnv, dotEnv, nil)
	envDiags := resolver.Resolve(reqs)

	if len(reqs.EnvRequired) > 0 || len(reqs.EnvOneOf) > 0 || len(reqs.EnvOptional) > 0 {
		fmt.Println("Environment:")
		for _, d := range envDiags {
			prefix := "  "
			switch d.Level {
			case "error":
				prefix = "  ERROR"
				hasErrors = true
			case "warning":
				prefix = "  WARN "
			}
			fmt.Printf("%s %s\n", prefix, d.Message)
		}
		if len(envDiags) == 0 {
			fmt.Println("  All environment requirements satisfied.")
		}
		fmt.Println()
	}

	// Summary
	if !hasErrors {
		fmt.Println("Validation passed.")
		return nil
	}

	return fmt.Errorf("validation failed: missing required environment variables")
}

func envFromOS() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if ok {
			env[k] = v
		}
	}
	return env
}
