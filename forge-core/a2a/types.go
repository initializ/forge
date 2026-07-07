// Package a2a provides shared types for the Agent-to-Agent (A2A) protocol.
package a2a

// TaskState represents the possible states of an A2A task.
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateAuthRequired  TaskState = "auth-required"
	TaskStateRejected      TaskState = "rejected"

	// TaskStateDeferred (governance R4c / #211): the executor paused
	// mid-task awaiting an out-of-band decision (typically human
	// approval for a high-risk action). Distinct from
	// `input-required` (which needs more input FROM the user) and
	// `auth-required` (which needs step-up auth per R4b). A deferred
	// task is resolved by `POST /tasks/{id}/decisions`; on timeout,
	// it auto-transitions to `failed` with a deferred-timeout audit.
	// See docs/security/defer-decisions.md.
	TaskStateDeferred TaskState = "deferred"
)

// TaskStatus holds the current state of a task along with an optional message.
type TaskStatus struct {
	State   TaskState `json:"state"`
	Message *Message  `json:"message,omitempty"`
}

// Task represents an A2A task exchanged between agents.
type Task struct {
	ID        string         `json:"id"`
	Status    TaskStatus     `json:"status"`
	History   []Message      `json:"history,omitempty"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// MessageRole indicates who produced a message.
type MessageRole string

const (
	MessageRoleUser  MessageRole = "user"
	MessageRoleAgent MessageRole = "agent"
)

// Message is a single conversational turn in the A2A protocol.
//
// Summary, when set, is a short LLM-generated synopsis of the agent's full
// response. Channel adapters prefer it over head-truncating the verbose body
// when an inline-friendly message is needed. Empty for short responses where
// the full text already fits inline.
type Message struct {
	Role    MessageRole `json:"role"`
	Parts   []Part      `json:"parts"`
	Summary string      `json:"summary,omitempty"`
}

// PartKind discriminates the content type of a Part.
type PartKind string

const (
	PartKindText PartKind = "text"
	PartKindData PartKind = "data"
	PartKindFile PartKind = "file"
)

// Part is a flat union struct representing a piece of message content.
// Exactly one of Text, Data, or File should be set, indicated by Kind.
type Part struct {
	Kind PartKind     `json:"kind"`
	Text string       `json:"text,omitempty"`
	Data any          `json:"data,omitempty"`
	File *FileContent `json:"file,omitempty"`
}

// FileContent holds the contents or reference for a file part.
type FileContent struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Bytes    []byte `json:"bytes,omitempty"`
}

// Artifact is a named output produced by an agent task.
type Artifact struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parts       []Part `json:"parts"`
}

// AgentCard describes an agent's capabilities for discovery.
//
// The serialized JSON shape conforms to the Agent2Agent (A2A) Protocol
// 0.3.0 Agent Card specification:
//
//	https://github.com/google/a2a-spec
//
// Forge serves the card at /.well-known/agent-card.json (the spec's
// canonical path). The legacy /.well-known/agent.json path is also
// served for backward compatibility and emits a Deprecation response
// header — that alias will be removed in a future release.
//
// Forge-internal fields (egress, denied_tools, trust hints) live in
// agentspec.AgentSpec and are intentionally NOT serialized into the
// Agent Card. The card carries only what the A2A spec defines.
type AgentCard struct {
	// Name is the human-readable agent name. Required.
	Name string `json:"name"`

	// Description is the agent's one-line summary. Optional in the
	// spec, but Forge always populates it.
	Description string `json:"description,omitempty"`

	// URL is the agent's primary service endpoint (the base URL where
	// the JSON-RPC and REST handlers live). Required.
	URL string `json:"url"`

	// Version is the agent's semantic version string (e.g. "0.1.0").
	// Required by A2A 0.3.0. Forge sources this from forge.yaml's
	// version field (or the build-time agent.json's version).
	Version string `json:"version"`

	// ProtocolVersion pins the A2A protocol version this card claims
	// to conform to. Forge always emits "0.3.0".
	ProtocolVersion string `json:"protocolVersion"`

	// DefaultInputModes lists the MIME types the agent accepts on
	// message parts when a skill doesn't override them. A2A 0.3.0
	// requires at least one entry. Forge defaults to text/plain +
	// application/json.
	DefaultInputModes []string `json:"defaultInputModes"`

	// DefaultOutputModes lists the MIME types the agent emits on
	// message parts when a skill doesn't override them. Required.
	DefaultOutputModes []string `json:"defaultOutputModes"`

	// Skills lists the agent's discoverable capabilities. Each entry
	// maps to an A2A AgentSkill object.
	Skills []Skill `json:"skills,omitempty"`

	// Capabilities declares optional A2A features the agent supports.
	Capabilities *AgentCapabilities `json:"capabilities,omitempty"`

	// SecuritySchemes maps a scheme name to its definition. Mirrors the
	// OpenAPI 3.1 securitySchemes shape per A2A 0.3.0. Forge derives
	// these from the configured auth chain (static_token → httpBearer,
	// oidc → openIdConnect, etc.).
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`

	// Security is the list of accepted security requirements. Each
	// entry is a map of scheme name → required scopes (empty array for
	// schemes that don't use scopes). Per OpenAPI semantics, the
	// outer list is OR (any one entry suffices), the inner map is AND.
	Security []map[string][]string `json:"security,omitempty"`

	// Provider identifies the organization publishing the agent.
	// Optional.
	Provider *AgentProvider `json:"provider,omitempty"`

	// DocumentationURL is a link to the agent's external docs. Optional.
	DocumentationURL string `json:"documentationUrl,omitempty"`

	// IconURL is a link to an icon for UI display. Optional.
	IconURL string `json:"iconUrl,omitempty"`
}

