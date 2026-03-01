package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/initializ/forge/forge-cli/channels"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/a2a"
	corechannels "github.com/initializ/forge/forge-core/channels"
	"github.com/spf13/cobra"
)

var (
	runPort              int
	runHost              string
	runShutdownTimeout   time.Duration
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
	runCmd.Flags().StringVar(&runHost, "host", "", "bind address (e.g. 0.0.0.0 for containers)")
	runCmd.Flags().DurationVar(&runShutdownTimeout, "shutdown-timeout", 0, "graceful shutdown timeout (e.g. 30s)")
	runCmd.Flags().BoolVar(&runMockTools, "mock-tools", false, "use mock runtime instead of subprocess")
	runCmd.Flags().BoolVar(&runEnforceGuardrails, "enforce-guardrails", false, "enforce guardrail violations as errors")
	runCmd.Flags().StringVar(&runModel, "model", "", "override model name (sets MODEL_NAME env var)")
	runCmd.Flags().StringVar(&runProvider, "provider", "", "LLM provider (openai, anthropic, ollama)")
	runCmd.Flags().StringVar(&runEnvFile, "env", ".env", "path to .env file")
	runCmd.Flags().StringVar(&runWithChannels, "with", "", "comma-separated channel adapters to start (e.g. slack,telegram)")
}

func runRun(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadAndPrepareConfig(runEnvFile)
	if err != nil {
		return err
	}

	activeChannels := parseChannels(runWithChannels)

	runner, err := runtime.NewRunner(runtime.RunnerConfig{
		Config:            cfg,
		WorkDir:           workDir,
		Port:              runPort,
		Host:              runHost,
		ShutdownTimeout:   runShutdownTimeout,
		MockTools:         runMockTools,
		EnforceGuardrails: runEnforceGuardrails,
		ModelOverride:     runModel,
		ProviderOverride:  runProvider,
		EnvFilePath:       resolveEnvPath(workDir, runEnvFile),
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

		// Collect initialized plugins so the scheduler can deliver results.
		activePlugins := make(map[string]corechannels.ChannelPlugin)

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

			activePlugins[name] = plugin

			go func() {
				if err := plugin.Start(ctx, router.Handler()); err != nil {
					fmt.Fprintf(os.Stderr, "channel %s error: %v\n", plugin.Name(), err)
				}
			}()

			fmt.Fprintf(os.Stderr, "  Channel:    %s adapter started\n", name)
		}

		// Wire up schedule notifier so cron results are delivered to channels.
		if len(activePlugins) > 0 {
			runner.SetScheduleNotifier(func(ctx context.Context, channel, target string, response *a2a.Message) error {
				plugin, ok := activePlugins[channel]
				if !ok {
					return fmt.Errorf("channel adapter %q not active", channel)
				}
				event := &corechannels.ChannelEvent{
					Channel:     channel,
					WorkspaceID: target,
				}
				return plugin.SendResponse(event, response)
			})
		}
	}

	return runner.Run(ctx)
}
