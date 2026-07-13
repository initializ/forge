// Package types holds configuration types for forge.yaml.
package types

import (
	"fmt"
	"time"

	"github.com/initializ/forge/forge-core/credentials"
	"gopkg.in/yaml.v3"
)

// ForgeConfig represents the top-level forge.yaml configuration.
type ForgeConfig struct {
	AgentID        string              `yaml:"agent_id"`
	Version        string              `yaml:"version"`
	Framework      string              `yaml:"framework"`
	Entrypoint     string              `yaml:"entrypoint"`
	Model          ModelRef            `yaml:"model,omitempty"`
	Tools          []ToolRef           `yaml:"tools,omitempty"`
	BuiltinTools   []string            `yaml:"builtin_tools,omitempty"`
	Channels       []string            `yaml:"channels,omitempty"`
	Registry       string              `yaml:"registry,omitempty"`
	Egress         EgressRef           `yaml:"egress,omitempty"`
	Skills         SkillsRef           `yaml:"skills,omitempty"`
	Memory         MemoryConfig        `yaml:"memory,omitempty"`
	Compression    CompressionConfig   `yaml:"compression,omitempty"`
	Secrets        SecretsConfig       `yaml:"secrets,omitempty"`
	Auth           AuthConfig          `yaml:"auth,omitempty"`
	MCP            MCPConfig           `yaml:"mcp,omitempty"`
	Schedules      []ScheduleConfig    `yaml:"schedules,omitempty"`
	Scheduler      SchedulerConfig     `yaml:"scheduler,omitempty"`
	CORSOrigins    []string            `yaml:"cors_origins,omitempty"`
	Package        PackageConfig       `yaml:"package,omitempty"`
	GuardrailsPath string              `yaml:"guardrails_path,omitempty"` // path to guardrails.json (default: "guardrails.json")
	Server         ServerConfig        `yaml:"server,omitempty"`
	Observability  ObservabilityConfig `yaml:"observability,omitempty"`
	Security       SecurityConfig      `yaml:"security,omitempty"`
	Audit          AuditConfig         `yaml:"audit,omitempty"`
	// WorkflowPropagation gates which downstream hosts auto-receive
	// the X-Workflow-* / X-Invocation-Caller headers when this agent
	// calls them as a tool. See issue #186 / FORGE-1 — auto-propagation
	// is off by default to stop workflow identity from leaking to
	// third-party APIs.
	WorkflowPropagation WorkflowPropagationConfig `yaml:"workflow_propagation,omitempty"`

	// Credentials declares per-tool JIT credential specs (governance R9).
	// Each entry names a provider (registered in credentials.DefaultRegistry
	// via one of the credentials/* subpackages) plus provider-specific
	// spec bytes. The runner materializes fresh credentials on every
	// tool call whose Tool + Binary matches the spec; the resulting env
	// is merged into the tool's subprocess environment. Empty → no JIT
	// injection (pre-R9 behavior).
	//
	// Coupling note (per @initializ-mk's #236 review): `types` intentionally
	// imports `credentials` here so operators get a single self-describing
	// yaml schema type. The invariant that keeps this safe: the base
	// `credentials` package MUST stay stdlib-only. Provider implementations
	// with external deps (AWS SDK, Vault client, HSM libs) belong in
	// `credentials/<provider>` subpackages — which the runner imports for
	// its init()-time registration but which the config type never sees.
	// Do NOT add non-stdlib imports to `forge-core/credentials/*.go`
	// (root files) — a lint / CI check should be added if this becomes a
	// recurring temptation.
	//
	// Example:
	//
	//   credentials:
	//     - tool: cli_execute
	//       binary: aws
	//       provider: sts_assume_role
	//       spec:
	//         role_arn: arn:aws:iam::123456789012:role/skill-read
	//         duration: 15m
	//
	// See docs/security/least-privilege-credentials.md.
	Credentials []credentials.CredentialSpec `yaml:"credentials,omitempty"`
}

// WorkflowPropagationConfig opts-in specific downstream hosts to
// receive workflow correlation headers automatically when this agent
// invokes them from any built-in HTTP tool. Without an entry on the
// allow-list, the headers stay opt-in and tools must call
// `WorkflowContextFromContext(ctx).ApplyToHTTPHeaders(req.Header)`
// explicitly to propagate them (the pre-#186 behavior).
//
// Allowed entries follow the same exact + wildcard shape as the
// egress allow-list (`security.DomainMatcher`):
//
//   - Exact: "orchestrator.svc"
//   - Wildcard suffix: "*.agents.internal"
//
// The matcher strips ports before comparing, so an entry like
// "peer.local" also covers "peer.local:8443".
//
// The auto-apply hook lives in forge-core/runtime; the runner installs
// a transport wrapper around the egress client's transport that
// consults this matcher per outbound request. See issue #186.
type WorkflowPropagationConfig struct {
	// AllowedHosts is the list of exact + wildcard hostname patterns
	// that should auto-receive the X-Workflow-* / X-Invocation-Caller
	// headers when the current request carries a non-zero
	// WorkflowContext. Empty list = opt-in only (default).
	AllowedHosts []string `yaml:"allowed_hosts,omitempty"`
}

// AuditConfig groups audit-pipeline knobs that are operator-visible in
// forge.yaml. Today it carries the FWS-8 payload-capture block (issue
// #163); the export-sink knobs (#95 / FWS-7) are CLI flags and env
// vars only because they correspond to deployment-platform choices,
// not per-agent configuration.
type AuditConfig struct {
	Capture AuditCaptureConfig `yaml:"capture,omitempty"`
}

