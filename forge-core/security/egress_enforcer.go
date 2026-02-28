package security

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// egressClientKey is the context key for the egress-enforced HTTP client.
type egressClientKey struct{}

// EgressEnforcer is an http.RoundTripper that validates outbound requests
// against a domain allowlist before forwarding them to the base transport.
type EgressEnforcer struct {
	base      http.RoundTripper
	matcher   *DomainMatcher
	OnAttempt func(ctx context.Context, domain string, allowed bool)
}

// NewEgressEnforcer creates a new EgressEnforcer wrapping the given base transport.
// If base is nil, http.DefaultTransport is used. Domains may include wildcard
// prefixes (e.g. "*.github.com") which match any subdomain.
func NewEgressEnforcer(base http.RoundTripper, mode EgressMode, domains []string) *EgressEnforcer {
	if base == nil {
		base = http.DefaultTransport
	}

	return &EgressEnforcer{
		base:    base,
		matcher: NewDomainMatcher(mode, domains),
	}
}

// RoundTrip implements http.RoundTripper. It checks the request hostname
// against the allowlist and fires the OnAttempt callback.
func (e *EgressEnforcer) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())

	ctx := req.Context()

	// Localhost is always allowed.
	if IsLocalhost(host) {
		if e.OnAttempt != nil {
			e.OnAttempt(ctx, host, true)
		}
		return e.base.RoundTrip(req)
	}

	allowed := e.matcher.IsAllowed(host)

	if e.OnAttempt != nil {
		e.OnAttempt(ctx, host, allowed)
	}

	if !allowed {
		return nil, fmt.Errorf("egress blocked: domain %q not in allowlist (mode=%s)", host, e.matcher.Mode())
	}

	return e.base.RoundTrip(req)
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
