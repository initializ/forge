package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/util"
	"github.com/initializ/forge/forge-core/validate"
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

	// Build the AgentStartFunc that wires into forge-cli's runtime.
	startFunc := func(ctx context.Context, agentDir string, port int) error {
		cfgPath := filepath.Join(agentDir, "forge.yaml")
		cfg, err := config.LoadForgeConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		result := validate.ValidateForgeConfig(cfg)
		if !result.IsValid() {
			for _, e := range result.Errors {
				fmt.Fprintf(os.Stderr, "[%s] ERROR: %s\n", cfg.AgentID, e)
			}
			return fmt.Errorf("config validation failed: %d error(s)", len(result.Errors))
		}

		// Load .env
		envPath := filepath.Join(agentDir, ".env")
		envVars, err := runtime.LoadEnvFile(envPath)
		if err != nil {
			return fmt.Errorf("loading env: %w", err)
		}
		for k, v := range envVars {
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}

		// Overlay secrets
		runtime.OverlaySecretsToEnv(cfg, agentDir)

		runner, err := runtime.NewRunner(runtime.RunnerConfig{
			Config:      cfg,
			WorkDir:     agentDir,
			Port:        port,
			EnvFilePath: envPath,
			Verbose:     verbose,
		})
		if err != nil {
			return fmt.Errorf("creating runner: %w", err)
		}

		return runner.Run(ctx)
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

	server := forgeui.NewUIServer(forgeui.UIServerConfig{
		Port:        uiPort,
		WorkDir:     workDir,
		StartFunc:   startFunc,
		CreateFunc:  createFunc,
		OAuthFunc:   oauthFunc,
		AgentPort:   9100,
		OpenBrowser: !uiNoOpen,
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
