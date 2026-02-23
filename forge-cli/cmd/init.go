package cmd

import (
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
	skillreg "github.com/initializ/forge/forge-core/registry"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/util"
)

// initOptions holds all the collected options for project scaffolding.
type initOptions struct {
	Name           string
	AgentID        string
	Framework      string
	Language       string
	ModelProvider  string
	APIKey         string // validated provider key
	Channels       []string
	SkillsFile     string
	Tools          []toolEntry
	BuiltinTools   []string // selected builtin tool names
	Skills         []string // selected registry skill names
	EnvVars        map[string]string
	NonInteractive bool   // skip auto-run in non-interactive mode
	Force          bool   // overwrite existing directory
	CustomModel    string // custom provider model name
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
	Channels      []string
	Tools         []toolEntry
	BuiltinTools  []string
	SkillEntries  []skillTmplData
	EgressDomains []string
	EnvVars       []envVarEntry
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
	initCmd.Flags().StringP("framework", "f", "", "framework: crewai, langchain, or custom")
	initCmd.Flags().StringP("language", "l", "", "language: python, typescript, or go (custom only)")
	initCmd.Flags().StringP("model-provider", "m", "", "model provider: openai, anthropic, gemini, ollama, or custom")
	initCmd.Flags().StringSlice("channels", nil, "communication channels (e.g., slack,telegram)")
	initCmd.Flags().String("from-skills", "", "path to skills.md file to parse for tools")
	initCmd.Flags().Bool("non-interactive", false, "run without interactive prompts (requires all flags)")
	initCmd.Flags().StringSlice("tools", nil, "builtin tools to enable (e.g., web_search,http_request)")
	initCmd.Flags().StringSlice("skills", nil, "registry skills to include (e.g., github,weather)")
	initCmd.Flags().String("api-key", "", "LLM provider API key")
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
	regSkills, err := skillreg.LoadIndex()
	if err == nil {
		for _, s := range regSkills {
			skillInfos = append(skillInfos, steps.SkillInfo{
				Name:          s.Name,
				DisplayName:   s.DisplayName,
				Description:   s.Description,
				RequiredEnv:   s.RequiredEnv,
				RequiredBins:  s.RequiredBins,
				EgressDomains: s.EgressDomains,
			})
		}
	}

	// Build the egress derivation callback (avoids circular import)
	deriveEgressFn := func(provider string, channels, tools, skills []string) []string {
		tmpOpts := &initOptions{
			ModelProvider: provider,
			Channels:      channels,
			BuiltinTools:  tools,
			EnvVars:       make(map[string]string),
		}
		selectedInfos := lookupSelectedSkills(skills)
		return deriveEgressDomains(tmpOpts, selectedInfos)
	}

	// Build validation callbacks
	validateKeyFn := func(provider, key string) error {
		return validateProviderKey(provider, key)
	}
	validatePerpFn := func(key string) error {
		return validatePerplexityKey(key)
	}

	// Build step list
	wizardSteps := []tui.Step{
		steps.NewNameStep(styles, opts.Name),
		steps.NewProviderStep(styles, validateKeyFn),
		steps.NewChannelStep(styles),
		steps.NewToolsStep(styles, toolInfos, validatePerpFn),
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

	// Default framework and language
	if opts.Framework == "" {
		opts.Framework = "custom"
	}
	if opts.Language == "" {
		opts.Language = "python"
	}

	opts.ModelProvider = ctx.Provider
	opts.APIKey = ctx.APIKey
	opts.CustomModel = ctx.CustomModel

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

	// Default framework and language if not provided
	if opts.Framework == "" {
		opts.Framework = "custom"
	}
	if opts.Language == "" {
		opts.Language = "python"
	}

	// Validate framework
	switch opts.Framework {
	case "crewai", "langchain", "custom":
	default:
		return fmt.Errorf("invalid framework %q: must be crewai, langchain, or custom", opts.Framework)
	}

	// Validate language
	switch opts.Framework {
	case "crewai", "langchain":
		if opts.Language != "python" {
			return fmt.Errorf("framework %q only supports python", opts.Framework)
		}
	case "custom":
		switch opts.Language {
		case "python", "typescript", "go":
		default:
			return fmt.Errorf("invalid language %q: must be python, typescript, or go", opts.Language)
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
		regSkills, err := skillreg.LoadIndex()
		if err != nil {
			fmt.Printf("Warning: could not load skill registry: %s\n", err)
		} else {
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
	for _, skillName := range opts.Skills {
		info := skillreg.GetSkillByName(skillName)
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

// lookupSelectedSkills returns SkillInfo entries for the selected skill names.
func lookupSelectedSkills(skillNames []string) []skillreg.SkillInfo {
	var result []skillreg.SkillInfo
	for _, name := range skillNames {
		info := skillreg.GetSkillByName(name)
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

	// Write .env file with collected env vars
	if err := writeEnvFile(dir, data.EnvVars); err != nil {
		return fmt.Errorf("writing .env file: %w", err)
	}

	// Vendor selected registry skills
	for _, skillName := range opts.Skills {
		content, err := skillreg.LoadSkillFile(skillName)
		if err != nil {
			fmt.Printf("Warning: could not load skill file for %q: %s\n", skillName, err)
			continue
		}
		skillPath := filepath.Join(dir, "skills", skillName+".md")
		if err := os.WriteFile(skillPath, content, 0o644); err != nil {
			return fmt.Errorf("writing skill file %s: %w", skillName, err)
		}

		// Vendor script if the skill has one
		if skillreg.HasSkillScript(skillName) {
			scriptContent, sErr := skillreg.LoadSkillScript(skillName)
			if sErr == nil {
				scriptDir := filepath.Join(dir, "skills", "scripts")
				_ = os.MkdirAll(scriptDir, 0o755)
				scriptPath := filepath.Join(scriptDir, skillName+".sh")
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
		{TemplatePath: "skills.md.tmpl", OutputPath: "skills.md"},
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
	case "custom":
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

	// Set entrypoint based on framework/language
	switch opts.Framework {
	case "crewai", "langchain":
		data.Entrypoint = "python agent.py"
	case "custom":
		switch opts.Language {
		case "python":
			data.Entrypoint = "python agent.py"
		case "typescript":
			data.Entrypoint = "bun run agent.ts"
		case "go":
			data.Entrypoint = "go run main.go"
		}
	}

	// Set default model name based on provider
	switch opts.ModelProvider {
	case "openai":
		data.ModelName = "gpt-4o-mini"
	case "anthropic":
		data.ModelName = "claude-sonnet-4-20250514"
	case "gemini":
		data.ModelName = "gemini-2.5-flash"
	case "ollama":
		data.ModelName = "llama3"
	default:
		if opts.CustomModel != "" {
			data.ModelName = opts.CustomModel
		} else {
			data.ModelName = "default"
		}
	}

	// Build skill entries for templates
	for _, skillName := range opts.Skills {
		info := skillreg.GetSkillByName(skillName)
		if info != nil {
			data.SkillEntries = append(data.SkillEntries, skillTmplData{
				Name:        info.Name,
				DisplayName: info.DisplayName,
				Description: info.Description,
			})
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

	return data
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

	// Skill env vars
	for _, skillName := range opts.Skills {
		info := skillreg.GetSkillByName(skillName)
		if info == nil {
			continue
		}
		for _, env := range info.RequiredEnv {
			val := opts.EnvVars[env]
			if val == "" {
				val = ""
			}
			vars = append(vars, envVarEntry{Key: env, Value: val, Comment: fmt.Sprintf("Required by %s skill", skillName)})
		}
		if len(info.OneOfEnv) > 0 {
			for _, env := range info.OneOfEnv {
				val := opts.EnvVars[env]
				vars = append(vars, envVarEntry{
					Key:     env,
					Value:   val,
					Comment: fmt.Sprintf("One of required by %s skill", skillName),
				})
			}
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

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
