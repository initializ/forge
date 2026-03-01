package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/steps"
	"github.com/initializ/forge/forge-cli/skills"
	"github.com/initializ/forge/forge-cli/templates"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/secrets"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/util"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/local"
)

// initOptions holds all the collected options for project scaffolding.
type initOptions struct {
	Name           string
	AgentID        string
	Framework      string
	Language       string
	ModelProvider  string
	APIKey         string // validated provider key
	Fallbacks      []tui.FallbackProvider
	Channels       []string
	SkillsFile     string
	Tools          []toolEntry
	BuiltinTools   []string // selected builtin tool names
	Skills         []string // selected registry skill names
	EnvVars        map[string]string
	NonInteractive bool   // skip auto-run in non-interactive mode
	Force          bool   // overwrite existing directory
	CustomModel    string // custom provider model name
	AuthMethod     string // "apikey" or "oauth"
}

// toolEntry represents a tool parsed from a skills file.
type toolEntry struct {
	Name string
	Type string
}

// templateData is passed to all templates during rendering.
type templateData struct {
	Name          string
	AgentID       string
	Framework     string
	Language      string
	Entrypoint    string
	ModelProvider string
	ModelName     string
	Fallbacks     []fallbackTmplData
	Channels      []string
	Tools         []toolEntry
	BuiltinTools  []string
	SkillEntries  []skillTmplData
	EgressDomains []string
	EnvVars       []envVarEntry
	HasSecrets    bool
}

// fallbackTmplData holds template data for a fallback provider.
type fallbackTmplData struct {
	Provider  string
	ModelName string
}

// skillTmplData holds template data for a registry skill.
type skillTmplData struct {
	Name        string
	DisplayName string
	Description string
}

// envVarEntry represents an environment variable for templates.
type envVarEntry struct {
	Key     string
	Value   string
	Comment string
}

// fileToRender maps a template path to its output destination.
type fileToRender struct {
	TemplatePath string
	OutputPath   string
}

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Initialize a new agent project",
	Long:  "Scaffold a new AI agent project with the specified framework, language, and model provider.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runInit,
}

func init() {
	initCmd.Flags().StringP("name", "n", "", "agent name")
	initCmd.Flags().StringP("framework", "f", "", "framework: forge (default), crewai, or langchain")
	initCmd.Flags().StringP("language", "l", "", "language for crewai/langchain entrypoint (python only)")
	initCmd.Flags().StringP("model-provider", "m", "", "model provider: openai, anthropic, gemini, ollama, or custom")
	initCmd.Flags().StringSlice("channels", nil, "communication channels (e.g., slack,telegram)")
	initCmd.Flags().String("from-skills", "", "path to SKILL.md file to parse for tools")
	initCmd.Flags().Bool("non-interactive", false, "run without interactive prompts (requires all flags)")
	initCmd.Flags().StringSlice("tools", nil, "builtin tools to enable (e.g., web_search,http_request)")
	initCmd.Flags().StringSlice("skills", nil, "registry skills to include (e.g., github,weather)")
	initCmd.Flags().String("api-key", "", "LLM provider API key")
	initCmd.Flags().StringSlice("fallbacks", nil, "fallback LLM providers (e.g., openai,gemini)")
	initCmd.Flags().Bool("force", false, "overwrite existing directory")
}

func runInit(cmd *cobra.Command, args []string) error {
	opts := &initOptions{
		EnvVars: make(map[string]string),
	}

	// Get name from positional arg or flag
	if len(args) > 0 {
		opts.Name = args[0]
	}
	if n, _ := cmd.Flags().GetString("name"); n != "" {
		opts.Name = n
	}

	// Read flags
	opts.Framework, _ = cmd.Flags().GetString("framework")
	opts.Language, _ = cmd.Flags().GetString("language")
	opts.ModelProvider, _ = cmd.Flags().GetString("model-provider")
	opts.Channels, _ = cmd.Flags().GetStringSlice("channels")
	opts.SkillsFile, _ = cmd.Flags().GetString("from-skills")
	opts.BuiltinTools, _ = cmd.Flags().GetStringSlice("tools")
	opts.Skills, _ = cmd.Flags().GetStringSlice("skills")
	opts.APIKey, _ = cmd.Flags().GetString("api-key")
	fallbackProviders, _ := cmd.Flags().GetStringSlice("fallbacks")
	for _, p := range fallbackProviders {
		opts.Fallbacks = append(opts.Fallbacks, tui.FallbackProvider{Provider: p})
	}

	nonInteractive, _ := cmd.Flags().GetBool("non-interactive")
	opts.NonInteractive = nonInteractive
	opts.Force, _ = cmd.Flags().GetBool("force")

	// TTY detection: require a terminal for interactive mode
	if !nonInteractive && !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("interactive mode requires a terminal; use --non-interactive")
	}

	var err error
	if nonInteractive {
		err = collectNonInteractive(opts)
	} else {
		err = collectInteractive(opts)
	}
	if err != nil {
		return err
	}

	// Derive agent ID
	opts.AgentID = util.Slugify(opts.Name)

	// Parse skills file if provided
	if opts.SkillsFile != "" {
		tools, parseErr := parseSkillsFile(opts.SkillsFile)
		if parseErr != nil {
			return fmt.Errorf("parsing skills file: %w", parseErr)
		}
		opts.Tools = tools
	}

	return scaffold(opts)
}

