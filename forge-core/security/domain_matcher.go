package security

import (
	"net"
	"strings"
)

// DomainMatcher checks hostnames against an exact+wildcard allowlist.
// It is used by both EgressEnforcer (Go HTTP) and EgressProxy (subprocess HTTP).
type DomainMatcher struct {
	mode          EgressMode
	allowedHosts  map[string]bool
	wildcardHosts []string // suffix patterns: ".github.com"
}

// NewDomainMatcher creates a new DomainMatcher for the given mode and domain list.
// Domains may include wildcard prefixes (e.g. "*.github.com") which match any subdomain.
func NewDomainMatcher(mode EgressMode, domains []string) *DomainMatcher {
	allowed := make(map[string]bool, len(domains))
	var wildcards []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if strings.HasPrefix(d, "*.") {
			// *.github.com -> suffix ".github.com"
			wildcards = append(wildcards, d[1:]) // ".github.com"
		} else {
			allowed[d] = true
		}
	}

	return &DomainMatcher{
		mode:          mode,
		allowedHosts:  allowed,
		wildcardHosts: wildcards,
	}
}

// IsAllowed checks if a host is permitted under the current mode.
// Exact match is checked first, then wildcard suffix, then mode fallback.
func (m *DomainMatcher) IsAllowed(host string) bool {
	host = strings.ToLower(host)
	switch m.mode {
	case ModeDevOpen:
		return true
	case ModeDenyAll:
		return false
	case ModeAllowlist:
		// Exact match
		if m.allowedHosts[host] {
			return true
		}
		// Wildcard suffix match: *.github.com matches api.github.com
		for _, suffix := range m.wildcardHosts {
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// Mode returns the egress mode of this matcher.
func (m *DomainMatcher) Mode() EgressMode {
	return m.mode
}

// IsLocalhost returns true for loopback addresses.
func IsLocalhost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
