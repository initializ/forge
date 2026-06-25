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
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/providers"
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

		// Construct the LLM client from the resolved workspace config.
		// No env reading, no os.Setenv. OAuth is intentionally NOT
		// supported on this path — workspace-level config requires an
		// explicit API key under api_key_env. (Operators who want to
		// use ChatGPT OAuth specifically can set OPENAI_API_KEY from
		// the OAuth token themselves; the workspace-LLM design does
		// not silently override an explicit endpoint via the codex
		// backend, per the issue #83 guardrail.)
		clientCfg := llm.ClientConfig{
			Model:   opts.LLM.Model,
			APIKey:  opts.LLM.APIKey,
			BaseURL: opts.LLM.BaseURL,
		}
		client, err := providers.NewClient(opts.LLM.Provider, clientCfg)
		if err != nil {
			return fmt.Errorf("creating LLM client: %w", err)
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
