package security

// DefaultCapabilityBundles maps capability names to their required domain sets.
var DefaultCapabilityBundles = map[string][]string{
	"slack":    {"slack.com", "wss-primary.slack.com", "api.slack.com", "files.slack.com"},
	"telegram": {"api.telegram.org"},
	// MS Teams via Microsoft Graph polling. Sovereign clouds (US Gov, China)
	// stay out of this default bundle — operators add the appropriate domains
	// (graph.microsoft.us / microsoftgraph.chinacloudapi.cn / their respective
	// login hosts) via egress.allowed_domains.
	"msteams": {"graph.microsoft.com", "login.microsoftonline.com"},
	// WhatsApp Web multidevice. The websocket is a fixed host; media hosts are
	// handed to the client at runtime by the server (mmg, mmg-fallback and
	// regional media-*.cdn names), so the media side has to be a wildcard —
	// pinning today's hostnames would break on the next CDN reshuffle.
	"whatsapp": {"web.whatsapp.com", "*.whatsapp.net"},
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
