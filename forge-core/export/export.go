// Package export provides pure logic for building Command platform export envelopes.
package export

import (
	"encoding/json"
	"time"

	"github.com/initializ/forge/forge-core/agentspec"
)

// ExportMeta contains metadata added to the export envelope.
type ExportMeta struct {
	ExportedAt                string         `json:"exported_at"`
	ForgeCLIVersion           string         `json:"forge_cli_version"`
	CompatibleCommandVersions []string       `json:"compatible_command_versions"`
	ToolCategories            map[string]int `json:"tool_categories"`
	SkillsCount               int            `json:"skills_count"`
	EgressProfile             string         `json:"egress_profile,omitempty"`
}

// SecurityBlock represents the security section of the export envelope.
type SecurityBlock struct {
	Egress EgressBlock `json:"egress"`
}

// EgressBlock represents egress settings in the security block.
type EgressBlock struct {
	Profile        string   `json:"profile"`
	Mode           string   `json:"mode"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// NetworkPolicyBlock represents the network_policy section of the export envelope.
type NetworkPolicyBlock struct {
	DefaultEgress  string   `json:"default_egress"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// ExportValidation holds warnings generated during export validation.
type ExportValidation struct {
	Warnings []string
	Errors   []string
}

// ValidateForExport checks an AgentSpec for export-specific issues.
// devMode=true allows dev-category tools.
func ValidateForExport(spec *agentspec.AgentSpec, devMode bool) *ExportValidation {
	v := &ExportValidation{}

	for _, tool := range spec.Tools {
		if tool.Category == "dev" && !devMode {
			v.Errors = append(v.Errors, "tool "+tool.Name+" has category \"dev\"; use --dev flag to include dev-category tools in export")
		}
	}
	if spec.ToolInterfaceVersion == "" {
		v.Warnings = append(v.Warnings, "tool_interface_version is empty")
	}
	for _, tool := range spec.Tools {
		if len(tool.InputSchema) == 0 {
			v.Warnings = append(v.Warnings, "tool "+tool.Name+" has empty input_schema (prompt-only tools won't map cleanly)")
		}
	}
	if spec.EgressMode == "dev-open" {
		v.Warnings = append(v.Warnings, "egress_mode is \"dev-open\" (not recommended for export)")
	}
	if spec.EgressProfile == "" && spec.EgressMode == "allowlist" {
		v.Warnings = append(v.Warnings, "egress_profile is empty and egress_mode is \"allowlist\" (agent may not reach LLM)")
	}

	return v
}

// BuildEnvelope constructs the export envelope from an AgentSpec.
// allowlistDomains is the list of domains from egress_allowlist.json (can be nil).
// cliVersion is the forge CLI version string.
func BuildEnvelope(spec *agentspec.AgentSpec, allowlistDomains []string, cliVersion string) (map[string]any, error) {
	// Marshal spec to JSON then unmarshal to map for envelope construction
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	var envelope map[string]any
	if err := json.Unmarshal(specBytes, &envelope); err != nil {
		return nil, err
	}

	// Build tool category counts
	toolCategories := map[string]int{}
	for _, tool := range spec.Tools {
		if tool.Category != "" {
			toolCategories[tool.Category]++
		}
	}

	skillsCount := 0
	if spec.A2A != nil {
		skillsCount = len(spec.A2A.Skills)
	}

	meta := map[string]any{
		"exported_at":                 time.Now().UTC().Format(time.RFC3339),
		"forge_cli_version":           cliVersion,
		"compatible_command_versions": []string{">=1.0.0"},
		"tool_categories":             toolCategories,
		"skills_count":                skillsCount,
	}
	if spec.EgressProfile != "" {
		meta["egress_profile"] = spec.EgressProfile
	}
	envelope["_forge_export_meta"] = meta

	// Add security block
	if spec.EgressProfile != "" || spec.EgressMode != "" {
		security := map[string]any{
			"egress": map[string]any{
				"profile": spec.EgressProfile,
				"mode":    spec.EgressMode,
			},
		}
		if len(allowlistDomains) > 0 {
			security["egress"].(map[string]any)["allowed_domains"] = allowlistDomains
		}
		envelope["security"] = security
	}

	// Add network_policy block
	if spec.EgressMode != "" {
		np := map[string]any{
			"default_egress": "deny",
		}
		if spec.EgressMode == "allowlist" && len(allowlistDomains) > 0 {
			np["allowed_domains"] = allowlistDomains
		}
		envelope["network_policy"] = np
	}

	return envelope, nil
}

// KnownDevTools lists tool names that should be filtered in production builds.
var KnownDevTools = map[string]bool{
	"local_shell":        true,
	"local_file_browser": true,
	"debug_console":      true,
	"test_runner":        true,
}

// ValidateProdConfig checks that a config is valid for production builds.
func ValidateProdConfig(egressMode string, toolNames []string) *ExportValidation {
	v := &ExportValidation{}
	if egressMode == "dev-open" {
		v.Errors = append(v.Errors, "egress mode 'dev-open' is not allowed in production builds")
	}
	for _, name := range toolNames {
		if KnownDevTools[name] {
			v.Errors = append(v.Errors, "dev tool "+name+" is not allowed in production builds")
		}
	}
	return v
}
