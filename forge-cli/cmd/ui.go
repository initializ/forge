package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/llm/providers"
	"github.com/initializ/forge/forge-core/settings"
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
			AWSRegion:      opts.AWSRegion,
			Fallbacks:      fallbacks,
			Channels:       opts.Channels,
			BuiltinTools:   opts.BuiltinTools,
			Skills:         opts.Skills,
			EnvVars:        opts.EnvVars,
			Force:          opts.Force,
			NonInteractive: true,
			Description:    opts.Description,
			SystemPrompt:   opts.SystemPrompt,
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

		// Web UI auth chain selection (PR6). Translate the wizard payload
		// into the same fields the TUI wizard / non-interactive flags use,
		// so scaffold() has a single source of truth.
		if opts.Auth != nil && opts.Auth.Mode != "" {
			initOpts.AuthMode = opts.Auth.Mode
			initOpts.AuthSettings = opts.Auth.Settings
			initOpts.AuthEgressHosts = authEgressHostsFromSettings(opts.Auth.Mode, opts.Auth.Settings)
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
	//
	// Per issue #92, this callback consumes the workspace-level LLM
	// configuration resolved by forge-ui (opts.LLM) and DOES NOT re-read
	// the agent's forge.yaml / .env or mutate the UI process's env.
	// Per-agent credentials live with the agent runtime, not the skill
	// builder.
	llmStreamFunc := func(ctx context.Context, opts forgeui.LLMStreamOptions) error {
		if opts.LLM.Provider == "" {
			return fmt.Errorf("skill-builder LLM is not configured (no workspace ui.yaml and no agent fallback available)")
		}

		// Resolve auth for the builder LLM exactly the way `forge run` /
		// `forge try` do, so the skill/agent builder authenticates through
		// the operator's existing login:
		//
		//   1. A model gateway from settings (managed OR user layer) for this
		//      provider — overlays base_url + auth_scheme (bearer / x-api-key /
		//      apikey_header) and injects the token from ~/.forge/credentials,
		//      minting/refreshing it via the gateway's api_key_helper. This is
		//      the enterprise-Anthropic path (and works for OpenAI gateways):
		//      settings decide HOW the token is sent.
		//   2. Otherwise, for openai with a stored ChatGPT OAuth token, the
		//      codex Responses OAuth client.
		//   3. Otherwise the explicit API key forge-ui resolved.
		clientCfg := llm.ClientConfig{
			Model:   opts.LLM.Model,
			APIKey:  opts.LLM.APIKey,
			BaseURL: opts.LLM.BaseURL,
		}

		var client llm.Client
		gwApplied, gwErr := applyBuilderGateway(ctx, workDir, opts.LLM.Provider, &clientCfg)
		if gwErr != nil {
			return gwErr
		}
		switch {
		case gwApplied:
			c, err := providers.NewClient(opts.LLM.Provider, clientCfg)
			if err != nil {
				return fmt.Errorf("creating gateway LLM client: %w", err)
			}
			client = c
		case opts.LLM.UseOAuth:
			c, err := buildSkillBuilderOAuthClient(opts.LLM.Provider, clientCfg)
			if err != nil {
				return err
			}
			client = c
		default:
			c, err := providers.NewClient(opts.LLM.Provider, clientCfg)
			if err != nil {
				return fmt.Errorf("creating LLM client: %w", err)
			}
			client = c
		}

		// Build chat request with system prompt + conversation messages.
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
			Model:    opts.LLM.Model,
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

	// Build the SkillSaveFunc for saving generated skills. The
	// actual disk-writing logic lives in SaveSkillToDisk so it's
	// directly unit-testable — particularly the edit-mode
	// scripts/-cleanup behavior (issue #193).
	skillSaveFunc := SaveSkillToDisk

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

// applyBuilderGateway overlays a settings model gateway (managed or user
// layer) for provider onto cfg — base_url + auth_scheme + auth_header_name —
// and, when the gateway declares an api_key_helper, injects a fresh token
// from the credential cache (~/.forge/credentials), minting/refreshing it via
// the helper on expiry. It mirrors the runtime's applyGatewaySettings so the
// skill/agent builder authenticates exactly like `forge run` / `forge try`.
// Returns true when a gateway matched (the caller then builds a native client
// whose auth_scheme carries bearer / x-api-key / apikey_header).
//
// Only TRUSTED layers (system + user, never a cloned repo's project
// .forge/settings.json) can supply base_url / api_key_helper — the same trust
// boundary the runtime enforces (#464), so a hostile repo can't redirect the
// endpoint or run a host command.
func applyBuilderGateway(ctx context.Context, workDir, provider string, cfg *llm.ClientConfig) (bool, error) {
	if provider == "" {
		return false, nil
	}
	layers, err := settings.LoadAllLayers(settings.LoadOptions{WorkingDir: workDir})
	if err != nil {
		// No/!unreadable settings → no gateway; the native/OAuth path applies.
		return false, nil
	}
	gw := settings.Resolve(settings.TrustedGatewayLayers(layers)).Models.GatewayForProvider(provider)
	if gw == nil {
		return false, nil
	}
	if gw.BaseURL != "" {
		cfg.BaseURL = gw.BaseURL
	}
	if gw.AuthScheme != "" {
		cfg.AuthScheme = gw.AuthScheme
	}
	if gw.AuthHeaderName != "" {
		cfg.AuthHeaderName = gw.AuthHeaderName
	}
	if gw.APIKeyHelper != "" {
		tok, err := runtime.EnsureGatewayToken(ctx, gw.APIKeyHelper, gw.Env)
		if err != nil {
			return true, fmt.Errorf("gateway login failed for %s (run 'forge auth login'): %w", provider, err)
		}
		if tok != nil && tok.AccessToken != "" {
			cfg.APIKey = tok.AccessToken
		}
	}
	return true, nil
}

// buildSkillBuilderOAuthClient constructs the OAuth-aware LLM client the
// skill/agent builder uses when credentials come from a stored ChatGPT OAuth
// token (uiconfig.SkillBuilderLLM.UseOAuth) and no gateway overrides it. It
// mirrors the runtime's createProviderClient OAuth path: load the token,
// route through its base URL (or the provider default), and hand off to
// providers.NewOAuthClient, which refreshes on each call. OAuth without a
// gateway is only wired for provider=openai (the codex Responses backend);
// enterprise Anthropic goes through the gateway path above.
func buildSkillBuilderOAuthClient(provider string, cfg llm.ClientConfig) (llm.Client, error) {
	if provider != "openai" {
		return nil, fmt.Errorf("OAuth builder auth without a gateway is not supported for provider %q (configure a model gateway in settings)", provider)
	}
	token, err := oauth.LoadCredentials(provider)
	if err != nil {
		return nil, fmt.Errorf("loading global OAuth credentials for %s: %w", provider, err)
	}
	if token == nil || token.RefreshToken == "" {
		return nil, fmt.Errorf("no usable global OAuth credentials for %s; run 'forge init' with OAuth or set an API key in Settings → Skill Builder", provider)
	}
	oauthCfg := oauth.OpenAIConfig()
	baseURL := token.BaseURL
	if baseURL == "" {
		baseURL = oauthCfg.BaseURL
	}
	cfg.APIKey = token.AccessToken
	cfg.BaseURL = baseURL
	return providers.NewOAuthClient(cfg, provider, oauthCfg), nil
}