// AuditCaptureConfig is the forge.yaml-facing payload-capture
// configuration. Each capture flag is a `*bool` so an operator can
// distinguish "unset, fall through to env" from "explicitly false";
// the env layer is the next layer below, and the `FORGE_AUDIT_CAPTURE_*`
// defaults sit at the bottom.
//
// Precedence (high → low): forge.yaml `audit.capture` > env var > default.
// Same pattern the export config and guardrail audit config use.
//
// Off by default per the FWS-8 commitment: an absent `audit.capture`
// block leaves every flag false and emits metadata-only events.
// Operators turn capture on for one of:
//   - debugging a misbehaving tool (set `tool_args + tool_result` for
//     the session, then turn off);
//   - compliance evidence (typically just `tool_args` — the inputs
//     the agent produced);
//   - supervised-learning corpora collection (probably `llm_messages`
//     and `tool_result` together).
//
// See docs/security/audit-logging.md `Raw payload capture` section
// for the verbosity table and operator guidance.
type AuditCaptureConfig struct {
	// ToolArgs captures the raw input on `tool_exec phase=start`.
	ToolArgs *bool `yaml:"tool_args,omitempty"`
	// ToolResult captures the raw output on `tool_exec phase=end`.
	ToolResult *bool `yaml:"tool_result,omitempty"`
	// LLMMessages captures the chat-messages array on `llm_call`.
	LLMMessages *bool `yaml:"llm_messages,omitempty"`
	// LLMResponse captures the model completion text on `llm_call`.
	LLMResponse *bool `yaml:"llm_response,omitempty"`
	// Redact runs the vendor-secret regex scrub on captured fields
	// before truncation. ON by default — only flip OFF if a
	// downstream sink scrubs.
	Redact *bool `yaml:"redact,omitempty"`
	// MaxBytes is the single-knob per-field cap (16 KiB default).
	// Zero leaves whichever value the env layer or per-field default
	// resolved to.
	MaxBytes int `yaml:"max_bytes,omitempty"`
}

// SecurityConfig groups build-time security knobs. Today it carries
// only the security-policy override; future build-time security
// concerns (e.g. signing requirements, allowlisted base images) belong
// here too so operators have a single security stanza in forge.yaml.
//
// The `forge skills audit --policy` flag and the `forge build`
// security-analysis stage both consume `analyzer.SecurityPolicy` files
// — populating `policy_path` here lets a committed forge.yaml gate
// builds on the same custom policy without re-typing the flag at
// every invocation. See issue #145.
type SecurityConfig struct {
	// PolicyPath points at a YAML SecurityPolicy file
	// (analyzer.SecurityPolicy schema). When set, the build's
	// security-analysis stage loads it instead of using the
	// builtin defaults. Resolved relative to the forge.yaml's
	// directory when not absolute. The `forge build --policy`
	// flag overrides this field.
	PolicyPath string `yaml:"policy_path,omitempty"`

	// IntentAlignment configures the R3 intent-alignment check
	// (governance #208): every tool call is scored against the
	// stated agent intent captured on tasks/send entry. Opt-in;
	// see docs/security/intent-alignment.md.
	IntentAlignment IntentAlignmentConfig `yaml:"intent_alignment,omitempty"`

	// IntentDrift configures the R7 rolling-window drift detector
	// (governance #214). Where R3 (IntentAlignment) is per-action
	// policy, R7 is longitudinal telemetry — it watches the trend
	// of alignment scores over the last N tool calls and emits an
	// `intent_drift` audit event when the mean drops below a
	// threshold or the sequence trends monotonically downward.
	// Opt-in; requires IntentAlignment.Enabled (shares the same
	// embedding + cosine machinery). See
	// docs/security/intent-alignment.md.
	IntentDrift IntentDriftConfig `yaml:"intent_drift,omitempty"`

	// StepUp configures the R4b (#210) step-up authorization policy.
	// Names tools that require a higher auth-context class before
	// the runtime executes them; on a mismatch, Forge returns an
	// RFC 9470 challenge and the caller re-authenticates.
	// Opt-in — no default enforcement.
	StepUp StepUpConfig `yaml:"step_up,omitempty"`

	// Defer configures the R4c (#211) DEFER authorization decision.
	// Names tools that pause execution mid-run and hand off to an
	// external approver (typically a human via a channel adapter).
	// Opt-in; requires a decision to arrive at
	// `POST /tasks/{id}/decisions` before the tool call proceeds.
	Defer DeferConfig `yaml:"defer,omitempty"`
}