// AgentProvider identifies the organization publishing the agent.
type AgentProvider struct {
	Organization string `json:"organization,omitempty"`
	URL          string `json:"url,omitempty"`
}

// AgentCapabilities declares optional A2A features an agent supports.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming,omitempty"`
	PushNotifications      bool `json:"pushNotifications,omitempty"`
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
}

// Skill describes a discrete capability an agent exposes — the A2A
// AgentSkill object.
type Skill struct {
	// ID is a slug-format identifier unique within the agent.
	ID string `json:"id"`

	// Name is the human-readable skill name.
	Name string `json:"name"`

	// Description is the skill's one-line summary.
	Description string `json:"description,omitempty"`

	// Tags is a free-form classification list (category, capability
	// labels, etc.). A2A 0.3.0 makes this required; Forge always
	// populates with at least one entry (derived from SKILL.md
	// frontmatter's `category` or `tags` list).
	Tags []string `json:"tags"`

	// Examples is an optional list of example prompts that exercise
	// this skill. Used by A2A clients to surface the skill in a UI.
	Examples []string `json:"examples,omitempty"`

	// InputModes overrides AgentCard.DefaultInputModes for this skill
	// only. Optional.
	InputModes []string `json:"inputModes,omitempty"`

	// OutputModes overrides AgentCard.DefaultOutputModes for this skill
	// only. Optional.
	OutputModes []string `json:"outputModes,omitempty"`
}

// SecurityScheme describes one authentication mechanism advertised in
// the Agent Card. The shape mirrors the OpenAPI 3.1 Security Scheme
// object per A2A 0.3.0 §6.5. Only one of the type-specific groupings
// (Bearer, ApiKey, OpenIDConnect, OAuth2) is populated per instance.
type SecurityScheme struct {
	// Type is one of: "http", "apiKey", "openIdConnect", "oauth2",
	// "mutualTLS".
	Type string `json:"type"`

	// Description is an optional human-readable explanation.
	Description string `json:"description,omitempty"`

	// http: Scheme is the HTTP auth scheme — typically "bearer" or
	// "basic". For "http" Type only.
	Scheme string `json:"scheme,omitempty"`

	// http (bearer): BearerFormat is a hint about the token format
	// (e.g. "JWT").
	BearerFormat string `json:"bearerFormat,omitempty"`

	// apiKey: In identifies where the API key is sent — "header",
	// "query", or "cookie".
	In string `json:"in,omitempty"`

	// apiKey: Name is the name of the header/query/cookie that carries
	// the key. For "apiKey" Type only.
	Name string `json:"name,omitempty"`

	// openIdConnect: OpenIDConnectURL is the OIDC discovery document
	// URL (issuer + /.well-known/openid-configuration).
	OpenIDConnectURL string `json:"openIdConnectUrl,omitempty"`

	// oauth2: Flows describes the supported OAuth 2.0 flows.
	Flows *OAuthFlows `json:"flows,omitempty"`
}

// OAuthFlows describes the OAuth 2.0 flows supported by an auth scheme.
// Each field describes one flow; at least one must be populated.
type OAuthFlows struct {
	Implicit          *OAuthFlow `json:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty"`
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty"`
}

// OAuthFlow describes one OAuth 2.0 flow.
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty"`
}

// NewTextPart creates a Part containing text content.
func NewTextPart(text string) Part {
	return Part{Kind: PartKindText, Text: text}
}

// NewDataPart creates a Part containing structured data.
func NewDataPart(data any) Part {
	return Part{Kind: PartKindData, Data: data}
}

// NewFilePart creates a Part referencing a file.
func NewFilePart(file FileContent) Part {
	return Part{Kind: PartKindFile, File: &file}
}
