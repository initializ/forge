package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/initializ/forge/forge-cli/server"
	cliskills "github.com/initializ/forge/forge-cli/skills"
	clitools "github.com/initializ/forge/forge-cli/tools"
	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/auth"
	// Side-effect imports: each provider sub-package registers its factory
	// with the auth registry via init() so forge.yaml `auth.providers[]`
	// blocks construct successfully via auth.Build("<type>", settings).
	// Listed here even when the package is also referenced directly
	// (httpverifier, statictoken) for grep-ability.
	_ "github.com/initializ/forge/forge-core/auth/providers/aws_sigv4"
	_ "github.com/initializ/forge/forge-core/auth/providers/azure_ad"
	_ "github.com/initializ/forge/forge-core/auth/providers/gcp_iap"
	"github.com/initializ/forge/forge-core/auth/providers/httpverifier"
	_ "github.com/initializ/forge/forge-core/auth/providers/oidc"
	"github.com/initializ/forge/forge-core/auth/providers/statictoken"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/llm/providers"
	"github.com/initializ/forge/forge-core/memory"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/scheduler"
	"github.com/initializ/forge/forge-core/secrets"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/adapters"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/requirements"
	"github.com/initializ/forge/forge-skills/resolver"
)

// RunnerConfig holds configuration for the Runner.
type RunnerConfig struct {
	Config            *types.ForgeConfig
	WorkDir           string
	Port              int
	Host              string        // bind host (e.g. "127.0.0.1" for serve, "" for run)
	ShutdownTimeout   time.Duration // graceful shutdown timeout (0 = immediate)
	MockTools         bool
	EnforceGuardrails bool
	ModelOverride     string
	ProviderOverride  string
	EnvFilePath       string
	Verbose           bool
	Channels          []string // active channel adapters from --with flag
	NoAuth            bool     // disable bearer token authentication
	AuthToken         string   // explicit bearer token (empty = auto-generate)
	AuthURL           string   // external auth provider URL for token validation
	AuthOrgID         string   // org_id sent to external auth provider
	CORSOrigins       []string // CORS allowed origins (from --cors-origins flag)

	// AuditExport configures the FWS-7 audit export sinks (Unix socket
	// or localhost HTTP fallback). Zero value = pre-FWS-7 behavior
	// (stderr only). See issue #95.
	AuditExport coreruntime.AuditExportConfig

	// AuditPayloadCapture is the opt-in raw-payload capture for audit
	// events: LLM messages / completions, tool args / results. All
	// flags default off (metadata-only audit). See issue #91 / FWS-8
	// and docs/security/audit-logging.md#payload-capture-fws-8.
	AuditPayloadCapture coreruntime.AuditPayloadCapture

	// RateLimitOverride carries CLI-flag-derived overrides for the
	// per-IP A2A rate limiter. Nil = no CLI overrides; the resolver
	// will fall through to FORGE_RATE_LIMIT_* env vars and
	// cfg.Server.RateLimit before defaulting to the FWS-10 baseline.
	// See issue #110 / FWS-10.
	RateLimitOverride *RateLimitOverride

	// TracingFlags carries CLI-flag-derived OTel tracing overrides.
	// Zero value = "no CLI overrides"; the runner's tracing resolver
	// falls through to env (OTEL_*) and the
	// observability.tracing block of forge.yaml. See issue #103 / OTel
	// Tracing v1 (initiative #108).
	TracingFlags TracingFlags

	// RuntimeVersion is the Forge cli's own build version. Used for
	// the `forge.runtime.version` OTel resource attribute so backends
	// can compare agent runs across Forge upgrade waves. Empty = "dev".
	RuntimeVersion string
}

// ScheduleNotifier is called after a scheduled task completes to deliver the
// result to the appropriate channel (e.g. Slack, Telegram).
type ScheduleNotifier func(ctx context.Context, channel, target string, response *a2a.Message) error

// codeAgentDirective is appended to the system prompt when code-agent skill
// is active. Forces the LLM to always call tools — never respond with text only.
const codeAgentDirective = `## Code Agent — MANDATORY RULES

You are a coding agent. Every response MUST include tool calls. NEVER respond with only text.

FORBIDDEN:
- Respond with "I'll do X now" or "Let me X" without calling tools in the same response
- Output code in markdown blocks for the user to copy-paste
- Ask the user for permission or confirmation before acting
- Describe what you plan to do without simultaneously doing it
- Read files unrelated to the error path or code you plan to change
- Edit test files before fixing the source code — always fix source first, then update tests

REQUIRED:
- New project → code_agent_scaffold → code_agent_write (all files) → code_agent_run
- Modify existing code → search + trace error origin + read functions to change → code_agent_edit or code_agent_write
- Any request → ACT IMMEDIATELY with tools. Write ALL files and run in ONE turn.

EXPLORATION RULES:
Bug fixes: search for the error message → trace to its origin (not just where it surfaces) → read functions you plan to call or replace → edit.
Features: search for similar patterns (2-3 searches) → read files you plan to modify → edit.
Both: complete the workflow (commit/push/PR if applicable).
Do NOT read files unrelated to the error path or code you plan to change. Do NOT replace function calls without reading both the old and new function.

VERIFY BUG FIXES:
After editing, trace the failing input through your new code. Read the functions your fix calls — confirm they handle the type that was failing. If the codebase has a working path for similar logic (e.g., another provider), your fix must use the same approach. Type annotations alone do not fix runtime bugs.`

// Runner orchestrates the local A2A development server.
type Runner struct {
	cfg              RunnerConfig
	logger           coreruntime.Logger
	cliExecTool      *clitools.CLIExecuteTool
	modelConfig      *coreruntime.ModelConfig          // resolved model config (for banner)
	derivedCLIConfig *contract.DerivedCLIConfig        // auto-derived from skill requirements
	skillGuardrails  *agentspec.SkillGuardrailRules    // runtime-parsed skill guardrails (fallback when no build artifact)
	schedBackend     scheduler.Backend                 // schedule backend (nil until started); FileBackend in non-cluster deploys, KubernetesBackend (#162 part 2b) when running in-cluster with scheduler.backend=auto|kubernetes
	startTime        time.Time                         // server start time (for /health uptime)
	scheduleNotifier ScheduleNotifier                  // optional: delivers cron results to channels
	authToken        string                            // resolved auth token (empty if --no-auth)
	cancelRegistry   *coreruntime.CancellationRegistry // per-Runner in-flight cancellation registry (issue #88 / FWS-4)
}

// NewRunner creates a Runner from the given config.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.Port <= 0 {
		cfg.Port = 8080
	}
	// FWS-9 (issue #100): ops logs go to stdout so audit NDJSON on
	// stderr can be consumed as a single-stream concern by container
	// log collectors and SIEM pipelines — no payload parsing needed
	// to split ops from audit. Audit destination is unchanged; it
	// remains on stderr (with the FWS-7 dedicated sink overlay when
	// configured).
	logger := coreruntime.NewJSONLogger(os.Stdout, cfg.Verbose)
	return &Runner{
		cfg:            cfg,
		logger:         logger,
		cancelRegistry: coreruntime.NewCancellationRegistry(),
	}, nil
}

// SetScheduleNotifier sets the callback used to deliver scheduled task results
// to channel adapters. Must be called before Run().
func (r *Runner) SetScheduleNotifier(fn ScheduleNotifier) {
	r.scheduleNotifier = fn
}

// ResolveAuth resolves the auth token early (before Run). This is needed so
// channel adapters can be configured with the token before Run() blocks.
// Safe to call multiple times — subsequent calls are no-ops.
//
// Invariant: after this returns nil, EITHER r.authToken is non-empty OR
// r.cfg.NoAuth is true. resolveAuth() relies on this when it conditionally
// prepends the loopback static_token (review #10). If a future refactor
// adds a return path that violates this invariant, channel-adapter
// callbacks will silently break — the test
// TestResolveAuth_InvariantMintsTokenInNonNoAuthPath in
// auth_chain_test.go pins the property.
func (r *Runner) ResolveAuth() error {
	if r.authToken != "" || r.cfg.NoAuth {
		return nil // already resolved
	}
	// Fall back to env vars for external auth configuration.
	if r.cfg.AuthURL == "" {
		r.cfg.AuthURL = os.Getenv("FORGE_AUTH_URL")
	}
	if r.cfg.AuthOrgID == "" {
		r.cfg.AuthOrgID = os.Getenv("FORGE_AUTH_ORG_ID")
	}
	// When using an external auth URL, still generate an internal token
	// for channel adapter loopback calls, but external requests are
	// validated against the auth provider.
	if r.cfg.AuthURL != "" {
		token, err := auth.GenerateToken()
		if err != nil {
			return fmt.Errorf("generating internal auth token: %w", err)
		}
		r.authToken = token
		return nil
	}
	local := isLocalhost(r.cfg.Host)
	if r.cfg.NoAuth && !local {
		return fmt.Errorf("--no-auth is only allowed when binding to localhost (current host: %s)", r.cfg.Host)
	}
	token := r.cfg.AuthToken
	if token == "" {
		var err error
		token, err = auth.GenerateToken()
		if err != nil {
			return fmt.Errorf("generating auth token: %w", err)
		}
	}
	r.authToken = token
	if err := auth.StoreToken(r.cfg.WorkDir, token); err != nil {
		return fmt.Errorf("storing auth token: %w", err)
	}
	ensureGitignore(r.cfg.WorkDir)
	return nil
}

// AuthToken returns the resolved bearer token. Empty if auth is disabled.
func (r *Runner) AuthToken() string {
	return r.authToken
}

