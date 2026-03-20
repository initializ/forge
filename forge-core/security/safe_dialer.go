package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Resolver abstracts DNS resolution for testability.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// SafeDialer validates resolved IPs before establishing connections,
// preventing DNS rebinding and SSRF via post-resolution checks.
type SafeDialer struct {
	resolver     Resolver
	dialer       net.Dialer
	allowPrivate bool
}

// NewSafeDialer creates a SafeDialer. If resolver is nil, net.DefaultResolver
// is used. Set allowPrivateIPs to true in container environments where RFC 1918
// addresses are used for inter-service communication.
func NewSafeDialer(resolver Resolver, allowPrivateIPs bool) *SafeDialer {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &SafeDialer{
		resolver:     resolver,
		dialer:       net.Dialer{Timeout: 10 * time.Second},
		allowPrivate: allowPrivateIPs,
	}
}

// SafeDialContext resolves the address, validates all resulting IPs against
// blocked ranges, then dials the first safe IP directly to avoid TOCTOU
// re-resolution.
func (s *SafeDialer) SafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("safe dialer: invalid address %q: %w", addr, err)
	}

	// Check for non-standard IP formats first
	if err := ValidateHostIP(host); err != nil {
		return nil, fmt.Errorf("safe dialer: %w", err)
	}

	// If it's an IP literal, validate and dial directly
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip, s.allowPrivate) {
			return nil, fmt.Errorf("safe dialer: blocked IP %s", ip)
		}
		return s.dialer.DialContext(ctx, network, addr)
	}

	// Resolve hostname
	addrs, err := s.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("safe dialer: DNS lookup failed for %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("safe dialer: no addresses found for %q", host)
	}

	// Validate ALL resolved IPs — reject if ANY is blocked
	for _, a := range addrs {
		if IsBlockedIP(a.IP, s.allowPrivate) {
			return nil, fmt.Errorf("safe dialer: domain %q resolved to blocked IP %s", host, a.IP)
		}
	}

	// Dial the first safe IP directly (avoids TOCTOU re-resolution)
	directAddr := net.JoinHostPort(addrs[0].IP.String(), port)
	return s.dialer.DialContext(ctx, network, directAddr)
}

// NewSafeTransport creates an http.Transport that uses SafeDialer for all
// connections. If resolver is nil, net.DefaultResolver is used.
func NewSafeTransport(resolver Resolver, allowPrivateIPs bool) *http.Transport {
	sd := NewSafeDialer(resolver, allowPrivateIPs)
	return &http.Transport{
		DialContext:           sd.SafeDialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
