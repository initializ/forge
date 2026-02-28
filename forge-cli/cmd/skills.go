package cmd

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/runtime"
	cliskills "github.com/initializ/forge/forge-cli/skills"
	"github.com/initializ/forge/forge-skills/analyzer"
	"github.com/initializ/forge/forge-skills/local"
	"github.com/initializ/forge/forge-skills/requirements"
	"github.com/initializ/forge/forge-skills/resolver"
	"github.com/initializ/forge/forge-skills/trust"
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

var skillsAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Run security audit on skills file",
	RunE:  runSkillsAudit,
}

var skillsSignCmd = &cobra.Command{
	Use:   "sign <skill-file>",
	Short: "Sign a skill file with an Ed25519 key",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillsSign,
}

var skillsKeygenCmd = &cobra.Command{
	Use:   "keygen <key-name>",
	Short: "Generate an Ed25519 key pair for skill signing",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillsKeygen,
}

var auditFormat string
var auditEmbedded bool
var auditDir string
var signKeyPath string

func init() {
	skillsCmd.AddCommand(skillsValidateCmd)
	skillsCmd.AddCommand(skillsAddCmd)
	skillsCmd.AddCommand(skillsAuditCmd)
	skillsCmd.AddCommand(skillsSignCmd)
	skillsCmd.AddCommand(skillsKeygenCmd)

	skillsAuditCmd.Flags().StringVar(&auditFormat, "format", "text", "Output format: text or json")
	skillsAuditCmd.Flags().BoolVar(&auditEmbedded, "embedded", false, "Audit embedded skills from the binary")
	skillsAuditCmd.Flags().StringVar(&auditDir, "dir", "", "Audit skills from a directory of SKILL.md subdirectories")
	skillsSignCmd.Flags().StringVar(&signKeyPath, "key", "", "Path to Ed25519 private key")
	_ = skillsSignCmd.MarkFlagRequired("key")
}

func runSkillsAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Create embedded registry
	reg, err := local.NewEmbeddedRegistry()
	if err != nil {
		return fmt.Errorf("loading skill registry: %w", err)
	}

	// Look up skill in registry
	info := reg.Get(name)
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

	content, err := reg.LoadContent(name)
	if err != nil {
		return fmt.Errorf("loading skill file: %w", err)
	}

	skillPath := filepath.Join(skillDir, name+".md")
	if err := os.WriteFile(skillPath, content, 0o644); err != nil {
		return fmt.Errorf("writing skill file: %w", err)
	}
	fmt.Printf("  Added skill file: skills/%s.md\n", name)

	// Write all scripts for the skill
	scriptFiles := reg.ListScripts(name)
	if len(scriptFiles) > 0 {
		scriptDir := filepath.Join(skillDir, "scripts")
		if mkErr := os.MkdirAll(scriptDir, 0o755); mkErr != nil {
			fmt.Printf("  Warning: could not create scripts directory: %s\n", mkErr)
		} else {
			for _, sf := range scriptFiles {
				scriptContent, sErr := reg.LoadScriptByName(name, sf)
				if sErr != nil {
					fmt.Printf("  Warning: could not load script %s: %s\n", sf, sErr)
					continue
				}
				scriptPath := filepath.Join(scriptDir, sf)
				if wErr := os.WriteFile(scriptPath, scriptContent, 0o755); wErr != nil {
					fmt.Printf("  Warning: could not write script: %s\n", wErr)
				} else {
					fmt.Printf("  Added script:     skills/scripts/%s\n", sf)
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

	// Load existing .env file to check alongside OS env
	envPath := filepath.Join(wd, ".env")
	dotEnv, _ := runtime.LoadEnvFile(envPath)
	secretKeys := loadSecretPlaceholders(envPath)

	// Check env var requirements against OS env + .env file + secrets
	missingEnvs := []string{}
	if len(info.RequiredEnv) > 0 {
		fmt.Println("\n  Environment requirements:")
		for _, env := range info.RequiredEnv {
			switch {
			case os.Getenv(env) != "":
				fmt.Printf("    %s — ok (environment)\n", env)
			case dotEnv[env] != "":
				fmt.Printf("    %s — ok (.env)\n", env)
			case secretKeys[env]:
				fmt.Printf("    %s — ok (secrets)\n", env)
			default:
				fmt.Printf("    %s — NOT SET\n", env)
				missingEnvs = append(missingEnvs, env)
			}
		}
	}

	// Prompt for missing env vars
	if len(missingEnvs) > 0 {
		if hasSecretKeys(missingEnvs) {
			fmt.Println("\n  Tip: For sensitive values, consider using 'forge secrets set <KEY>' instead.")
		}
		reader := bufio.NewReader(os.Stdin)
		for _, env := range missingEnvs {
			fmt.Printf("\n  Enter value for %s (or press Enter to skip): ", env)
			val, _ := reader.ReadString('\n')
			val = strings.TrimSpace(val)
			if val != "" {
				// Check if key already exists in .env before appending
				if existing, ok := dotEnv[env]; ok && existing != "" {
					fmt.Printf("  %s already set in .env, skipping\n", env)
					continue
				}
				f, fErr := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if fErr == nil {
					_, _ = fmt.Fprintf(f, "# Required by %s skill\n%s=%s\n", name, env, val)
					_ = f.Close()
					dotEnv[env] = val // track for subsequent iterations
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
	skillsPath := "SKILL.md"

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
	reqs := requirements.AggregateRequirements(entries)

	hasErrors := false

	// Check binaries
	if len(reqs.Bins) > 0 {
		fmt.Println("Binaries:")
		binDiags := resolver.BinDiagnostics(reqs.Bins)
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

	envResolver := resolver.NewEnvResolver(osEnv, dotEnv, nil)
	envDiags := envResolver.Resolve(reqs)

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

// hasSecretKeys returns true if any of the env var names look like secrets.
func hasSecretKeys(keys []string) bool {
	return slices.ContainsFunc(keys, isSecretKey)
}

// loadSecretPlaceholders scans a .env file for commented-out secret placeholders
// like "# TAVILY_API_KEY=<encrypted>" and returns a set of those key names.
func loadSecretPlaceholders(path string) map[string]bool {
	keys := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return keys
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Match lines like: # KEY_NAME=<encrypted>
		after, found := strings.CutPrefix(line, "#")
		if !found {
			continue
		}
		after = strings.TrimSpace(after)
		k, v, ok := strings.Cut(after, "=")
		if !ok {
			continue
		}
		if strings.Contains(v, "<encrypted>") {
			keys[k] = true
		}
	}
	return keys
}

func runSkillsAudit(cmd *cobra.Command, args []string) error {
	policy := analyzer.DefaultPolicy()
	var report *analyzer.AuditReport

	switch {
	case auditEmbedded:
		reg, err := local.NewEmbeddedRegistry()
		if err != nil {
			return fmt.Errorf("loading embedded registry: %w", err)
		}
		r, err := analyzer.GenerateReport(reg, policy)
		if err != nil {
			return fmt.Errorf("generating report: %w", err)
		}
		report = r

	case auditDir != "":
		reg, err := local.NewLocalRegistry(os.DirFS(auditDir))
		if err != nil {
			return fmt.Errorf("loading directory registry %q: %w", auditDir, err)
		}
		r, err := analyzer.GenerateReport(reg, policy)
		if err != nil {
			return fmt.Errorf("generating report: %w", err)
		}
		report = r

	default:
		// File-based audit (original behavior)
		skillsPath := "SKILL.md"

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
		entries, _, parseErr := cliskills.ParseFileWithMetadata(skillsPath)
		if parseErr != nil {
			return fmt.Errorf("parsing skills file: %w", parseErr)
		}

		// Build hasScript checker from filesystem
		skillsDir := filepath.Dir(skillsPath)
		hasScript := func(name string) bool {
			scriptPath := filepath.Join(skillsDir, "scripts", name+".sh")
			_, statErr := os.Stat(scriptPath)
			return statErr == nil
		}

		report = analyzer.GenerateReportFromEntries(entries, hasScript, policy)
	}

	switch auditFormat {
	case "json":
		data, jsonErr := analyzer.FormatJSON(report)
		if jsonErr != nil {
			return fmt.Errorf("formatting JSON report: %w", jsonErr)
		}
		fmt.Println(string(data))
	default:
		fmt.Print(analyzer.FormatText(report))
	}

	if !report.PolicySummary.Passed {
		return fmt.Errorf("security policy check failed: %d error(s)", report.PolicySummary.Errors)
	}
	return nil
}

func runSkillsSign(cmd *cobra.Command, args []string) error {
	skillFile := args[0]

	// Read skill content
	content, err := os.ReadFile(skillFile)
	if err != nil {
		return fmt.Errorf("reading skill file: %w", err)
	}

	// Read private key
	keyData, err := os.ReadFile(signKeyPath)
	if err != nil {
		return fmt.Errorf("reading private key: %w", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
	if err != nil {
		return fmt.Errorf("decoding private key: %w", err)
	}

	if len(privBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key size: %d (expected %d)", len(privBytes), ed25519.PrivateKeySize)
	}

	privateKey := ed25519.PrivateKey(privBytes)
	sig, err := trust.SignSkill(content, privateKey)
	if err != nil {
		return fmt.Errorf("signing skill: %w", err)
	}

	// Write detached signature
	sigPath := skillFile + ".sig"
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	if err := os.WriteFile(sigPath, []byte(sigB64+"\n"), 0644); err != nil {
		return fmt.Errorf("writing signature: %w", err)
	}

	fmt.Printf("Signature written to %s\n", sigPath)
	return nil
}

func runSkillsKeygen(cmd *cobra.Command, args []string) error {
	keyName := args[0]

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	keysDir := filepath.Join(home, ".forge", "keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return fmt.Errorf("creating keys directory: %w", err)
	}

	pub, priv, err := trust.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generating key pair: %w", err)
	}

	// Write private key
	privPath := filepath.Join(keysDir, keyName+".key")
	privB64 := base64.StdEncoding.EncodeToString(priv)
	if err := os.WriteFile(privPath, []byte(privB64+"\n"), 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}

	// Write public key
	pubPath := filepath.Join(keysDir, keyName+".pub")
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(pubPath, []byte(pubB64+"\n"), 0644); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}

	fmt.Printf("Key pair generated:\n  Private: %s\n  Public:  %s\n", privPath, pubPath)
	fmt.Printf("\nTo trust this key for signature verification, copy %s to ~/.forge/trusted-keys/\n", filepath.Base(pubPath))
	return nil
}
