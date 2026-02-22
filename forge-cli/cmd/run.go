package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/initializ/forge/forge-cli/channels"
	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/validate"
	"github.com/spf13/cobra"
)

var (
	runPort              int
	runMockTools         bool
	runEnforceGuardrails bool
	runModel             string
	runProvider          string
	runEnvFile           string
	runWithChannels      string
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the agent locally with an A2A-compliant dev server",
	RunE:  runRun,
}

func init() {
	runCmd.Flags().IntVar(&runPort, "port", 8080, "port for the A2A dev server")
	runCmd.Flags().BoolVar(&runMockTools, "mock-tools", false, "use mock runtime instead of subprocess")
	runCmd.Flags().BoolVar(&runEnforceGuardrails, "enforce-guardrails", false, "enforce guardrail violations as errors")
	runCmd.Flags().StringVar(&runModel, "model", "", "override model name (sets MODEL_NAME env var)")
	runCmd.Flags().StringVar(&runProvider, "provider", "", "LLM provider (openai, anthropic, ollama)")
	runCmd.Flags().StringVar(&runEnvFile, "env", ".env", "path to .env file")
	runCmd.Flags().StringVar(&runWithChannels, "with", "", "comma-separated channel adapters to start (e.g. slack,telegram)")
}

func runRun(cmd *cobra.Command, args []string) error {
	cfgPath := cfgFile
	if !filepath.IsAbs(cfgPath) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		cfgPath = filepath.Join(wd, cfgPath)
	}

	cfg, err := config.LoadForgeConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	result := validate.ValidateForgeConfig(cfg)
	if !result.IsValid() {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
		}
		return fmt.Errorf("config validation failed: %d error(s)", len(result.Errors))
	}

	workDir := filepath.Dir(cfgPath)

	// Resolve env file path relative to workdir
	envPath := runEnvFile
	if !filepath.IsAbs(envPath) {
		envPath = filepath.Join(workDir, envPath)
	}

	// Load .env into process environment so channel adapters can resolve env vars
	envVars, err := runtime.LoadEnvFile(envPath)
	if err != nil {
		return fmt.Errorf("loading env file: %w", err)
	}
	for k, v := range envVars {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}

	// Parse channel names from --with flag for banner display
	var activeChannels []string
	if runWithChannels != "" {
		for _, name := range strings.Split(runWithChannels, ",") {
			if n := strings.TrimSpace(name); n != "" {
				activeChannels = append(activeChannels, n)
			}
		}
	}

	runner, err := runtime.NewRunner(runtime.RunnerConfig{
		Config:            cfg,
		WorkDir:           workDir,
		Port:              runPort,
		MockTools:         runMockTools,
		EnforceGuardrails: runEnforceGuardrails,
		ModelOverride:     runModel,
		ProviderOverride:  runProvider,
		EnvFilePath:       envPath,
		Verbose:           verbose,
		Channels:          activeChannels,
	})
	if err != nil {
		return fmt.Errorf("creating runner: %w", err)
	}

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down...")
		cancel()
	}()

	// Start channel adapters if --with flag is set
	if runWithChannels != "" {
		registry := defaultRegistry()
		agentURL := fmt.Sprintf("http://localhost:%d", runPort)
		router := channels.NewRouter(agentURL)

		names := strings.Split(runWithChannels, ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}

			plugin := registry.Get(name)
			if plugin == nil {
				return fmt.Errorf("unknown channel adapter: %s", name)
			}

			chCfgPath := filepath.Join(workDir, name+"-config.yaml")
			chCfg, err := channels.LoadChannelConfig(chCfgPath)
			if err != nil {
				return fmt.Errorf("loading %s config: %w", name, err)
			}

			if err := plugin.Init(*chCfg); err != nil {
				return fmt.Errorf("initialising %s: %w", name, err)
			}

			defer plugin.Stop() //nolint:errcheck

			go func() {
				if err := plugin.Start(ctx, router.Handler()); err != nil {
					fmt.Fprintf(os.Stderr, "channel %s error: %v\n", plugin.Name(), err)
				}
			}()

			fmt.Fprintf(os.Stderr, "  Channel:    %s adapter started\n", name)
		}
	}

	return runner.Run(ctx)
}