// IntentDriftConfig is the forge.yaml-facing block for R7 drift
// tracking.
//
// The drift signal is audit-only by default (no tool call is
// denied). To combine drift with hard-deny semantics, tune
// IntentAlignment.HardThreshold instead — drift is telemetry, not
// a policy gate.
type IntentDriftConfig struct {
	// Enabled turns the drift analyzer on. Requires IntentAlignment
	// to also be enabled (drift derives from alignment scores).
	// Default false → no drift analysis, no audit events.
	Enabled bool `yaml:"enabled,omitempty"`

	// Window is the number of recent alignment scores considered
	// for the rolling-mean test. Sensible default 5. Must be ≥ 2 —
	// a window of 1 can't distinguish "trending down" from "just
	// low."
	Window int `yaml:"window,omitempty"`

	// DriftThreshold is the mean-score floor. When the rolling
	// window mean drops strictly below this value, an intent_drift
	// event fires. Sensible default 0.35 (chosen slightly above
	// the R3 hard_threshold default 0.3 so drift is a leading
	// indicator, not a same-event trailing one). Pointer so an
	// explicit 0 (a meaningful "only flag when the mean goes
	// negative" floor on cosine's [-1,1] range) survives the
	// runner's zero-value defaulting.
	DriftThreshold *float64 `yaml:"drift_threshold,omitempty"`

	// MonotoneN, when non-zero, additionally emits intent_drift on
	// N-consecutive strictly-decreasing scores even if the mean is
	// still above DriftThreshold. Catches the "boiling frog"
	// pattern where each step is small but the cumulative drift is
	// large. Zero disables the monotone check. Sensible value: 3.
	MonotoneN int `yaml:"monotone_n,omitempty"`
}

// IntentAlignmentConfig is the forge.yaml-facing block for the R3
// intent-alignment check.
//
// Defaults ship as "off" — operators explicitly turn it on after
// picking an embedder provider. When on, an unavailable embedder
// causes tool calls to fail closed (deny) rather than silently
// bypass the check.
//
// Recommended rollout: start warn-only for a sprint (leave
// `hard_threshold` at the default 0.3 or lower, tune `threshold`)
// to observe the score distribution before enabling denies.
type IntentAlignmentConfig struct {
	// Enabled turns the check on. Default false → no embedder calls,
	// no hooks registered, wire shape unchanged.
	Enabled bool `yaml:"enabled,omitempty"`

	// Provider is the embedder provider name — one of "openai",
	// "gemini", "ollama". Must produce an embedder via
	// forge-core/llm/providers.NewEmbedder.
	Provider string `yaml:"provider,omitempty"`

	// Model is the embedder-model name (e.g. "text-embedding-3-small",
	// "nomic-embed-text"). Passed through to the provider.
	Model string `yaml:"model,omitempty"`

	// BaseURL overrides the provider's default endpoint — for
	// self-hosted OpenAI-compatible gateways or Ollama on a
	// non-localhost host.
	BaseURL string `yaml:"base_url,omitempty"`

	// APIKeyEnv names the env var to source the API key from.
	// Defaults follow the provider: OPENAI_API_KEY / GEMINI_API_KEY.
	// Empty for Ollama (unauthenticated).
	APIKeyEnv string `yaml:"api_key_env,omitempty"`

	// Threshold is the soft floor. Scores strictly below produce
	// WARN. Sensible default 0.5. Pointer so the operator can
	// distinguish "unset, apply default" from "explicit 0" — 0 is
	// a meaningful value on the cosine range [-1,1] and used to
	// silently collide with the zero-value default.
	Threshold *float64 `yaml:"threshold,omitempty"`

	// HardThreshold is the hard floor. Scores strictly below DENY
	// the tool call. Sensible default 0.3. Set equal to Threshold
	// to disable the WARN tier; set to a negative value (e.g. -1)
	// to run warn-only during the initial rollout — see the
	// "Recommended rollout" section in docs/security/intent-
	// alignment.md. Pointer so an explicit 0 is preserved (see
	// Threshold's comment).
	HardThreshold *float64 `yaml:"hard_threshold,omitempty"`

	// CacheSize is the max entries in the action-side embedding LRU.
	// 0 disables the LRU (still caches per-task intent). Default
	// 1024 when enabled and unspecified.
	CacheSize int `yaml:"cache_size,omitempty"`
}

// StepUpConfig is the forge.yaml-facing block for R4b step-up
// authorization.
//
// Tools listed in Tools require the caller's authenticated identity
// to carry an `acr` claim matching (or exceeding, if AcrHierarchy is
// declared) the required value. On a mismatch, the runtime aborts
// the tool call and returns HTTP 401 with a
// `WWW-Authenticate: Bearer error="step_up_required",
// acr_values="<value>"` header per RFC 9470. The caller's client is
// expected to trigger a higher-assurance authentication and retry.
type StepUpConfig struct {
	// Enabled turns step-up enforcement on. Default false — the
	// engine is constructed but the hook is not registered.
	Enabled bool `yaml:"enabled,omitempty"`

	// Tools maps tool name → required acr value. A tool absent from
	// this map has no step-up requirement. When a tool IS present,
	// the caller's auth Identity MUST carry an `acr` claim equal to
	// the value (or listed in AcrHierarchy above the required value).
	//
	// Example:
	//   tools:
	//     cli_execute: acr:mfa
	//     http_request: acr:mfa
	Tools map[string]string `yaml:"tools,omitempty"`

	// AcrHierarchy is an optional ordered list of acr values,
	// lowest-assurance first. A caller presenting acr X satisfies a
	// requirement for acr Y iff index(X) >= index(Y) in this list.
	// When AcrHierarchy is empty, comparison is strict-equal.
	//
	// Example:
	//   acr_hierarchy: ["acr:password", "acr:mfa", "acr:hardware"]
	// — a caller with "acr:hardware" satisfies a requirement for
	// "acr:mfa" or "acr:password".
	AcrHierarchy []string `yaml:"acr_hierarchy,omitempty"`
}

