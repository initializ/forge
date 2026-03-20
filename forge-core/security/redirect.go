package security

import (
	"fmt"
	"net/http"
	"strings"
)

// SafeRedirectPolicy returns a CheckRedirect function that strips sensitive
// credentials (Authorization, Cookie, etc.) when a redirect crosses origin
// boundaries (different scheme, host, or port from the original request).
func SafeRedirectPolicy(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}

		// Compare against the original (first) request
		original := via[0]
		if !isSameOrigin(original, req) {
			req.Header.Del("Authorization")
			req.Header.Del("Proxy-Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Cookie2")
		}

		return nil
	}
}

// isSameOrigin returns true if two requests share the same scheme, host, and port.
func isSameOrigin(a, b *http.Request) bool {
	aScheme := strings.ToLower(a.URL.Scheme)
	bScheme := strings.ToLower(b.URL.Scheme)
	if aScheme != bScheme {
		return false
	}

	aHost := strings.ToLower(a.URL.Hostname())
	bHost := strings.ToLower(b.URL.Hostname())
	if aHost != bHost {
		return false
	}

	aPort := effectivePort(a.URL.Port(), aScheme)
	bPort := effectivePort(b.URL.Port(), bScheme)
	return aPort == bPort
}

// effectivePort returns the port or the default for the scheme.
func effectivePort(port, scheme string) string {
	if port != "" {
		return port
	}
	switch scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
