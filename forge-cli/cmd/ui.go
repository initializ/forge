package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/llm/providers"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/util"
	forgeui "github.com/initializ/forge/forge-ui"
	"github.com/spf13/cobra"
)

var (
	uiPort   int
	uiDir    string
	uiNoOpen bool
)

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Launch the local agent dashboard",
	Long:  "Start a web dashboard for managing, monitoring, and interacting with agents in a workspace.",
	RunE:  runUI,
}

func init() {
	uiCmd.Flags().IntVar(&uiPort, "port", 4200, "dashboard server port")
	uiCmd.Flags().StringVar(&uiDir, "dir", "", "workspace directory (default: current directory)")
	uiCmd.Flags().BoolVar(&uiNoOpen, "no-open", false, "do not open browser automatically")
}

func runUI(cmd *cobra.Command, args []string) error {
	workDir := uiDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		workDir = wd
	}

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("resolving directory: %w", err)
	}
	workDir = absDir

	// Find forge executable path for daemon management.
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding forge executable: %w", err)
	}

	// Build the AgentCreateFunc that wraps scaffold() from init.go.
	createFunc := func(opts forgeui.AgentCreateOptions) (string, error) {
		// Convert fallback providers
		var fallbacks []tui.FallbackProvider
		for _, fb := range opts.Fallbacks {
			fallbacks = append(fallbacks, tui.FallbackProvider{
				Provider: fb.Provider,
				APIKey:   fb.APIKey,
			})
		}

		initOpts := &initOptions{
			Name:           opts.Name,
			AgentID:        util.Slugify(opts.Name),
			Framework:      opts.Framework,
			ModelProvider:  opts.ModelProvider,
			CustomModel:    opts.ModelName,
			APIKey:         opts.APIKey,
			AuthMethod:     opts.AuthMethod,
			OrganizationID: opts.OrganizationID,
			Fallbacks:      fallbacks,
			Channels:       opts.Channels,
			BuiltinTools:   opts.BuiltinTools,
			Skills:         opts.Skills,
			EnvVars:        opts.EnvVars,
			Force:          opts.Force,
			NonInteractive: true,
		}
		if initOpts.Framework == "" {
			initOpts.Framework = "forge"
		}
		if initOpts.EnvVars == nil {
			initOpts.EnvVars = make(map[string]string)
		}

		// Store web search provider preference
		if opts.WebSearchProvider != "" {
			initOpts.EnvVars["WEB_SEARCH_PROVIDER"] = opts.WebSearchProvider
		}

		// Store organization ID for OpenAI enterprise
		if opts.OrganizationID != "" {
			initOpts.EnvVars["OPENAI_ORG_ID"] = opts.OrganizationID
		}

		// Set passphrase for secret encryption if provided
		if opts.Passphrase != "" {
			_ = os.Setenv("FORGE_PASSPHRASE", opts.Passphrase)
		}

		storeProviderEnvVar(initOpts)
		checkSkillRequirements(initOpts)

		// scaffold() uses relative paths — chdir to workspace
		origDir, _ := os.Getwd()
		if err := os.Chdir(workDir); err != nil {
			return "", fmt.Errorf("changing to workspace: %w", err)
		}
		defer func() { _ = os.Chdir(origDir) }()

		if err := scaffold(initOpts); err != nil {
			return "", err
		}
		return filepath.Join(workDir, initOpts.AgentID), nil
	}

	// Build the OAuthFlowFunc for browser-based login.
	oauthFunc := func(provider string) (string, error) {
		return runOAuthFlow(provider)
	}

	// Build the LLMStreamFunc for skill builder conversations.
	llmStreamFunc := func(ctx context.Context, opts forgeui.LLMStreamOptions) error {
		// Load agent config
		cfgPath := filepath.Join(opts.AgentDir, "forge.yaml")
		cfg, err := config.LoadForgeConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		// Load .env — force-set values so __oauth__ sentinels from prior
		// handler calls don't block real keys from encrypted secrets.
		envPath := filepath.Join(opts.AgentDir, ".env")
		envVars, err := runtime.LoadEnvFile(envPath)
		if err != nil {
			return fmt.Errorf("loading env: %w", err)
		}
		for k, v := range envVars {
			_ = os.Setenv(k, v)
		}

		// Clear __oauth__ sentinels so OverlaySecretsToEnv can replace them
		// with real keys from the encrypted secrets store.
		for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"} {
			if os.Getenv(key) == "__oauth__" {
				_ = os.Unsetenv(key)
			}
		}

		// Overlay encrypted secrets
		runtime.OverlaySecretsToEnv(cfg, opts.AgentDir)

		// Build env map for model resolution. OS env takes priority over .env
		// file because OverlaySecretsToEnv may have replaced sentinels with
		// real keys from the encrypted store.
		envMap := make(map[string]string)
		for k, v := range envVars {
			if v != "__oauth__" {
				envMap[k] = v
			}
		}
		for _, kv := range os.Environ() {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}

		mc := coreruntime.ResolveModelConfig(cfg, envMap, "")
		if mc == nil {
			return fmt.Errorf("unable to resolve model configuration")
		}

		// Resolve OAuth credentials when the API key is the __oauth__ sentinel
		// or empty. OAuth/Codex backend has its own model constraints, so
		// skip the codegen model upgrade for OAuth clients.
		var client llm.Client
		needsOAuth := mc.Provider == "openai" && (mc.Client.APIKey == "" || mc.Client.APIKey == "__oauth__")
		if needsOAuth {
			token, oauthErr := oauth.LoadCredentials(mc.Provider)
			if oauthErr == nil && token != nil && token.RefreshToken != "" {
				oauthCfg := oauth.OpenAIConfig()
				baseURL := token.BaseURL
				if baseURL == "" {
					baseURL = oauthCfg.BaseURL
				}
				mc.Client.APIKey = token.AccessToken
				mc.Client.BaseURL = baseURL
				client = providers.NewOAuthClient(mc.Client, mc.Provider, oauthCfg)
			} else if mc.Client.APIKey == "" || mc.Client.APIKey == "__oauth__" {
				// No API key and OAuth failed — surface the error instead
				// of silently falling through to a client with no auth.
				if oauthErr != nil {
					return fmt.Errorf("loading OAuth credentials: %w", oauthErr)
				}
				return fmt.Errorf("no OpenAI API key or OAuth credentials found; run 'forge init' with OAuth or set OPENAI_API_KEY")
			}
		}
		if client == nil {
			// Only upgrade to the codegen model for non-OAuth (API key) clients.
			mc.Client.Model = forgeui.SkillBuilderCodegenModel(mc.Provider, mc.Client.Model)
			var clientErr error
			client, clientErr = providers.NewClient(mc.Provider, mc.Client)
			if clientErr != nil {
				return fmt.Errorf("creating LLM client: %w", clientErr)
			}
		}

		// Build chat request with system prompt + conversation messages
		messages := []llm.ChatMessage{
			{Role: "system", Content: opts.SystemPrompt},
		}
		for _, m := range opts.Messages {
			messages = append(messages, llm.ChatMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}

		req := &llm.ChatRequest{
			Model:    mc.Client.Model,
			Messages: messages,
			Stream:   true,
		}

		ch, err := client.ChatStream(ctx, req)
		if err != nil {
			return fmt.Errorf("starting LLM stream: %w", err)
		}

		var fullResponse strings.Builder
		for delta := range ch {
			if delta.Content != "" {
				fullResponse.WriteString(delta.Content)
				if opts.OnChunk != nil {
					opts.OnChunk(delta.Content)
				}
			}
		}

		if opts.OnDone != nil {
			opts.OnDone(fullResponse.String())
		}

		return nil
	}

	// Build the SkillSaveFunc for saving generated skills.
	skillSaveFunc := func(opts forgeui.SkillSaveOptions) (*forgeui.SkillSaveResult, error) {
		skillDir := filepath.Join(opts.AgentDir, "skills", opts.SkillName)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating skill directory: %w", err)
		}

		skillPath := filepath.Join(skillDir, "SKILL.md")
		if err := os.WriteFile(skillPath, []byte(opts.SkillMD), 0o644); err != nil {
			return nil, fmt.Errorf("writing SKILL.md: %w", err)
		}

		if len(opts.Scripts) > 0 {
			scriptsDir := filepath.Join(skillDir, "scripts")
			if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
				return nil, fmt.Errorf("creating scripts directory: %w", err)
			}
			for filename, content := range opts.Scripts {
				scriptPath := filepath.Join(scriptsDir, filename)
				if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
					return nil, fmt.Errorf("writing script %s: %w", filename, err)
				}
			}
		}

		result := &forgeui.SkillSaveResult{
			Path: "skills/" + opts.SkillName + "/SKILL.md",
		}

		// Parse skill requirements from SKILL.md
		reqInfo := ParseSkillRequirements(opts.SkillMD)

		// Write user-provided env vars to .env
		if len(opts.EnvVars) > 0 {
			written, _ := AppendEnvVars(opts.AgentDir, opts.EnvVars, opts.SkillName)
			result.EnvConfigured = written
		}

		// Merge egress domains into forge.yaml
		if len(reqInfo.EgressDomains) > 0 {
			added, _ := MergeEgressDomains(opts.AgentDir, reqInfo.EgressDomains)
			result.EgressAdded = added
		}

		// Check for missing env vars
		if reqInfo.EnvReqs != nil {
			result.EnvMissing = CheckMissingEnv(opts.AgentDir, reqInfo.EnvReqs)
		}

		return result, nil
	}

	server := forgeui.NewUIServer(forgeui.UIServerConfig{
		Port:          uiPort,
		WorkDir:       workDir,
		ExePath:       exePath,
		Version:       appVersion,
		CreateFunc:    createFunc,
		OAuthFunc:     oauthFunc,
		LLMStreamFunc: llmStreamFunc,
		SkillSaveFunc: skillSaveFunc,
		AgentPort:     9100,
		OpenBrowser:   !uiNoOpen,
	})

	// Signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down dashboard...")
		cancel()
	}()

	return server.Start(ctx)
}
