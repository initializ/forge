package forgeui

import (
	"context"
	"time"

	"github.com/initializ/forge/forge-ui/uiconfig"
)

// ProcessState represents the lifecycle state of an agent process.
type ProcessState string

const (
	StateStopped  ProcessState = "stopped"
	StateStarting ProcessState = "starting"
	StateRunning  ProcessState = "running"
	StateStopping ProcessState = "stopping"
	StateErrored  ProcessState = "errored"
)

// AgentInfo describes a discovered agent and its runtime state.
type AgentInfo struct {
	ID              string       `json:"id"`
	Version         string       `json:"version"`
	Framework       string       `json:"framework"`
	Model           AgentModel   `json:"model"`
	Tools           []string     `json:"tools"`
	Channels        []string     `json:"channels"`
	Skills          int          `json:"skills"`
	Directory       string       `json:"directory"`
	Status          ProcessState `json:"status"`
	Port            int          `json:"port,omitempty"`
	Error           string       `json:"error,omitempty"`
	StartedAt       *time.Time   `json:"started_at,omitempty"`
	NeedsPassphrase bool         `json:"needs_passphrase,omitempty"`
}

// StartRequest is the optional POST body for the start endpoint.
type StartRequest struct {
	Passphrase string `json:"passphrase,omitempty"`
}

// AgentModel holds model provider and name.
type AgentModel struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

// ChatRequest is the POST body for the chat endpoint.
type ChatRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

// SessionInfo describes a stored chat session for listing.
type SessionInfo struct {
	ID        string    `json:"id"`
	Preview   string    `json:"preview"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SSEEvent is an event broadcast to connected UI clients.
type SSEEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// AgentCreateFunc scaffolds a new agent in the workspace.
// Injected by forge-cli.
type AgentCreateFunc func(opts AgentCreateOptions) (agentDir string, err error)

// OAuthFlowFunc runs the OAuth browser flow for a provider and returns the access token.
// Injected by forge-cli when OAuth is available.
type OAuthFlowFunc func(provider string) (accessToken string, err error)

// SkillBuilderMessage is a chat message for the skill builder conversation.
type SkillBuilderMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMStreamOptions configures a streaming LLM call for the skill builder.
//
// The LLM struct is the resolved skill-builder LLM configuration —
// workspace-level (per issue #92) when available, with the agent-
// fallback path used only when no workspace/user config exists.
// Callers in forge-cli MUST consume LLM directly rather than re-reading
// the agent's forge.yaml / .env: doing so would re-introduce the
// per-agent env-stomping the workspace-LLM design replaced.
//
// AgentDir is retained for the deprecated fallback resolution path
// only — forge-ui passes it so the loader can read the agent's
// forge.yaml when no workspace/user config exists. New code paths
// should not depend on it.
type LLMStreamOptions struct {
	LLM          uiconfig.SkillBuilderLLM
	AgentDir     string
	SystemPrompt string
	Messages     []SkillBuilderMessage
	OnChunk      func(string)
	OnDone       func(fullResponse string)
}

// LLMStreamFunc streams an LLM response for the skill builder.
// Injected by forge-cli.
type LLMStreamFunc func(ctx context.Context, opts LLMStreamOptions) error

// SkillSaveOptions configures saving a skill to an agent's skills directory.
type SkillSaveOptions struct {
	AgentDir  string
	SkillName string
	SkillMD   string
	Scripts   map[string]string
	EnvVars   map[string]string // env vars to write to .env
}

// SkillSaveResult holds the result of saving a skill, including env/egress changes.
type SkillSaveResult struct {
	Path          string          `json:"path"`
	EgressAdded   []string        `json:"egress_added,omitempty"`
	EnvConfigured []string        `json:"env_configured,omitempty"`
	EnvMissing    []SkillEnvEntry `json:"env_missing,omitempty"`
}

// SkillEnvEntry describes a missing environment variable requirement.
type SkillEnvEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "required", "one_of", "optional"
}

// SkillSaveFunc saves a generated skill to disk and configures env/egress.
// Injected by forge-cli.
type SkillSaveFunc func(opts SkillSaveOptions) (*SkillSaveResult, error)

// SkillBuilderChatRequest is the POST body for the skill builder chat endpoint.
type SkillBuilderChatRequest struct {
	Messages []SkillBuilderMessage `json:"messages"`
}

// SkillBuilderValidateRequest is the POST body for skill validation.
type SkillBuilderValidateRequest struct {
	SkillMD string            `json:"skill_md"`
	Scripts map[string]string `json:"scripts,omitempty"`
}

// SkillBuilderSaveRequest is the POST body for saving a skill.
type SkillBuilderSaveRequest struct {
	SkillName string            `json:"skill_name"`
	SkillMD   string            `json:"skill_md"`
	Scripts   map[string]string `json:"scripts,omitempty"`
	EnvVars   map[string]string `json:"env_vars,omitempty"`
}

// SkillValidationResult holds the result of validating a SKILL.md.
type SkillValidationResult struct {
	Valid    bool              `json:"valid"`
	Errors   []ValidationError `json:"errors,omitempty"`
	Warnings []ValidationError `json:"warnings,omitempty"`
}

// ValidationError describes a single validation issue.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// AgentCreateOptions contains all parameters for creating a new agent.
type AgentCreateOptions struct {
	Name              string             `json:"name"`
	Framework         string             `json:"framework"`
	ModelProvider     string             `json:"model_provider"`
	ModelName         string             `json:"model_name,omitempty"`
	APIKey            string             `json:"api_key,omitempty"`
	AuthMethod        string             `json:"auth_method,omitempty"` // "apikey" or "oauth"
	OrganizationID    string             `json:"organization_id,omitempty"`
	Channels          []string           `json:"channels,omitempty"`
	BuiltinTools      []string           `json:"builtin_tools,omitempty"`
	Skills            []string           `json:"skills,omitempty"`
	Fallbacks         []FallbackProvider `json:"fallbacks,omitempty"`
	WebSearchProvider string             `json:"web_search_provider,omitempty"` // "tavily" or "perplexity"
	Passphrase        string             `json:"passphrase,omitempty"`
	EnvVars           map[string]string  `json:"env_vars,omitempty"`
	Force             bool               `json:"force,omitempty"`
	Auth              *AuthCreateOptions `json:"auth,omitempty"` // A2A server auth chain (PR6+)
}

// AuthCreateOptions describes the auth chain selection the web wizard
// captured. Mode is one of "none", "oidc", "http_verifier", "custom".
// Settings is the provider-type-specific settings block (issuer, audience,
// url, default_org, claim_map, …). Mirrors the TUI wizard's contract so
// both surfaces feed the same scaffold path.
type AuthCreateOptions struct {
	Mode     string         `json:"mode"`
	Settings map[string]any `json:"settings,omitempty"`
}

// FallbackProvider describes a fallback LLM provider with its API key.
type FallbackProvider struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key,omitempty"`
}