// Run starts the development server. It blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	// 0. Materialize inline KUBECONFIG content to a file.
	if materialized, err := materializeKubeconfig(r.cfg.WorkDir); err != nil {
		r.logger.Warn("failed to materialize KUBECONFIG", map[string]any{"error": err.Error()})
	} else if materialized {
		r.logger.Info("materialized inline KUBECONFIG to file", map[string]any{
			"path": os.Getenv("KUBECONFIG"),
		})
	}

	// 0b. Verify build output integrity if checksums.json exists.
	// Inside a Forge container, .forge-output/ is flattened into
	// WorkDir (typically /app) — the .dockerignore drops the dir
	// while keeping checksums.json at /app/checksums.json. On the
	// operator side `forge run` is invoked next to forge.yaml, so
	// the build output still lives under <WorkDir>/.forge-output/.
	// Try the operator-side layout first, then fall back to the
	// flattened container layout (issue #147).
	outputDir := filepath.Join(r.cfg.WorkDir, ".forge-output")
	if _, err := os.Stat(filepath.Join(outputDir, "checksums.json")); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(r.cfg.WorkDir, "checksums.json")); err == nil {
			outputDir = r.cfg.WorkDir
		}
	}
	if err := VerifyBuildOutput(outputDir); err != nil {
		r.logger.Warn("build output verification failed", map[string]any{"error": err.Error()})
	}

	// 1. Load .env file
	envVars, err := LoadEnvFile(r.cfg.EnvFilePath)
	if err != nil {
		return fmt.Errorf("loading env file: %w", err)
	}

	// Overlay secrets from configured providers
	if err := r.overlaySecrets(envVars); err != nil {
		return fmt.Errorf("secret validation failed: %w", err)
	}

	// Apply model override
	if r.cfg.ModelOverride != "" {
		envVars["MODEL_NAME"] = r.cfg.ModelOverride
	}

	// 1b. Validate skill requirements
	if err := r.validateSkillRequirements(envVars); err != nil {
		return err
	}

	// 2. Still load scaffold for SkillGuardrails (separate concern)
	scaffold, err := LoadPolicyScaffold(r.cfg.WorkDir)
	if err != nil {
		r.logger.Warn("failed to load policy scaffold", map[string]any{"error": err.Error()})
	}
	if scaffold == nil {
		scaffold = DefaultPolicyScaffold()
	}

	// 3. Build agent card. Populate security schemes from the configured
	// auth chain so the published card reflects what the middleware
	// actually accepts, then enrich with SKILL.md frontmatter parsed
	// at runtime so dev (no build artifact) and post-build deployments
	// surface the same skill list.
	card, err := BuildAgentCard(r.cfg.WorkDir, r.cfg.Config, r.cfg.Port)
	if err != nil {
		return fmt.Errorf("building agent card: %w", err)
	}
	coreruntime.PopulateSecuritySchemes(card, r.cfg.Config)
	r.enrichAgentCardWithSkills(card)

	// 4. Create audit logger. FWS-7 (issue #95): when AuditExport is
	// configured (--audit-socket / --audit-http-endpoint), a second
	// sink is registered alongside the stderr safety-net so the
	// in-pod sidecar can consume events. Zero config = stderr only,
	// pre-FWS-7 compatible.
	auditLogger := coreruntime.NewAuditLoggerFromConfig(r.cfg.AuditExport)
	auditLogger.SetOpsLogger(r.logger)
	// Deployment-time tenancy stamp (#157). FORGE_ORG_ID /
	// FORGE_WORKSPACE_ID are read once here and stamped on every
	// emitted event — startup banners (agent_card_published,
	// policy_loaded) AND per-invocation events all get the stamp.
	// Per-request X-Forge-Org-ID / X-Forge-Workspace-ID headers
	// (picked up in the A2A handlers) override the static stamp.
	// Empty env → empty stamp → fields omitted (backward compatible).
	auditLogger.WithTenancy(os.Getenv("FORGE_ORG_ID"), os.Getenv("FORGE_WORKSPACE_ID"))
	// Deployment-time entity stamp (#164). Resolution mirrors
	// BuildGuardrailChecker's existing agent-ID resolution
	// (guardrails_loader.go) so the Forge NDJSON stream's entity_id
	// matches the library's MongoDB GuardrailAuditEvent.entity_id
	// column 1:1 — SIEM consumers can join on the same value.
	// EntityType is hardcoded to "agent" because that's the only
	// entity Forge runs today; future entity types would change
	// the value, not the schema.
	agentID := os.Getenv("FORGE_AGENT_ID")
	if agentID == "" && r.cfg.Config != nil {
		agentID = r.cfg.Config.AgentID
	}
	auditLogger.WithEntity("agent", agentID)

	// Resolve TracingConfig early so we can thread it into the
	// guardrail engine before the tracer provider itself is installed
	// further down. ResolveTracingConfig is a pure config-resolution
	// function — no I/O, no provider construction — so this is safe
	// to call ahead of NewTracerProvider. The provider install at
	// line ~561 still owns lifecycle; the engine just needs the
	// CaptureContent + Redact flags. See issue #161.
	tracingCfgEarly := ResolveTracingConfig(
		r.cfg.Config.Observability.Tracing,
		r.cfg.TracingFlags,
		r.cfg.Config.AgentID,
		r.cfg.Config.Version,
		r.cfg.RuntimeVersion,
	)

	// 4a. Build guardrail checker (DB mode → file mode → defaults) and
	// wire the audit logger so every mask/block/warn decision lands on
	// the configured audit sinks as a guardrail_check event. Capture-
	// evidence posture comes from env (FORGE_GUARDRAIL_*), default
	// metadata-only. tracingCfgEarly carries the
	// CaptureContent/Redact knobs the guardrail.<gate> spans use for
	// evidence stamping (#161); the spans themselves are opened
	// unconditionally — when tracing is disabled, the noop tracer
	// short-circuits.
	guardrails := BuildGuardrailChecker(r.cfg.Config, r.cfg.WorkDir, r.cfg.EnforceGuardrails, r.logger, auditLogger, GuardrailAuditConfigFromEnv(), tracingCfgEarly)
	// Periodic audit_export_status — one event every 60s with per-sink
	// health counters. Operators tail the audit stream to answer
	// "is my sidecar healthy?". The stop func blocks until the
	// goroutine exits, so this is safe to defer alongside Close.
	stopAuditStatus := coreruntime.StartAuditExportStatus(ctx, auditLogger)
	defer func() {
		stopAuditStatus()
		// Drain export sinks (no-op for stderr-only). Bound the close
		// to 2s per the FWS-7 contract — slow sinks must not block
		// shutdown.
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = auditLogger.Close(closeCtx)
	}()

	// 4a. Load + enforce platform policy across three layers
	// (issue #90 / FWS-6, building on FWS-5):
	//
	//   - system    → /etc/forge/policy.yaml  (sysadmin-managed)
	//   - user      → ~/.forge/policy.yaml    (developer-managed via
	//                                          TUI/GUI)
	//   - workspace → FORGE_PLATFORM_POLICY   (operator-managed at
	//                                          deploy)
	//
	// Each layer is optional. The runtime unions the deny lists and
	// takes the most-restrictive max bound across all loaded layers.
	// A malformed file at any layer aborts startup (silently treating
	// a broken policy as "no policy" defeats the safety net).
	platformLayers, policyErr := security.LoadAllPolicyLayers()
	if policyErr != nil {
		return fmt.Errorf("loading platform policy layers: %w", policyErr)
	}
	for _, layer := range platformLayers {
		auditLogger.EmitPolicyLoaded(map[string]any{
			"layer":                  layer.Source,
			"source":                 layer.Path,
			"denied_egress_count":    len(layer.Policy.DeniedEgressDomains),
			"denied_tools_count":     len(layer.Policy.DeniedTools),
			"forbidden_models_count": len(layer.Policy.ForbiddenModels),
			"denied_channels_count":  len(layer.Policy.DeniedChannels),
			"max_egress_allowlist":   layer.Policy.MaxEgressAllowlistSize,
			"max_tool_count":         layer.Policy.MaxToolCount,
		})
	}
	if violations := security.EnforcePolicy(r.cfg.Config, platformLayers); len(violations) > 0 {
		// One audit event per violation so cost / compliance dashboards
		// can group by kind AND by deciding layer; then abort with a
		// combined developer-facing error listing every offence.
		for _, v := range violations {
			auditLogger.EmitPolicyViolationAtBuildTime(map[string]any{
				"violation_kind":   string(v.Kind),
				"offending_value":  v.OffendingValue,
				"forge_yaml_field": v.ForgeYAMLField,
				"layer":            v.Layer,
				"source":           v.LayerPath,
			})
		}
		return fmt.Errorf("%s", security.FormatViolations(violations))
	}

	// 4b. Resolve egress config and start proxy (if not in container)
	var egressClient *http.Client
	var egressProxy *security.EgressProxy
	var proxyURL string
	egressToolNames := make([]string, len(r.cfg.Config.Tools))
	for i, t := range r.cfg.Config.Tools {
		egressToolNames[i] = t.Name
	}
	// Merge skill-derived egress domains with explicitly configured domains.
	// Both sources may contain $VAR or ${VAR} references which are
	// expanded from .env and OS environment (e.g. "$K8S_API_DOMAIN").
	//
	// Platform-policy intersection (issue #89 / FWS-5): the developer's
	// forge.yaml allow list is filtered through the policy deny list
	// BEFORE expansion. The EnforcePolicy check above already aborted
	// startup on a declared-but-denied entry; this filter is the
	// belt-and-suspenders defence-in-depth pass — any new code path
	// that injects egress entries can call it independently.
	declaredAllowed := security.EffectiveEgressAllowlist(r.cfg.Config, platformLayers)
	var egressDomains []string
	for _, d := range declaredAllowed {
		egressDomains = append(egressDomains, expandEgressDomains(d, envVars)...)
	}
	if r.derivedCLIConfig != nil && len(r.derivedCLIConfig.EgressDomains) > 0 {
		for _, d := range r.derivedCLIConfig.EgressDomains {
			egressDomains = append(egressDomains, expandEgressDomains(d, envVars)...)
		}
	}
	// Auto-merge auth-provider issuer/verifier hosts. Without this, an
	// OIDC issuer or http_verifier URL configured in forge.yaml would be
	// silently blocked at runtime by the egress enforcer.
	egressDomains = append(egressDomains, security.AuthDomains(r.cfg.Config.Auth)...)
	// Same for MCP servers — without this, every HTTPS MCP call would
	// be silently blocked. Mirror the AuthDomains pattern.
	egressDomains = append(egressDomains, security.MCPDomains(r.cfg.Config.MCP)...)
	// Phase 6 (#107 / #108) — same for the OTel collector. Without
	// this, dev runs with `observability.tracing.enabled: true` and
	// `egress.mode: allowlist` would silently drop spans on shutdown.
	// Matches the build pipeline's egress_stage so `forge run` and
	// `forge package`-then-deploy behave identically on the
	// allowlist surface.
	egressDomains = append(egressDomains, security.OTelDomain(r.cfg.Config.Observability.Tracing)...)
	// Issue #139 — auto-merge LLM provider base URLs. Two sources:
	//   1. The new ModelRef.BaseURL field (the durable signal that
	//      also flows through `forge package` to the deployed
	//      NetworkPolicy). This is the canonical path going forward.
	//   2. The standard SDK base-URL env vars (OPENAI_BASE_URL /
	//      ANTHROPIC_BASE_URL / OLLAMA_BASE_URL / GEMINI_BASE_URL).
	//      Safety-net for deployments that haven't migrated to the
	//      schema field yet — `envVars` already carries the resolved
	//      .env + .forge/secrets.enc state at this point.
	// Both are deduped via the helper. Without these merges, an agent
	// using a custom OpenAI-compatible / Anthropic-compatible /
	// remote-Ollama endpoint would be silently blocked by the egress
	// enforcer at runtime.
	egressDomains = append(egressDomains, security.LLMProviderDomains(r.cfg.Config)...)
	egressDomains = append(egressDomains, security.LLMProviderEnvDomains(envVars)...)
	egressCfg, egressErr := security.Resolve(
		r.cfg.Config.Egress.Profile,
		r.cfg.Config.Egress.Mode,
		egressDomains,
		egressToolNames,
		r.cfg.Config.Egress.Capabilities,
	)
	if egressErr != nil {
		r.logger.Warn("failed to resolve egress config, using default", map[string]any{"error": egressErr.Error()})
		egressClient = http.DefaultClient
	} else {
		// Resolve allowPrivateIPs: explicit config > container auto-detect > false
		allowPrivateIPs := false
		if r.cfg.Config.Egress.AllowPrivateIPs != nil {
			allowPrivateIPs = *r.cfg.Config.Egress.AllowPrivateIPs
		} else if security.InContainer() {
			allowPrivateIPs = true
		}

		enforcer := security.NewEgressEnforcer(nil, egressCfg.Mode, egressCfg.AllDomains, allowPrivateIPs)
		enforcer.OnAttempt = func(ctx context.Context, domain string, allowed bool) {
			event := coreruntime.AuditEgressAllowed
			if !allowed {
				event = coreruntime.AuditEgressBlocked
			}
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         event,
				CorrelationID: coreruntime.CorrelationIDFromContext(ctx),
				TaskID:        coreruntime.TaskIDFromContext(ctx),
				Fields:        map[string]any{"domain": domain, "mode": string(egressCfg.Mode)},
			})
		}
		// Phase 3 (#104) — wrap the egress-enforced transport with
		// otelhttp instrumentation so every outbound HTTP request the
		// in-process clients (LLM providers, MCP, channels, OAuth)
		// make through this client produces an "http.client" span
		// automatically. The wrap also injects the OTel
		// traceparent + baggage headers on outbound requests (Phase 0
		// installed the composite propagator), which is the wire-level
		// precursor to Phase 5 (#106) end-to-end propagation.
		//
		// When tracing is disabled the otelhttp wrapper is a near
		// pass-through (the noop TracerProvider short-circuits span
		// creation), so this is safe to always-wrap regardless of
		// observability.tracing.enabled.
		egressClient = &http.Client{Transport: observability.WrapHTTPTransport(enforcer)}

		// Start local proxy for subprocess egress enforcement
		if !security.InContainer() && egressCfg.Mode != security.ModeDevOpen {
			matcher := security.NewDomainMatcher(egressCfg.Mode, egressCfg.AllDomains)
			egressProxy = security.NewEgressProxy(matcher, allowPrivateIPs)
			egressProxy.OnAttempt = func(domain string, allowed bool) {
				event := coreruntime.AuditEgressAllowed
				if !allowed {
					event = coreruntime.AuditEgressBlocked
				}
				auditLogger.Emit(coreruntime.AuditEvent{
					Event:  event,
					Fields: map[string]any{"domain": domain, "mode": string(egressCfg.Mode), "source": "proxy"},
				})
			}
			var pErr error
			proxyURL, pErr = egressProxy.Start(ctx)
			if pErr != nil {
				r.logger.Warn("failed to start egress proxy", map[string]any{"error": pErr.Error()})
				egressProxy = nil
			} else {
				r.logger.Info("egress proxy started", map[string]any{"url": proxyURL})
			}
		}
	}
	if egressProxy != nil {
		defer egressProxy.Stop() //nolint:errcheck
	}

	// 4c. OTel tracing (Phase 2, issue #103 / initiative #108).
	//
	// Ordering: this runs AFTER the egress enforcer is built so the
	// OTLP/HTTP exporter inherits the same egress-enforced transport
	// every other in-process Forge HTTP client uses. The egress
	// allowlist + post-DNS IP guard therefore bound where Forge can
	// send spans — the operator declares the collector host in
	// forge.yaml egress, and a misconfigured exporter cannot exfiltrate
	// span content to an unapproved destination.
	//
	// Resolver precedence: forge.yaml < OTEL_* env vars < CLI flags
	// (see ResolveTracingConfig).
	//
	// Disabled paths: a nil/Enabled=false config returns ErrDisabled
	// from observability.NewTracerProvider; we install the noop tracer
	// in that case (the default already set in forge-core/runtime/
	// tracing.go) and continue. Tracing is off-by-default per the
	// initiative ruling — a misconfigured exporter must never crash
	// the agent.
	tracingCfg := tracingCfgEarly
	var tracingTransport http.RoundTripper
	if egressClient != nil {
		tracingTransport = egressClient.Transport
	}
	tp, tpErr := observability.NewTracerProvider(ctx, tracingCfg, tracingTransport)
	switch {
	case errors.Is(tpErr, observability.ErrDisabled):
		r.logger.Info("tracing disabled", nil)
	case tpErr != nil:
		// Telemetry failures must not crash the agent. Log loudly and
		// fall through with the noop tracer the package default already
		// installed. An operator watching the audit stream sees this in
		// the ops log right alongside other startup diagnostics.
		r.logger.Warn("tracing setup failed; falling back to noop tracer", map[string]any{
			"error":    tpErr.Error(),
			"endpoint": tracingCfg.Endpoint,
		})
	default:
		coreruntime.SetTracerProvider(tp)
		r.logger.Info(FormatTracingStartupLine(tracingCfg), nil)
		// Shutdown drains the batch span processor and closes the OTLP
		// exporter. Bound to 5s — slow collectors must not block agent
		// shutdown. Registered AFTER the audit-logger defer above so it
		// runs FIRST on shutdown (LIFO): tracer flushes its final batch
		// while the egress proxy is still alive, then audit drains.
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tp.Shutdown(shutdownCtx); err != nil {
				r.logger.Warn("tracer provider shutdown error", map[string]any{"error": err.Error()})
			}
		}()
	}

	// 5. Choose executor and optional lifecycle runtime
	var executor coreruntime.AgentExecutor
	var lifecycle coreruntime.AgentRuntime // optional, for subprocess lifecycle management
	if r.cfg.MockTools {
		toolSpecs := r.loadToolSpecs()
		executor = NewMockExecutor(toolSpecs)
		r.logger.Info("using mock executor", map[string]any{"tools": len(toolSpecs)})
	} else {
		switch r.cfg.Config.Framework {
		case "crewai", "langchain":
			rt := NewSubprocessRuntime(r.cfg.Config.Entrypoint, r.cfg.WorkDir, envVars, r.logger)
			lifecycle = rt
			executor = NewSubprocessExecutor(rt)
		default:
			// Forge framework — build tool registry and use built-in LLM executor
			reg := tools.NewRegistry()
			if err := builtins.RegisterAll(reg); err != nil {
				r.logger.Warn("failed to register builtin tools", map[string]any{"error": err.Error()})
			}

			// Register search/exploration tools (grep, glob, tree).
			// When code-agent skill is active, scope them to workspace/ so searches
			// default to cloned repos. Otherwise scope to the main working directory.
			searchRoot := r.cfg.WorkDir
			if r.hasSkill("code-agent") {
				codeDir := filepath.Join(r.cfg.WorkDir, "workspace")
				if mkErr := os.MkdirAll(codeDir, 0o755); mkErr != nil {
					r.logger.Warn("failed to create code workspace directory", map[string]any{"error": mkErr.Error()})
				}
				searchRoot = codeDir
				r.logger.Info("code-agent skill detected: workspace ready", map[string]any{"workspace": codeDir})
				// Script tools (code_agent_read, code_agent_write, code_agent_run)
				// are registered by registerSkillTools() from SKILL.md ## Tool: entries.
			}
			if err := builtins.RegisterCodeAgentSearchTools(reg, searchRoot); err != nil {
				r.logger.Warn("failed to register search tools", map[string]any{"error": err.Error()})
			}

			// Register read_skill tool for lazy-loading skill instructions
			readSkill := builtins.NewReadSkillTool(r.cfg.WorkDir)
			if regErr := reg.Register(readSkill); regErr != nil {
				r.logger.Warn("failed to register read_skill", map[string]any{"error": regErr.Error()})
			}

			// Register cli_execute if configured explicitly or auto-derived from skills
			hasExplicitCLI := false
			for _, toolRef := range r.cfg.Config.Tools {
				if toolRef.Name == "cli_execute" && toolRef.Config != nil {
					hasExplicitCLI = true
					cliCfg := clitools.ParseCLIExecuteConfig(toolRef.Config)
					cliCfg.WorkDir = r.cfg.WorkDir
					// Apply timeout hint from skill requirements if larger than explicit config
					if r.derivedCLIConfig != nil && r.derivedCLIConfig.TimeoutHint > cliCfg.TimeoutSeconds {
						cliCfg.TimeoutSeconds = r.derivedCLIConfig.TimeoutHint
					}
					if len(cliCfg.AllowedBinaries) > 0 {
						r.cliExecTool = clitools.NewCLIExecuteTool(cliCfg)
						if regErr := reg.Register(r.cliExecTool); regErr != nil {
							r.logger.Warn("failed to register cli_execute", map[string]any{"error": regErr.Error()})
						} else {
							avail, missing := r.cliExecTool.Availability()
							r.logger.Info("cli_execute registered", map[string]any{
								"available": len(avail), "missing": len(missing),
							})
						}
					}
					break
				}
			}
			// Auto-register cli_execute from skill-derived config when not explicitly configured
			if !hasExplicitCLI && r.derivedCLIConfig != nil && len(r.derivedCLIConfig.AllowedBinaries) > 0 {
				cliCfg := clitools.CLIExecuteConfig{
					AllowedBinaries: r.derivedCLIConfig.AllowedBinaries,
					EnvPassthrough:  r.derivedCLIConfig.EnvPassthrough,
					TimeoutSeconds:  r.derivedCLIConfig.TimeoutHint,
					WorkDir:         r.cfg.WorkDir,
				}
				r.cliExecTool = clitools.NewCLIExecuteTool(cliCfg)
				if regErr := reg.Register(r.cliExecTool); regErr != nil {
					r.logger.Warn("failed to register auto-derived cli_execute", map[string]any{"error": regErr.Error()})
				} else {
					avail, missing := r.cliExecTool.Availability()
					r.logger.Info("cli_execute auto-registered from skill requirements", map[string]any{
						"binaries":  r.derivedCLIConfig.AllowedBinaries,
						"available": len(avail), "missing": len(missing),
					})
				}
			}

			// Discover custom tools in tools/ directory
			toolsDir := filepath.Join(r.cfg.WorkDir, "tools")
			discovered := clitools.DiscoverTools(toolsDir)
			cmdExec := &clitools.OSCommandExecutor{}
			for _, dt := range discovered {
				// Entrypoint must be relative to WorkDir so execution from agent root finds the file
				dtCopy := dt
				dtCopy.Entrypoint = filepath.Join("tools", dt.Entrypoint)
				ct := tools.NewCustomTool(dtCopy, cmdExec)
				if valErr := ct.ValidateEntrypoint(r.cfg.WorkDir); valErr != nil {
					r.logger.Warn("skipping custom tool with invalid entrypoint", map[string]any{
						"tool": dt.Name, "error": valErr.Error(),
					})
					continue
				}
				if regErr := reg.Register(ct); regErr != nil {
					r.logger.Warn("failed to register custom tool", map[string]any{
						"tool": dt.Name, "error": regErr.Error(),
					})
				}
			}
			if len(discovered) > 0 {
				r.logger.Info("discovered custom tools", map[string]any{"count": len(discovered)})
			}

			// Set proxy URL on cli_execute tool
			if r.cliExecTool != nil && proxyURL != "" {
				r.cliExecTool.SetProxyURL(proxyURL)
			}

			// Register skill tools from skill files
			r.registerSkillTools(reg, proxyURL)

			// Remove denied tools from the registry. The effective deny
			// list is the union of forge.yaml's denies (via the derived
			// CLI config) and the platform policy's denies (issue #89 /
			// FWS-5). User-selected builtins are preserved unless the
			// platform policy denies them — a platform-level deny is
			// not overridable by user selection.
			var forgeDenied []string
			if r.derivedCLIConfig != nil {
				forgeDenied = r.derivedCLIConfig.DeniedTools
			}
			effectiveDenied := security.EffectiveDeniedTools(forgeDenied, platformLayers)
			if len(effectiveDenied) > 0 {
				userSelected := make(map[string]bool, len(r.cfg.Config.BuiltinTools))
				for _, name := range r.cfg.Config.BuiltinTools {
					userSelected[name] = true
				}
				// Union every policy layer's tool denies. A user-selected
				// builtin survives forge.yaml-only denies but NOT any
				// policy-layer deny (system/user/workspace all outrank
				// per-agent selection).
				policyDenied := make(map[string]bool)
				for _, l := range platformLayers {
					for _, name := range l.Policy.DeniedTools {
						policyDenied[name] = true
					}
				}

				var removed []string
				for _, denied := range effectiveDenied {
					// User-selected builtins survive forge.yaml denies
					// but NOT platform-policy denies — workspace policy
					// outranks per-agent declaration.
					if userSelected[denied] && !policyDenied[denied] {
						continue
					}
					reg.Remove(denied)
					removed = append(removed, denied)
				}
				if len(removed) > 0 {
					r.logger.Info("removed denied tools", map[string]any{"denied": removed})
				}
			}

			// Start MCP servers (Phase 1: HTTP-only) and register their
			// discovered tools as namespaced "<server>__<tool>" entries.
			// Required=true server failures here cause Run() to return
			// non-zero — K8s sees CrashLoopBackOff. Required=false
			// failures log a warning and continue.
			if mcpMgr, err := r.startMCPManager(ctx, egressClient, auditLogger); err != nil {
				return fmt.Errorf("starting mcp manager: %w", err)
			} else if mcpMgr != nil {
				defer func() { _ = mcpMgr.Stop() }()
				for _, h := range mcpMgr.Tools() {
					mcpTool, ctorErr := adapters.NewMCPTool(adapters.MCPToolOpts{
						Server:     h.Server,
						Descriptor: h.Descriptor,
						Client:     h.Client,
						Audit:      auditLogger,
					})
					if ctorErr != nil {
						// Bad descriptor (empty name or "__" — review B9).
						// Audit as a conflict; the tool never enters the
						// registry so the LLM never sees an ambiguous name.
						r.logger.Warn("mcp tool construction rejected", map[string]any{
							"server": h.Server, "tool": h.Descriptor.Name, "error": ctorErr.Error(),
						})
						auditLogger.Emit(coreruntime.AuditEvent{
							Event: coreruntime.EventMCPToolConflict,
							Fields: map[string]any{
								"server":        h.Server,
								"incoming_name": h.Descriptor.Name,
								"error":         ctorErr.Error(),
							},
						})
						continue
					}
					if regErr := reg.Register(mcpTool); regErr != nil {
						r.logger.Warn("mcp tool registration", map[string]any{
							"tool": mcpTool.Name(), "error": regErr.Error(),
						})
						auditLogger.Emit(coreruntime.AuditEvent{
							Event: coreruntime.EventMCPToolConflict,
							Fields: map[string]any{
								"incoming_name": mcpTool.Name(),
								"error":         regErr.Error(),
							},
						})
					}
				}
			}

			// Log registered tool names
			toolNames := reg.List()
			r.logger.Info("registered tools", map[string]any{"tools": toolNames})

			// Try LLM executor, fall back to stub
			mc := coreruntime.ResolveModelConfig(r.cfg.Config, envVars, r.cfg.ProviderOverride)
			if mc != nil {
				r.modelConfig = mc
				// Export org ID for skill scripts
				if mc.Client.OrgID != "" {
					_ = os.Setenv("OPENAI_ORG_ID", mc.Client.OrgID)
				}
				llmClient, llmErr := r.buildLLMClient(mc)
				if llmErr != nil {
					r.logger.Warn("failed to create LLM client, using stub", map[string]any{"error": llmErr.Error()})
					executor = NewStubExecutor(r.cfg.Config.Framework)
				} else {
					// Build logging and audit hooks for agent loop observability
					hooks := coreruntime.NewHookRegistry()
					r.registerLoggingHooks(hooks)
					r.registerAuditHooks(hooks, auditLogger)
					r.registerProgressHooks(hooks)
					r.registerGuardrailHooks(hooks, guardrails)

					// Register skill-level guardrails if present.
					// Prefer build-time artifact; fall back to runtime-parsed guardrails.
					sgRules := scaffold.SkillGuardrails
					if sgRules == nil {
						sgRules = r.skillGuardrails
					}
					if sgRules != nil {
						sg := coreruntime.NewSkillGuardrailEngine(sgRules, r.cfg.EnforceGuardrails, r.logger)
						r.registerSkillGuardrailHooks(hooks, sg)
					}

					// Compute model-aware character budget.
					charBudget := r.cfg.Config.Memory.CharBudget
					if charBudget == 0 {
						charBudget = coreruntime.ContextBudgetForModel(mc.Client.Model)
					}

					// Build system prompt; append code-agent tool directives if those tools are registered.
					sysPrompt := r.buildSystemPrompt()
					if r.hasSkill("code-agent") {
						sysPrompt += "\n\n" + codeAgentDirective
					}

					execCfg := coreruntime.LLMExecutorConfig{
						Client:        llmClient,
						Tools:         reg,
						Hooks:         hooks,
						SystemPrompt:  sysPrompt,
						Logger:        r.logger,
						ModelName:     mc.Client.Model,
						Provider:      mc.Provider,
						MaxIterations: 100,
						CharBudget:    charBudget,
						FilesDir:      filepath.Join(r.cfg.WorkDir, ".forge", "files"),
						// Issue #130 — the same resolved TracingConfig
						// already passed to NewTracerProvider drives Phase
						// 3.5 span-content capture inside the executor
						// loop. Disabled state (Enabled=false +
						// CaptureContent=false) is the zero-value default,
						// so missing this on an older config schema is
						// equivalent to "metadata-only spans" — the
						// posture this initiative preserves.
						TracingConfig: tracingCfg,
					}
					if r.derivedCLIConfig != nil {
						execCfg.WorkflowPhases = r.derivedCLIConfig.WorkflowPhases
					}

					// Initialize memory persistence (enabled by default).
					// Disable via FORGE_MEMORY_PERSISTENCE=false or memory.persistence: false in forge.yaml.
					memPersistence := true
					if r.cfg.Config.Memory.Persistence != nil {
						memPersistence = *r.cfg.Config.Memory.Persistence
					}
					if os.Getenv("FORGE_MEMORY_PERSISTENCE") == "false" {
						memPersistence = false
					}
					if memPersistence {
						sessDir := r.cfg.Config.Memory.SessionsDir
						if sessDir == "" {
							sessDir = filepath.Join(r.cfg.WorkDir, ".forge", "sessions")
						}
						memStore, storeErr := coreruntime.NewMemoryStore(sessDir)
						if storeErr != nil {
							r.logger.Warn("failed to create memory store, persistence disabled", map[string]any{
								"error": storeErr.Error(),
							})
						} else {
							// Clean up old sessions on startup (7-day TTL).
							deleted, _ := memStore.Cleanup(7 * 24 * time.Hour)
							if deleted > 0 {
								r.logger.Info("cleaned up old sessions", map[string]any{"deleted": deleted})
							}

							compactor := coreruntime.NewCompactor(coreruntime.CompactorConfig{
								Client:       llmClient,
								Store:        memStore,
								Logger:       r.logger,
								CharBudget:   charBudget,
								TriggerRatio: r.cfg.Config.Memory.TriggerRatio,
							})

							execCfg.Store = memStore
							execCfg.Compactor = compactor

							// Session max age: stale sessions are discarded to prevent
							// poisoned error context from blocking tool retries.
							if v := os.Getenv("FORGE_SESSION_MAX_AGE"); v != "" {
								if d, err := time.ParseDuration(v); err == nil {
									execCfg.SessionMaxAge = d
								}
							} else if r.cfg.Config.Memory.SessionMaxAge != "" {
								if d, err := time.ParseDuration(r.cfg.Config.Memory.SessionMaxAge); err == nil {
									execCfg.SessionMaxAge = d
								}
							}

							r.logger.Info("memory persistence enabled", map[string]any{
								"sessions_dir": sessDir,
							})
						}
					}

					// Initialize long-term memory if enabled.
					memMgr := r.initLongTermMemory(ctx, mc, reg, execCfg.Compactor)
					if memMgr != nil {
						defer memMgr.Close() //nolint:errcheck
					}

					// Initialize scheduler store and register schedule tools.
					schedStore := r.initScheduler(reg)

					executor = coreruntime.NewLLMExecutor(execCfg)

					// Start cron scheduler after executor is ready.
					if schedStore != nil {
						dispatch := r.makeScheduleDispatcher(executor, egressClient, auditLogger)
						var auditFn scheduler.AuditFunc
						if auditLogger != nil {
							auditFn = func(event, scheduleID string, fields map[string]any) {
								if fields == nil {
									fields = make(map[string]any)
								}
								fields["schedule_id"] = scheduleID
								auditLogger.Emit(coreruntime.AuditEvent{
									Event:  event,
									Fields: fields,
								})
							}
						}
						// Pick the schedule backend per scheduler.backend:
						// "kubernetes" — always K8s (errors at startup when not in-cluster);
						// "file"       — always the file backend;
						// "auto" / ""  — kubernetes when in-cluster, file otherwise.
						// FileBackend wraps the existing Scheduler ticker
						// + ScheduleStore behind the unified Backend
						// interface introduced in #162 part 2; the
						// KubernetesBackend (#162 part 2b) delegates timing
						// to the cluster's CronJob controller and persists
						// state as CronJob resources in etcd.
						backend, berr := r.selectScheduleBackend(schedStore, dispatch, auditFn)
						if berr != nil {
							return berr
						}
						r.schedBackend = backend
						if syncErr := r.schedBackend.Sync(ctx, r.declaredSchedules()); syncErr != nil {
							r.logger.Warn("schedule backend sync failed", map[string]any{"error": syncErr.Error()})
						}
						r.schedBackend.Start(ctx)
						defer r.schedBackend.Stop()
					}

					r.logger.Info("using LLM executor", map[string]any{
						"provider":  mc.Provider,
						"model":     mc.Client.Model,
						"tools":     len(toolNames),
						"fallbacks": len(mc.Fallbacks),
					})
				}
			} else {
				executor = NewStubExecutor(r.cfg.Config.Framework)
				r.logger.Warn("no LLM provider configured, using stub executor", map[string]any{
					"framework": r.cfg.Config.Framework,
				})
			}
		}
	}
	defer executor.Close() //nolint:errcheck

	// Start lifecycle runtime if present
	if lifecycle != nil {
		if err := lifecycle.Start(ctx); err != nil {
			return fmt.Errorf("starting runtime: %w", err)
		}
		defer lifecycle.Stop() //nolint:errcheck
	}

	// 6a. Resolve auth configuration.
	authCfg, err := r.resolveAuth(auditLogger)
	if err != nil {
		return fmt.Errorf("resolving auth: %w", err)
	}

	// 6b. Resolve CORS origins: CLI flag > env var > forge.yaml > defaults
	corsOrigins := r.cfg.CORSOrigins
	if len(corsOrigins) == 0 {
		if envCORS := os.Getenv("FORGE_CORS_ORIGINS"); envCORS != "" {
			corsOrigins = strings.Split(envCORS, ",")
			for i := range corsOrigins {
				corsOrigins[i] = strings.TrimSpace(corsOrigins[i])
			}
		}
	}
	if len(corsOrigins) == 0 && len(r.cfg.Config.CORSOrigins) > 0 {
		corsOrigins = r.cfg.Config.CORSOrigins
	}
	if len(corsOrigins) == 0 {
		corsOrigins = server.DefaultAllowedOrigins()
	}

	// 6. Create A2A server. Rate limit resolution order
	// (FWS-10 / issue #110): CLI flags > FORGE_RATE_LIMIT_* env >
	// cfg.Server.RateLimit in forge.yaml > built-in defaults
	// (60/min read+write, burst 10/20, tasks/cancel exempt). nil
	// return means "no overrides anywhere" — let the server install
	// its own defaults.
	rateLimit := ResolveRateLimit(r.cfg.Config, r.cfg.RateLimitOverride)
	r.startTime = time.Now()
	srv := server.NewServer(server.ServerConfig{
		Port:            r.cfg.Port,
		Host:            r.cfg.Host,
		ShutdownTimeout: r.cfg.ShutdownTimeout,
		AgentCard:       card,
		AuthMiddleware:  installSequenceCounterMiddleware(auth.Middleware(authCfg)),
		AllowedOrigins:  corsOrigins,
		RateLimit:       rateLimit,
	})

	// 7. Register JSON-RPC handlers
	r.registerHandlers(srv, executor, guardrails, egressClient, auditLogger)

	// 7b. Register REST-style HTTP handlers
	r.registerRESTHandlers(srv, executor, guardrails, egressClient, auditLogger)

	// 9. Start file watcher
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	watcher := NewFileWatcher(r.cfg.WorkDir, func() {
		// Reload config and agent card
		newCard, err := BuildAgentCard(r.cfg.WorkDir, r.cfg.Config, r.cfg.Port)
		if err != nil {
			r.logger.Error("failed to reload agent card", map[string]any{"error": err.Error()})
		} else {
			coreruntime.PopulateSecuritySchemes(newCard, r.cfg.Config)
			r.enrichAgentCardWithSkills(newCard)
			srv.UpdateAgentCard(newCard)
			r.logger.Info("agent card reloaded", nil)
			// Re-emit agent_card_published so audit consumers see the
			// new card hash — same event shape as the startup emit.
			r.emitAgentCardPublished(auditLogger, newCard)
		}

		// Restart subprocess lifecycle (no-op if lifecycle is nil)
		if lifecycle != nil {
			if err := lifecycle.Restart(ctx); err != nil {
				r.logger.Error("failed to restart runtime", map[string]any{"error": err.Error()})
			}
		}
	}, r.logger)
	go watcher.Watch(watchCtx)

	// 10. Print startup banner
	r.printBanner(proxyURL)

	// 10b. Emit the agent_card_published audit event (issue #85). One
	// per startup; carries identity + size + a sha256 of the JSON-
	// encoded card so consumers can detect config drift. Hot-reload
	// re-emits via the file watcher above (UpdateAgentCard path).
	r.emitAgentCardPublished(auditLogger, card)

	// 11. Start server (blocks)
	return srv.Start(ctx)
}