// DeferConfig is the forge.yaml-facing block for R4c deferred
// authorization.
//
// Tools listed in Tools trigger a pause on BeforeToolExec: the
// executor blocks the calling goroutine, flips the A2A task status
// to `deferred`, and waits for a decision to arrive via the
// decisions endpoint (or the configured timeout, whichever first).
// On approve, the tool proceeds; on reject or timeout, the tool
// call fails with a defer-denied error.
type DeferConfig struct {
	// Enabled turns deferred authorization on. Default false — the
	// hook is not registered; POST /tasks/{id}/decisions returns 404.
	Enabled bool `yaml:"enabled,omitempty"`

	// Tools maps tool name → deferral parameters. A tool absent from
	// this map has no deferral requirement. Value shape:
	//   tools:
	//     cli_execute:
	//       to: channel:slack:#oncall
	//       timeout: 10m
	//       context_template: "agent about to run {binary} {args}"
	Tools map[string]DeferToolConfig `yaml:"tools,omitempty"`

	// DefaultTimeout applies when a tool's Timeout is unset. Zero →
	// 10 minutes.
	DefaultTimeout time.Duration `yaml:"default_timeout,omitempty"`

	// DefaultTo applies when a tool's To is unset. Empty is
	// legal — the deferral fires and the audit event carries "" for
	// operators to route on downstream.
	DefaultTo string `yaml:"default_to,omitempty"`
}

// Validate returns an error when the config would silently no-op or
// misroute at runtime. Called at Runner construction so an operator
// who typos `enabled: true` without declaring any tools fails
// startup rather than getting a "defer engine wired" log line and
// zero enforcement. Matches the fail-loud posture of the sibling R4
// PRs (step_up + intent_alignment).
func (c DeferConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Tools) == 0 {
		return fmt.Errorf("security.defer: enabled but no tools declared — either list tools under `defer.tools:` or set `defer.enabled: false`")
	}
	return nil
}

// DeferToolConfig configures deferral for one tool.
type DeferToolConfig struct {
	// To identifies the decision target (channel, human, external
	// endpoint). Empty falls back to DeferConfig.DefaultTo.
	To string `yaml:"to,omitempty"`

	// Timeout is the maximum wait before auto-deny. Empty falls
	// back to DeferConfig.DefaultTimeout.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// ContextTemplate is the string that becomes the approver's
	// context payload. `{tool}` / `{args}` placeholders are expanded
	// at hook time. When empty, the payload is
	// `"tool={tool} args={args}"`.
	ContextTemplate string `yaml:"context_template,omitempty"`
}

// ObservabilityConfig groups telemetry-related sub-blocks. Today it
// carries only `tracing:` (OTel Tracing v1, issue #108). Future
// metrics / logs configuration belongs here too so operators have a
// single observability stanza in forge.yaml.
type ObservabilityConfig struct {
	Tracing TracingYAML `yaml:"tracing,omitempty"`
}

// TracingYAML is the yaml-facing tracing configuration. It maps onto
// (a subset of) observability.TracingConfig at runtime; the cli's
// resolver layers env + CLI flags on top before calling
// observability.NewTracerProvider. Operator-facing only — fields the
// cli derives at runtime (ServiceVersion from cfg.Version,
// RuntimeVersion from the build) are not exposed here.
//
// Off by default per the initiative ruling (#108): an absent or empty
// `observability.tracing:` block leaves Enabled false, which means
// the runner installs the noop tracer and emits no telemetry.
type TracingYAML struct {
	// Enabled gates the whole subsystem. Default false.
	Enabled bool `yaml:"enabled,omitempty"`

	// Endpoint is the OTLP target URL. Required when Enabled=true.
	// For http/protobuf: "https://collector.svc.cluster.local:4318/v1/traces".
	// For gRPC: "collector.svc.cluster.local:4317".
	Endpoint string `yaml:"endpoint,omitempty"`

	// Protocol selects the OTLP encoding. One of "http/protobuf"
	// (default) or "grpc". HTTP is recommended because the
	// in-process egress enforcer can wrap it; gRPC relies on the
	// build-time allowlist + NetworkPolicy.
	Protocol string `yaml:"protocol,omitempty"`

	// Sampler is one of the standard OTEL_TRACES_SAMPLER names:
	// always_on, always_off, traceidratio, parentbased_always_on
	// (default), parentbased_always_off, parentbased_traceidratio.
	Sampler string `yaml:"sampler,omitempty"`

	// SamplerRatio applies to the *traceidratio* samplers (0.0–1.0).
	// Ignored for the always_on / always_off variants. Default 1.0.
	SamplerRatio float64 `yaml:"sampler_ratio,omitempty"`

	// Headers are extra OTLP request headers (typically auth tokens).
	// Prefer env-driven values (OTEL_EXPORTER_OTLP_HEADERS) so
	// secrets do not end up committed to forge.yaml.
	Headers map[string]string `yaml:"headers,omitempty"`

	// Timeout bounds each exporter request. Default 10s.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// ServiceName is the OTel `service.name` resource attribute.
	// Empty means the cli will derive one (OTEL_SERVICE_NAME env
	// var, then AgentID).
	ServiceName string `yaml:"service_name,omitempty"`

	// ResourceAttrs are extra OTel resource attributes merged with
	// the service.* / forge.* attributes the cli derives. Operators
	// can also append via OTEL_RESOURCE_ATTRIBUTES.
	ResourceAttrs map[string]string `yaml:"resource_attrs,omitempty"`

	// Redact controls whether prompt / completion / tool I/O content
	// in spans is scrubbed before export. Default true. Honored by
	// the Phase 3 (#104) span instrumentation; surfaced here so the
	// cli layer wires every tracing knob in one place.
	Redact *bool `yaml:"redact,omitempty"`

	// CaptureContent is the enterprise opt-in for raw prompt /
	// completion content on spans. Default false (metadata only,
	// matching the FWS-8 audit posture). Honored by Phase 3.
	CaptureContent bool `yaml:"capture_content,omitempty"`
}

