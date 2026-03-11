package requirements

import (
	"sort"

	"github.com/initializ/forge/forge-skills/contract"
)

// deniedShells lists shell interpreters that must never appear in the
// cli_execute allowlist. Shells bypass the no-shell exec.Command security
// model, so they are excluded even when a skill declares them in requires.bins.
var deniedShells = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true,
	"ksh": true, "csh": true, "tcsh": true, "fish": true,
}

// DeriveCLIConfig produces cli_execute configuration from aggregated requirements.
// AllowedBinaries = reqs.Bins (minus shell interpreters), EnvPassthrough = union of all env vars.
func DeriveCLIConfig(reqs *contract.AggregatedRequirements) *contract.DerivedCLIConfig {
	if reqs == nil {
		return &contract.DerivedCLIConfig{}
	}

	envSet := make(map[string]bool)
	for _, v := range reqs.EnvRequired {
		envSet[v] = true
	}
	for _, group := range reqs.EnvOneOf {
		for _, v := range group {
			envSet[v] = true
		}
	}
	for _, v := range reqs.EnvOptional {
		envSet[v] = true
	}

	var envPass []string
	if len(envSet) > 0 {
		envPass = make([]string, 0, len(envSet))
		for k := range envSet {
			envPass = append(envPass, k)
		}
		sort.Strings(envPass)
	}

	// Filter out shell interpreters — they are blocked by cli_execute anyway
	// but including them confuses the LLM (they appear in the enum/description
	// yet always fail, causing the LLM to attempt shell commands via cli_execute).
	var bins []string
	for _, b := range reqs.Bins {
		if !deniedShells[b] {
			bins = append(bins, b)
		}
	}

	return &contract.DerivedCLIConfig{
		AllowedBinaries: bins,
		EnvPassthrough:  envPass,
		TimeoutHint:     reqs.MaxTimeoutHint,
		DeniedTools:     reqs.DeniedTools,    // already sorted from AggregateRequirements
		EgressDomains:   reqs.EgressDomains,  // already sorted from AggregateRequirements
		WorkflowPhases:  reqs.WorkflowPhases, // already sorted from AggregateRequirements
	}
}

// MergeCLIConfig merges derived config with explicit forge.yaml config.
// Explicit non-nil slices override derived values entirely.
// Nil/empty explicit slices allow derived values through.
func MergeCLIConfig(explicit, derived *contract.DerivedCLIConfig) *contract.DerivedCLIConfig {
	if derived == nil {
		return explicit
	}
	if explicit == nil {
		return derived
	}

	merged := &contract.DerivedCLIConfig{}

	if len(explicit.AllowedBinaries) > 0 {
		merged.AllowedBinaries = explicit.AllowedBinaries
	} else {
		merged.AllowedBinaries = derived.AllowedBinaries
	}

	if len(explicit.EnvPassthrough) > 0 {
		merged.EnvPassthrough = explicit.EnvPassthrough
	} else {
		merged.EnvPassthrough = derived.EnvPassthrough
	}

	// Use the larger timeout hint
	if explicit.TimeoutHint > derived.TimeoutHint {
		merged.TimeoutHint = explicit.TimeoutHint
	} else {
		merged.TimeoutHint = derived.TimeoutHint
	}

	return merged
}