func collectInteractive(opts *initOptions) error {
	// Detect theme
	theme := tui.DetectTheme(themeOverride)
	styles := tui.NewStyleSet(theme)

	// Load tool info for the tools step
	allTools := builtins.All()
	var toolInfos []steps.ToolInfo
	for _, t := range allTools {
		toolInfos = append(toolInfos, steps.ToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
		})
	}

	// Load skill info for the skills step
	var skillInfos []steps.SkillInfo
	reg, regErr := local.NewEmbeddedRegistry()
	if regErr == nil {
		regSkills, listErr := reg.List()
		if listErr == nil {
			for _, s := range regSkills {
				skillInfos = append(skillInfos, steps.SkillInfo{
					Name:          s.Name,
					DisplayName:   s.DisplayName,
					Description:   s.Description,
					RequiredEnv:   s.RequiredEnv,
					OneOfEnv:      s.OneOfEnv,
					OptionalEnv:   s.OptionalEnv,
					RequiredBins:  s.RequiredBins,
					EgressDomains: s.EgressDomains,
				})
			}
		}
	}

	// Build the egress derivation callback (avoids circular import)
	deriveEgressFn := func(provider string, channels, tools, selectedSkills []string, envVars map[string]string) []string {
		tmpOpts := &initOptions{
			ModelProvider: provider,
			Channels:      channels,
			BuiltinTools:  tools,
			EnvVars:       envVars,
		}
		selectedInfos := lookupSelectedSkills(selectedSkills)
		return deriveEgressDomains(tmpOpts, selectedInfos)
	}

	// Build validation callback
	validateKeyFn := func(provider, key string) error {
		return validateProviderKey(provider, key)
	}

	// Build OAuth flow callback
	oauthFlowFn := func(provider string) (string, error) {
		return runOAuthFlow(provider)
	}

	// Build web search key validation callback
	validateWebSearchKeyFn := func(provider, key string) error {
		return validateWebSearchKey(provider, key)
	}

	// Build step list
	wizardSteps := []tui.Step{
		steps.NewNameStep(styles, opts.Name),
		steps.NewProviderStep(styles, validateKeyFn, oauthFlowFn),
		steps.NewFallbackStep(styles, validateKeyFn),
		steps.NewChannelStep(styles),
		steps.NewToolsStep(styles, toolInfos, validateWebSearchKeyFn),
		steps.NewSkillsStep(styles, skillInfos),
		steps.NewEgressStep(styles, deriveEgressFn),
		steps.NewReviewStep(styles), // scaffold is handled by the caller after collectInteractive returns
	}

	// Create and run the Bubble Tea program
	model := tui.NewWizardModel(theme, wizardSteps, appVersion)
	p := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI wizard error: %w", err)
	}

	wiz, ok := finalModel.(tui.WizardModel)
	if !ok {
		return fmt.Errorf("unexpected model type from wizard")
	}

	if wiz.Err() != nil {
		return wiz.Err()
	}

	// Convert WizardContext → initOptions
	ctx := wiz.Context()
	opts.Name = ctx.Name

	// Default framework
	if opts.Framework == "" {
		opts.Framework = "forge"
	}

	opts.ModelProvider = ctx.Provider
	opts.APIKey = ctx.APIKey
	opts.AuthMethod = ctx.AuthMethod
	opts.Fallbacks = ctx.Fallbacks
	opts.CustomModel = ctx.CustomModel
	// Use wizard-selected model name if available
	if ctx.ModelName != "" {
		opts.CustomModel = ctx.ModelName
	}

	if ctx.Channel != "" && ctx.Channel != "none" {
		opts.Channels = []string{ctx.Channel}
	}

	opts.BuiltinTools = ctx.BuiltinTools
	opts.Skills = ctx.Skills

	// Store provider env var
	storeProviderEnvVar(opts)

	// Copy channel tokens
	for k, v := range ctx.ChannelTokens {
		opts.EnvVars[k] = v
	}

	// Copy other env vars from wizard
	for k, v := range ctx.EnvVars {
		opts.EnvVars[k] = v
	}

	// Custom provider env vars
	if ctx.CustomBaseURL != "" {
		opts.EnvVars["MODEL_BASE_URL"] = ctx.CustomBaseURL
	}
	if ctx.CustomAPIKey != "" {
		opts.EnvVars["MODEL_API_KEY"] = ctx.CustomAPIKey
	}

	// Store egress domains
	if len(ctx.EgressDomains) > 0 {
		opts.EnvVars["__egress_domains"] = strings.Join(ctx.EgressDomains, ",")
	}

	// Check skill requirements
	checkSkillRequirements(opts)

	return nil
}