// ServerConfig groups A2A-server-side knobs that don't fit elsewhere
// in the schema. Today it carries only the rate-limit sub-block; new
// server-level concerns (TLS, request-size limits) belong here.
// See issue #110 / FWS-10.
type ServerConfig struct {
	RateLimit RateLimitYAML `yaml:"rate_limit,omitempty"`
}

// RateLimitYAML mirrors the runtime RateLimitConfig but lives in
// forge-core/types so it can be unmarshaled from forge.yaml without
// importing forge-cli/server. The runner copies the populated fields
// into the runtime RateLimitConfig. Zero values mean "use the
// runtime default" — so an empty server.rate_limit block in
// forge.yaml is equivalent to no block at all.
type RateLimitYAML struct {
	ReadRPS      float64 `yaml:"read_rps,omitempty"`
	ReadBurst    int     `yaml:"read_burst,omitempty"`
	WriteRPS     float64 `yaml:"write_rps,omitempty"`
	WriteBurst   int     `yaml:"write_burst,omitempty"`
	CancelExempt *bool   `yaml:"cancel_exempt,omitempty"` // pointer so "explicitly false" can override the true default
}

// MCPConfig declares Model Context Protocol servers for the agent.
//
// Phase 1 (v0.12.0): HTTP transport only. Stdio servers are on the
// roadmap — see docs/mcp/index.md. The Forge runtime never spawns
// subprocesses for MCP; transport=stdio is rejected at validate time.
type MCPConfig struct {
	// TokenStorePath overrides the encrypted OAuth-token store location.
	// Default: ~/.forge/mcp-tokens.enc. Override via env MCP_TOKEN_STORE_PATH.
	TokenStorePath string `yaml:"token_store_path,omitempty"`

	// Servers is the ordered list of MCP servers Forge connects to.
	// Each server's discovered tools are registered as namespaced
	// "<server>__<tool>" entries in the agent's tool registry.
	Servers []MCPServer `yaml:"servers,omitempty"`
}

// MCPServer is one entry in MCPConfig.Servers.
type MCPServer struct {
	// Name is the slug-format identifier used as the tool namespace
	// prefix and in audit logs (e.g., name "linear" → tools
	// "linear__create_issue", "linear__list_issues", ...). Required.
	Name string `yaml:"name"`

	// Transport selects the wire protocol. Phase 1: "http" only.
	// "stdio" is rejected at validate time with a roadmap pointer.
	Transport string `yaml:"transport"`

	// URL is the HTTP endpoint for the MCP server. Required for
	// transport=http. Examples:
	//   - https://mcp.linear.app/sse        (vendor-hosted)
	//   - http://notion-mcp.mcp-servers.svc.cluster.local:8080/mcp
	URL string `yaml:"url"`

	// Auth, when non-nil, declares how Forge authenticates outbound
	// calls to this MCP server. nil means no auth (typical for
	// in-cluster trust networks).
	Auth *MCPAuth `yaml:"auth,omitempty"`

	// Tools filters which tools discovered via tools/list are exposed
	// to the LLM. Allow / Deny cannot BOTH be empty — operators must
	// be explicit. See ValidateMCPConfig.
	Tools MCPToolFilter `yaml:"tools,omitempty"`

	// Timeout caps each MCP RPC. Default 60s. Minimum 1s.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// Required controls startup failure behavior:
	//   - true:  this server failing during startup aborts forge run
	//            with a non-zero exit (K8s observes CrashLoopBackOff).
	//   - false: warn + continue without this server's tools.
	Required bool `yaml:"required,omitempty"`
}

// MCPToolFilter controls which discovered MCP tools are exposed to
// the LLM.
//
// Default-deny: if Allow is empty AND Deny is empty, validation
// rejects the entry. Operators must be explicit about tool exposure.
type MCPToolFilter struct {
	// Allow is the explicit whitelist of tool names to expose. Use
	// "*" to expose every tool discovered at first connect (snapshot
	// semantics — tools added by the MCP server later do NOT appear
	// without a re-build).
	Allow []string `yaml:"allow,omitempty"`

	// Deny subtracts from the Allow set (or from "all discovered"
	// when Allow=["*"]). A tool listed in both Allow and Deny is a
	// validation error.
	Deny []string `yaml:"deny,omitempty"`
}

