package security

import (
	"net/url"
	"sort"

	"github.com/initializ/forge/forge-core/types"
)

// AuthDomains extracts the outbound hosts that auth providers must be able
// to reach: OIDC issuers, http_verifier URLs, future Okta tenants, etc.
//
// These domains are merged into the egress allowlist BEFORE the egress
// enforcer is constructed, so configuring an OIDC provider does not
// silently fail at runtime with a network-blocked JWKS fetch.
//
// Returned hosts are deduplicated and sorted for stable test output.
// Empty/malformed URLs are skipped (validation happens elsewhere).
func AuthDomains(cfg types.AuthConfig) []string {
	if len(cfg.Providers) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, p := range cfg.Providers {
		for _, raw := range authProviderURLs(p) {
			host := hostFromURL(raw)
			if host == "" {
				continue
			}
			seen[host] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// authProviderURLs returns the URLs that a given provider needs to reach.
// Centralizes per-provider extraction so adding a new provider type in
// the registry only requires touching one map.
func authProviderURLs(p types.AuthProvider) []string {
	switch p.Type {
	case "oidc":
		return []string{
			settingString(p.Settings, "issuer"),
			settingString(p.Settings, "jwks_url"),
		}
	case "http_verifier":
		return []string{
			settingString(p.Settings, "url"),
		}
	// static_token has no outbound; not listed
	// okta (Phase 3) will be added here with issuer + api domain
	default:
		return nil
	}
}

func settingString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// hostFromURL extracts the bare host (without port) from a URL string.
// Returns "" for unparseable values — the caller skips those silently
// since real validation happens in validate.ValidateAuthConfig.
func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}