func collectNonInteractive(opts *initOptions) error {
	if opts.Name == "" {
		return fmt.Errorf("--name is required in non-interactive mode")
	}
	if opts.ModelProvider == "" {
		return fmt.Errorf("--model-provider is required in non-interactive mode")
	}

	// Default framework if not provided
	if opts.Framework == "" {
		opts.Framework = "forge"
	}

	// Validate framework
	switch opts.Framework {
	case "forge", "crewai", "langchain":
	default:
		return fmt.Errorf("invalid framework %q: must be forge, crewai, or langchain", opts.Framework)
	}

	// Validate language (only relevant for crewai/langchain)
	if opts.Framework == "crewai" || opts.Framework == "langchain" {
		if opts.Language == "" {
			opts.Language = "python"
		}
		if opts.Language != "python" {
			return fmt.Errorf("framework %q only supports python", opts.Framework)
		}
	}

	// Validate model provider
	switch opts.ModelProvider {
	case "openai", "anthropic", "gemini", "ollama", "custom":
	default:
		return fmt.Errorf("invalid model-provider %q: must be openai, anthropic, gemini, ollama, or custom", opts.ModelProvider)
	}

	// Validate API key if provided
	if opts.APIKey != "" {
		if err := validateProviderKey(opts.ModelProvider, opts.APIKey); err != nil {
			fmt.Printf("Warning: API key validation failed: %s\n", err)
		}
	}

	// Store provider env var
	storeProviderEnvVar(opts)

	// Validate builtin tool names
	if len(opts.BuiltinTools) > 0 {
		allTools := builtins.All()
		validNames := make(map[string]bool)
		for _, t := range allTools {
			validNames[t.Name()] = true
		}
		for _, name := range opts.BuiltinTools {
			if !validNames[name] {
				fmt.Printf("Warning: unknown builtin tool %q\n", name)
			}
		}
	}

	// Validate skill names and check requirements
	if len(opts.Skills) > 0 {
		niReg, niErr := local.NewEmbeddedRegistry()
		if niErr != nil {
			fmt.Printf("Warning: could not load skill registry: %s\n", niErr)
		} else {
			regSkills, listErr := niReg.List()
			if listErr == nil {
				validNames := make(map[string]bool)
				for _, s := range regSkills {
					validNames[s.Name] = true
				}
				for _, name := range opts.Skills {
					if !validNames[name] {
						fmt.Printf("Warning: unknown skill %q\n", name)
					}
				}
			}
		}
		checkSkillRequirements(opts)
	}

	return nil
}

// storeProviderEnvVar stores the appropriate environment variable for the selected provider.
func storeProviderEnvVar(opts *initOptions) {
	if opts.APIKey == "" {
		return
	}
	switch opts.ModelProvider {
	case "openai":
		opts.EnvVars["OPENAI_API_KEY"] = opts.APIKey
	case "anthropic":
		opts.EnvVars["ANTHROPIC_API_KEY"] = opts.APIKey
	case "gemini":
		opts.EnvVars["GEMINI_API_KEY"] = opts.APIKey
	}
}

