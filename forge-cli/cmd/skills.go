package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-cli/config"
	cliskills "github.com/initializ/forge/forge-cli/skills"
	coreskills "github.com/initializ/forge/forge-core/skills"
	skillreg "github.com/initializ/forge/forge-core/registry"
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

var skillsAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a registry skill to the current project",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillsAdd,
}

func init() {
	skillsCmd.AddCommand(skillsValidateCmd)
	skillsCmd.AddCommand(skillsAddCmd)
}

func runSkillsAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Look up skill in registry
	info := skillreg.GetSkillByName(name)
	if info == nil {
		return fmt.Errorf("skill %q not found in registry", name)
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Write skill markdown
	skillDir := filepath.Join(wd, "skills")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("creating skills directory: %w", err)
	}

	content, err := skillreg.LoadSkillFile(name)
	if err != nil {
		return fmt.Errorf("loading skill file: %w", err)
	}

	skillPath := filepath.Join(skillDir, name+".md")
	if err := os.WriteFile(skillPath, content, 0o644); err != nil {
		return fmt.Errorf("writing skill file: %w", err)
	}
	fmt.Printf("  Added skill file: skills/%s.md\n", name)

	// Write script if the skill has one
	if skillreg.HasSkillScript(name) {
		scriptContent, sErr := skillreg.LoadSkillScript(name)
		if sErr == nil {
			scriptDir := filepath.Join(skillDir, "scripts")
			if mkErr := os.MkdirAll(scriptDir, 0o755); mkErr != nil {
				fmt.Printf("  Warning: could not create scripts directory: %s\n", mkErr)
			} else {
				scriptPath := filepath.Join(scriptDir, name+".sh")
				if wErr := os.WriteFile(scriptPath, scriptContent, 0o755); wErr != nil {
					fmt.Printf("  Warning: could not write script: %s\n", wErr)
				} else {
					fmt.Printf("  Added script:     skills/scripts/%s.sh\n", name)
				}
			}
		}
	}

	// Check binary requirements
	if len(info.RequiredBins) > 0 {
		fmt.Println("\n  Binary requirements:")
		for _, bin := range info.RequiredBins {
			if _, lookErr := exec.LookPath(bin); lookErr != nil {
				fmt.Printf("    %s — MISSING (not found in PATH)\n", bin)
			} else {
				fmt.Printf("    %s — ok\n", bin)
			}
		}
	}

	// Check env var requirements
	missingEnvs := []string{}
	if len(info.RequiredEnv) > 0 {
		fmt.Println("\n  Environment requirements:")
		for _, env := range info.RequiredEnv {
			if os.Getenv(env) == "" {
				fmt.Printf("    %s — NOT SET\n", env)
				missingEnvs = append(missingEnvs, env)
			} else {
				fmt.Printf("    %s — ok\n", env)
			}
		}
	}

	// Prompt for missing env vars
	if len(missingEnvs) > 0 {
		reader := bufio.NewReader(os.Stdin)
		for _, env := range missingEnvs {
			fmt.Printf("\n  Enter value for %s (or press Enter to skip): ", env)
			val, _ := reader.ReadString('\n')
			val = strings.TrimSpace(val)
			if val != "" {
				// Append to .env file
				envPath := filepath.Join(wd, ".env")
				f, fErr := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if fErr == nil {
					_, _ = fmt.Fprintf(f, "# Required by %s skill\n%s=%s\n", name, env, val)
					_ = f.Close()
					fmt.Printf("  Added %s to .env\n", env)
				}
			}
		}
	}

	fmt.Printf("\nSkill %q added successfully.\n", info.DisplayName)
	return nil
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