func (r *Runner) registerHandlers(srv *server.Server, executor coreruntime.AgentExecutor, guardrails coreruntime.GuardrailChecker, egressClient *http.Client, auditLogger *coreruntime.AuditLogger) {
	store := srv.TaskStore()

	// tasks/send — synchronous request. Delegates to executeTask so the
	// JSON-RPC path goes through the same audit + accumulator wiring as
	// REST POST /tasks/send. See issue #87 / FWS-3.
	srv.RegisterHandler("tasks/send", func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse {
		var params a2a.SendTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid params: "+err.Error())
		}
		// Validate the message shape per A2A 0.3.0 (issue #119). The
		// most common failure is a client sending `"type": "text"`
		// instead of `"kind": "text"` — encoding/json silently drops
		// the unknown field, Part.Kind stays "", and the executor
		// downstream would respond with a confused "your message
		// didn't come through" rather than name the spec divergence.
		// Reject loudly with a diagnostic the operator can act on.
		if err := params.Message.Validate(); err != nil {
			r.logger.Warn("tasks/send rejected: invalid message shape", map[string]any{
				"task_id": params.ID,
				"reason":  err.Error(),
			})
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid message: "+err.Error())
		}
		r.logger.Info("tasks/send", map[string]any{"task_id": params.ID})
		// Delegate to executeTask so JSON-RPC and REST share the same
		// audit + accumulator + invocation_complete wiring (issue #87 /
		// FWS-3). The dispatcher already injected WorkflowContext into
		// ctx from inbound headers per issue #86 / FWS-2, so every audit
		// event executeTask emits carries workflow correlation fields
		// when present.
		task, snap, err := r.executeTask(ctx, params, store, executor, guardrails, egressClient, auditLogger)
		if err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInternal, err.Error())
		}
		// FWS-3 X-Forge-* response headers. The REST path at
		// POST /tasks/send stamps directly on w.Header() because the
		// REST handler has the writer in scope. The JSON-RPC Handler
		// signature deliberately omits the writer, so we publish the
		// snapshot-derived headers through the dispatcher's per-request
		// stage; handleJSONRPC drains the stage onto the writer before
		// writeJSON. Without this stamp the JSON-RPC path silently drops
		// the FWS-3 telemetry that orchestrators ceiling-check against.
		if stage := server.ResponseHeaderStageFromContext(ctx); stage != nil {
			applyForgeUsageHeaders(stage, snap)
		}
		return a2a.NewResponse(id, task)
	})

	// tasks/sendSubscribe — SSE streaming
	srv.RegisterSSEHandler("tasks/sendSubscribe", func(ctx context.Context, id any, rawParams json.RawMessage, w http.ResponseWriter, flusher http.Flusher) {
		var params a2a.SendTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			server.WriteSSEEvent(w, flusher, "error", a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, err.Error())) //nolint:errcheck
			return
		}
		// A2A 0.3.0 message-shape validation (issue #119). Same
		// rationale as the JSON-RPC tasks/send path: reject malformed
		// requests at the entry point with a clear diagnostic instead
		// of letting the executor produce a confusing "didn't come
		// through" reply.
		if err := params.Message.Validate(); err != nil {
			r.logger.Warn("tasks/sendSubscribe rejected: invalid message shape", map[string]any{
				"task_id": params.ID,
				"reason":  err.Error(),
			})
			server.WriteSSEEvent(w, flusher, "error", //nolint:errcheck
				a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid message: "+err.Error()))
			return
		}

		r.logger.Info("tasks/sendSubscribe", map[string]any{"task_id": params.ID})

		// Inject egress client, correlation/task IDs, and per-invocation
		// usage accumulator (issue #87 / FWS-3) into context. The
		// accumulator lets the AfterLLMCall hook fold each call's
		// tokens/duration into running totals for the invocation_complete
		// audit event emitted before this handler returns.
		correlationID := coreruntime.GenerateID()
		ctx = security.WithEgressClient(ctx, egressClient)
		ctx = coreruntime.WithCorrelationID(ctx, correlationID)
		ctx = coreruntime.WithTaskID(ctx, params.ID)
		// FWS-8: per-invocation sequence counter so every audit event
		// emitted on behalf of this request carries a monotonically
		// increasing `seq` field — consumers detect gaps + ordering
		// at the export side. Reuse the counter
		// installSequenceCounterMiddleware put on ctx before auth ran
		// (so auth_verify=seq=1 and session_start=seq=2) — see #174.
		// EnsureSequenceCounter installs a fresh one if missing
		// (--no-auth path / direct test invocations).
		ctx = coreruntime.EnsureSequenceCounter(ctx)
		sseAcc := coreruntime.NewLLMUsageAccumulator()
		ctx = coreruntime.WithLLMUsageAccumulator(ctx, sseAcc)
		defer func() {
			snap := sseAcc.Snapshot()
			fields := map[string]any{}
			if snap.LLMCallCount > 0 {
				fields["input_tokens_total"] = snap.InputTokens
				fields["output_tokens_total"] = snap.OutputTokens
				fields["llm_call_count"] = snap.LLMCallCount
				if snap.PrimaryModel != "" {
					fields["model"] = snap.PrimaryModel
				}
				if snap.PrimaryProvider != "" {
					fields["provider"] = snap.PrimaryProvider
				}
			}
			auditLogger.EmitInvocationComplete(ctx, snap.InvocationDuration, fields)
		}()

		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionStart,
			CorrelationID: correlationID,
			TaskID:        params.ID,
		})

		// Load existing task to preserve conversation history, or create new.
		task := store.Get(params.ID)
		if task == nil {
			task = &a2a.Task{ID: params.ID}
		}
		task.Status = a2a.TaskStatus{State: a2a.TaskStateSubmitted}
		store.Put(task)
		server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck

		// Guardrail check inbound
		if err := guardrails.CheckInbound(ctx, &params.Message); err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("Guardrail violation: " + err.Error())},
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return
		}

		// Append inbound user message to task history.
		task.History = append(task.History, params.Message)

		// Update to working
		task.Status = a2a.TaskStatus{State: a2a.TaskStateWorking}
		store.Put(task)
		server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck

		// Inject progress emitter for SSE clients
		ctx = coreruntime.WithProgressEmitter(ctx, func(event coreruntime.ProgressEvent) {
			progressTask := &a2a.Task{
				ID: params.ID,
				Status: a2a.TaskStatus{
					State: a2a.TaskStateWorking,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart(event.Message)},
					},
				},
				Metadata: map[string]any{
					"progress_phase": event.Phase,
					"progress_tool":  event.Tool,
				},
			}
			server.WriteSSEEvent(w, flusher, "progress", progressTask) //nolint:errcheck
		})

		// Stream from executor
		ch, err := executor.ExecuteStream(ctx, task, &params.Message)
		if err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart(err.Error())},
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return
		}

		var finalState a2a.TaskState
		for respMsg := range ch {
			// Guardrail check outbound
			if grErr := guardrails.CheckOutbound(ctx, respMsg); grErr != nil {
				task.Status = a2a.TaskStatus{
					State: a2a.TaskStateFailed,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart("Outbound guardrail violation: " + grErr.Error())},
					},
				}
				store.Put(task)
				server.WriteSSEEvent(w, flusher, "result", task) //nolint:errcheck
				finalState = a2a.TaskStateFailed
				break
			}

			// Append agent response to task history.
			task.History = append(task.History, *respMsg)

			// Build completed result
			task.Status = a2a.TaskStatus{
				State:   a2a.TaskStateCompleted,
				Message: respMsg,
			}
			task.Artifacts = []a2a.Artifact{
				{
					Name:  "response",
					Parts: respMsg.Parts,
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "result", task) //nolint:errcheck
			finalState = a2a.TaskStateCompleted
		}

		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionEnd,
			CorrelationID: correlationID,
			TaskID:        params.ID,
			Fields:        map[string]any{"state": string(finalState)},
		})
	})

	// tasks/get — lookup task by ID
	srv.RegisterHandler("tasks/get", func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse {
		var params a2a.GetTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid params: "+err.Error())
		}

		task := store.Get(params.ID)
		if task == nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "task not found: "+params.ID)
		}
		return a2a.NewResponse(id, task)
	})

	// tasks/cancel — signal the in-flight invocation for taskID. Maps
	// the optional CancelTaskParams.Reason onto a CancellationReason
	// the runtime then surfaces on the invocation_cancelled audit event
	// and the response task message. Idempotent: a cancel for a task
	// that already completed (or was never started) returns the stored
	// task without an error so the orchestrator can issue cancels
	// optimistically. See issue #88 / FWS-4.
	srv.RegisterHandler("tasks/cancel", func(_ context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse {
		var params a2a.CancelTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid params: "+err.Error())
		}

		task := store.Get(params.ID)
		if task == nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "task not found: "+params.ID)
		}

		reason := coreruntime.CancellationReason(params.Reason)
		if reason == "" {
			reason = coreruntime.CancelReasonExternalSignal
		}
		signalled := r.cancelRegistry.Cancel(params.ID, reason)
		r.logger.Info("tasks/cancel", map[string]any{
			"task_id":   params.ID,
			"reason":    string(reason),
			"signalled": signalled,
		})
		// When the registry had no entry, the invocation already
		// finished (or never started). Leave the stored task untouched —
		// flipping a completed task to canceled would corrupt audit
		// and orchestrator state. The response echoes whatever the
		// store has so the orchestrator reads the actual outcome.
		return a2a.NewResponse(id, task)
	})
}

