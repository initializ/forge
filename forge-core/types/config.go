// Package types holds configuration types for forge.yaml.
package types

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// ForgeConfig represents the top-level forge.yaml configuration.
type ForgeConfig struct {
	AgentID        string           `yaml:"agent_id"`
	Version        string           `yaml:"version"`
	Framework      string           `yaml:"framework"`
	Entrypoint     string           `yaml:"entrypoint"`
	Model          ModelRef         `yaml:"model,omitempty"`
	Tools          []ToolRef        `yaml:"tools,omitempty"`
	BuiltinTools   []string         `yaml:"builtin_tools,omitempty"`
	Channels       []string         `yaml:"channels,omitempty"`
	Registry       string           `yaml:"registry,omitempty"`
	Egress         EgressRef        `yaml:"egress,omitempty"`
	Skills         SkillsRef        `yaml:"skills,omitempty"`
	Memory         MemoryConfig     `yaml:"memory,omitempty"`
	Secrets        SecretsConfig    `yaml:"secrets,omitempty"`
	Auth           AuthConfig       `yaml:"auth,omitempty"`
	MCP            MCPConfig        `yaml:"mcp,omitempty"`
	Schedules      []ScheduleConfig `yaml:"schedules,omitempty"`
	CORSOrigins    []string         `yaml:"cors_origins,omitempty"`
	Package        PackageConfig    `yaml:"package,omitempty"`
	GuardrailsPath string           `yaml:"guardrails_path,omitempty"` // path to guardrails.json (default: "guardrails.json")
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
	//                Requires ClientID. Use `forge mcp login <name>` once.
	//   - "bearer" → static Bearer token from env var TokenEnv.
	//   - "static" → same as bearer; named separately for clarity in
	//                forge.yaml.
	Type string `yaml:"type"`

	// ClientID is the OAuth client identifier registered with the MCP
	// server's authorization service. Required when Type == "oauth".
	ClientID string `yaml:"client_id,omitempty"`

	// Scopes is the OAuth scope set requested at login.
	Scopes []string `yaml:"scopes,omitempty"`

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
	Provider       string          `yaml:"provider"`
	Name           string          `yaml:"name"`
	Version        string          `yaml:"version,omitempty"`
	OrganizationID string          `yaml:"organization_id,omitempty"`
	Fallbacks      []ModelFallback `yaml:"fallbacks,omitempty"`
}

// ModelFallback identifies an alternative LLM provider for fallback.
type ModelFallback struct {
	Provider       string `yaml:"provider"`
	Name           string `yaml:"name,omitempty"`
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
