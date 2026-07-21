package security

import (
	"fmt"
	"sort"
)

// DefaultProfile returns the default egress profile.
func DefaultProfile() EgressProfile { return ProfileStrict }

// DefaultMode returns the default egress mode.
func DefaultMode() EgressMode { return ModeDenyAll }

// Resolve builds an EgressConfig from profile, mode, explicit domains, tool
// names, capabilities, and (optionally) the raw allowed_private_cidrs and
// allowed_tcp lists from forge.yaml. All list entries are validated here so
// a bad string fails at config-load time, not at first-dial. Pass nil for
// pre-CIDR / pre-TCP defaults.
func Resolve(profile, mode string, explicitDomains, toolNames, capabilities, allowedPrivateCIDRs, allowedTCP []string) (*EgressConfig, error) {
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

	// Validate CIDRs early. We throw away the parsed value here; callers
	// re-parse via ParsePrivateCIDRs when they need []*net.IPNet. This keeps
	// EgressConfig JSON-serializable (net.IPNet is not) while still failing
	// closed on invalid config.
	if _, err := ParsePrivateCIDRs(allowedPrivateCIDRs); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	// Same posture for raw-TCP entries — invalid host:port trips at
	// config-load, not at first SOCKS5 dial.
	if _, err := NewTCPMatcher(allowedTCP); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}

	cfg := &EgressConfig{
		Profile:             p,
		Mode:                m,
		AllowedPrivateCIDRs: allowedPrivateCIDRs,
		AllowedTCP:          allowedTCP,
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