// executeTask is the shared task execution pipeline used by both JSON-RPC and REST handlers.
func (r *Runner) executeTask(
	ctx context.Context,
	params a2a.SendTaskParams,
	store *a2a.TaskStore,
	executor coreruntime.AgentExecutor,
	guardrails coreruntime.GuardrailChecker,
	egressClient *http.Client,
	auditLogger *coreruntime.AuditLogger,
) (*a2a.Task, coreruntime.LLMUsageSnapshot, error) {
	correlationID := coreruntime.GenerateID()
	ctx = security.WithEgressClient(ctx, egressClient)
	ctx = coreruntime.WithCorrelationID(ctx, correlationID)
	ctx = coreruntime.WithTaskID(ctx, params.ID)
	// FWS-8: per-invocation sequence counter (see issue #91 / FWS-8).
	// EnsureSequenceCounter reuses the counter the auth middleware
	// wrapper installed pre-auth so auth_verify lands seq=1 and
	// session_start lands seq=2 (#174); installs a fresh one when
	// missing (--no-auth path / direct test invocations).
	ctx = coreruntime.EnsureSequenceCounter(ctx)
	// Per-invocation usage accumulator so AfterLLMCall hooks can fold
	// each call's tokens/duration into running totals the response
	// handler reads back for X-Forge-* headers + the
	// invocation_complete audit event. See issue #87 / FWS-3.
	acc := coreruntime.NewLLMUsageAccumulator()
	ctx = coreruntime.WithLLMUsageAccumulator(ctx, acc)
	// Cancellation surface (issue #88 / FWS-4): derive a cancel-cause
	// ctx so the tasks/cancel handler can signal this in-flight
	// invocation by task ID. The release closure pops the registry
	// entry on return; cancel() at defer time is a no-op when the
	// invocation completed cleanly.
	ctx, cancelInvocation := context.WithCancelCause(ctx)
	release := r.cancelRegistry.Register(params.ID, cancelInvocation)
	defer release()
	defer cancelInvocation(nil) // nil cause = clean completion; no-op when already cancelled

	auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
		Event:         coreruntime.AuditSessionStart,
		CorrelationID: correlationID,
		TaskID:        params.ID,
	})

	task := store.Get(params.ID)
	if task == nil {
		task = &a2a.Task{ID: params.ID}
	}
	task.Status = a2a.TaskStatus{State: a2a.TaskStateSubmitted}
	store.Put(task)

	// emitInvocationLifecycle emits either invocation_complete or
	// invocation_cancelled at the response boundary, depending on
	// whether the ctx was cancelled mid-flight. The cancellation reason
	// flows through context.Cause — set by the tasks/cancel handler
	// via the cancellation registry. Partial usage from the accumulator
	// rides on both events so a downstream cost aggregator sees the
	// tokens consumed before cancellation. See issue #88 / FWS-4.
	emitInvocationLifecycle := func() {
		snap := acc.Snapshot()
		fields := map[string]any{"state": string(task.Status.State)}
		if snap.LLMCallCount > 0 {
			fields["input_tokens_total"] = snap.InputTokens
			fields["output_tokens_total"] = snap.OutputTokens
			fields["llm_call_count"] = snap.LLMCallCount
			if snap.PrimaryModel != "" {
				fields["model"] = snap.PrimaryModel
			}
			if snap.PrimaryProvider != "" {
				fields["provider"] = snap.PrimaryProvider
			}
		}
		if task.Status.State == a2a.TaskStateCanceled {
			auditLogger.EmitInvocationCancelled(ctx,
				coreruntime.CancellationReasonFromCause(ctx),
				snap.InvocationDuration, fields)
			return
		}
		auditLogger.EmitInvocationComplete(ctx, snap.InvocationDuration, fields)
	}

	if err := guardrails.CheckInbound(ctx, &params.Message); err != nil {
		task.Status = a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []a2a.Part{a2a.NewTextPart("Guardrail violation: " + err.Error())},
			},
		}
		store.Put(task)
		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionEnd,
			CorrelationID: correlationID,
			TaskID:        params.ID,
			Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
		})
		emitInvocationLifecycle()
		return task, acc.Snapshot(), nil
	}

	task.History = append(task.History, params.Message)
	task.Status = a2a.TaskStatus{State: a2a.TaskStateWorking}
	store.Put(task)

	respMsg, err := executor.Execute(ctx, task, &params.Message)
	if err != nil {
		// Cancellation gets a distinct lifecycle (state=canceled,
		// invocation_cancelled audit event) so the orchestrator can
		// distinguish "you asked me to stop" from "the agent crashed."
		// See issue #88 / FWS-4.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason := coreruntime.CancellationReasonFromCause(ctx)
			r.logger.Info("task cancelled mid-execution", map[string]any{
				"task_id": params.ID, "reason": string(reason),
			})
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateCanceled,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("cancelled: " + string(reason))},
				},
			}
			store.Put(task)
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateCanceled)},
			})
			emitInvocationLifecycle()
			return task, acc.Snapshot(), nil
		}
		r.logger.Error("execute failed", map[string]any{"task_id": params.ID, "error": err.Error()})
		task.Status = a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []a2a.Part{a2a.NewTextPart(err.Error())},
			},
		}
		store.Put(task)
		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionEnd,
			CorrelationID: correlationID,
			TaskID:        params.ID,
			Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
		})
		emitInvocationLifecycle()
		return task, acc.Snapshot(), nil
	}

	if respMsg != nil {
		if err := guardrails.CheckOutbound(ctx, respMsg); err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("Outbound guardrail violation: " + err.Error())},
				},
			}
			store.Put(task)
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			emitInvocationLifecycle()
			return task, acc.Snapshot(), nil
		}
	}

	if respMsg != nil {
		task.History = append(task.History, *respMsg)
	}

	task.Status = a2a.TaskStatus{
		State:   a2a.TaskStateCompleted,
		Message: respMsg,
	}
	if respMsg != nil {
		task.Artifacts = []a2a.Artifact{
			{
				Name:  "response",
				Parts: respMsg.Parts,
			},
		}
	}
	store.Put(task)
	auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
		Event:         coreruntime.AuditSessionEnd,
		CorrelationID: correlationID,
		TaskID:        params.ID,
		Fields:        map[string]any{"state": string(task.Status.State)},
	})
	emitInvocationLifecycle()
	r.logger.Info("task completed", map[string]any{"task_id": params.ID, "state": string(task.Status.State)})
	return task, acc.Snapshot(), nil
}

// restTaskRequest is the simplified JSON body for REST task endpoints.
type restTaskRequest struct {
	Task struct {
		ID      string      `json:"id"`
		Message a2a.Message `json:"message"`
	} `json:"task"`
}

// registerRESTHandlers registers REST-style HTTP endpoints on the server.
func (r *Runner) registerRESTHandlers(srv *server.Server, executor coreruntime.AgentExecutor, guardrails coreruntime.GuardrailChecker, egressClient *http.Client, auditLogger *coreruntime.AuditLogger) {
	store := srv.TaskStore()

	// POST /tasks/send — synchronous REST endpoint
	srv.RegisterHTTPHandler("POST /tasks/send", func(w http.ResponseWriter, req *http.Request) {
		var body restTaskRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
		if body.Task.ID == "" {
			body.Task.ID = coreruntime.GenerateID()
		}

		params := a2a.SendTaskParams{
			ID:      body.Task.ID,
			Message: body.Task.Message,
		}
		// A2A 0.3.0 message-shape validation (issue #119). Catches the
		// pre-0.3.0 `type` vs `kind` discriminator mismatch + missing
		// role / empty parts at the entry point so the executor never
		// sees a malformed message.
		if err := params.Message.Validate(); err != nil {
			r.logger.Warn("REST /tasks/send rejected: invalid message shape", map[string]any{
				"task_id":     params.ID,
				"reason":      err.Error(),
				"remote_addr": req.RemoteAddr,
			})
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid message: " + err.Error()})
			return
		}

		// Pull workflow correlation headers (issue #86 / FWS-2) so audit
		// events tagged via EmitFromContext carry the orchestrator's
		// workflow/stage/step identifiers. Absent headers → IsZero
		// WorkflowContext → fields omitted (backward compat).
		ctx := coreruntime.WithWorkflowContext(req.Context(),
			coreruntime.WorkflowContextFromHTTPHeaders(req.Header))
		// Same for tenancy override headers (#157).
		ctx = coreruntime.WithTenancyContext(ctx,
			coreruntime.TenancyContextFromHTTPHeaders(req.Header))
		task, snap, err := r.executeTask(ctx, params, store, executor, guardrails, egressClient, auditLogger)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		applyForgeUsageHeaders(w.Header(), snap)
		writeJSON(w, http.StatusOK, task)
	})

	// POST /tasks/sendSubscribe — SSE streaming REST endpoint
	srv.RegisterHTTPHandler("POST /tasks/sendSubscribe", func(w http.ResponseWriter, req *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
			return
		}

		var body restTaskRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
		if body.Task.ID == "" {
			body.Task.ID = coreruntime.GenerateID()
		}
		// A2A 0.3.0 message-shape validation (issue #119). Reject before
		// we commit SSE response headers — once Content-Type is set
		// to text/event-stream the client expects a stream, not a 400.
		if err := body.Task.Message.Validate(); err != nil {
			r.logger.Warn("REST /tasks/sendSubscribe rejected: invalid message shape", map[string]any{
				"task_id":     body.Task.ID,
				"reason":      err.Error(),
				"remote_addr": req.RemoteAddr,
			})
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid message: " + err.Error()})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		params := a2a.SendTaskParams{
			ID:      body.Task.ID,
			Message: body.Task.Message,
		}

		correlationID := coreruntime.GenerateID()
		ctx := security.WithEgressClient(req.Context(), egressClient)
		ctx = coreruntime.WithCorrelationID(ctx, correlationID)
		ctx = coreruntime.WithTaskID(ctx, params.ID)
		// FWS-8: per-invocation sequence counter so every audit event
		// emitted on behalf of this request carries a monotonically
		// increasing `seq` field — consumers detect gaps + ordering
		// at the export side. Reuse the counter
		// installSequenceCounterMiddleware put on ctx before auth ran
		// (#174); install fresh on the --no-auth path.
		ctx = coreruntime.EnsureSequenceCounter(ctx)
		// Pull workflow correlation headers (issue #86 / FWS-2) before
		// the accumulator setup so invocation_complete inherits workflow
		// tagging via EmitFromContext.
		ctx = coreruntime.WithWorkflowContext(ctx,
			coreruntime.WorkflowContextFromHTTPHeaders(req.Header))
		// Same for tenancy override headers (#157).
		ctx = coreruntime.WithTenancyContext(ctx,
			coreruntime.TenancyContextFromHTTPHeaders(req.Header))
		// Per-invocation usage accumulator + invocation_complete on exit.
		// See issue #87 / FWS-3.
		restSSEAcc := coreruntime.NewLLMUsageAccumulator()
		ctx = coreruntime.WithLLMUsageAccumulator(ctx, restSSEAcc)
		defer func() {
			snap := restSSEAcc.Snapshot()
			fields := map[string]any{}
			if snap.LLMCallCount > 0 {
				fields["input_tokens_total"] = snap.InputTokens
				fields["output_tokens_total"] = snap.OutputTokens
				fields["llm_call_count"] = snap.LLMCallCount
				if snap.PrimaryModel != "" {
					fields["model"] = snap.PrimaryModel
				}
				if snap.PrimaryProvider != "" {
					fields["provider"] = snap.PrimaryProvider
				}
			}
			auditLogger.EmitInvocationComplete(ctx, snap.InvocationDuration, fields)
		}()

		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionStart,
			CorrelationID: correlationID,
			TaskID:        params.ID,
		})

		task := store.Get(params.ID)
		if task == nil {
			task = &a2a.Task{ID: params.ID}
		}
		task.Status = a2a.TaskStatus{State: a2a.TaskStateSubmitted}
		store.Put(task)
		server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck

		if err := guardrails.CheckInbound(ctx, &params.Message); err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("Guardrail violation: " + err.Error())},
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return
		}

		task.History = append(task.History, params.Message)
		task.Status = a2a.TaskStatus{State: a2a.TaskStateWorking}
		store.Put(task)
		server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck

		ctx = coreruntime.WithProgressEmitter(ctx, func(event coreruntime.ProgressEvent) {
			progressTask := &a2a.Task{
				ID: params.ID,
				Status: a2a.TaskStatus{
					State: a2a.TaskStateWorking,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart(event.Message)},
					},
				},
				Metadata: map[string]any{
					"progress_phase": event.Phase,
					"progress_tool":  event.Tool,
				},
			}
			server.WriteSSEEvent(w, flusher, "progress", progressTask) //nolint:errcheck
		})

		ch, err := executor.ExecuteStream(ctx, task, &params.Message)
		if err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart(err.Error())},
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return
		}

		var finalState a2a.TaskState
		for respMsg := range ch {
			if grErr := guardrails.CheckOutbound(ctx, respMsg); grErr != nil {
				task.Status = a2a.TaskStatus{
					State: a2a.TaskStateFailed,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart("Outbound guardrail violation: " + grErr.Error())},
					},
				}
				store.Put(task)
				server.WriteSSEEvent(w, flusher, "result", task) //nolint:errcheck
				finalState = a2a.TaskStateFailed
				break
			}

			task.History = append(task.History, *respMsg)
			task.Status = a2a.TaskStatus{
				State:   a2a.TaskStateCompleted,
				Message: respMsg,
			}
			task.Artifacts = []a2a.Artifact{
				{
					Name:  "response",
					Parts: respMsg.Parts,
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "result", task) //nolint:errcheck
			finalState = a2a.TaskStateCompleted
		}

		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionEnd,
			CorrelationID: correlationID,
			TaskID:        params.ID,
			Fields:        map[string]any{"state": string(finalState)},
		})
	})

	// GET /health — health check with uptime
	srv.RegisterHTTPHandler("GET /health", func(w http.ResponseWriter, req *http.Request) {
		uptime := time.Since(r.startTime).Seconds()
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"uptime_seconds": int(uptime),
		})
	})

	// GET /info — agent metadata
	srv.RegisterHTTPHandler("GET /info", func(w http.ResponseWriter, req *http.Request) {
		info := map[string]any{
			"agent_id": r.cfg.Config.AgentID,
			"version":  r.cfg.Config.Version,
		}
		if r.modelConfig != nil {
			info["model"] = r.modelConfig.Provider + "/" + r.modelConfig.Client.Model
		}

		// Skills
		skillFiles := r.discoverSkillFiles()
		var skillNames []string
		for _, sf := range skillFiles {
			entries, _, err := cliskills.ParseFileWithMetadata(sf)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.Name != "" {
					skillNames = append(skillNames, e.Name)
				}
			}
		}
		if len(skillNames) > 0 {
			info["skills"] = skillNames
		}

		// Tools
		var toolNames []string
		for _, t := range r.cfg.Config.Tools {
			toolNames = append(toolNames, t.Name)
		}
		if len(toolNames) > 0 {
			info["tools"] = toolNames
		}

		// Channels
		if len(r.cfg.Channels) > 0 {
			info["channels"] = r.cfg.Channels
		}

		writeJSON(w, http.StatusOK, info)
	})
}

