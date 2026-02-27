package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// egressClientKey is the context key for the egress-enforced HTTP client.
type egressClientKey struct{}

// EgressEnforcer is an http.RoundTripper that validates outbound requests
// against a domain allowlist before forwarding them to the base transport.
type EgressEnforcer struct {
	base          http.RoundTripper
	mode          EgressMode
	allowedHosts  map[string]bool
	wildcardHosts []string // suffix patterns: ".github.com"
	OnAttempt     func(ctx context.Context, domain string, allowed bool)
}

// NewEgressEnforcer creates a new EgressEnforcer wrapping the given base transport.
// If base is nil, http.DefaultTransport is used. Domains may include wildcard
// prefixes (e.g. "*.github.com") which match any subdomain.
func NewEgressEnforcer(base http.RoundTripper, mode EgressMode, domains []string) *EgressEnforcer {
	if base == nil {
		base = http.DefaultTransport
	}

	allowed := make(map[string]bool, len(domains))
	var wildcards []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if strings.HasPrefix(d, "*.") {
			// *.github.com → suffix ".github.com"
			wildcards = append(wildcards, d[1:]) // ".github.com"
		} else {
			allowed[d] = true
		}
	}

	return &EgressEnforcer{
		base:          base,
		mode:          mode,
		allowedHosts:  allowed,
		wildcardHosts: wildcards,
	}
}

// RoundTrip implements http.RoundTripper. It checks the request hostname
// against the allowlist and fires the OnAttempt callback.
func (e *EgressEnforcer) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())

	ctx := req.Context()

	// Localhost is always allowed.
	if isLocalhost(host) {
		if e.OnAttempt != nil {
			e.OnAttempt(ctx, host, true)
		}
		return e.base.RoundTrip(req)
	}

	allowed := e.isAllowed(host)

	if e.OnAttempt != nil {
		e.OnAttempt(ctx, host, allowed)
	}

	if !allowed {
		return nil, fmt.Errorf("egress blocked: domain %q not in allowlist (mode=%s)", host, e.mode)
	}

	return e.base.RoundTrip(req)
}

// isAllowed checks if a host is permitted under the current mode.
func (e *EgressEnforcer) isAllowed(host string) bool {
	switch e.mode {
	case ModeDevOpen:
		return true
	case ModeDenyAll:
		return false
	case ModeAllowlist:
		// Exact match
		if e.allowedHosts[host] {
			return true
		}
		// Wildcard suffix match: *.github.com matches api.github.com
		for _, suffix := range e.wildcardHosts {
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// isLocalhost returns true for loopback addresses.
func isLocalhost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WithEgressClient stores an egress-enforced HTTP client in the context.
func WithEgressClient(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(ctx, egressClientKey{}, client)
}

// EgressClientFromContext retrieves the egress-enforced HTTP client from
// the context. Returns http.DefaultClient if none is set.
func EgressClientFromContext(ctx context.Context) *http.Client {
	if c, ok := ctx.Value(egressClientKey{}).(*http.Client); ok && c != nil {
		return c
	}
	return http.DefaultClient
}

// EgressTransportFromContext retrieves the transport from the egress client
// in the context. Returns nil if no egress client is set (so that
// http.Client{Transport: nil} falls back to http.DefaultTransport).
func EgressTransportFromContext(ctx context.Context) http.RoundTripper {
	if c, ok := ctx.Value(egressClientKey{}).(*http.Client); ok && c != nil {
		return c.Transport
	}
	return nil
}
