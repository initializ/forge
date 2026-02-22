package validate

import (
	"fmt"

	"github.com/initializ/forge/forge-core/agentspec"
)

// AgentDefinition represents what Command's import API produces from an AgentSpec.
type AgentDefinition struct {
	Slug                 string            `json:"slug"`
	DisplayName          string            `json:"display_name"`
	Description          string            `json:"description,omitempty"`
	ContainerImage       string            `json:"container_image,omitempty"`
	Port                 int               `json:"port,omitempty"`
	EnvVars              map[string]string `json:"env_vars,omitempty"`
	Tools                []ImportedTool    `json:"tools,omitempty"`
	ModelProvider        string            `json:"model_provider,omitempty"`
	ModelName            string            `json:"model_name,omitempty"`
	Capabilities         *A2ACaps          `json:"capabilities,omitempty"`
	Guardrails           []string          `json:"guardrails,omitempty"`
	ToolInterfaceVersion string            `json:"tool_interface_version,omitempty"`
	SkillsSpecVersion    string            `json:"skills_spec_version,omitempty"`
	EgressProfile        string            `json:"egress_profile,omitempty"`
	EgressMode           string            `json:"egress_mode,omitempty"`
	Skills               []string          `json:"skills,omitempty"`
}

// ImportedTool represents a tool as imported by Command.
type ImportedTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	HasSchema   bool   `json:"has_schema"`
	Category    string `json:"category,omitempty"`
	SkillOrigin string `json:"skill_origin,omitempty"`
}

// A2ACaps represents agent capabilities as understood by Command.
type A2ACaps struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"push_notifications"`
}

// ImportSimResult holds the simulated import output.
type ImportSimResult struct {
	Definition     *AgentDefinition `json:"agent_definition"`
	ImportWarnings []string         `json:"import_warnings"`
}

// SimulateImport simulates what Command's POST /api/v1/agents/import would
// produce from the given AgentSpec.
func SimulateImport(spec *agentspec.AgentSpec) *ImportSimResult {
	result := &ImportSimResult{
		Definition: &AgentDefinition{
			Slug:        spec.AgentID,
			DisplayName: spec.Name,
			Description: spec.Description,
		},
	}

	// Runtime mapping
	if spec.Runtime != nil {
		result.Definition.ContainerImage = spec.Runtime.Image
		result.Definition.Port = spec.Runtime.Port
		if len(spec.Runtime.Env) > 0 {
			result.Definition.EnvVars = spec.Runtime.Env
		}
	} else {
		result.ImportWarnings = append(result.ImportWarnings, "no runtime config; container image will not be set")
	}

	// Version fields mapping
	result.Definition.ToolInterfaceVersion = spec.ToolInterfaceVersion
	result.Definition.SkillsSpecVersion = spec.SkillsSpecVersion
	result.Definition.EgressProfile = spec.EgressProfile
	result.Definition.EgressMode = spec.EgressMode

	// Skills mapping
	if spec.A2A != nil {
		for _, skill := range spec.A2A.Skills {
			result.Definition.Skills = append(result.Definition.Skills, skill.ID)
		}
	}

	// Tools mapping
	for i, tool := range spec.Tools {
		result.Definition.Tools = append(result.Definition.Tools, ImportedTool{
			Name:        tool.Name,
			Description: tool.Description,
			HasSchema:   len(tool.InputSchema) > 0,
			Category:    tool.Category,
			SkillOrigin: tool.SkillOrigin,
		})
		if tool.Category != "" {
			result.ImportWarnings = append(result.ImportWarnings, fmt.Sprintf("tools[%d].category '%s' mapped — verify in Security step", i, tool.Category))
		}
		if tool.SkillOrigin != "" {
			result.ImportWarnings = append(result.ImportWarnings, fmt.Sprintf("tools[%d].skill_origin '%s' — skill-bound tool, ensure skill context available", i, tool.SkillOrigin))
		}
	}

	// Model mapping
	if spec.Model != nil {
		result.Definition.ModelProvider = spec.Model.Provider
		result.Definition.ModelName = spec.Model.Name
	} else {
		result.ImportWarnings = append(result.ImportWarnings, "no model config; model will not be configured")
	}

	// Capabilities mapping
	if spec.A2A != nil && spec.A2A.Capabilities != nil {
		result.Definition.Capabilities = &A2ACaps{
			Streaming:         spec.A2A.Capabilities.Streaming,
			PushNotifications: spec.A2A.Capabilities.PushNotifications,
		}
	} else {
		result.ImportWarnings = append(result.ImportWarnings, "no A2A capabilities; defaults will be used")
	}

	// Guardrails mapping
	if spec.PolicyScaffold != nil {
		for _, g := range spec.PolicyScaffold.Guardrails {
			result.Definition.Guardrails = append(result.Definition.Guardrails, g.Type)
			if !knownGuardrailTypes[g.Type] {
				result.ImportWarnings = append(result.ImportWarnings,
					fmt.Sprintf("unknown guardrail type %q may be ignored by Command", g.Type))
			}
		}
	}

	// Skills warnings
	if spec.A2A != nil && len(spec.A2A.Skills) > 0 {
		result.ImportWarnings = append(result.ImportWarnings, fmt.Sprintf("a2a.skills contains %d skills — verify skill activation behavior in test sandbox", len(spec.A2A.Skills)))
	}

	// Egress warnings
	if spec.EgressProfile != "" {
		result.ImportWarnings = append(result.ImportWarnings, fmt.Sprintf("security.egress.profile '%s' is Forge baseline — Command will apply org-level policy on top", spec.EgressProfile))
	}
	if spec.EgressMode != "" {
		result.ImportWarnings = append(result.ImportWarnings, "network_policy provided as baseline — Command will generate its own NetworkPolicy per cluster policy")
	}

	// Version field warnings
	if spec.ToolInterfaceVersion != "" {
		result.ImportWarnings = append(result.ImportWarnings, fmt.Sprintf("tool_interface_version '%s' — compatible with Command >=1.0.0", spec.ToolInterfaceVersion))
	}

	return result
}
