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
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
	"github.com/spf13/cobra"
)

var (
	runPort              int
	runHost              string
	runShutdownTimeout   time.Duration
	runMockTools         bool
	runEnforceGuardrails bool
	runNoGuardrails      bool
	runModel             string
	runProvider          string
	runEnvFile           string
	runWithChannels      string
	runNoAuth            bool
	runAuthToken         string
	runAuthURL           string
	runAuthOrgID         string
	runCORSOrigins       string

	// FWS-7 audit export sink flags (issue #95). Default zero means
	// "stderr only" — fully backward-compatible with pre-FWS-7. The
	// initializ deploy receiver sets the matching FORGE_AUDIT_* env
	// vars when running an agent under the platform.
	runAuditSocket       string
	runAuditHTTPEndpoint string
	runAuditWriteTimeout time.Duration
)

var runCmd = &cobra.Command{
	Use:          "run",
	Short:        "Run the agent locally with an A2A-compliant dev server",
	SilenceUsage: true,
	RunE:         runRun,
}

func init() {
	runCmd.Flags().IntVar(&runPort, "port", 8080, "port for the A2A dev server")
	runCmd.Flags().StringVar(&runHost, "host", "", "bind address (e.g. 0.0.0.0 for containers)")
	runCmd.Flags().DurationVar(&runShutdownTimeout, "shutdown-timeout", 0, "graceful shutdown timeout (e.g. 30s)")
	runCmd.Flags().BoolVar(&runMockTools, "mock-tools", false, "use mock runtime instead of subprocess")
	runCmd.Flags().BoolVar(&runEnforceGuardrails, "enforce-guardrails", true, "enforce guardrail violations as errors")
	runCmd.Flags().BoolVar(&runNoGuardrails, "no-guardrails", false, "disable all guardrail enforcement")
	runCmd.Flags().StringVar(&runModel, "model", "", "override model name (sets MODEL_NAME env var)")
	runCmd.Flags().StringVar(&runProvider, "provider", "", "LLM provider (openai, anthropic, ollama)")
	runCmd.Flags().StringVar(&runEnvFile, "env", ".env", "path to .env file")
	runCmd.Flags().StringVar(&runWithChannels, "with", "", "comma-separated channel adapters to start (e.g. slack,telegram)")
	runCmd.Flags().BoolVar(&runNoAuth, "no-auth", false, "disable bearer token authentication (localhost only)")
	runCmd.Flags().StringVar(&runAuthToken, "auth-token", "", "explicit bearer token (default: auto-generated)")
	runCmd.Flags().StringVar(&runAuthURL, "auth-url", "", "external auth provider URL for token validation (e.g. https://auth.example.com/verify)")
	runCmd.Flags().StringVar(&runAuthOrgID, "auth-org-id", "", "org_id sent to the external auth provider")
	runCmd.Flags().StringVar(&runCORSOrigins, "cors-origins", "", "comma-separated CORS allowed origins (default: localhost only, use '*' for wildcard)")

	// FWS-7 — audit export (issue #95). All three default to the
	// matching FORGE_AUDIT_* env var (resolved in runRun) so
	// platform deployers can inject via env without per-agent CLI
	// args. Flag wins when set explicitly.
	runCmd.Flags().StringVar(&runAuditSocket, "audit-socket", "", "Unix socket path to export audit events to (sidecar consumer); empty = stderr only")
	runCmd.Flags().StringVar(&runAuditHTTPEndpoint, "audit-http-endpoint", "", "localhost HTTP endpoint to POST audit events to (used only when --audit-socket is empty)")
	runCmd.Flags().DurationVar(&runAuditWriteTimeout, "audit-write-timeout", 0, "per-event timeout for the audit socket/HTTP sink (default 50ms)")
}

func runRun(cmd *cobra.Command, args []string) error {
	cfg, workDir, err := loadAndPrepareConfig(runEnvFile)
	if err != nil {
		return err
	}

	activeChannels := parseChannels(runWithChannels)

	enforceGuardrails := runEnforceGuardrails
	if runNoGuardrails {
		enforceGuardrails = false
	}

	var corsOrigins []string
	if runCORSOrigins != "" {
		for _, o := range strings.Split(runCORSOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				corsOrigins = append(corsOrigins, o)
			}
		}
	}

	// Resolve audit-export config. Start with env-var defaults; flag
	// values (when non-empty / non-zero) override. Empty after this
	// merge means "no export sink; stderr only" — pre-FWS-7 behavior.
	auditExport := coreruntime.AuditExportConfigFromEnv()
	if runAuditSocket != "" {
		auditExport.SocketPath = runAuditSocket
	}
	if runAuditHTTPEndpoint != "" {
		auditExport.HTTPEndpoint = runAuditHTTPEndpoint
	}
	if runAuditWriteTimeout > 0 {
		auditExport.WriteTimeout = runAuditWriteTimeout
	}

	runner, err := runtime.NewRunner(runtime.RunnerConfig{
		Config:            cfg,
		WorkDir:           workDir,
		Port:              runPort,
		Host:              runHost,
		ShutdownTimeout:   runShutdownTimeout,
		MockTools:         runMockTools,
		EnforceGuardrails: enforceGuardrails,
		ModelOverride:     runModel,
		ProviderOverride:  runProvider,
		EnvFilePath:       resolveEnvPath(workDir, runEnvFile),
		Verbose:           verbose,
		Channels:          activeChannels,
		NoAuth:            runNoAuth,
		AuthToken:         runAuthToken,
		AuthURL:           runAuthURL,
		AuthOrgID:         runAuthOrgID,
		CORSOrigins:       corsOrigins,
		AuditExport:       auditExport,
	})
	if err != nil {
		return fmt.Errorf("creating runner: %w", err)
	}

	// Resolve auth token early so channel adapters can use it.
	if err := runner.ResolveAuth(); err != nil {
		return fmt.Errorf("resolving auth: %w", err)
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
		router := channels.NewRouter(agentURL, runner.AuthToken())

		// Collect initialized plugins so the scheduler can deliver results.
		activePlugins := make(map[string]corechannels.ChannelPlugin)

		// Apply channel filtering (issue #90 / FWS-6 three-layer):
		// each declared channel runs through the union of system / user
		// / workspace policy denies. One channel_denied_by_policy
		// audit event per skip (carrying the deciding layer). Effective
		// list goes on to start adapters.
		channelLayers, channelLayerErr := security.LoadAllPolicyLayers()
		if channelLayerErr != nil {
			return fmt.Errorf("loading policy layers for channel filter: %w", channelLayerErr)
		}
		requested := strings.Split(runWithChannels, ",")
		var trimmed []string
		for _, n := range requested {
			if n = strings.TrimSpace(n); n != "" {
				trimmed = append(trimmed, n)
			}
		}
		effective, skipped := security.EffectiveChannels(trimmed, channelLayers)
		channelAudit := coreruntime.NewAuditLogger(os.Stderr)
		for _, s := range skipped {
			channelAudit.EmitChannelDeniedByPolicy(s.Channel, s.Layer, s.LayerPath)
			fmt.Fprintf(os.Stderr, "  Channel:    %s skipped (denied by %s policy)\n", s.Channel, s.Layer)
		}

		for _, name := range effective {
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