// CreateAgentResponse is returned after successful agent creation.
type CreateAgentResponse struct {
	AgentID   string `json:"agent_id"`
	Directory string `json:"directory"`
	Message   string `json:"message"`
}

// ConfigUpdateRequest is the PUT body for saving forge.yaml.
type ConfigUpdateRequest struct {
	Content string `json:"content"`
}

// ConfigValidateResponse returned from validate/save endpoints.
type ConfigValidateResponse struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// SkillBrowserEntry describes a registry skill for the API.
type SkillBrowserEntry struct {
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	Description   string   `json:"description"`
	Category      string   `json:"category"`
	Tags          []string `json:"tags"`
	RequiredEnv   []string `json:"required_env,omitempty"`
	OneOfEnv      []string `json:"one_of_env,omitempty"`
	OptionalEnv   []string `json:"optional_env,omitempty"`
	RequiredBins  []string `json:"required_bins,omitempty"`
	EgressDomains []string `json:"egress_domains,omitempty"`
}

// BuiltinToolInfo describes a builtin tool for the wizard.
type BuiltinToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ModelOption maps a display name to the actual model ID.
type ModelOption struct {
	DisplayName string `json:"display_name"`
	ModelID     string `json:"model_id"`
}

// ProviderModels holds model lists for a specific provider.
type ProviderModels struct {
	Default       string        `json:"default"`
	APIKey        []ModelOption `json:"api_key,omitempty"`
	OAuth         []ModelOption `json:"oauth,omitempty"`
	HasOAuth      bool          `json:"has_oauth,omitempty"`
	NeedsKey      bool          `json:"needs_key"`
	IsCustom      bool          `json:"is_custom,omitempty"`
	BaseURLEnv    string        `json:"base_url_env,omitempty"` // e.g. "OPENAI_BASE_URL"
	SupportsOrgID bool          `json:"supports_org_id,omitempty"`
}

// WebSearchProviderOption describes a web search provider.
type WebSearchProviderOption struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	EnvVar      string `json:"env_var"`
	Placeholder string `json:"placeholder"`
}

// WizardMetadata holds all reference data the frontend wizard needs.
type WizardMetadata struct {
	Providers          []string                  `json:"providers"`
	Frameworks         []string                  `json:"frameworks"`
	Channels           []string                  `json:"channels"`
	BuiltinTools       []BuiltinToolInfo         `json:"builtin_tools"`
	Skills             []SkillBrowserEntry       `json:"skills"`
	ProviderModels     map[string]ProviderModels `json:"provider_models"`
	WebSearchProviders []WebSearchProviderOption `json:"web_search_providers"`
	AuthProviderTypes  []AuthProviderTypeMeta    `json:"auth_provider_types"`
}

// AuthProviderTypeMeta describes one selectable auth provider type so the
// frontend renders its picker from server-driven metadata (no hardcoded
// provider list in JavaScript). When a new provider ships (e.g., Okta in
// Phase 3), append one entry here and the wizard picks it up.
type AuthProviderTypeMeta struct {
	Type        string `json:"type"`        // "none", "oidc", "http_verifier", "custom"
	Label       string `json:"label"`       // human-readable label for the picker
	Description string `json:"description"` // single-line description under the label
}
