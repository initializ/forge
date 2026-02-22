package skills

import (
	"fmt"
	"os/exec"
)

// EnvSource describes where an environment variable was found.
type EnvSource string

const (
	EnvSourceOS      EnvSource = "environment"
	EnvSourceDotEnv  EnvSource = "dotenv"
	EnvSourceConfig  EnvSource = "config"
	EnvSourceMissing EnvSource = "missing"
)

// ValidationDiagnostic represents a single validation finding.
type ValidationDiagnostic struct {
	Level   string // "error", "warning", "info"
	Message string
	Var     string
}

// EnvResolver checks env var availability across multiple sources.
type EnvResolver struct {
	osEnv  map[string]string
	dotEnv map[string]string
	cfgEnv map[string]string
}

// NewEnvResolver creates an EnvResolver with the given env sources.
func NewEnvResolver(osEnv, dotEnv, cfgEnv map[string]string) *EnvResolver {
	if osEnv == nil {
		osEnv = map[string]string{}
	}
	if dotEnv == nil {
		dotEnv = map[string]string{}
	}
	if cfgEnv == nil {
		cfgEnv = map[string]string{}
	}
	return &EnvResolver{osEnv: osEnv, dotEnv: dotEnv, cfgEnv: cfgEnv}
}

// Resolve checks all requirements against available env sources.
// Returns diagnostics: error for missing required/one_of, warning for missing optional.
func (r *EnvResolver) Resolve(reqs *AggregatedRequirements) []ValidationDiagnostic {
	if reqs == nil {
		return nil
	}
	var diags []ValidationDiagnostic

	// Check required vars
	for _, v := range reqs.EnvRequired {
		src := r.lookup(v)
		if src == EnvSourceMissing {
			diags = append(diags, ValidationDiagnostic{
				Level:   "error",
				Message: fmt.Sprintf("required env var %s is not set", v),
				Var:     v,
			})
		}
	}

	// Check one_of groups
	for _, group := range reqs.EnvOneOf {
		found := false
		for _, v := range group {
			if r.lookup(v) != EnvSourceMissing {
				found = true
				break
			}
		}
		if !found {
			diags = append(diags, ValidationDiagnostic{
				Level:   "error",
				Message: fmt.Sprintf("at least one of [%s] must be set", joinVars(group)),
				Var:     group[0],
			})
		}
	}

	// Check optional vars
	for _, v := range reqs.EnvOptional {
		src := r.lookup(v)
		if src == EnvSourceMissing {
			diags = append(diags, ValidationDiagnostic{
				Level:   "warning",
				Message: fmt.Sprintf("optional env var %s is not set", v),
				Var:     v,
			})
		}
	}

	return diags
}

// lookup checks for a var across all sources in priority order.
func (r *EnvResolver) lookup(key string) EnvSource {
	if _, ok := r.osEnv[key]; ok {
		return EnvSourceOS
	}
	if _, ok := r.dotEnv[key]; ok {
		return EnvSourceDotEnv
	}
	if _, ok := r.cfgEnv[key]; ok {
		return EnvSourceConfig
	}
	return EnvSourceMissing
}

// BinDiagnostics checks binary availability via exec.LookPath.
func BinDiagnostics(bins []string) []ValidationDiagnostic {
	var diags []ValidationDiagnostic
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			diags = append(diags, ValidationDiagnostic{
				Level:   "warning",
				Message: fmt.Sprintf("binary %q not found in PATH", bin),
				Var:     bin,
			})
		}
	}
	return diags
}

func joinVars(vars []string) string {
	result := ""
	for i, v := range vars {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}
