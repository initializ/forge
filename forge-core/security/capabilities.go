package security

// DefaultCapabilityBundles maps capability names to their required domain sets.
var DefaultCapabilityBundles = map[string][]string{
	"slack":    {"slack.com", "hooks.slack.com", "api.slack.com"},
	"telegram": {"api.telegram.org"},
}

// ResolveCapabilities returns a deduplicated list of domains for the given capability names.
func ResolveCapabilities(capabilities []string) []string {
	seen := make(map[string]bool)
	var domains []string
	for _, cap := range capabilities {
		for _, d := range DefaultCapabilityBundles[cap] {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	return domains
}
