package validate

import (
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/agentspec"
)

// supportedForgeVersions lists forge_version values accepted by Command.
var supportedForgeVersions = map[string]bool{
	"1.0": true,
	"1.1": true,
}

var supportedToolInterfaceVersions = map[string]bool{"1.0": true}
var supportedSkillsSpecVersions = map[string]bool{"agentskills-v1": true}
var supportedForgeSkillsExtVersions = map[string]bool{"1.0": true}
var knownToolCategories = map[string]bool{"builtin": true, "adapter": true, "dev": true, "custom": true}

// ValidateCommandCompat checks an AgentSpec against Command platform import
// requirements. It returns errors for hard incompatibilities and warnings for
// missing optional fields.
func ValidateCommandCompat(spec *agentspec.AgentSpec) *ValidationResult {
	r := &ValidationResult{}

	// Required fields
	if spec.AgentID == "" {
		r.Errors = append(r.Errors, "agent_id is required for Command import")
	} else if !agentIDPattern.MatchString(spec.AgentID) {
		r.Errors = append(r.Errors, fmt.Sprintf("agent_id %q must match ^[a-z0-9-]+$ for Command import", spec.AgentID))
	}

	if spec.Name == "" {
		r.Errors = append(r.Errors, "name is required for Command import")
	}

	if spec.Version == "" {
		r.Errors = append(r.Errors, "version is required for Command import")
	}

	if spec.ForgeVersion == "" {
		r.Errors = append(r.Errors, "forge_version is required for Command import")
	} else if !supportedForgeVersions[spec.ForgeVersion] {
		r.Errors = append(r.Errors, fmt.Sprintf("forge_version %q is not supported by Command (supported: 1.0, 1.1)", spec.ForgeVersion))
	}

	// Runtime checks
	if spec.Runtime == nil {
		r.Errors = append(r.Errors, "runtime is required for Command import")
	} else if spec.Runtime.Image == "" {
		r.Errors = append(r.Errors, "runtime.image is required for Command import")
	}

	// Tool input_schema validation
	for i, tool := range spec.Tools {
		if len(tool.InputSchema) > 0 {
			var js json.RawMessage
			if err := json.Unmarshal(tool.InputSchema, &js); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("tools[%d] (%s): input_schema is not valid JSON", i, tool.Name))
			}
		}
	}

	// Version field checks
	if spec.ToolInterfaceVersion != "" && !supportedToolInterfaceVersions[spec.ToolInterfaceVersion] {
		r.Errors = append(r.Errors, fmt.Sprintf("tool_interface_version %q not supported by Command", spec.ToolInterfaceVersion))
	}
	if spec.SkillsSpecVersion != "" && !supportedSkillsSpecVersions[spec.SkillsSpecVersion] {
		r.Errors = append(r.Errors, fmt.Sprintf("skills_spec_version %q not recognized", spec.SkillsSpecVersion))
	}
	if spec.ForgeSkillsExtVersion != "" && !supportedForgeSkillsExtVersions[spec.ForgeSkillsExtVersion] {
		r.Errors = append(r.Errors, fmt.Sprintf("forge_skills_ext_version %q not recognized", spec.ForgeSkillsExtVersion))
	}

	// Tool category validation
	for i, tool := range spec.Tools {
		if tool.Category != "" && !knownToolCategories[tool.Category] {
			r.Warnings = append(r.Warnings, fmt.Sprintf("tools[%d] (%s): unknown category %q", i, tool.Name, tool.Category))
		}
		// Validate skill_origin references a valid skill
		if tool.SkillOrigin != "" && spec.A2A != nil {
			found := false
			for _, s := range spec.A2A.Skills {
				if s.ID == tool.SkillOrigin {
					found = true
					break
				}
			}
			if !found {
				r.Warnings = append(r.Warnings, fmt.Sprintf("tools[%d] (%s): skill_origin %q not found in a2a.skills", i, tool.Name, tool.SkillOrigin))
			}
		}
	}

	// Skill validation
	if spec.A2A != nil {
		for i, skill := range spec.A2A.Skills {
			if !agentIDPattern.MatchString(skill.ID) {
				r.Errors = append(r.Errors, fmt.Sprintf("a2a.skills[%d].id %q must match ^[a-z0-9-]+$", i, skill.ID))
			}
			if skill.Description == "" {
				r.Warnings = append(r.Warnings, fmt.Sprintf("a2a.skills[%d] (%s): missing description", i, skill.Name))
			}
		}
		if len(spec.A2A.Skills) > 20 {
			r.Warnings = append(r.Warnings, fmt.Sprintf("a2a.skills has %d entries; >20 may cause context window issues in Command", len(spec.A2A.Skills)))
		}
	}

	// Egress validation
	if spec.EgressMode == "allowlist" && spec.EgressProfile == "" {
		r.Warnings = append(r.Warnings, "egress_mode is 'allowlist' but egress_profile is empty")
	}

	// Warn if tool_interface_version not set
	if spec.ToolInterfaceVersion == "" {
		r.Warnings = append(r.Warnings, "tool_interface_version not set; Command may use default behavior")
	}

	// Warnings for optional but recommended fields
	if spec.PolicyScaffold != nil {
		for _, g := range spec.PolicyScaffold.Guardrails {
			if !knownGuardrailTypes[g.Type] {
				r.Warnings = append(r.Warnings, fmt.Sprintf("unknown guardrail type %q may not be supported by Command", g.Type))
			}
		}
	}

	if spec.A2A == nil {
		r.Warnings = append(r.Warnings, "a2a config is not set; agent will have no A2A capabilities in Command")
	} else if spec.A2A.Capabilities == nil {
		r.Warnings = append(r.Warnings, "a2a.capabilities is not set; agent capabilities will default to false in Command")
	}

	if spec.Model == nil {
		r.Warnings = append(r.Warnings, "model config is not set; no model will be configured in Command")
	} else if spec.Model.Provider == "" {
		r.Warnings = append(r.Warnings, "model.provider is empty; model configuration may be incomplete in Command")
	}

	return r
}