func (r *Runner) loadToolSpecs() []agentspec.ToolSpec {
	var toolSpecs []agentspec.ToolSpec
	for _, t := range r.cfg.Config.Tools {
		toolSpecs = append(toolSpecs, agentspec.ToolSpec{Name: t.Name})
	}
	return toolSpecs
}

// registerLoggingHooks adds observability hooks to the LLM executor's agent loop.
func (r *Runner) registerLoggingHooks(hooks *coreruntime.HookRegistry) {
	hooks.Register(coreruntime.AfterLLMCall, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if hctx.Response == nil {
			return nil
		}
		fields := map[string]any{
			"finish_reason": hctx.Response.FinishReason,
		}
		if hctx.Response.Usage.TotalTokens > 0 {
			fields["tokens"] = hctx.Response.Usage.TotalTokens
		}
		if len(hctx.Response.Message.ToolCalls) > 0 {
			names := make([]string, len(hctx.Response.Message.ToolCalls))
			for i, tc := range hctx.Response.Message.ToolCalls {
				names[i] = tc.Function.Name
			}
			fields["tool_calls"] = names
		}
		if hctx.Response.Message.Content != "" {
			content := hctx.Response.Message.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fields["response"] = content
		}
		r.logger.Info("llm response", fields)
		return nil
	})

	hooks.Register(coreruntime.BeforeToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName}
		if hctx.ToolInput != "" {
			input := hctx.ToolInput
			if len(input) > 300 {
				input = input[:300] + "..."
			}
			fields["input"] = input
		}
		r.logger.Info("tool call", fields)
		return nil
	})

	hooks.Register(coreruntime.AfterToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName}
		if hctx.Error != nil {
			fields["error"] = hctx.Error.Error()
			r.logger.Error("tool error", fields)
		} else {
			output := hctx.ToolOutput
			if len(output) > 500 {
				output = output[:500] + "..."
			}
			fields["output_length"] = len(hctx.ToolOutput)
			fields["output"] = output
			r.logger.Info("tool result", fields)
		}
		return nil
	})

	hooks.Register(coreruntime.OnError, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if hctx.Error != nil {
			r.logger.Error("agent loop error", map[string]any{"error": hctx.Error.Error()})
		}
		return nil
	})
}

// registerAuditHooks adds structured audit event hooks to the LLM executor's agent loop.
// The default audit posture is metadata-only — token counts, sizes,
// durations, tool names, no raw bytes. r.cfg.AuditPayloadCapture
// (issue #91 / FWS-8) opts each capture surface in individually:
// LLMMessages, LLMResponse, ToolArgs, ToolResult. Captured strings
// are truncated to a per-field byte cap so a runaway prompt or
// gigabyte tool output cannot bloat one event.
func (r *Runner) registerAuditHooks(hooks *coreruntime.HookRegistry, auditLogger *coreruntime.AuditLogger) {
	capture := r.cfg.AuditPayloadCapture

	hooks.Register(coreruntime.BeforeToolExec, func(ctxStart context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName, "phase": "start"}
		// FWS-8: opt-in raw tool args. We only emit them here at the
		// start hook (the end hook has them too — duplicating would
		// double the audit footprint). args_size always lands; args
		// itself only when capture is enabled.
		if hctx.ToolInput != "" {
			fields["args_size"] = len(hctx.ToolInput)
			if capture.ToolArgs {
				fields["args"] = coreruntime.TruncateForAudit(hctx.ToolInput,
					coreruntime.CapOrDefault(capture.CapToolArgsBytes))
			}
		}
		auditLogger.EmitFromContext(ctxStart, coreruntime.AuditEvent{
			Event:         coreruntime.AuditToolExec,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			Fields:        fields,
		})
		return nil
	})

	hooks.Register(coreruntime.AfterToolExec, func(ctxEnd context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName, "phase": "end"}
		if hctx.Error != nil {
			fields["error"] = hctx.Error.Error()
		}
		if hctx.ToolInput != "" {
			fields["args_size"] = len(hctx.ToolInput)
		}
		if hctx.ToolOutput != "" {
			fields["result_size"] = len(hctx.ToolOutput)
			// FWS-8: opt-in raw tool result.
			if capture.ToolResult {
				fields["result"] = coreruntime.TruncateForAudit(hctx.ToolOutput,
					coreruntime.CapOrDefault(capture.CapToolResultBytes))
			}
		}
		ms := hctx.ToolExecDuration.Milliseconds()
		auditLogger.EmitFromContext(ctxEnd, coreruntime.AuditEvent{
			Event:         coreruntime.AuditToolExec,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			DurationMs:    &ms,
			Fields:        fields,
		})
		return nil
	})

	hooks.Register(coreruntime.AfterLLMCall, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		var usage coreruntime.LLMUsage
		var requestID string
		if hctx.Response != nil {
			usage.InputTokens = hctx.Response.Usage.InputTokens
			usage.OutputTokens = hctx.Response.Usage.OutputTokens
			usage.TotalTokens = hctx.Response.Usage.TotalTokens
			requestID = hctx.Response.ID
		}
		// FWS-8 payload-capture surfaces. Fields stays nil in the
		// default (metadata-only) posture so the emitted event's
		// `fields` key omits cleanly.
		var fields map[string]any
		if capture.LLMMessages && len(hctx.Messages) > 0 {
			if fields == nil {
				fields = map[string]any{}
			}
			marshaled, _ := json.Marshal(hctx.Messages)
			fields["prompt_messages"] = coreruntime.TruncateForAudit(string(marshaled),
				coreruntime.CapOrDefault(capture.CapLLMMessagesBytes))
			fields["prompt_messages_count"] = len(hctx.Messages)
		}
		if capture.LLMResponse && hctx.Response != nil && hctx.Response.Message.Content != "" {
			if fields == nil {
				fields = map[string]any{}
			}
			fields["completion_text"] = coreruntime.TruncateForAudit(hctx.Response.Message.Content,
				coreruntime.CapOrDefault(capture.CapLLMResponseBytes))
		}
		auditLogger.EmitLLMCall(ctx, coreruntime.LLMCallAuditArgs{
			Model:     hctx.Model,
			Provider:  hctx.Provider,
			RequestID: requestID,
			Usage:     usage,
			Duration:  hctx.LLMCallDuration,
			Fields:    fields,
		})
		// Accumulate per-invocation usage totals so the response handler
		// can populate X-Forge-Tokens-In/Out + X-Forge-Duration-Ms +
		// X-Forge-Model + X-Forge-Provider headers. See issue #87 / FWS-3.
		if acc := coreruntime.LLMUsageAccumulatorFromContext(ctx); acc != nil {
			acc.AddLLMCall(hctx.Model, hctx.Provider, usage, hctx.LLMCallDuration)
		}
		return nil
	})
}

// registerProgressHooks adds hooks that emit progress events via ProgressEmitter.
// The emitter is injected into context by SSE handlers so clients receive real-time
// progress during long-running tool executions.
func (r *Runner) registerProgressHooks(hooks *coreruntime.HookRegistry) {
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if emitter := coreruntime.ProgressEmitterFromContext(ctx); emitter != nil {
			emitter(coreruntime.ProgressEvent{
				Phase:   "tool_start",
				Tool:    hctx.ToolName,
				Message: fmt.Sprintf("Executing %s...", hctx.ToolName),
			})
		}
		return nil
	})

	hooks.Register(coreruntime.AfterToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if emitter := coreruntime.ProgressEmitterFromContext(ctx); emitter != nil {
			msg := fmt.Sprintf("Completed %s", hctx.ToolName)
			if hctx.Error != nil {
				msg = fmt.Sprintf("Failed %s: %s", hctx.ToolName, hctx.Error.Error())
			}
			emitter(coreruntime.ProgressEvent{
				Phase:   "tool_end",
				Tool:    hctx.ToolName,
				Message: msg,
			})
		}
		return nil
	})
}

// registerGuardrailHooks registers all four runtime-side guardrail
// gates as hooks on the agent loop:
//
//   - BeforeLLMCall  → ContextGate over each system-role message
//     (closest thing Forge has to "retrieved context" today;
//     future memory / RAG work can call CheckContext directly from
//     the recall path for a finer-grained seam)
//   - BeforeToolExec → ToolCallGate over the args the agent passes
//     to the tool
//   - AfterToolExec  → OutputGate over the tool's return text (with
//     fields.tool set so the emitted guardrail_check distinguishes
//     it from output-gate fires on the model's reply to the user)
//
// CheckInbound / CheckOutbound are called directly from the A2A
// handlers in registerHandlers* — they sit outside the agent loop's
// hook surface because the loop only sees ChatMessages, not the
// outer A2A envelope.
//
// StreamGate has no auto-wire point — Forge's ExecuteStream is a
// buffered wrapper around non-streaming Execute. The CheckStream
// method is exposed for callers that consume llm.Client.ChatStream
// directly. See issue #159.
func (r *Runner) registerGuardrailHooks(hooks *coreruntime.HookRegistry, guardrails coreruntime.GuardrailChecker) {
	// ContextGate over system-role messages. Re-scans on every
	// iteration — acceptable because system messages are small and
	// the library's evaluator chain is cheap when no rule matches.
	hooks.Register(coreruntime.BeforeLLMCall, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		for i, m := range hctx.Messages {
			if m.Role != "system" || m.Content == "" {
				continue
			}
			masked, err := guardrails.CheckContext(ctx, m.Content)
			if err != nil {
				return err
			}
			if masked != m.Content {
				hctx.Messages[i].Content = masked
			}
		}
		return nil
	})
	// ToolCallGate over the args the agent is about to pass.
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if hctx.ToolInput == "" {
			return nil
		}
		masked, err := guardrails.CheckToolCall(ctx, hctx.ToolName, hctx.ToolInput)
		if err != nil {
			return err
		}
		hctx.ToolInput = masked
		return nil
	})
	// OutputGate over the tool's return text (existing).
	hooks.Register(coreruntime.AfterToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if hctx.ToolOutput == "" {
			return nil
		}
		redacted, err := guardrails.CheckToolOutput(ctx, hctx.ToolName, hctx.ToolOutput)
		if err != nil {
			return err
		}
		hctx.ToolOutput = redacted
		return nil
	})
}

// registerSkillGuardrailHooks registers hooks that enforce skill-declared deny
// patterns on user prompts (BeforeLLMCall), command inputs (BeforeToolExec),
// and tool outputs (AfterToolExec).
func (r *Runner) registerSkillGuardrailHooks(hooks *coreruntime.HookRegistry, sg *coreruntime.SkillGuardrailEngine) {
	// Block capability-enumeration and other denied prompts before the LLM sees them.
	hooks.Register(coreruntime.BeforeLLMCall, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if len(hctx.Messages) == 0 {
			return nil
		}
		// Check only the latest user message.
		last := hctx.Messages[len(hctx.Messages)-1]
		if last.Role == "user" {
			return sg.CheckUserInput(last.Content)
		}
		return nil
	})
	hooks.Register(coreruntime.BeforeToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		return sg.CheckCommandInput(hctx.ToolName, hctx.ToolInput)
	})
	hooks.Register(coreruntime.AfterToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		redacted, err := sg.CheckCommandOutput(hctx.ToolName, hctx.ToolOutput)
		if err != nil {
			return err
		}
		hctx.ToolOutput = redacted
		return nil
	})
	// Rewrite LLM responses that enumerate binary names or internal tooling.
	hooks.Register(coreruntime.AfterLLMCall, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if hctx.Response == nil {
			return nil
		}
		replaced, changed := sg.CheckLLMResponse(hctx.Response.Message.Content)
		if changed {
			hctx.Response.Message.Content = replaced
		}
		return nil
	})
}

// buildLLMClient creates the LLM client from the resolved model config.
// If fallback providers are configured, wraps them in a FallbackChain.
func (r *Runner) buildLLMClient(mc *coreruntime.ModelConfig) (llm.Client, error) {
	primaryClient, err := r.createProviderClient(mc.Provider, mc.Client)
	if err != nil {
		return nil, err
	}

	// No fallbacks — return primary client directly
	if len(mc.Fallbacks) == 0 {
		return primaryClient, nil
	}

	// Build fallback chain
	candidates := []llm.FallbackCandidate{
		{Provider: mc.Provider, Model: mc.Client.Model, Client: primaryClient},
	}
	for _, fb := range mc.Fallbacks {
		fbClient, fbErr := r.createProviderClient(fb.Provider, fb.Client)
		if fbErr != nil {
			r.logger.Warn("skipping fallback provider", map[string]any{
				"provider": fb.Provider, "error": fbErr.Error(),
			})
			continue
		}
		candidates = append(candidates, llm.FallbackCandidate{
			Provider: fb.Provider,
			Model:    fb.Client.Model,
			Client:   fbClient,
		})
	}

	return llm.NewFallbackChain(candidates), nil
}

// createProviderClient creates an LLM client for a provider, using OAuth
// credentials if available for supported providers.
//
// OAuth precedence guardrail (issue #83): when the operator has set
// OPENAI_BASE_URL (i.e. an explicit OpenAI-compatible endpoint), do NOT
// fall through to the stored ChatGPT OAuth credentials — the OAuth
// path overrides cfg.BaseURL with chatgpt.com/backend-api/codex and
// silently routes requests there, defeating the explicit override.
// An operator pointing at OpenRouter / vLLM / Kimi / etc. must set
// OPENAI_API_KEY for that endpoint; if it's missing, surface the
// configuration error rather than tunneling to ChatGPT.
func (r *Runner) createProviderClient(provider string, cfg llm.ClientConfig) (llm.Client, error) {
	// Check for stored OAuth credentials — but only if no real API key is
	// configured. The "__oauth__" sentinel means the user chose OAuth auth
	// during init, so we should load the actual token from the credential store.
	needsOAuth := provider == "openai" && (cfg.APIKey == "" || cfg.APIKey == "__oauth__")

	// Explicit OPENAI_BASE_URL disqualifies the OAuth path. The OAuth
	// flow's base URL (chatgpt.com/backend-api/codex) is mutually
	// exclusive with a user-supplied endpoint.
	if needsOAuth && cfg.BaseURL != "" {
		return nil, fmt.Errorf(
			"OPENAI_BASE_URL is set to %q but no OPENAI_API_KEY was provided; "+
				"the OpenAI OAuth credentials path is disabled when an explicit "+
				"base URL is in use (it would silently override your endpoint with "+
				"chatgpt.com/backend-api/codex). Set OPENAI_API_KEY for the configured endpoint",
			cfg.BaseURL,
		)
	}

	if needsOAuth {
		token, err := oauth.LoadCredentials(provider)
		if err == nil && token != nil && token.RefreshToken != "" {
			oauthCfg := oauth.OpenAIConfig()
			// Use token's base URL, or fall back to the OAuth config default
			baseURL := token.BaseURL
			if baseURL == "" {
				baseURL = oauthCfg.BaseURL
			}
			r.logger.Info("using OAuth credentials for provider", map[string]any{
				"provider": provider,
				"base_url": baseURL,
			})
			cfg.APIKey = token.AccessToken
			cfg.BaseURL = baseURL
			return providers.NewOAuthClient(cfg, provider, oauthCfg), nil
		}
		// No API key and OAuth failed — surface the error instead of
		// creating a client with no auth that will fail with 401.
		if cfg.APIKey == "" || cfg.APIKey == "__oauth__" {
			if err != nil {
				return nil, fmt.Errorf("loading OAuth credentials: %w", err)
			}
			return nil, fmt.Errorf("no OpenAI API key or OAuth credentials found; run 'forge init' with OAuth or set OPENAI_API_KEY")
		}
	}

	return providers.NewClient(provider, cfg)
}