// checkSkillRequirements checks binary and env requirements for selected skills.
func checkSkillRequirements(opts *initOptions) {
	chkReg, chkErr := local.NewEmbeddedRegistry()
	if chkErr != nil {
		return
	}

	for _, skillName := range opts.Skills {
		info := chkReg.Get(skillName)
		if info == nil {
			continue
		}

		// Check required binaries
		for _, bin := range info.RequiredBins {
			if _, err := exec.LookPath(bin); err != nil {
				fmt.Printf("  Warning: skill %q requires %q binary (not found in PATH)\n", skillName, bin)
			}
		}

		// Check required env vars
		for _, env := range info.RequiredEnv {
			if os.Getenv(env) == "" {
				if _, exists := opts.EnvVars[env]; !exists {
					fmt.Printf("  Note: skill %q requires %s (will be added to .env)\n", skillName, env)
					opts.EnvVars[env] = ""
				}
			}
		}

		// Check one-of env vars
		if len(info.OneOfEnv) > 0 {
			found := false
			for _, env := range info.OneOfEnv {
				if os.Getenv(env) != "" {
					found = true
					break
				}
				if v, exists := opts.EnvVars[env]; exists && v != "" {
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("  Note: skill %q requires one of: %s (will be added to .env)\n",
					skillName, strings.Join(info.OneOfEnv, ", "))
				opts.EnvVars[info.OneOfEnv[0]] = ""
			}
		}
	}
}

// lookupSelectedSkills returns SkillDescriptor entries for the selected skill names.
func lookupSelectedSkills(skillNames []string) []contract.SkillDescriptor {
	reg, err := local.NewEmbeddedRegistry()
	if err != nil {
		return nil
	}
	var result []contract.SkillDescriptor
	for _, name := range skillNames {
		info := reg.Get(name)
		if info != nil {
			result = append(result, *info)
		}
	}
	return result
}

func parseSkillsFile(path string) ([]toolEntry, error) {
	entries, err := skills.ParseFile(path)
	if err != nil {
		return nil, err
	}
	var tools []toolEntry
	for _, e := range entries {
		tools = append(tools, toolEntry{Name: e.Name, Type: "custom"})
	}
	return tools, nil
}

func scaffold(opts *initOptions) error {
	dir := filepath.Join(".", opts.AgentID)

	// Check if directory already exists
	if !opts.Force {
		if _, err := os.Stat(dir); err == nil {
			return fmt.Errorf("directory %q already exists (use --force to overwrite)", dir)
		}
	}

	// Create project directories
	for _, subDir := range []string{"tools", "skills"} {
		if err := os.MkdirAll(filepath.Join(dir, subDir), 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", subDir, err)
		}
	}

	data := buildTemplateData(opts)
	manifest := getFileManifest(opts)

	for _, f := range manifest {
		tmplContent, err := templates.GetInitTemplate(f.TemplatePath)
		if err != nil {
			return fmt.Errorf("reading template %s: %w", f.TemplatePath, err)
		}

		tmpl, err := template.New(f.TemplatePath).Parse(tmplContent)
		if err != nil {
			return fmt.Errorf("parsing template %s: %w", f.TemplatePath, err)
		}

		outPath := filepath.Join(dir, f.OutputPath)

		// Ensure parent directory exists
		if parentDir := filepath.Dir(outPath); parentDir != dir {
			if err := os.MkdirAll(parentDir, 0o755); err != nil {
				return fmt.Errorf("creating directory for %s: %w", f.OutputPath, err)
			}
		}

		out, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("creating file %s: %w", f.OutputPath, err)
		}

		if err := tmpl.Execute(out, data); err != nil {
			_ = out.Close()
			return fmt.Errorf("rendering template %s: %w", f.TemplatePath, err)
		}
		_ = out.Close()
	}

	// Split env vars into secrets and config
	secretVars, configVars := splitEnvVars(data.EnvVars)

	// Write .env file with non-secret config only
	if err := writeEnvFile(dir, configVars); err != nil {
		return fmt.Errorf("writing .env file: %w", err)
	}

	// Write secrets to encrypted file
	if len(secretVars) > 0 {
		storedKeys, sErr := writeSecrets(dir, secretVars, opts.NonInteractive)
		if sErr != nil {
			fmt.Printf("  Warning: could not encrypt secrets: %s\n", sErr)
			fmt.Println("  Secrets will be stored in plaintext .env file instead.")
			if aErr := appendEnvFile(dir, secretVars); aErr != nil {
				return fmt.Errorf("writing secrets to .env fallback: %w", aErr)
			}
		} else {
			appendSecretComments(dir, storedKeys)
			fmt.Printf("  Encrypted %d secret(s) in %s\n", len(storedKeys), filepath.Join(dir, ".forge", "secrets.enc"))
		}
	}

	// Migrate OAuth credentials to encrypted store when passphrase is available
	if opts.AuthMethod == "oauth" && os.Getenv("FORGE_PASSPHRASE") != "" {
		if err := oauth.MigrateToEncrypted(opts.ModelProvider); err != nil {
			fmt.Printf("  Warning: could not migrate OAuth credentials: %s\n", err)
		}
	}

	// Vendor selected registry skills
	scfReg, scfErr := local.NewEmbeddedRegistry()
	if scfErr != nil {
		fmt.Printf("Warning: could not load skill registry: %s\n", scfErr)
	}
	for _, skillName := range opts.Skills {
		if scfReg == nil {
			continue
		}
		content, err := scfReg.LoadContent(skillName)
		if err != nil {
			fmt.Printf("Warning: could not load skill file for %q: %s\n", skillName, err)
			continue
		}
		// Write to subdirectory: skills/{name}/SKILL.md
		skillSubDir := filepath.Join(dir, "skills", skillName)
		_ = os.MkdirAll(skillSubDir, 0o755)
		skillPath := filepath.Join(skillSubDir, "SKILL.md")
		if err := os.WriteFile(skillPath, content, 0o644); err != nil {
			return fmt.Errorf("writing skill file %s: %w", skillName, err)
		}

		// Vendor scripts to skills/{name}/scripts/
		scriptFiles := scfReg.ListScripts(skillName)
		if len(scriptFiles) > 0 {
			scriptDir := filepath.Join(skillSubDir, "scripts")
			_ = os.MkdirAll(scriptDir, 0o755)
			for _, sf := range scriptFiles {
				scriptContent, sErr := scfReg.LoadScriptByName(skillName, sf)
				if sErr != nil {
					continue
				}
				scriptPath := filepath.Join(scriptDir, sf)
				if wErr := os.WriteFile(scriptPath, scriptContent, 0o755); wErr != nil {
					fmt.Printf("Warning: could not write script for %q: %s\n", skillName, wErr)
				}
			}
		}
	}

	fmt.Printf("\nCreated agent project in ./%s\n", opts.AgentID)

	// In non-interactive mode, just print the command
	if opts.NonInteractive {
		fmt.Printf("  cd %s && forge run\n", opts.AgentID)
		return nil
	}

	// Auto-run the agent
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("changing to project dir: %w", err)
	}

	args := []string{"run"}
	if len(opts.Channels) > 0 {
		args = append(args, "--with", strings.Join(opts.Channels, ","))
	}

	forgeBin, err := os.Executable()
	if err != nil {
		forgeBin = "forge"
	}
	runCmd := exec.Command(forgeBin, args...)
	runCmd.Stdin = os.Stdin
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	return runCmd.Run()
}

