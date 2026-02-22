package security

import (
	"fmt"
	"sort"
)

// DefaultProfile returns the default egress profile.
func DefaultProfile() EgressProfile { return ProfileStrict }

// DefaultMode returns the default egress mode.
func DefaultMode() EgressMode { return ModeDenyAll }

// Resolve builds an EgressConfig from profile, mode, explicit domains, tool names, and capabilities.
func Resolve(profile, mode string, explicitDomains, toolNames, capabilities []string) (*EgressConfig, error) {
	p := EgressProfile(profile)
	if p == "" {
		p = DefaultProfile()
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}

	m := EgressMode(mode)
	if m == "" {
		m = DefaultMode()
	}
	if err := validateMode(m); err != nil {
		return nil, err
	}

	cfg := &EgressConfig{
		Profile: p,
		Mode:    m,
	}

	switch m {
	case ModeDenyAll:
		// No domains allowed
		return cfg, nil
	case ModeDevOpen:
		// No restrictions
		return cfg, nil
	case ModeAllowlist:
		cfg.AllowedDomains = explicitDomains
		cfg.ToolDomains = InferToolDomains(toolNames)
		capDomains := ResolveCapabilities(capabilities)
		all := append([]string{}, explicitDomains...)
		all = append(all, cfg.ToolDomains...)
		all = append(all, capDomains...)
		cfg.AllDomains = dedup(all)
		return cfg, nil
	}

	return cfg, nil
}

func validateProfile(p EgressProfile) error {
	switch p {
	case ProfileStrict, ProfileStandard, ProfilePermissive:
		return nil
	default:
		return fmt.Errorf("invalid egress profile %q: must be strict, standard, or permissive", p)
	}
}

func validateMode(m EgressMode) error {
	switch m {
	case ModeDenyAll, ModeAllowlist, ModeDevOpen:
		return nil
	default:
		return fmt.Errorf("invalid egress mode %q: must be deny-all, allowlist, or dev-open", m)
	}
}

func dedup(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}
