package agentspec

import "encoding/json"

// ToolSpec defines a tool available to an agent.
type ToolSpec struct {
	Name        string          `json:"name" bson:"name" yaml:"name"`
	Description string          `json:"description,omitempty" bson:"description,omitempty" yaml:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty" bson:"input_schema,omitempty" yaml:"input_schema,omitempty"`
	Permissions []string        `json:"permissions,omitempty" bson:"permissions,omitempty" yaml:"permissions,omitempty"`
	ForgeMeta   *ForgeToolMeta  `json:"forge_meta,omitempty" bson:"forge_meta,omitempty" yaml:"forge_meta,omitempty"`
	Category    string          `json:"category,omitempty" bson:"category,omitempty" yaml:"category,omitempty"`
	SkillOrigin string          `json:"skill_origin,omitempty" bson:"skill_origin,omitempty" yaml:"skill_origin,omitempty"`
}

// ForgeToolMeta carries Forge-specific metadata for a tool.
type ForgeToolMeta struct {
	AllowedTables    []string `json:"allowed_tables,omitempty" bson:"allowed_tables,omitempty" yaml:"allowed_tables,omitempty"`
	AllowedEndpoints []string `json:"allowed_endpoints,omitempty" bson:"allowed_endpoints,omitempty" yaml:"allowed_endpoints,omitempty"`
	NetworkScopes    []string `json:"network_scopes,omitempty" bson:"network_scopes,omitempty" yaml:"network_scopes,omitempty"`
	AllowedBinaries  []string `json:"allowed_binaries,omitempty" bson:"allowed_binaries,omitempty" yaml:"allowed_binaries,omitempty"`
	EnvPassthrough   []string `json:"env_passthrough,omitempty" bson:"env_passthrough,omitempty" yaml:"env_passthrough,omitempty"`
}