// isSecretKey returns true if the env var key looks like a secret (API key, token, etc.).
func isSecretKey(key string) bool {
	return strings.HasSuffix(key, "_API_KEY") ||
		strings.HasSuffix(key, "_TOKEN") ||
		strings.HasSuffix(key, "_SECRET")
}

// splitEnvVars separates env vars into secrets and config based on key naming
// conventions. Only entries with real values (not placeholders) are classified
// as secrets — placeholders stay in .env as reminders.
func splitEnvVars(vars []envVarEntry) (secretVars, configVars []envVarEntry) {
	placeholders := map[string]bool{
		"your-api-key-here":        true,
		"your-tavily-key-here":     true,
		"your-perplexity-key-here": true,
		"__oauth__":                true, // sentinel, not a real secret
		"":                         true,
	}

	for _, v := range vars {
		if isSecretKey(v.Key) && !placeholders[v.Value] {
			secretVars = append(secretVars, v)
		} else {
			configVars = append(configVars, v)
		}
	}
	return
}

// resolvePassphraseForInit obtains a passphrase for secret encryption.
// It checks FORGE_PASSPHRASE env, then detects whether ~/.forge/secrets.enc
// already exists. If it does, it prompts once and validates by attempting
// decryption. If not, it prompts twice (enter + confirm) for first-time setup.
func resolvePassphraseForInit(nonInteractive bool) (string, error) {
	if p := os.Getenv("FORGE_PASSPHRASE"); p != "" {
		return p, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("FORGE_PASSPHRASE not set; cannot encrypt secrets in non-interactive mode")
	}

	// Check if a global secrets file already exists.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	globalPath := filepath.Join(home, ".forge", "secrets.enc")

	if _, statErr := os.Stat(globalPath); statErr == nil {
		// Existing file — prompt once and validate by decryption.
		for {
			fmt.Print("\n  Enter passphrase: ")
			pass, readErr := term.ReadPassword(int(os.Stdin.Fd()))
			if readErr != nil {
				return "", fmt.Errorf("reading passphrase: %w", readErr)
			}
			fmt.Println()

			if len(pass) == 0 {
				fmt.Println("  Passphrase cannot be empty. Try again.")
				continue
			}

			// Validate by attempting to decrypt the existing file.
			testProvider := secrets.NewEncryptedFileProvider(globalPath, func() (string, error) {
				return string(pass), nil
			})
			if _, listErr := testProvider.List(); listErr != nil {
				fmt.Println("  Incorrect passphrase. Try again.")
				continue
			}

			return string(pass), nil
		}
	}

	// No existing file — first-time setup, prompt with confirmation.
	fmt.Print("\n  Enter passphrase for secret encryption: ")
	pass1, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	fmt.Print("  Confirm passphrase: ")
	pass2, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", fmt.Errorf("reading passphrase confirmation: %w", err)
	}
	fmt.Println()

	if string(pass1) != string(pass2) {
		return "", fmt.Errorf("passphrases do not match")
	}
	if len(pass1) == 0 {
		return "", fmt.Errorf("passphrase cannot be empty")
	}

	return string(pass1), nil
}