func (r *Runner) printBanner(proxyURL string) {
	title := "Forge Dev Server"
	if r.cfg.Host != "" {
		title = "Forge Server"
	}
	host := defaultStr(r.cfg.Host, "0.0.0.0")

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  %s\n", title)
	fmt.Fprintf(os.Stderr, "  ────────────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Agent:      %s (v%s)\n", r.cfg.Config.AgentID, r.cfg.Config.Version)
	fmt.Fprintf(os.Stderr, "  Framework:  %s\n", r.cfg.Config.Framework)
	fmt.Fprintf(os.Stderr, "  Listen:     %s:%d\n", host, r.cfg.Port)
	if r.cfg.MockTools {
		fmt.Fprintf(os.Stderr, "  Mode:       mock (no subprocess)\n")
	} else if r.cfg.Config.Entrypoint != "" {
		fmt.Fprintf(os.Stderr, "  Entrypoint: %s\n", r.cfg.Config.Entrypoint)
	}
	// Model info
	if r.modelConfig != nil {
		fmt.Fprintf(os.Stderr, "  Model:      %s/%s\n", r.modelConfig.Provider, r.modelConfig.Client.Model)
		if len(r.modelConfig.Fallbacks) > 0 {
			var fbNames []string
			for _, fb := range r.modelConfig.Fallbacks {
				fbNames = append(fbNames, fb.Provider+"/"+fb.Client.Model)
			}
			fmt.Fprintf(os.Stderr, "  Fallbacks:  %s\n", strings.Join(fbNames, ", "))
		}
	}
	// Tools
	if len(r.cfg.Config.Tools) > 0 {
		names := make([]string, 0, len(r.cfg.Config.Tools))
		for _, t := range r.cfg.Config.Tools {
			names = append(names, t.Name)
		}
		fmt.Fprintf(os.Stderr, "  Tools:      %d (%s)\n", len(names), strings.Join(names, ", "))
	}
	// CLI Exec binaries
	if r.cliExecTool != nil {
		avail, missing := r.cliExecTool.Availability()
		total := len(avail) + len(missing)
		parts := make([]string, 0, total)
		for _, b := range avail {
			parts = append(parts, b+" ok")
		}
		for _, b := range missing {
			parts = append(parts, b+" MISSING")
		}
		fmt.Fprintf(os.Stderr, "  CLI Exec:   %d/%d binaries (%s)\n", len(avail), total, strings.Join(parts, ", "))
	}
	// Channels
	if len(r.cfg.Channels) > 0 {
		fmt.Fprintf(os.Stderr, "  Channels:   %s\n", strings.Join(r.cfg.Channels, ", "))
	}
	// Egress
	if r.cfg.Config.Egress.Profile != "" || r.cfg.Config.Egress.Mode != "" {
		fmt.Fprintf(os.Stderr, "  Egress:     %s / %s\n",
			defaultStr(r.cfg.Config.Egress.Profile, "strict"),
			defaultStr(r.cfg.Config.Egress.Mode, "deny-all"))
	}
	// Auth
	if r.cfg.NoAuth {
		fmt.Fprintf(os.Stderr, "  Auth:       disabled (--no-auth)\n")
	} else if r.cfg.AuthURL != "" {
		fmt.Fprintf(os.Stderr, "  Auth:       external (%s)\n", r.cfg.AuthURL)
	} else if r.authToken != "" {
		fmt.Fprintf(os.Stderr, "  Auth:       enabled (token in .forge/runtime.token)\n")
	}
	// LAN exposure warning
	if !isLocalhost(r.cfg.Host) && !r.cfg.NoAuth {
		fmt.Fprintf(os.Stderr, "  WARNING:    binding to non-localhost; ensure firewall rules are in place\n")
	}
	// Egress proxy
	if proxyURL != "" {
		fmt.Fprintf(os.Stderr, "  Proxy:      %s\n", proxyURL)
	}
	fmt.Fprintf(os.Stderr, "  ────────────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Agent Card: http://localhost:%d/.well-known/agent-card.json\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  Health:     http://localhost:%d/healthz\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  REST:       http://localhost:%d/tasks/send\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  JSON-RPC:   POST http://localhost:%d/\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  ────────────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Press Ctrl+C to stop\n\n")
}

// resolveAuth builds the auth middleware options for the A2A server.
//
// Source precedence (highest first):
//  1. --no-auth flag       → nil chain, anonymous access
//  2. forge.yaml auth:     → Registry.BuildChain(cfg.Auth.Providers)
//  3. --auth-url / env     → legacy http_verifier chain
//  4. nothing              → loopback-token-only chain (or nil if also no channels)
//
// If BOTH forge.yaml auth: AND --auth-url are configured, the YAML block
// wins and a warning is logged — silent merging would be surprising.
//
// Loopback static_token prepending:
//   - The internal loopback token is prepended at the chain head WHEN
//     ResolveAuth() has minted one (i.e., r.authToken != ""). In the
//     non-NoAuth path that's an invariant ResolveAuth maintains (review
//     #10). When --no-auth is in effect we return early via the
//     AllowAnonymous path above and never reach the prepend.
//   - Channel adapter callbacks rely on the loopback short-circuit; if
//     ResolveAuth is ever refactored to skip token minting on the
//     non-NoAuth path, channels will silently break. TestResolveAuth_
//     InvariantMintsTokenInNonNoAuthPath in auth_chain_test.go pins
//     that invariant.
func (r *Runner) resolveAuth(auditLogger *coreruntime.AuditLogger) (auth.MiddlewareOptions, error) {
	// Ensure token is resolved (no-op if already done by ResolveAuth).
	if err := r.ResolveAuth(); err != nil {
		return auth.MiddlewareOptions{}, err
	}

	if r.cfg.NoAuth {
		// Cross-check --no-auth against the forge.yaml auth block. The
		// flag and the YAML have historically been treated independently;
		// review #4 closes the gap so a misaligned pair fails loudly
		// instead of silently serving anonymous traffic on what the
		// operator declared a required-auth deployment.
		if r.cfg.Config != nil {
			authCfg := r.cfg.Config.Auth
			if authCfg.Required {
				return auth.MiddlewareOptions{}, fmt.Errorf(
					"--no-auth conflicts with forge.yaml 'auth.required: true' — " +
						"either remove --no-auth, set 'auth.required: false', or " +
						"delete the 'auth:' block to confirm anonymous access is intended")
			}
			if len(authCfg.Providers) > 0 {
				r.logger.Warn(
					"--no-auth overrides forge.yaml 'auth.providers' — configured "+
						"providers will be ignored and the agent will accept anonymous "+
						"traffic. Remove --no-auth to enforce the configured chain.",
					map[string]any{"providers_configured": len(authCfg.Providers)},
				)
			}
		}
		// Operator explicitly chose anonymous via --no-auth. AllowAnonymous
		// makes that choice visible at the middleware boundary (review #3).
		return auth.MiddlewareOptions{
			AllowAnonymous: true,
			SkipPaths:      auth.DefaultSkipPaths(),
		}, nil
	}

	userChain, err := r.buildUserAuthChain()
	if err != nil {
		return auth.MiddlewareOptions{}, fmt.Errorf("building auth chain: %w", err)
	}

	// Prepend the loopback static_token so channel adapter callbacks
	// short-circuit before any user-configured provider runs.
	//
	// PRECONDITION: r.authToken is non-empty on this branch because
	// ResolveAuth() always mints one in the non-NoAuth path. The
	// conditional here is defensive — if someone refactors ResolveAuth
	// to skip minting (and forgets to update channels), we'd want the
	// rest of the function to still produce a coherent middleware
	// (without a loopback) rather than panic. The invariant itself is
	// pinned by TestResolveAuth_InvariantMintsTokenInNonNoAuthPath
	// (review #10).
	chain := userChain
	if r.authToken != "" {
		loopback, err := statictoken.New(statictoken.Config{
			Token: r.authToken,
			Identity: auth.Identity{
				UserID: "forge-internal",
				Source: "internal",
			},
		})
		if err != nil {
			return auth.MiddlewareOptions{}, fmt.Errorf("loopback static_token: %w", err)
		}
		chain = auth.PrependChain(userChain, loopback)
	}

	// No user chain AND no loopback token → legacy "no auth config, no
	// channels" default. Preserve backward compat by allowing anonymous,
	// but flag it explicitly so the middleware's nil-chain panic guard
	// is satisfied (review #3).
	if chain == nil {
		return auth.MiddlewareOptions{
			AllowAnonymous: true,
			SkipPaths:      auth.DefaultSkipPaths(),
			OnAuth:         makeAuthAuditCallback(auditLogger),
		}, nil
	}

	return auth.MiddlewareOptions{
		Chain:     chain,
		SkipPaths: auth.DefaultSkipPaths(),
		OnAuth:    makeAuthAuditCallback(auditLogger),
	}, nil
}

// makeAuthAuditCallback returns the OnAuth callback that emits structured
// auth_verify / auth_fail audit events.
//
// Fields emitted (NO PII — never email, claims, token bytes, or secrets):
//
//	auth_verify: { provider, user_id, org_id, groups_count, token_kind, method, path, remote_addr }
//	auth_fail:   { reason, token_kind, method, path, remote_addr }
//
// Reason codes for auth_fail:
//
//	missing_token   → no Authorization header
//	rejected        → provider recognized + denied (revoked, expired, wrong iss/aud)
//	invalid         → token malformed or cryptographically invalid
//	not_for_me      → chain exhausted (no provider claimed the token)
//	infrastructure  → transient provider error (network, etc.)
func makeAuthAuditCallback(auditLogger *coreruntime.AuditLogger) func(*http.Request, *auth.Identity, error, string) {
	if auditLogger == nil {
		return nil
	}
	return func(req *http.Request, id *auth.Identity, err error, tokenKind string) {
		correlationID := coreruntime.CorrelationIDFromContext(req.Context())
		// Auth middleware runs BEFORE handleJSONRPC has had a chance to
		// extract workflow correlation headers into ctx. Pull them
		// directly from req.Header here so auth events still carry
		// workflow tags. Empty when the orchestrator didn't send them
		// — fields then omit (backward compat).
		wc := coreruntime.WorkflowContextFromHTTPHeaders(req.Header)
		// Same for the per-request tenancy override (#157). When
		// absent, the AuditLogger's static deployment-time stamp still
		// kicks in so auth events match the rest of the stream's
		// org_id / workspace_id columns.
		tc := coreruntime.TenancyContextFromHTTPHeaders(req.Header)

		// EmitFromContext stamps `seq` from the SequenceCounter the
		// installSequenceCounterMiddleware wrapper installed on
		// req.Context() before the auth chain ran (#174). The
		// runner's per-A2A-request setup downstream calls
		// EnsureSequenceCounter and reuses this counter, so
		// session_start lands at seq=2 and the per-correlation_id
		// sequence is gap-free for FWS-8 consumers.
		if err == nil && id != nil {
			// Success → auth_verify.
			fields := map[string]any{
				"provider":     id.Source,
				"user_id":      id.UserID,
				"org_id":       id.OrgID,
				"groups_count": len(id.Groups),
				"token_kind":   tokenKind,
				"method":       req.Method,
				"path":         req.URL.Path,
				"remote_addr":  req.RemoteAddr,
			}
			auditLogger.EmitFromContext(req.Context(), coreruntime.AuditEvent{
				Event:            coreruntime.EventAuthVerify,
				CorrelationID:    correlationID,
				WorkflowID:       wc.WorkflowID,
				StageID:          wc.StageID,
				StepID:           wc.StepID,
				InvocationCaller: wc.InvocationCaller,
				OrgID:            tc.OrgID,
				WorkspaceID:      tc.WorkspaceID,
				Fields:           fields,
			})
			return
		}

		// Failure → auth_fail with reason code.
		auditLogger.EmitFromContext(req.Context(), coreruntime.AuditEvent{
			Event:            coreruntime.EventAuthFail,
			CorrelationID:    correlationID,
			WorkflowID:       wc.WorkflowID,
			StageID:          wc.StageID,
			StepID:           wc.StepID,
			InvocationCaller: wc.InvocationCaller,
			OrgID:            tc.OrgID,
			WorkspaceID:      tc.WorkspaceID,
			Fields: map[string]any{
				"reason":      authFailReason(err),
				"token_kind":  tokenKind,
				"method":      req.Method,
				"path":        req.URL.Path,
				"remote_addr": req.RemoteAddr,
			},
		})
	}
}

// authFailReason maps a chain error to a stable, low-cardinality reason
// code suitable for dashboarding and alerting. Reason strings are part
// of the audit-event contract — changing them is a breaking change for
// downstream consumers.
//
// Reason codes:
//
//	missing_token        - no Authorization header
//	rejected             - provider recognized + denied (revoked, expired, 401, 4xx)
//	invalid              - token malformed or cryptographically invalid
//	not_for_me           - chain exhausted, no provider claimed the token
//	provider_unavailable - verifier/IdP unreachable (5xx, network, undecodable)
//	infrastructure       - other unexpected error
//
// provider_unavailable was added in review #6 so operators can
// distinguish "the token is bad" alerts from "the IdP is down" alerts
// in their dashboards — the response and the runbook are different.
func authFailReason(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, auth.ErrMissingBearer):
		return "missing_token"
	case errors.Is(err, auth.ErrTokenRejected):
		return "rejected"
	case errors.Is(err, auth.ErrInvalidToken):
		return "invalid"
	case errors.Is(err, auth.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, auth.ErrTokenNotForMe):
		return "not_for_me"
	default:
		return "infrastructure"
	}
}

// buildUserAuthChain returns the user-facing portion of the auth chain
// (everything EXCEPT the loopback static_token, which the caller prepends).
//
//   - forge.yaml auth.providers populated → Registry.BuildChain
//   - --auth-url / FORGE_AUTH_URL only     → legacy http_verifier
//   - neither                              → nil (no user chain)
func (r *Runner) buildUserAuthChain() (auth.Provider, error) {
	hasYAMLAuth := len(r.cfg.Config.Auth.Providers) > 0
	hasLegacyURL := r.cfg.AuthURL != ""

	if hasYAMLAuth && hasLegacyURL {
		r.logger.Warn("both --auth-url and forge.yaml 'auth:' block configured; preferring 'auth:' block (--auth-url ignored)", nil)
	}

	if hasYAMLAuth {
		return buildChainFromConfig(r.cfg.Config.Auth)
	}
	if hasLegacyURL {
		return buildLegacyHTTPVerifierChain(r.cfg.AuthURL, r.cfg.AuthOrgID)
	}
	return nil, nil
}

// buildChainFromConfig builds a ChainProvider from a typed AuthConfig by
// delegating to the package-level registry. Each AuthProvider entry is
// constructed via its registered factory.
func buildChainFromConfig(cfg types.AuthConfig) (auth.Provider, error) {
	if len(cfg.Providers) == 0 {
		return nil, nil
	}
	providers := make([]auth.Provider, 0, len(cfg.Providers))
	for i, spec := range cfg.Providers {
		p, err := auth.Build(spec.Type, spec.Settings)
		if err != nil {
			return nil, fmt.Errorf("auth.providers[%d] (%s): %w", i, spec.Type, err)
		}
		providers = append(providers, p)
	}
	return auth.NewChainProvider(providers...), nil
}

// buildLegacyHTTPVerifierChain constructs the legacy --auth-url chain
// (single http_verifier provider). Kept separate from
// buildChainFromConfig so the legacy code path is obvious.
func buildLegacyHTTPVerifierChain(authURL, authOrgID string) (auth.Provider, error) {
	p, err := httpverifier.New(httpverifier.Config{
		URL:        authURL,
		DefaultOrg: authOrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("legacy http_verifier: %w", err)
	}
	return auth.NewChainProvider(p), nil
}

// buildLegacyAuthChain is a test-friendly helper that combines the
// loopback-token prepend and the legacy http_verifier chain. Mirrors the
// production wiring in resolveAuth so PR1's e2e tests keep working
// unchanged.
//
// Order:
//  1. static_token (loopback) — if internalToken non-empty
//  2. http_verifier — if authURL non-empty
//
// Returns nil chain when both inputs are empty.
func buildLegacyAuthChain(internalToken, authURL, authOrgID string) (auth.Provider, error) {
	var userChain auth.Provider
	if authURL != "" {
		c, err := buildLegacyHTTPVerifierChain(authURL, authOrgID)
		if err != nil {
			return nil, err
		}
		userChain = c
	}
	if internalToken != "" {
		loopback, err := statictoken.New(statictoken.Config{
			Token: internalToken,
			Identity: auth.Identity{
				UserID: "forge-internal",
				Source: "internal",
			},
		})
		if err != nil {
			return nil, fmt.Errorf("static_token provider: %w", err)
		}
		if userChain == nil {
			return auth.NewChainProvider(loopback), nil
		}
		return auth.PrependChain(userChain, loopback), nil
	}
	return userChain, nil
}

// ensureGitignore makes sure .forge/ is listed in the project's .gitignore.
func ensureGitignore(workDir string) {
	gitignorePath := filepath.Join(workDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return
	}

	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == ".forge/" || strings.TrimSpace(line) == ".forge" {
			return // already present
		}
	}

	// Append .forge/ to .gitignore.
	entry := ".forge/\n"
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		entry = "\n" + entry
	}
	os.WriteFile(gitignorePath, []byte(content+entry), 0644) //nolint:errcheck
}

// hasSkill checks whether a skill with the given name is present in the project's
// discovered skill files. Checks both ## Tool: entry names and frontmatter name.
func (r *Runner) hasSkill(name string) bool {
	for _, sf := range r.discoverSkillFiles() {
		entries, meta, err := cliskills.ParseFileWithMetadata(sf)
		if err != nil {
			continue
		}
		// Check frontmatter name (for skills without ## Tool: entries)
		if meta != nil && meta.Name == name {
			return true
		}
		// Check individual tool entry names
		for _, e := range entries {
			if e.Name == name {
				return true
			}
		}
	}
	return false
}

// discoverSkillFiles returns all skill file paths from both flat and subdirectory formats,
// plus the main SKILL.md (or custom path from forge.yaml).
func (r *Runner) discoverSkillFiles() []string {
	skillsDir := filepath.Join(r.cfg.WorkDir, "skills")

	// Flat format: skills/*.md
	matches, _ := filepath.Glob(filepath.Join(skillsDir, "*.md"))

	// Subdirectory format: skills/*/SKILL.md
	subDirMatches, _ := filepath.Glob(filepath.Join(skillsDir, "*", "SKILL.md"))
	matches = append(matches, subDirMatches...)

	// Main SKILL.md (or custom path from forge.yaml)
	mainSkill := "SKILL.md"
	if r.cfg.Config.Skills.Path != "" {
		mainSkill = r.cfg.Config.Skills.Path
	}
	if !filepath.IsAbs(mainSkill) {
		mainSkill = filepath.Join(r.cfg.WorkDir, mainSkill)
	}
	if info, err := os.Stat(mainSkill); err == nil && !info.IsDir() {
		matches = append(matches, mainSkill)
	}

	return matches
}