// MCPAuth declares the authentication mechanism for an MCP server.
type MCPAuth struct {
	// Type is one of:
	//   - "oauth"  → OAuth 2.1 PKCE; tokens stored in MCPConfig.TokenStorePath.
	//                Requires ClientID, AuthorizeURL, TokenURL.
	//                Use `forge mcp login <name>` once at laptop time.
	//   - "bearer" → static Bearer token from env var TokenEnv.
	//   - "static" → same as bearer; named separately for clarity in
	//                forge.yaml.
	Type string `yaml:"type"`

	// ClientID is the OAuth client identifier registered with the MCP
	// server's authorization service. Required when Type == "oauth".
	ClientID string `yaml:"client_id,omitempty"`

	// Scopes is the OAuth scope set requested at login.
	Scopes []string `yaml:"scopes,omitempty"`

	// AuthorizeURL is the OAuth 2.1 authorization endpoint (where
	// `forge mcp login` opens the browser). Required when Type ==
	// "oauth". Phase 1 requires this be explicit; Phase 1.5 will add
	// RFC 9728 / RFC 8414 automated discovery via the MCP server URL.
	AuthorizeURL string `yaml:"authorize_url,omitempty"`

	// TokenURL is the OAuth 2.1 token endpoint (where authorization
	// codes and refresh tokens are exchanged). Required when
	// Type == "oauth".
	TokenURL string `yaml:"token_url,omitempty"`

	// TokenEnv names the environment variable holding the bearer
	// token. Required when Type ∈ {"bearer", "static"}. The variable
	// is read at runtime, never stored in forge.yaml.
	TokenEnv string `yaml:"token_env,omitempty"`
}

// AuthConfig declares the auth provider chain for the A2A server. Mirrors
// the secrets.providers pattern: each entry is { type, settings } and the
// runner builds them in order via auth.Registry.BuildChain.
//
// Backward compatibility: if AuthConfig.Providers is empty, the legacy
// --auth-url / FORGE_AUTH_URL / FORGE_AUTH_ORG_ID flow synthesizes a
// single-element http_verifier chain (unchanged from pre-PR3 behavior).
type AuthConfig struct {
	// Required indicates whether auth is mandatory. When false (default),
	// the runtime treats Providers as the source of truth — operators may
	// still opt out via --no-auth on localhost. Reserved for future
	// TUI/UI gating logic.
	Required bool `yaml:"required,omitempty"`

	// Providers is the ordered list of auth providers that compose into
	// the A2A server's auth chain. First-match wins.
	Providers []AuthProvider `yaml:"providers,omitempty"`
}

// AuthProvider is one entry in AuthConfig.Providers. The Type names a
// factory registered with the auth package (e.g., "oidc", "http_verifier",
// "static_token", and — in Phase 3 — "okta"). Settings is unmarshaled
// into the provider-specific Config struct via auth.UnmarshalSettings.
type AuthProvider struct {
	Type     string         `yaml:"type"`
	Name     string         `yaml:"name,omitempty"`
	Settings map[string]any `yaml:"settings,omitempty"`
}

// ScheduleConfig defines a recurring scheduled task in forge.yaml.
type ScheduleConfig struct {
	ID            string `yaml:"id"`
	Cron          string `yaml:"cron"`
	Task          string `yaml:"task"`
	Skill         string `yaml:"skill,omitempty"`
	Channel       string `yaml:"channel,omitempty"`        // channel adapter name (e.g. "slack", "telegram")
	ChannelTarget string `yaml:"channel_target,omitempty"` // destination ID (channel ID, chat ID)
}

// SchedulerConfig selects the scheduler backend and tunes its
// behavior. Default zero value is "auto": file backend on the
// laptop / CI, Kubernetes backend when running in-cluster (the
// in-cluster signal is the projected ServiceAccount token at
// /var/run/secrets/kubernetes.io/serviceaccount/token). See issue
// #162.
type SchedulerConfig struct {
	// Backend is one of "auto" (default), "file", or "kubernetes".
	// - "auto": file when not in-cluster, kubernetes when in-cluster.
	// - "file": always the file-backed scheduler with the 30s ticker.
	// - "kubernetes": always the K8s CronJob backend. Errors at startup
	//   when not in-cluster and FORGE_IN_CLUSTER is not set true.
	Backend string `yaml:"backend,omitempty"`

	// Kubernetes carries backend-specific tuning that's only consulted
	// when Backend resolves to "kubernetes".
	Kubernetes K8sSchedulerConfig `yaml:"kubernetes,omitempty"`
}

// K8sSchedulerConfig is the kubernetes-backend tuning block. Wired
// in #162 part 2b (runtime CronJob CRUD) and #162 part 3
// (`forge package` manifest generation).
type K8sSchedulerConfig struct {
	// Namespace is the K8s namespace CronJobs land in. Empty defaults
	// to the agent pod's own namespace at runtime; `forge package`
	// emits "default" when this field is unset.
	Namespace string `yaml:"namespace,omitempty"`

	// ServiceURL is the in-cluster URL CronJob trigger pods POST to.
	// Required when Backend resolves to "kubernetes". Typical value:
	// http://<agent-svc>.<ns>.svc:<port>/
	ServiceURL string `yaml:"service_url,omitempty"`

	// AllowDynamic gates whether the LLM-driven `schedule_set` builtin
	// tool can create new CronJobs at runtime. Default false — only
	// declarative forge.yaml `schedules[]` entries materialize as
	// CronJobs. Flipping to true requires granting the agent's
	// ServiceAccount create/patch/delete RBAC on batch/cronjobs in its
	// own namespace; see docs/deployment/scheduler-kubernetes.md.
	AllowDynamic bool `yaml:"allow_dynamic,omitempty"`

	// TriggerImage is the container image the CronJob runs to make the
	// curl request. Empty defaults to DefaultTriggerImage.
	TriggerImage string `yaml:"trigger_image,omitempty"`

	// AuthSecretName overrides the K8s Secret name CronJobs mount for
	// the internal bearer token. Empty defaults to
	// "<agent_id>-internal-token" matching `forge auth secret-yaml`.
	AuthSecretName string `yaml:"auth_secret_name,omitempty"`
}

