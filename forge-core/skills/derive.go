package skills

import "sort"

// DerivedCLIConfig holds auto-derived cli_execute configuration from skill requirements.
type DerivedCLIConfig struct {
	AllowedBinaries []string
	EnvPassthrough  []string
}

// DeriveCLIConfig produces cli_execute configuration from aggregated requirements.
// AllowedBinaries = reqs.Bins, EnvPassthrough = union of all env vars.
func DeriveCLIConfig(reqs *AggregatedRequirements) *DerivedCLIConfig {
	if reqs == nil {
		return &DerivedCLIConfig{}
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

	return &DerivedCLIConfig{
		AllowedBinaries: reqs.Bins, // already sorted from AggregateRequirements
		EnvPassthrough:  envPass,
	}
}

// MergeCLIConfig merges derived config with explicit forge.yaml config.
// Explicit non-nil slices override derived values entirely.
// Nil/empty explicit slices allow derived values through.
func MergeCLIConfig(explicit, derived *DerivedCLIConfig) *DerivedCLIConfig {
	if derived == nil {
		return explicit
	}
	if explicit == nil {
		return derived
	}

	merged := &DerivedCLIConfig{}

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

	return merged
}
