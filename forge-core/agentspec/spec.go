package agentspec

// AgentSpec is the canonical in-memory representation of an agent specification.
type AgentSpec struct {
	ForgeVersion   string          `json:"forge_version" bson:"forge_version" yaml:"forge_version"`
	AgentID        string          `json:"agent_id" bson:"agent_id" yaml:"agent_id"`
	Version        string          `json:"version" bson:"version" yaml:"version"`
	Name           string          `json:"name" bson:"name" yaml:"name"`
	Description    string          `json:"description,omitempty" bson:"description,omitempty" yaml:"description,omitempty"`
	Runtime        *RuntimeConfig  `json:"runtime,omitempty" bson:"runtime,omitempty" yaml:"runtime,omitempty"`
	Tools          []ToolSpec      `json:"tools,omitempty" bson:"tools,omitempty" yaml:"tools,omitempty"`
	PolicyScaffold *PolicyScaffold `json:"policy_scaffold,omitempty" bson:"policy_scaffold,omitempty" yaml:"policy_scaffold,omitempty"`
	Identity       *Identity       `json:"identity,omitempty" bson:"identity,omitempty" yaml:"identity,omitempty"`
	A2A            *A2AConfig      `json:"a2a,omitempty" bson:"a2a,omitempty" yaml:"a2a,omitempty"`
	Model          *ModelConfig    `json:"model,omitempty" bson:"model,omitempty" yaml:"model,omitempty"`

	Requirements *AgentRequirements `json:"requirements,omitempty" bson:"requirements,omitempty" yaml:"requirements,omitempty"`

	// Container packaging extensions
	ToolInterfaceVersion  string `json:"tool_interface_version,omitempty" bson:"tool_interface_version,omitempty" yaml:"tool_interface_version,omitempty"`
	SkillsSpecVersion     string `json:"skills_spec_version,omitempty" bson:"skills_spec_version,omitempty" yaml:"skills_spec_version,omitempty"`
	ForgeSkillsExtVersion string `json:"forge_skills_ext_version,omitempty" bson:"forge_skills_ext_version,omitempty" yaml:"forge_skills_ext_version,omitempty"`
	EgressProfile         string `json:"egress_profile,omitempty" bson:"egress_profile,omitempty" yaml:"egress_profile,omitempty"`
	EgressMode            string `json:"egress_mode,omitempty" bson:"egress_mode,omitempty" yaml:"egress_mode,omitempty"`
}

// RuntimeConfig holds container runtime settings.
type RuntimeConfig struct {
	Image          string            `json:"image,omitempty" bson:"image,omitempty" yaml:"image,omitempty"`
	Entrypoint     []string          `json:"entrypoint,omitempty" bson:"entrypoint,omitempty" yaml:"entrypoint,omitempty"`
	Port           int               `json:"port,omitempty" bson:"port,omitempty" yaml:"port,omitempty"`
	Env            map[string]string `json:"env,omitempty" bson:"env,omitempty" yaml:"env,omitempty"`
	DepsFile       string            `json:"deps_file,omitempty" bson:"deps_file,omitempty" yaml:"deps_file,omitempty"`
	DepsInstallCmd string            `json:"deps_install_cmd,omitempty" bson:"deps_install_cmd,omitempty" yaml:"deps_install_cmd,omitempty"`
	HealthCheck    string            `json:"health_check,omitempty" bson:"health_check,omitempty" yaml:"health_check,omitempty"`
	User           string            `json:"user,omitempty" bson:"user,omitempty" yaml:"user,omitempty"`
}

// Identity holds agent identity and authentication metadata.
type Identity struct {
	Issuer   string   `json:"issuer,omitempty" bson:"issuer,omitempty" yaml:"issuer,omitempty"`
	Audience string   `json:"audience,omitempty" bson:"audience,omitempty" yaml:"audience,omitempty"`
	Scopes   []string `json:"scopes,omitempty" bson:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// A2AConfig holds Agent-to-Agent protocol settings.
type A2AConfig struct {
	Endpoint     string          `json:"endpoint,omitempty" bson:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Skills       []A2ASkill      `json:"skills,omitempty" bson:"skills,omitempty" yaml:"skills,omitempty"`
	Capabilities *A2ACapabilities `json:"capabilities,omitempty" bson:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// A2ASkill describes a single skill exposed over the A2A protocol.
type A2ASkill struct {
	ID          string   `json:"id" bson:"id" yaml:"id"`
	Name        string   `json:"name" bson:"name" yaml:"name"`
	Description string   `json:"description,omitempty" bson:"description,omitempty" yaml:"description,omitempty"`
	Tags        []string `json:"tags,omitempty" bson:"tags,omitempty" yaml:"tags,omitempty"`
}

// A2ACapabilities declares optional A2A features supported by the agent.
type A2ACapabilities struct {
	Streaming              bool `json:"streaming,omitempty" bson:"streaming,omitempty" yaml:"streaming,omitempty"`
	PushNotifications      bool `json:"push_notifications,omitempty" bson:"push_notifications,omitempty" yaml:"push_notifications,omitempty"`
	StateTransitionHistory bool `json:"state_transition_history,omitempty" bson:"state_transition_history,omitempty" yaml:"state_transition_history,omitempty"`
}

// AgentRequirements declares runtime requirements for the agent.
type AgentRequirements struct {
	Bins        []string `json:"bins,omitempty" bson:"bins,omitempty" yaml:"bins,omitempty"`
	EnvRequired []string `json:"env_required,omitempty" bson:"env_required,omitempty" yaml:"env_required,omitempty"`
	EnvOptional []string `json:"env_optional,omitempty" bson:"env_optional,omitempty" yaml:"env_optional,omitempty"`
}

// ModelConfig holds LLM/model configuration for the agent.
type ModelConfig struct {
	Provider   string         `json:"provider,omitempty" bson:"provider,omitempty" yaml:"provider,omitempty"`
	Name       string         `json:"name,omitempty" bson:"name,omitempty" yaml:"name,omitempty"`
	Version    string         `json:"version,omitempty" bson:"version,omitempty" yaml:"version,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty" bson:"parameters,omitempty" yaml:"parameters,omitempty"`
}
