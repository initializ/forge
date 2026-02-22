package validate

import (
	"fmt"
	"regexp"

	"github.com/initializ/forge/forge-core/types"
)

var (
	agentIDPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	semverPattern  = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

	knownFrameworks     = map[string]bool{"crewai": true, "langchain": true, "custom": true}
	knownEgressProfiles = map[string]bool{"strict": true, "standard": true, "permissive": true}
	knownEgressModes    = map[string]bool{"deny-all": true, "allowlist": true, "dev-open": true}
	knownGuardrailTypes = map[string]bool{
		"no_pii":                   true,
		"jailbreak_protection":     true,
		"tool_scope_enforcement":   true,
		"output_format_validation": true,
		"content_filter":           true,
	}
)

// ValidationResult holds errors and warnings from config validation.
type ValidationResult struct {
	Errors   []string
	Warnings []string
}

// IsValid returns true if there are no validation errors.
func (r *ValidationResult) IsValid() bool {
	return len(r.Errors) == 0
}

// ValidateForgeConfig checks a ForgeConfig for errors and warnings.
func ValidateForgeConfig(cfg *types.ForgeConfig) *ValidationResult {
	r := &ValidationResult{}

	if cfg.AgentID == "" {
		r.Errors = append(r.Errors, "agent_id is required")
	} else if !agentIDPattern.MatchString(cfg.AgentID) {
		r.Errors = append(r.Errors, fmt.Sprintf("agent_id %q must match ^[a-z0-9-]+$", cfg.AgentID))
	}

	if cfg.Version == "" {
		r.Errors = append(r.Errors, "version is required")
	} else if !semverPattern.MatchString(cfg.Version) {
		r.Errors = append(r.Errors, fmt.Sprintf("version %q is not valid semver", cfg.Version))
	}

	if cfg.Entrypoint == "" {
		r.Errors = append(r.Errors, "entrypoint is required")
	}

	for i, t := range cfg.Tools {
		if t.Name == "" {
			r.Errors = append(r.Errors, fmt.Sprintf("tools[%d]: name is required", i))
		}
	}

	if cfg.Model.Provider != "" && cfg.Model.Name == "" {
		r.Warnings = append(r.Warnings, "model.provider is set but model.name is empty")
	}

	if cfg.Framework != "" && !knownFrameworks[cfg.Framework] {
		r.Warnings = append(r.Warnings, fmt.Sprintf("unknown framework %q (known: crewai, langchain, custom)", cfg.Framework))
	}

	// Validate egress config
	if cfg.Egress.Profile != "" && !knownEgressProfiles[cfg.Egress.Profile] {
		r.Errors = append(r.Errors, fmt.Sprintf("egress.profile %q must be one of: strict, standard, permissive", cfg.Egress.Profile))
	}
	if cfg.Egress.Mode != "" && !knownEgressModes[cfg.Egress.Mode] {
		r.Errors = append(r.Errors, fmt.Sprintf("egress.mode %q must be one of: deny-all, allowlist, dev-open", cfg.Egress.Mode))
	}
	if cfg.Egress.Mode == "dev-open" {
		r.Warnings = append(r.Warnings, "egress mode 'dev-open' is not recommended for production")
	}

	return r
}