// SecretsConfig configures secret management providers.
type SecretsConfig struct {
	Providers []string `yaml:"providers,omitempty"` // e.g. ["env"], ["encrypted-file","env"]
	Path      string   `yaml:"path,omitempty"`      // encrypted file path, default ~/.forge/secrets.enc
}

// MemoryConfig configures agent memory persistence and compaction.
type MemoryConfig struct {
	Persistence   *bool   `yaml:"persistence,omitempty"` // default: true
	SessionsDir   string  `yaml:"sessions_dir,omitempty"`
	SessionMaxAge string  `yaml:"session_max_age,omitempty"` // e.g. "30m", "1h" (default: 30m)
	TriggerRatio  float64 `yaml:"trigger_ratio,omitempty"`
	CharBudget    int     `yaml:"char_budget,omitempty"`

	// SessionStore selects the session-memory backend (issue #243):
	//   "file"   (default) — local .forge/sessions/*.json; single-pod / dev.
	//   "remote"           — push snapshots to a platform session service
	//                        so stateless pods resume any task on any replica.
	// Env override: FORGE_SESSION_STORE. When "remote", SessionStoreURL
	// (or FORGE_SESSION_STORE_URL) must point at the service; the pod
	// reuses FORGE_PLATFORM_TOKEN + FORGE_ORG_ID/FORGE_WORKSPACE_ID for
	// auth/tenancy, exactly as the admission client does.
	SessionStore    string `yaml:"session_store,omitempty"`
	SessionStoreURL string `yaml:"session_store_url,omitempty"`

	// Long-term memory (persistent cross-session knowledge).
	LongTerm          *bool   `yaml:"long_term,omitempty"`            // default: false
	MemoryDir         string  `yaml:"memory_dir,omitempty"`           // default: .forge/memory
	EmbeddingProvider string  `yaml:"embedding_provider,omitempty"`   // auto-detect from LLM
	EmbeddingModel    string  `yaml:"embedding_model,omitempty"`      // provider default
	VectorWeight      float64 `yaml:"vector_weight,omitempty"`        // default: 0.7
	KeywordWeight     float64 `yaml:"keyword_weight,omitempty"`       // default: 0.3
	DecayHalfLifeDays int     `yaml:"decay_half_life_days,omitempty"` // default: 7
}

// CompressionConfig configures reversible context compression (ctxzip).
//
// When enabled, bulky tool outputs and conversation content are compressed
// before reaching the LLM; everything dropped is stored locally (bbolt) and
// retrievable via the context_expand tool, so compression is lossy on the
// wire but lossless end-to-end. Enable via `compression.enabled: true` in
// forge.yaml or FORGE_COMPRESSION=true (env wins; "false" forces off).
type CompressionConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"` // default: false
	// StorePath is the bbolt file holding offloaded originals.
	StorePath string `yaml:"store_path,omitempty"` // default: .forge/ctxzip.db
	// TTL is how long offloaded originals stay retrievable (Go duration,
	// e.g. "30m", "2h").
	TTL string `yaml:"ttl,omitempty"` // default: 30m
	// MinToolOutputChars is the tool-output size below which the
	// compression hook leaves the output alone.
	MinToolOutputChars int `yaml:"min_tool_output_chars,omitempty"` // default: 2048
	// CacheHints controls provider prompt-cache hints (Anthropic
	// cache_control breakpoints, OpenAI prompt_cache_key). Defaults to the
	// value of Enabled; set explicitly to run hints without compression or
	// compression without hints.
	CacheHints *bool `yaml:"cache_hints,omitempty"`
	// KeepPatterns is the agent's domain vocabulary of case-insensitive
	// substrings that compression must never drop — e.g. Kubernetes state
	// words ("CrashLoopBackOff", "ImagePullBackOff") or product error codes.
	// Union with the built-in error floor (error/fail/panic/timeout/...):
	// entries only ever add protection.
	KeepPatterns []string `yaml:"keep_patterns,omitempty"`
}

// EgressRef configures egress security controls.
type EgressRef struct {
	Profile         string   `yaml:"profile,omitempty"` // strict, standard, permissive
	Mode            string   `yaml:"mode,omitempty"`    // deny-all, allowlist, dev-open
	AllowedDomains  []string `yaml:"allowed_domains,omitempty"`
	Capabilities    []string `yaml:"capabilities,omitempty"` // capability bundles (e.g., "slack", "telegram")
	AllowPrivateIPs *bool    `yaml:"allow_private_ips,omitempty"`
}

// SkillsRef references a skills definition file.
type SkillsRef struct {
	Path string `yaml:"path,omitempty"` // default: "SKILL.md"
}