// registerSkillTools scans skill files for skill entries that have associated
// scripts. Each script-backed skill is registered as a first-class tool in the registry.
func (r *Runner) registerSkillTools(reg *tools.Registry, proxyURL string) {
	matches := r.discoverSkillFiles()

	var registered int
	for _, match := range matches {
		entries, meta, err := cliskills.ParseFileWithMetadata(match)
		if err != nil {
			continue
		}

		// Derive skill directory name from the SKILL.md path (for subdirectory skills)
		skillDirName := ""
		if strings.HasSuffix(match, "/SKILL.md") {
			skillDirName = filepath.Base(filepath.Dir(match))
		}

		for _, entry := range entries {
			// Map tool name (underscores) to script name (hyphens)
			scriptName := strings.ReplaceAll(entry.Name, "_", "-")

			// Look for scripts in subdirectory layout first: skills/{dir}/scripts/{name}.sh
			// Then fall back to legacy flat layout: skills/scripts/{name}.sh
			var scriptPath string
			if skillDirName != "" {
				candidate := filepath.Join("skills", skillDirName, "scripts", scriptName+".sh")
				if _, err := os.Stat(filepath.Join(r.cfg.WorkDir, candidate)); err == nil {
					scriptPath = candidate
				}
			}
			if scriptPath == "" {
				candidate := filepath.Join("skills", "scripts", scriptName+".sh")
				if _, err := os.Stat(filepath.Join(r.cfg.WorkDir, candidate)); err == nil {
					scriptPath = candidate
				}
			}
			if scriptPath == "" {
				continue // No script file, skip
			}

			// Extract timeout_hint from metadata
			timeout := 120 * time.Second
			if meta != nil && meta.Metadata != nil {
				if forgeMap, ok := meta.Metadata["forge"]; ok {
					if raw, ok := forgeMap["timeout_hint"]; ok {
						switch v := raw.(type) {
						case int:
							timeout = time.Duration(v) * time.Second
						case float64:
							timeout = time.Duration(int(v)) * time.Second
						}
					}
				}
			}

			// Collect env vars for passthrough
			var envVars []string
			if entry.ForgeReqs != nil && entry.ForgeReqs.Env != nil {
				envVars = append(envVars, entry.ForgeReqs.Env.Required...)
				envVars = append(envVars, entry.ForgeReqs.Env.OneOf...)
				envVars = append(envVars, entry.ForgeReqs.Env.Optional...)
			}

			var modelName string
			if r.modelConfig != nil {
				modelName = r.modelConfig.Client.Model
			}
			skillExec := &clitools.SkillCommandExecutor{
				Timeout:  timeout,
				WorkDir:  r.cfg.WorkDir,
				EnvVars:  envVars,
				ProxyURL: proxyURL,
				Model:    modelName,
			}

			st := tools.NewSkillTool(entry.Name, entry.Description, entry.InputSpec, scriptPath, skillExec)
			if err := reg.Register(st); err != nil {
				r.logger.Warn("failed to register skill tool", map[string]any{
					"skill": entry.Name, "error": err.Error(),
				})
			} else {
				registered++
			}
		}
	}
	if registered > 0 {
		r.logger.Info("registered skill tools", map[string]any{"count": registered})
	}
}

// buildSystemPrompt constructs the system prompt with an optional skill catalog.
func (r *Runner) buildSystemPrompt() string {
	base := fmt.Sprintf("You are %s, an AI agent.", r.cfg.Config.AgentID)
	catalog := r.buildSkillCatalog()
	if catalog != "" {
		base += "\n\n" + catalog
	}

	// Add scheduler awareness if schedules are configured or tools are available.
	schedSection := r.buildSchedulerPrompt()
	if schedSection != "" {
		base += "\n\n" + schedSection
	}

	return base
}