// writeSecrets encrypts the given secret env vars into <dir>/.forge/secrets.enc
// (agent-local) and ensures the global ~/.forge/secrets.enc exists as a marker
// for passphrase validation on subsequent inits.
// Returns the list of stored key names on success.
func writeSecrets(dir string, secretVars []envVarEntry, nonInteractive bool) ([]string, error) {
	passphrase, err := resolvePassphraseForInit(nonInteractive)
	if err != nil {
		return nil, err
	}

	passCb := func() (string, error) { return passphrase, nil }

	// Write secrets to agent-local file.
	encPath := filepath.Join(dir, ".forge", "secrets.enc")
	provider := secrets.NewEncryptedFileProvider(encPath, passCb)

	pairs := make(map[string]string, len(secretVars))
	keys := make([]string, 0, len(secretVars))
	for _, v := range secretVars {
		pairs[v.Key] = v.Value
		keys = append(keys, v.Key)
	}

	if err := provider.SetBatch(pairs); err != nil {
		return nil, fmt.Errorf("writing encrypted secrets: %w", err)
	}

	// Ensure global secrets file exists as a marker for passphrase validation.
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		globalPath := filepath.Join(home, ".forge", "secrets.enc")
		if _, statErr := os.Stat(globalPath); os.IsNotExist(statErr) {
			globalProvider := secrets.NewEncryptedFileProvider(globalPath, passCb)
			_ = globalProvider.SetBatch(map[string]string{})
		}
	}

	// Export passphrase so the auto-run subprocess can decrypt secrets.
	_ = os.Setenv("FORGE_PASSPHRASE", passphrase)

	return keys, nil
}

// appendEnvFile appends additional entries to an existing .env file (fallback path).
func appendEnvFile(dir string, vars []envVarEntry) error {
	envPath := filepath.Join(dir, ".env")
	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	for _, v := range vars {
		if v.Comment != "" {
			_, _ = fmt.Fprintf(f, "# %s\n", v.Comment)
		}
		_, _ = fmt.Fprintf(f, "%s=%s\n", v.Key, v.Value)
	}
	return nil
}

// appendSecretComments appends comments to .env indicating which keys are
// stored in the encrypted secrets file.
func appendSecretComments(dir string, keys []string) {
	envPath := filepath.Join(dir, ".env")
	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	_, _ = fmt.Fprintln(f, "\n# Secrets stored in .forge/secrets.enc (managed by `forge secret --local`)")
	for _, k := range keys {
		_, _ = fmt.Fprintf(f, "# %s=<encrypted>\n", k)
	}
}

