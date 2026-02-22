// Package security provides egress security resolution for containerized agents.
package security

// EgressProfile controls the overall security posture.
type EgressProfile string

const (
	ProfileStrict     EgressProfile = "strict"
	ProfileStandard   EgressProfile = "standard"
	ProfilePermissive EgressProfile = "permissive"
)

// EgressMode controls egress behavior.
type EgressMode string

const (
	ModeDenyAll   EgressMode = "deny-all"
	ModeAllowlist EgressMode = "allowlist"
	ModeDevOpen   EgressMode = "dev-open"
)

// EgressConfig holds the resolved egress configuration.
type EgressConfig struct {
	Profile        EgressProfile `json:"profile"`
	Mode           EgressMode    `json:"mode"`
	AllowedDomains []string      `json:"allowed_domains,omitempty"` // explicit user domains
	ToolDomains    []string      `json:"tool_domains,omitempty"`    // inferred from tools
	AllDomains     []string      `json:"all_domains,omitempty"`     // deduplicated union
}
