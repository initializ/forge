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
	case "aws_sigv4":
		// STS at sts.<region>.amazonaws.com is the only outbound.
		// The test-only sts_endpoint override is honored so dev/test
		// runs against a local fake aren't blocked by egress.
		region := settingString(p.Settings, "region")
		out := []string{}
		if region != "" {
			out = append(out, "https://sts."+region+".amazonaws.com")
		}
		if override := settingString(p.Settings, "sts_endpoint"); override != "" {
			out = append(out, override)
		}
		return out
	// static_token has no outbound; not listed
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
//
// Port stripping is intentional and depends on a cross-package contract:
// the egress matcher itself is port-agnostic at the three callsites
// where it's consulted —
//
//   - egress_enforcer.go: req.URL.Hostname() before matcher.IsAllowed
//   - egress_proxy.go:    extractHost() via net.SplitHostPort
//   - safe_dialer.go:     net.SplitHostPort(addr)
//
// As long as those callsites strip the port off the OUTBOUND host before
// matching, a hostname entry in the allowlist suffices for any port. If
// any of those callsites is ever changed to pass host:port to the
// matcher, this function (and TestAuthDomains_AssumesPortAgnosticMatcher)
// must change too — an issuer at https://example.com:8443 would otherwise
// be silently blocked because the allowlist would contain only
// "example.com" while the dialer asked for "example.com:8443".
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