// writeEnvFile creates a .env file with the collected environment variables.
func writeEnvFile(dir string, vars []envVarEntry) error {
	if len(vars) == 0 {
		return nil
	}

	envPath := filepath.Join(dir, ".env")
	f, err := os.Create(envPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	for _, v := range vars {
		if v.Comment != "" {
			_, _ = fmt.Fprintf(f, "# %s\n", v.Comment)
		}
		_, _ = fmt.Fprintf(f, "%s=%s\n", v.Key, v.Value)
	}
	return nil
}

func getFileManifest(opts *initOptions) []fileToRender {
	files := []fileToRender{
		{TemplatePath: "forge.yaml.tmpl", OutputPath: "forge.yaml"},
		{TemplatePath: "SKILL.md.tmpl", OutputPath: "SKILL.md"},
		{TemplatePath: "env.example.tmpl", OutputPath: ".env.example"},
		{TemplatePath: "gitignore.tmpl", OutputPath: ".gitignore"},
	}

	switch opts.Framework {
	case "crewai":
		files = append(files,
			fileToRender{TemplatePath: "crewai/agent.py.tmpl", OutputPath: "agent.py"},
			fileToRender{TemplatePath: "crewai/example_tool.py.tmpl", OutputPath: "tools/example_tool.py"},
		)
	case "langchain":
		files = append(files,
			fileToRender{TemplatePath: "langchain/agent.py.tmpl", OutputPath: "agent.py"},
			fileToRender{TemplatePath: "langchain/example_tool.py.tmpl", OutputPath: "tools/example_tool.py"},
		)
	case "forge":
		// No entrypoint scaffolding — forge uses the built-in LLM executor.
		// The tools/ directory is still created for custom tool scripts.
	case "custom":
		// Backward compat alias for "forge"
		switch opts.Language {
		case "python":
			files = append(files,
				fileToRender{TemplatePath: "custom/agent.py.tmpl", OutputPath: "agent.py"},
				fileToRender{TemplatePath: "custom/example_tool.py.tmpl", OutputPath: "tools/example_tool.py"},
			)
		case "typescript":
			files = append(files,
				fileToRender{TemplatePath: "custom/agent.ts.tmpl", OutputPath: "agent.ts"},
				fileToRender{TemplatePath: "custom/example_tool.ts.tmpl", OutputPath: "tools/example_tool.ts"},
			)
		case "go":
			files = append(files,
				fileToRender{TemplatePath: "custom/main.go.tmpl", OutputPath: "main.go"},
				fileToRender{TemplatePath: "custom/example_tool.go.tmpl", OutputPath: "tools/example_tool.go"},
			)
		}
	}

	// Channel config files
	for _, ch := range opts.Channels {
		files = append(files, fileToRender{
			TemplatePath: ch + "-config.yaml.tmpl",
			OutputPath:   ch + "-config.yaml",
		})
	}

	return files
}

func buildTemplateData(opts *initOptions) templateData {
	data := templateData{
		Name:          opts.Name,
		AgentID:       opts.AgentID,
		Framework:     opts.Framework,
		Language:      opts.Language,
		ModelProvider: opts.ModelProvider,
		Channels:      opts.Channels,
		Tools:         opts.Tools,
		BuiltinTools:  opts.BuiltinTools,
	}

	// Set entrypoint based on framework (only for subprocess-based frameworks)
	switch opts.Framework {
	case "crewai", "langchain":
		data.Entrypoint = "python agent.py"
	case "custom":
		// Backward compat: custom still sets entrypoint
		switch opts.Language {
		case "python":
			data.Entrypoint = "python agent.py"
		case "typescript":
			data.Entrypoint = "bun run agent.ts"
		case "go":
			data.Entrypoint = "go run main.go"
		}
	}
	// "forge" framework: no entrypoint (built-in LLM executor)

	// Set model name: use wizard-selected model, or fall back to provider default
	if opts.CustomModel != "" {
		data.ModelName = opts.CustomModel
	} else {
		data.ModelName = defaultModelNameForProvider(opts.ModelProvider)
	}

	// Build fallback entries for templates
	for _, fb := range opts.Fallbacks {
		modelName := defaultModelNameForProvider(fb.Provider)
		data.Fallbacks = append(data.Fallbacks, fallbackTmplData{
			Provider:  fb.Provider,
			ModelName: modelName,
		})
	}

	// Build skill entries for templates
	tmplReg, tmplRegErr := local.NewEmbeddedRegistry()
	if tmplRegErr == nil {
		for _, skillName := range opts.Skills {
			info := tmplReg.Get(skillName)
			if info != nil {
				data.SkillEntries = append(data.SkillEntries, skillTmplData{
					Name:        info.Name,
					DisplayName: info.DisplayName,
					Description: info.Description,
				})
			}
		}
	}

	// Compute egress domains
	selectedSkillInfos := lookupSelectedSkills(opts.Skills)
	data.EgressDomains = deriveEgressDomains(opts, selectedSkillInfos)

	// Check if egress domains were overridden in interactive mode
	if stored, ok := opts.EnvVars["__egress_domains"]; ok && stored != "" {
		data.EgressDomains = strings.Split(stored, ",")
	}

	// Build env vars
	data.EnvVars = buildEnvVars(opts)

	// Check if any env vars are secrets with real values
	secretVars, _ := splitEnvVars(data.EnvVars)
	data.HasSecrets = len(secretVars) > 0

	return data
}

// defaultModelNameForProvider returns the default model name for wizard templates.
func defaultModelNameForProvider(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.2-2025-12-11"
	case "anthropic":
		return "claude-sonnet-4-20250514"
	case "gemini":
		return "gemini-2.5-flash"
	case "ollama":
		return "llama3"
	default:
		return "default"
	}
}