// buildSkillCatalog generates a lightweight catalog of binary-backed skills
// (those without scripts) for the system prompt. Script-backed skills are
// already registered as first-class tools and don't need catalog entries.
func (r *Runner) buildSkillCatalog() string {
	matches := r.discoverSkillFiles()
	if len(matches) == 0 {
		return ""
	}

	var catalogEntries []string
	for _, match := range matches {
		entries, meta, err := cliskills.ParseFileWithMetadata(match)
		if err != nil {
			continue
		}

		// Derive skill directory name from the SKILL.md path (for subdirectory skills)
		catalogSkillDir := ""
		if strings.HasSuffix(match, "/SKILL.md") {
			catalogSkillDir = filepath.Base(filepath.Dir(match))
		}

		// If no ## Tool: entries were parsed but frontmatter has name+description,
		// create a synthetic entry so the skill appears in the catalog summary.
		if len(entries) == 0 && meta != nil && meta.Name != "" && meta.Description != "" {
			entries = []contract.SkillEntry{{
				Name:        meta.Name,
				Description: meta.Description,
				Metadata:    meta,
			}}
		}

		for _, entry := range entries {
			// Skip skills that have scripts (already registered as tools)
			scriptName := strings.ReplaceAll(entry.Name, "_", "-")
			hasScript := false
			// Check subdirectory layout: skills/{dir}/scripts/{name}.sh
			if catalogSkillDir != "" {
				sp := filepath.Join(r.cfg.WorkDir, "skills", catalogSkillDir, "scripts", scriptName+".sh")
				if _, err := os.Stat(sp); err == nil {
					hasScript = true
				}
			}
			// Check legacy flat layout: skills/scripts/{name}.sh
			if !hasScript {
				sp := filepath.Join(r.cfg.WorkDir, "skills", "scripts", scriptName+".sh")
				if _, err := os.Stat(sp); err == nil {
					hasScript = true
				}
			}
			if hasScript {
				continue
			}

			if entry.Name != "" && entry.Description != "" {
				line := fmt.Sprintf("- %s: %s", entry.Name, entry.Description)
				// Note that skill uses cli_execute without listing specific
				// binary names — the LLM already sees the allowed enum in the
				// tool schema, and listing names here leaks internal tooling
				// when users ask "what skills/tools do you have?"
				if entry.ForgeReqs != nil && len(entry.ForgeReqs.Bins) > 0 {
					line += " (uses cli_execute)"
				}
				catalogEntries = append(catalogEntries, line)
			}
		}
	}

	if len(catalogEntries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Available Skills\n\n")
	b.WriteString("Use `read_skill` to load full instructions for a skill before using it.\n\n")
	for _, entry := range catalogEntries {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return b.String()
}

// validateSkillRequirements loads skill requirements and validates them.
// It also auto-derives cli_execute config from skill requirements.
func (r *Runner) validateSkillRequirements(envVars map[string]string) error {
	matches := r.discoverSkillFiles()
	if len(matches) == 0 {
		return nil
	}

	var allEntries []contract.SkillEntry
	for _, match := range matches {
		entries, _, err := cliskills.ParseFileWithMetadata(match)
		if err != nil {
			r.logger.Warn("failed to parse skills with metadata", map[string]any{
				"file": match, "error": err.Error(),
			})
			continue
		}
		allEntries = append(allEntries, entries...)
	}
	if len(allEntries) == 0 {
		return nil
	}

	entries := allEntries

	reqs := requirements.AggregateRequirements(entries)

	// Store runtime-parsed skill guardrails early so they are available at
	// hook registration even when no bins/env requirements exist.
	if reqs.SkillGuardrails != nil {
		r.skillGuardrails = convertSkillGuardrails(reqs.SkillGuardrails)
	}

	if len(reqs.Bins) == 0 && len(reqs.EnvRequired) == 0 && len(reqs.EnvOneOf) == 0 && len(reqs.EnvOptional) == 0 {
		return nil
	}

	// Build env resolver
	osEnv := envFromOS()
	envResolver := resolver.NewEnvResolver(osEnv, envVars, nil)

	// Check binaries
	binDiags := resolver.BinDiagnostics(reqs.Bins)
	for _, d := range binDiags {
		r.logger.Warn(d.Message, nil)
	}

	// Check env vars
	envDiags := envResolver.Resolve(reqs)
	for _, d := range envDiags {
		switch d.Level {
		case "error":
			return fmt.Errorf("skill requirement not met: %s", d.Message)
		case "warning":
			r.logger.Warn(d.Message, nil)
		}
	}

	// Auto-derive cli_execute config from skill requirements
	derived := requirements.DeriveCLIConfig(reqs)
	if derived != nil && len(derived.AllowedBinaries) > 0 {
		// Check if cli_execute is already explicitly configured
		hasExplicit := false
		for _, toolRef := range r.cfg.Config.Tools {
			if toolRef.Name == "cli_execute" {
				hasExplicit = true
				break
			}
		}

		if !hasExplicit {
			fields := map[string]any{
				"binaries": len(derived.AllowedBinaries),
				"env_vars": len(derived.EnvPassthrough),
			}
			if derived.TimeoutHint > 0 {
				fields["timeout_hint"] = derived.TimeoutHint
			}
			r.logger.Info("auto-derived cli_execute from skill requirements", fields)
		}
	}

	// Store the derived config for use during executor setup
	r.derivedCLIConfig = derived

	return nil
}

// convertSkillGuardrails converts skill-contract guardrail config into the
// agentspec representation used by the guardrail engine. This mirrors the
// conversion in build/policy_stage.go for the runtime (no-build) path.
func convertSkillGuardrails(sg *contract.SkillGuardrailConfig) *agentspec.SkillGuardrailRules {
	rules := &agentspec.SkillGuardrailRules{}
	for _, c := range sg.DenyCommands {
		rules.DenyCommands = append(rules.DenyCommands, agentspec.CommandFilter{
			Pattern: c.Pattern,
			Message: c.Message,
		})
	}
	for _, o := range sg.DenyOutput {
		rules.DenyOutput = append(rules.DenyOutput, agentspec.OutputFilter{
			Pattern: o.Pattern,
			Action:  o.Action,
		})
	}
	for _, p := range sg.DenyPrompts {
		rules.DenyPrompts = append(rules.DenyPrompts, agentspec.CommandFilter{
			Pattern: p.Pattern,
			Message: p.Message,
		})
	}
	for _, r := range sg.DenyResponses {
		rules.DenyResponses = append(rules.DenyResponses, agentspec.CommandFilter{
			Pattern: r.Pattern,
			Message: r.Message,
		})
	}
	if len(rules.DenyCommands) == 0 && len(rules.DenyOutput) == 0 && len(rules.DenyPrompts) == 0 && len(rules.DenyResponses) == 0 {
		return nil
	}
	return rules
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

// expandEgressDomains expands $VAR and ${VAR} references in an egress domain
// string using the provided env vars map, falling back to OS environment.
// The expanded result is split on commas so a single env var can provide
// multiple domains (e.g. K8S_API_DOMAIN="a.eks.amazonaws.com,b.azmk8s.io").
// Returns nil if the domain is a pure variable reference that resolves to empty.
func expandEgressDomains(domain string, envVars map[string]string) []string {
	if !strings.Contains(domain, "$") {
		return []string{domain} // no variable reference, return as-is
	}

	result := os.Expand(domain, func(key string) string {
		if v, ok := envVars[key]; ok && v != "" {
			return v
		}
		return os.Getenv(key)
	})

	result = strings.TrimSpace(result)
	if result == "" {
		return nil
	}

	// Split on commas to support multiple domains in a single variable.
	parts := strings.Split(result, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// initLongTermMemory sets up the long-term memory system if enabled.
// It resolves the embedder, creates a memory.Manager, registers memory tools,
// and starts background indexing. Returns the Manager (caller must Close) or nil.
func (r *Runner) initLongTermMemory(ctx context.Context, mc *coreruntime.ModelConfig, reg *tools.Registry, compactor *coreruntime.Compactor) *memory.Manager {
	// Check if long-term memory is enabled.
	enabled := false
	if r.cfg.Config.Memory.LongTerm != nil {
		enabled = *r.cfg.Config.Memory.LongTerm
	}
	if os.Getenv("FORGE_MEMORY_LONG_TERM") == "true" {
		enabled = true
	}
	if !enabled {
		return nil
	}

	memDir := r.cfg.Config.Memory.MemoryDir
	if memDir == "" {
		memDir = filepath.Join(r.cfg.WorkDir, ".forge", "memory")
	}

	// Resolve embedder.
	embedder := r.resolveEmbedder(mc)

	// Build search config from forge.yaml.
	searchCfg := memory.DefaultSearchConfig()
	if r.cfg.Config.Memory.VectorWeight > 0 {
		searchCfg.VectorWeight = r.cfg.Config.Memory.VectorWeight
	}
	if r.cfg.Config.Memory.KeywordWeight > 0 {
		searchCfg.KeywordWeight = r.cfg.Config.Memory.KeywordWeight
	}
	if r.cfg.Config.Memory.DecayHalfLifeDays > 0 {
		searchCfg.DecayHalfLife = time.Duration(r.cfg.Config.Memory.DecayHalfLifeDays) * 24 * time.Hour
	}

	mgr, err := memory.NewManager(memory.ManagerConfig{
		MemoryDir:    memDir,
		Embedder:     embedder,
		Logger:       r.logger,
		SearchConfig: searchCfg,
	})
	if err != nil {
		r.logger.Warn("failed to create memory manager, long-term memory disabled", map[string]any{
			"error": err.Error(),
		})
		return nil
	}

	// Register memory tools.
	if regErr := reg.Register(builtins.NewMemorySearchTool(mgr)); regErr != nil {
		r.logger.Warn("failed to register memory_search tool", map[string]any{"error": regErr.Error()})
	}
	if regErr := reg.Register(builtins.NewMemoryGetTool(mgr)); regErr != nil {
		r.logger.Warn("failed to register memory_get tool", map[string]any{"error": regErr.Error()})
	}

	// Wire memory flusher into compactor (if compactor exists).
	if compactor != nil {
		compactor.SetMemoryFlusher(mgr)
	}

	// Index memory files at startup in background.
	go func() {
		if idxErr := mgr.IndexAll(ctx); idxErr != nil {
			r.logger.Warn("background memory indexing failed", map[string]any{"error": idxErr.Error()})
		}
	}()

	mode := "keyword-only"
	if embedder != nil {
		mode = "vector+keyword"
	}
	r.logger.Info("long-term memory enabled", map[string]any{
		"memory_dir": memDir,
		"mode":       mode,
	})

	return mgr
}

// resolveEmbedder creates an embedder from config or auto-detection.
// Returns nil if no embedder can be created (keyword-only mode).
func (r *Runner) resolveEmbedder(mc *coreruntime.ModelConfig) llm.Embedder {
	// Resolution order: config override → env → primary LLM provider.
	embProvider := r.cfg.Config.Memory.EmbeddingProvider
	if embProvider == "" {
		embProvider = os.Getenv("FORGE_EMBEDDING_PROVIDER")
	}
	if embProvider == "" {
		embProvider = mc.Provider
	}

	// Anthropic has no embedding API — skip.
	if embProvider == "anthropic" {
		r.logger.Info("primary provider is anthropic (no embedding API), trying fallbacks for embeddings", nil)
		// Try fallback providers.
		for _, fb := range mc.Fallbacks {
			if fb.Provider != "anthropic" {
				embProvider = fb.Provider
				break
			}
		}
		if embProvider == "anthropic" {
			r.logger.Info("no embedding-capable provider found, using keyword-only search", nil)
			return nil
		}
	}

	cfg := providers.OpenAIEmbedderConfig{
		APIKey: mc.Client.APIKey,
		OrgID:  mc.Client.OrgID,
		Model:  r.cfg.Config.Memory.EmbeddingModel,
	}

	// Use the correct API key for the embedding provider if it differs from primary.
	if embProvider != mc.Provider {
		for _, fb := range mc.Fallbacks {
			if fb.Provider == embProvider {
				cfg.APIKey = fb.Client.APIKey
				cfg.BaseURL = fb.Client.BaseURL
				cfg.OrgID = fb.Client.OrgID
				break
			}
		}
	}

	embedder, err := providers.NewEmbedder(embProvider, cfg)
	if err != nil {
		r.logger.Warn("failed to create embedder, using keyword-only search", map[string]any{
			"provider": embProvider,
			"error":    err.Error(),
		})
		return nil
	}

	return embedder
}

// builtinSecretKeys is the set of forge-internal secret keys whose purpose
// (LLM / search / channel) is recognized by secretCategory and that are always
// attempted via provider.Get, even when the provider cannot enumerate keys
// (e.g. the env provider). Custom skill-declared keys do not need to appear
// here — they are discovered dynamically via provider.List in secretOverlayKeys.
var builtinSecretKeys = []string{
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"LLM_API_KEY",
	"MODEL_API_KEY",
	"TAVILY_API_KEY",
	"PERPLEXITY_API_KEY",
	"TELEGRAM_BOT_TOKEN",
	"SLACK_APP_TOKEN",
	"SLACK_BOT_TOKEN",
}

// secretOverlayKeys returns the set of secret keys to overlay into the env:
// the builtin keys unioned with whatever the provider exposes via List().
// Providers that cannot enumerate (e.g. EnvProvider) return nil from List, in
// which case only the builtins are returned. List errors are non-fatal — the
// builtin keys are still tried via Get downstream.
func secretOverlayKeys(provider secrets.Provider) ([]string, error) {
	seen := make(map[string]bool, len(builtinSecretKeys))
	keys := make([]string, 0, len(builtinSecretKeys))
	for _, k := range builtinSecretKeys {
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}

	listed, err := provider.List()
	for _, k := range listed {
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys, err
}

// overlaySecrets reads secrets from the configured provider chain and overlays
// them into envVars. The key set is the builtin LLM/channel keys plus any
// custom keys the provider enumerates via List() — so skill-declared env vars
// stored as encrypted secrets are loaded without needing a code change here.
// Existing values are not overwritten. Returns an error if the same secret
// value is reused across different purpose categories among the builtin keys.
func (r *Runner) overlaySecrets(envVars map[string]string) error {
	provider := r.buildSecretProvider()
	if provider == nil {
		return nil
	}

	keys, listErr := secretOverlayKeys(provider)
	if listErr != nil {
		r.logger.Warn("provider list failed; overlaying builtin keys only", map[string]any{
			"provider": provider.Name(), "error": listErr.Error(),
		})
	}

	for _, key := range keys {
		if envVars[key] != "" {
			continue // don't overwrite existing values
		}
		val, err := provider.Get(key)
		if err == nil {
			envVars[key] = val
			r.logger.Info("secret loaded", map[string]any{"key": key, "provider": provider.Name()})
		}
	}

	// Cross-category secret reuse is only meaningful for keys whose category
	// is known — i.e. the builtin set. Custom keys have no defined category.
	valueToKeys := make(map[string][]string)
	for _, key := range builtinSecretKeys {
		val := envVars[key]
		if val == "" {
			continue
		}
		valueToKeys[val] = append(valueToKeys[val], key)
	}

	for val, keys := range valueToKeys {
		if len(keys) < 2 {
			continue
		}
		categories := make(map[string]bool)
		for _, k := range keys {
			cat := secretCategory(k)
			if cat != "" {
				categories[cat] = true
			}
		}
		if len(categories) > 1 {
			_ = val // avoid logging the actual secret value
			r.logger.Warn("cross-category secret reuse detected", map[string]any{"keys": keys})
			return fmt.Errorf("secret reuse: keys %v share the same value across different categories", keys)
		}
	}

	return nil
}

// secretCategory returns the purpose category for a known secret key.
func secretCategory(key string) string {
	switch key {
	case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "LLM_API_KEY", "MODEL_API_KEY":
		return "llm"
	case "TAVILY_API_KEY", "PERPLEXITY_API_KEY":
		return "search"
	case "TELEGRAM_BOT_TOKEN":
		return "telegram"
	case "SLACK_APP_TOKEN", "SLACK_BOT_TOKEN":
		return "slack"
	default:
		return ""
	}
}

// passphraseFromEnv returns a callback that reads the passphrase from FORGE_PASSPHRASE.
// Since run.go prompts interactively and sets the env var before calling into the
// runner, this callback will find the passphrase when a TTY is available.
func passphraseFromEnv() func() (string, error) {
	return func() (string, error) {
		if p := os.Getenv("FORGE_PASSPHRASE"); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("FORGE_PASSPHRASE not set")
	}
}

// buildSecretProvider creates a Provider from the config's secrets.providers list.
// Returns nil if no providers are configured (backward compat: default is env only,
// which is already handled by the env file loading).
func (r *Runner) buildSecretProvider() secrets.Provider {
	providerNames := r.cfg.Config.Secrets.Providers
	if len(providerNames) == 0 {
		return nil // no explicit secret providers configured
	}

	passCb := passphraseFromEnv()

	var providers []secrets.Provider
	for _, name := range providerNames {
		switch name {
		case "env":
			providers = append(providers, secrets.NewEnvProvider(""))
		case "encrypted-file":
			providers = append(providers, viableEncryptedFileProviders(r.cfg.WorkDir, passCb, r.logger.Warn)...)
		default:
			r.logger.Warn("unknown secret provider, skipping", map[string]any{"provider": name})
		}
	}

	if len(providers) == 0 {
		return nil
	}
	if len(providers) == 1 {
		return providers[0]
	}
	return secrets.NewChainProvider(providers...)
}

// viableEncryptedFileProviders returns the agent-local and global
// encrypted-file providers that pass an eager-load check. Files that don't
// exist are silently skipped (the common case: the operator never ran
// `forge secret set --global`). Files that fail to decrypt (wrong passphrase,
// corruption) emit a warning via warnFn and are dropped from the chain — so a
// stale global file with a different passphrase cannot poison subsequent
// ChainProvider.Get/List calls once admitted to the chain.
//
// The returned providers retain their decrypted cache (EncryptedFileProvider
// flags `loaded = true` after a successful List()), so subsequent reads — by
// secretOverlayKeys, by Get on individual keys — reuse the work and don't
// trigger another Argon2id derivation.
//
// warnFn may be nil; in that case decryption failures are silently skipped.
func viableEncryptedFileProviders(workDir string, passCb func() (string, error), warnFn func(msg string, fields map[string]any)) []secrets.Provider {
	candidates := []struct{ path, label string }{
		{filepath.Join(workDir, ".forge", "secrets.enc"), "agent-local"},
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, struct{ path, label string }{filepath.Join(home, ".forge", "secrets.enc"), "global"})
	}

	var viable []secrets.Provider
	for _, c := range candidates {
		if _, err := os.Stat(c.path); os.IsNotExist(err) {
			continue
		}
		provider := secrets.NewEncryptedFileProvider(c.path, passCb)
		// Eagerly validate the file can be decrypted. List() runs
		// ensureLoaded which performs the decrypt and caches the cleartext
		// for later calls.
		if _, err := provider.List(); err != nil {
			if warnFn != nil {
				warnFn("skipping secrets provider that failed to load", map[string]any{
					"path":  c.path,
					"label": c.label,
					"error": err.Error(),
				})
			}
			continue
		}
		viable = append(viable, provider)
	}
	return viable
}

// OverlaySecretsToEnv loads secrets from the config's provider chain and sets
// them in the OS environment so that channel adapters (which use os.Getenv) can
// access encrypted secrets. Only keys not already set in the env are written.
// workDir is the agent directory used to locate agent-local secrets.
//
// Runs before the Runner exists (called from cmd/common.go), so it doesn't
// have access to the structured logger — warnings about unloadable secret
// files go to stderr in the same style as other early-startup messages.
func OverlaySecretsToEnv(cfg *types.ForgeConfig, workDir string) {
	providerNames := cfg.Secrets.Providers
	if len(providerNames) == 0 {
		return
	}

	passCb := passphraseFromEnv()

	var chain []secrets.Provider
	for _, name := range providerNames {
		switch name {
		case "encrypted-file":
			chain = append(chain, viableEncryptedFileProviders(workDir, passCb, stderrWarn)...)
		case "env":
			// env provider uses os.Getenv — already available, skip
		}
	}

	if len(chain) == 0 {
		return
	}

	var provider secrets.Provider
	if len(chain) == 1 {
		provider = chain[0]
	} else {
		provider = secrets.NewChainProvider(chain...)
	}

	keys, _ := secretOverlayKeys(provider)
	for _, key := range keys {
		if os.Getenv(key) != "" {
			continue
		}
		val, err := provider.Get(key)
		if err == nil && val != "" {
			_ = os.Setenv(key, val)
		}
	}
}

// stderrWarn is a warning sink for code paths that run before the structured
// logger is available (e.g. OverlaySecretsToEnv).
func stderrWarn(msg string, fields map[string]any) {
	var parts []string
	for k, v := range fields {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	fmt.Fprintf(os.Stderr, "  forge: %s", msg)
	if len(parts) > 0 {
		fmt.Fprintf(os.Stderr, " (%s)", strings.Join(parts, ", "))
	}
	fmt.Fprintln(os.Stderr)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func defaultStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// isLocalhost returns true if the host string refers to a localhost address.
func isLocalhost(host string) bool {
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// materializeKubeconfig checks whether the KUBECONFIG env var contains inline
// YAML content (rather than a file path). If so, it writes the content to a
// file and updates KUBECONFIG to point to that file. This allows users to pass
// kubeconfig content directly via `-e KUBECONFIG="<yaml>"`.
// materializeKubeconfig checks whether the KUBECONFIG env var contains inline
// YAML content (rather than a file path). If so, it writes the content to a
// file and updates KUBECONFIG to point to that file. Returns true if content
// was materialized.
func materializeKubeconfig(workDir string) (bool, error) {
	val := os.Getenv("KUBECONFIG")
	if val == "" {
		return false, nil
	}
	// Heuristic: if the value contains a newline or starts with typical
	// kubeconfig YAML markers, treat it as inline content rather than a path.
	isInline := strings.Contains(val, "\n") ||
		strings.HasPrefix(strings.TrimSpace(val), "apiVersion:") ||
		strings.Contains(val, "certificate-authority-data:") ||
		strings.Contains(val, "clusters:")
	if !isInline {
		return false, nil // looks like a file path
	}

	kubeDir := filepath.Join(workDir, ".kube")
	if err := os.MkdirAll(kubeDir, 0700); err != nil {
		return false, fmt.Errorf("creating .kube directory: %w", err)
	}
	kubePath := filepath.Join(kubeDir, "config")
	if err := os.WriteFile(kubePath, []byte(val), 0600); err != nil {
		return false, fmt.Errorf("writing kubeconfig file: %w", err)
	}
	if err := os.Setenv("KUBECONFIG", kubePath); err != nil {
		return false, fmt.Errorf("updating KUBECONFIG env: %w", err)
	}
	return true, nil
}

// initScheduler creates the schedule store and registers schedule tools.
func (r *Runner) initScheduler(reg *tools.Registry) scheduler.ScheduleStore {
	schedPath := filepath.Join(r.cfg.WorkDir, ".forge", "memory", "SCHEDULES.md")
	store := NewMemoryScheduleStore(schedPath)

	// We can't pass the scheduler itself yet (it's created after), so we use
	// a lazy reloader that will be set once the scheduler is created.
	reloader := &lazyScheduleReloader{runner: r}

	if regErr := reg.Register(builtins.NewScheduleSetTool(store, reloader)); regErr != nil {
		r.logger.Warn("failed to register schedule_set tool", map[string]any{"error": regErr.Error()})
	}
	if regErr := reg.Register(builtins.NewScheduleListTool(store)); regErr != nil {
		r.logger.Warn("failed to register schedule_list tool", map[string]any{"error": regErr.Error()})
	}
	if regErr := reg.Register(builtins.NewScheduleDeleteTool(store, reloader)); regErr != nil {
		r.logger.Warn("failed to register schedule_delete tool", map[string]any{"error": regErr.Error()})
	}
	if regErr := reg.Register(builtins.NewScheduleHistoryTool(store)); regErr != nil {
		r.logger.Warn("failed to register schedule_history tool", map[string]any{"error": regErr.Error()})
	}

	r.logger.Info("schedule tools registered", nil)
	return store
}

// lazyScheduleReloader implements builtins.ScheduleReloader by delegating to the
// runner's scheduler, which may not exist yet at tool registration time.
type lazyScheduleReloader struct {
	runner *Runner
}

func (l *lazyScheduleReloader) Reload(ctx context.Context) {
	if l.runner.schedBackend != nil {
		l.runner.schedBackend.Reload(ctx)
	}
}

// makeScheduleDispatcher creates a TaskDispatcher that executes scheduled tasks
// via the LLM executor.
func (r *Runner) makeScheduleDispatcher(executor coreruntime.AgentExecutor, egressClient *http.Client, auditLogger *coreruntime.AuditLogger) scheduler.TaskDispatcher {
	return func(ctx context.Context, sched scheduler.Schedule) error {
		taskID := fmt.Sprintf("sched-%s-%d", sched.ID, time.Now().Unix())
		correlationID := coreruntime.GenerateID()

		// Set up context with security and tracing.
		ctx = security.WithEgressClient(ctx, egressClient)
		ctx = coreruntime.WithCorrelationID(ctx, correlationID)
		ctx = coreruntime.WithTaskID(ctx, taskID)
		// FWS-8: scheduled invocations also need a per-invocation
		// sequence counter so their audit stream is gap-detectable.
		ctx = coreruntime.WithSequenceCounter(ctx, new(coreruntime.SequenceCounter))

		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditScheduleFire,
			CorrelationID: correlationID,
			TaskID:        taskID,
			Fields:        map[string]any{"schedule_id": sched.ID},
		})

		// Build the task message.
		msgText := fmt.Sprintf("[Scheduled Task: %s]\n\n%s", sched.ID, sched.Task)
		if sched.Skill != "" {
			msgText = fmt.Sprintf("[Scheduled Task: %s] [Skill: %s]\n\n%s", sched.ID, sched.Skill, sched.Task)
		}

		task := &a2a.Task{
			ID:     taskID,
			Status: a2a.TaskStatus{State: a2a.TaskStateWorking},
		}

		msg := &a2a.Message{
			Role:  a2a.MessageRoleUser,
			Parts: []a2a.Part{a2a.NewTextPart(msgText)},
		}

		respMsg, err := executor.Execute(ctx, task, msg)

		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditScheduleComplete,
			CorrelationID: correlationID,
			TaskID:        taskID,
			Fields: map[string]any{
				"schedule_id": sched.ID,
				"success":     err == nil,
			},
		})

		// Deliver result to channel if configured.
		if err == nil && respMsg != nil && sched.Channel != "" && sched.ChannelTarget != "" {
			if r.scheduleNotifier != nil {
				if notifyErr := r.scheduleNotifier(ctx, sched.Channel, sched.ChannelTarget, respMsg); notifyErr != nil {
					r.logger.Warn("failed to notify channel for scheduled task", map[string]any{
						"schedule_id": sched.ID,
						"channel":     sched.Channel,
						"error":       notifyErr.Error(),
					})
				}
			} else {
				r.logger.Warn("schedule has channel configured but no channel adapters are active; use --with flag", map[string]any{
					"schedule_id": sched.ID,
					"channel":     sched.Channel,
				})
			}
		}

		return err
	}
}

// selectScheduleBackend picks the scheduler.Backend implementation
// based on forge.yaml's `scheduler.backend` field. Resolution:
//
//   - "kubernetes" → always KubernetesBackend; returns a hard error
//     when not in-cluster and FORGE_IN_CLUSTER is not set true.
//   - "file"       → always FileBackend.
//   - "auto" / ""  → KubernetesBackend when in-cluster, otherwise
//     FileBackend.
//
// The FileBackend is constructed with the supplied store/dispatch/audit
// from the existing wiring. The KubernetesBackend ignores those (the
// cluster handles timing and audit linkage lands via the
// X-Forge-Schedule-Id header at the A2A boundary in part 3).
func (r *Runner) selectScheduleBackend(
	store scheduler.ScheduleStore,
	dispatch scheduler.TaskDispatcher,
	auditFn scheduler.AuditFunc,
) (scheduler.Backend, error) {
	mode := r.cfg.Config.Scheduler.Backend
	useK8s := false
	switch mode {
	case "", "auto":
		useK8s = scheduler.InCluster()
	case "kubernetes":
		useK8s = true
		if !scheduler.InCluster() {
			return nil, fmt.Errorf("scheduler.backend=kubernetes requires running in a Kubernetes pod (set FORGE_IN_CLUSTER=true to override for tests)")
		}
	case "file":
		useK8s = false
	default:
		return nil, fmt.Errorf("scheduler.backend = %q: must be one of auto / file / kubernetes", mode)
	}
	if !useK8s {
		sched := scheduler.New(store, dispatch, r.logger, auditFn)
		return scheduler.NewFileBackend(store, sched), nil
	}
	k8sCfg := r.cfg.Config.Scheduler.Kubernetes
	backend, err := NewKubernetesBackend(
		r.cfg.Config.AgentID,
		k8sCfg.Namespace,
		K8sBackendConfig{
			ServiceURL:     k8sCfg.ServiceURL,
			Port:           r.cfg.Port,
			AuthSecretName: k8sCfg.AuthSecretName,
			TriggerImage:   k8sCfg.TriggerImage,
			AllowDynamic:   k8sCfg.AllowDynamic,
		},
		r.logger,
	)
	if err != nil {
		return nil, err
	}
	r.logger.Info("scheduler: using kubernetes backend", map[string]any{
		"namespace":     k8sCfg.Namespace,
		"service_url":   k8sCfg.ServiceURL,
		"allow_dynamic": k8sCfg.AllowDynamic,
	})
	return backend, nil
}

// declaredSchedules translates the forge.yaml schedules[] block into
// the scheduler.Schedule shape Backend.Sync consumes. Marks each as
// SourceYAML so the backend's reconciliation distinguishes them from
// LLM-set entries.
func (r *Runner) declaredSchedules() []scheduler.Schedule {
	out := make([]scheduler.Schedule, 0, len(r.cfg.Config.Schedules))
	now := time.Now().UTC()
	for _, sc := range r.cfg.Config.Schedules {
		out = append(out, scheduler.Schedule{
			ID:            sc.ID,
			Cron:          sc.Cron,
			Task:          sc.Task,
			Skill:         sc.Skill,
			Channel:       sc.Channel,
			ChannelTarget: sc.ChannelTarget,
			Source:        scheduler.SourceYAML,
			Enabled:       true,
			Created:       now,
		})
	}
	return out
}

// buildSchedulerPrompt generates the scheduler awareness section for the system prompt.
func (r *Runner) buildSchedulerPrompt() string {
	return `## Scheduler

You have access to a built-in cron scheduler for recurring tasks. Use these tools to manage schedules:

- **schedule_set**: Create or update a recurring schedule (cron expression + task description)
- **schedule_list**: List all active and inactive schedules
- **schedule_delete**: Remove a schedule (LLM-created only; yaml-defined cannot be deleted)
- **schedule_history**: View execution history for scheduled tasks

Cron expressions support: standard 5-field (min hour dom mon dow), aliases (@hourly, @daily, @weekly, @monthly), and intervals (@every 5m, @every 1h).

### Channel delivery
Messages from channels include a context line: ` + "`" + `[channel:<name> channel_target:<id>]` + "`" + `
When creating a schedule from a channel conversation, **always** extract these values and pass them to schedule_set:
- **channel**: the adapter name from the context line (e.g. "slack", "telegram")
- **channel_target**: the destination ID from the context line (Slack channel ID, Telegram chat ID)
Without these, scheduled task results will execute but not be sent to any channel.`
}