// ModelRef identifies the model an agent uses.
type ModelRef struct {
	Provider string `yaml:"provider"`
	Name     string `yaml:"name"`

	// BaseURL overrides the provider's default API host. Operators
	// configure this when running against an OpenAI-compatible
	// (Together.ai, OpenRouter, Groq, Fireworks, Anyscale, vLLM,
	// llama.cpp's server), Anthropic-compatible (Bedrock proxy,
	// custom gateway), or remotely-served Ollama endpoint. The
	// canonical env vars (OPENAI_BASE_URL / ANTHROPIC_BASE_URL /
	// OLLAMA_BASE_URL / GEMINI_BASE_URL) still take precedence at
	// runtime — this field exists so the build pipeline can
	// auto-merge the hostname into egress_allowlist.json + the
	// generated NetworkPolicy. See issue #139.
	BaseURL string `yaml:"base_url,omitempty"`

	// AuthScheme selects the outbound authentication scheme used to
	// reach the configured BaseURL. Issue #202 Phase 2 — symmetric
	// across the openai and anthropic providers so an operator can
	// point either at AWS Bedrock (Anthropic passthrough or the
	// OpenAI compatibility endpoint) using AWS SigV4 credentials in
	// place of the provider's native API-key header.
	//
	// Valid values:
	//
	//	""           — provider default
	//	                 (anthropic: x-api-key header from APIKey;
	//	                  openai:    Authorization: Bearer header from APIKey)
	//	"x_api_key"  — explicit anthropic native (same as "")
	//	"bearer"     — explicit openai native (same as "")
	//	"aws_sigv4"  — AWS Signature V4 signing on every request,
	//	                using credentials resolved from the standard
	//	                environment (AWS_ACCESS_KEY_ID / _SECRET_ /
	//	                _SESSION_TOKEN) plus the AWSRegion field
	//	                below. APIKey is ignored on this path.
	//	"apikey_header" — sends APIKey in the AuthHeaderName header
	//	                (default "apikey") IN ADDITION TO the
	//	                provider-native header. For API gateways whose
	//	                auth plugin reads a fixed header — e.g. Kong AI
	//	                Gateway key-auth. Additive, so safe against
	//	                non-gateway endpoints. Issue #302.
	//
	// Unset for the vast majority of deployments — the default
	// behavior matches the pre-#202 contract byte-for-byte.
	AuthScheme string `yaml:"auth_scheme,omitempty"`

	// AWSRegion is the AWS region used for SigV4 signing when
	// AuthScheme == "aws_sigv4". Required on that path. Forge does
	// not parse the region out of the BaseURL because Bedrock URLs
	// often go through customer-side proxies that re-write the host.
	AWSRegion string `yaml:"aws_region,omitempty"`

	// AuthHeaderName overrides the header used by the "apikey_header"
	// scheme (issue #302). Empty → "apikey" (Kong key-auth's default
	// key_names). Set it for a gateway with custom key_names, e.g.
	// "x-gateway-key". Ignored for every other AuthScheme.
	AuthHeaderName string `yaml:"auth_header_name,omitempty"`

	Version        string          `yaml:"version,omitempty"`
	OrganizationID string          `yaml:"organization_id,omitempty"`
	Fallbacks      []ModelFallback `yaml:"fallbacks,omitempty"`
}

// ModelFallback identifies an alternative LLM provider for fallback.
type ModelFallback struct {
	Provider string `yaml:"provider"`
	Name     string `yaml:"name,omitempty"`

	// BaseURL — same semantics as ModelRef.BaseURL. Fallback
	// providers commonly run on a different base URL than the
	// primary; both need to make it into the egress allowlist.
	BaseURL string `yaml:"base_url,omitempty"`

	OrganizationID string `yaml:"organization_id,omitempty"`

	// NOTE: AuthScheme / AuthHeaderName / AWSRegion are intentionally
	// absent — auth_scheme (#202 aws_sigv4, #302 apikey_header) applies to
	// the PRIMARY model only. A fallback routed through the same gateway
	// authenticates with its provider-native header. Adding per-fallback
	// scheme fields is tracked as a follow-up (the FORGE_MODEL_FALLBACKS
	// env source would need a parallel encoding).
}

// ToolRef is a lightweight reference to a tool in forge.yaml.
type ToolRef struct {
	Name   string         `yaml:"name"`
	Type   string         `yaml:"type,omitempty"`
	Config map[string]any `yaml:"config,omitempty"`
}

// PackageConfig controls container packaging behavior.
type PackageConfig struct {
	BaseImage    string                 `yaml:"base_image,omitempty"`
	Alpine       bool                   `yaml:"alpine,omitempty"`
	Slim         bool                   `yaml:"slim,omitempty"`
	BinOverrides map[string]BinOverride `yaml:"bin_overrides,omitempty"`
}

// BinOverride provides explicit install instructions for a binary in the container.
type BinOverride struct {
	AptPackage  string   `yaml:"apt,omitempty"`
	ApkPackage  string   `yaml:"apk,omitempty"`
	DirectURL   string   `yaml:"url,omitempty"`
	Dest        string   `yaml:"dest,omitempty"`
	Chmod       string   `yaml:"chmod,omitempty"`
	CustomLines []string `yaml:"run,omitempty"`
	LocalPath   string   `yaml:"local,omitempty"` // host path to local binary file
}

// ParseForgeConfig parses raw YAML bytes into a ForgeConfig and validates required fields.
func ParseForgeConfig(data []byte) (*ForgeConfig, error) {
	var cfg ForgeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing forge config: %w", err)
	}

	if cfg.AgentID == "" {
		return nil, fmt.Errorf("forge config: agent_id is required")
	}
	if cfg.Version == "" {
		return nil, fmt.Errorf("forge config: version is required")
	}
	if cfg.Entrypoint == "" && cfg.Framework != "forge" {
		return nil, fmt.Errorf("forge config: entrypoint is required")
	}

	return &cfg, nil
}