// buildEnvVars builds the list of environment variables for the .env file.
func buildEnvVars(opts *initOptions) []envVarEntry {
	var vars []envVarEntry

	// Provider key
	switch opts.ModelProvider {
	case "openai":
		val := opts.EnvVars["OPENAI_API_KEY"]
		if val == "" {
			val = "your-api-key-here"
		}
		vars = append(vars, envVarEntry{Key: "OPENAI_API_KEY", Value: val, Comment: "OpenAI API key"})
	case "anthropic":
		val := opts.EnvVars["ANTHROPIC_API_KEY"]
		if val == "" {
			val = "your-api-key-here"
		}
		vars = append(vars, envVarEntry{Key: "ANTHROPIC_API_KEY", Value: val, Comment: "Anthropic API key"})
	case "gemini":
		val := opts.EnvVars["GEMINI_API_KEY"]
		if val == "" {
			val = "your-api-key-here"
		}
		vars = append(vars, envVarEntry{Key: "GEMINI_API_KEY", Value: val, Comment: "Gemini API key"})
	case "ollama":
		vars = append(vars, envVarEntry{Key: "OLLAMA_HOST", Value: "http://localhost:11434", Comment: "Ollama host"})
	case "custom":
		baseURL := opts.EnvVars["MODEL_BASE_URL"]
		if baseURL != "" {
			vars = append(vars, envVarEntry{Key: "MODEL_BASE_URL", Value: baseURL, Comment: "Custom model endpoint URL"})
		}
		apiKeyVal := opts.EnvVars["MODEL_API_KEY"]
		if apiKeyVal == "" {
			apiKeyVal = "your-api-key-here"
		}
		vars = append(vars, envVarEntry{Key: "MODEL_API_KEY", Value: apiKeyVal, Comment: "Model provider API key"})
	}

	// Web search provider key if web_search selected
	if containsStr(opts.BuiltinTools, "web_search") {
		provider := opts.EnvVars["WEB_SEARCH_PROVIDER"]
		if provider == "perplexity" {
			val := opts.EnvVars["PERPLEXITY_API_KEY"]
			if val == "" {
				val = "your-perplexity-key-here"
			}
			vars = append(vars, envVarEntry{Key: "PERPLEXITY_API_KEY", Value: val, Comment: "Perplexity API key for web_search"})
			vars = append(vars, envVarEntry{Key: "WEB_SEARCH_PROVIDER", Value: "perplexity", Comment: "Web search provider"})
		} else {
			// Default to Tavily
			val := opts.EnvVars["TAVILY_API_KEY"]
			if val == "" {
				val = "your-tavily-key-here"
			}
			vars = append(vars, envVarEntry{Key: "TAVILY_API_KEY", Value: val, Comment: "Tavily API key for web_search"})
		}
	}

	// Channel env vars
	for _, ch := range opts.Channels {
		switch ch {
		case "telegram":
			val := opts.EnvVars["TELEGRAM_BOT_TOKEN"]
			vars = append(vars, envVarEntry{Key: "TELEGRAM_BOT_TOKEN", Value: val, Comment: "Telegram bot token"})
		case "slack":
			appVal := opts.EnvVars["SLACK_APP_TOKEN"]
			vars = append(vars, envVarEntry{Key: "SLACK_APP_TOKEN", Value: appVal, Comment: "Slack app-level token (xapp-...)"})
			botVal := opts.EnvVars["SLACK_BOT_TOKEN"]
			vars = append(vars, envVarEntry{Key: "SLACK_BOT_TOKEN", Value: botVal, Comment: "Slack bot token (xoxb-...)"})
		}
	}

	// Fallback provider env vars
	fallbackKeyMap := map[string]string{
		"openai":    "OPENAI_API_KEY",
		"anthropic": "ANTHROPIC_API_KEY",
		"gemini":    "GEMINI_API_KEY",
	}
	for _, fb := range opts.Fallbacks {
		envKey, ok := fallbackKeyMap[fb.Provider]
		if !ok || fb.APIKey == "" {
			continue
		}
		// Skip if already written (e.g., primary provider)
		alreadyWritten := false
		for _, v := range vars {
			if v.Key == envKey {
				alreadyWritten = true
				break
			}
		}
		if !alreadyWritten {
			vars = append(vars, envVarEntry{
				Key:     envKey,
				Value:   fb.APIKey,
				Comment: fmt.Sprintf("%s API key (fallback)", titleCase(fb.Provider)),
			})
		}
	}

	// Skill env vars (skip keys already added above)
	written := make(map[string]bool)
	for _, v := range vars {
		written[v.Key] = true
	}
	envReg, envRegErr := local.NewEmbeddedRegistry()
	for _, skillName := range opts.Skills {
		if envRegErr != nil {
			continue
		}
		info := envReg.Get(skillName)
		if info == nil {
			continue
		}
		for _, env := range info.RequiredEnv {
			if written[env] {
				continue
			}
			written[env] = true
			val := opts.EnvVars[env]
			vars = append(vars, envVarEntry{Key: env, Value: val, Comment: fmt.Sprintf("Required by %s skill", skillName)})
		}
		if len(info.OneOfEnv) > 0 {
			for _, env := range info.OneOfEnv {
				if written[env] {
					continue
				}
				written[env] = true
				val := opts.EnvVars[env]
				vars = append(vars, envVarEntry{
					Key:     env,
					Value:   val,
					Comment: fmt.Sprintf("One of required by %s skill", skillName),
				})
			}
		}
		for _, env := range info.OptionalEnv {
			if written[env] {
				continue
			}
			val := opts.EnvVars[env]
			if val == "" {
				continue // only write optional vars the user actually provided
			}
			written[env] = true
			vars = append(vars, envVarEntry{Key: env, Value: val, Comment: fmt.Sprintf("Optional for %s skill", skillName)})
		}
	}

	return vars
}

// containsStr checks if a string slice contains the given value.
func containsStr(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// runOAuthFlow executes the OAuth browser flow for a provider and returns the access token.
func runOAuthFlow(provider string) (string, error) {
	var config oauth.ProviderConfig
	switch provider {
	case "openai":
		config = oauth.OpenAIConfig()
	default:
		return "", fmt.Errorf("OAuth not supported for provider %q", provider)
	}

	flow := oauth.NewFlow(config)
	token, err := flow.Execute(context.Background(), provider)
	if err != nil {
		return "", err
	}

	return token.AccessToken, nil
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
