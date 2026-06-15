// Package types holds configuration types for forge.yaml.
package types

import (
	"fmt"
	"time"

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

	// Long-term memory (persistent cross-session knowledge).
	LongTerm          *bool   `yaml:"long_term,omitempty"`            // default: false
	MemoryDir         string  `yaml:"memory_dir,omitempty"`           // default: .forge/memory
	EmbeddingProvider string  `yaml:"embedding_provider,omitempty"`   // auto-detect from LLM
	EmbeddingModel    string  `yaml:"embedding_model,omitempty"`      // provider default
	VectorWeight      float64 `yaml:"vector_weight,omitempty"`        // default: 0.7
	KeywordWeight     float64 `yaml:"keyword_weight,omitempty"`       // default: 0.3
	DecayHalfLifeDays int     `yaml:"decay_half_life_days,omitempty"` // default: 7
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
