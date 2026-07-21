package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// errHostUnreachable distinguishes "dial failed" from "policy denied" so the
// SOCKS5 handler can pick the right REP code. Both are surfaced to the caller
// as errors — the audit hook fires on both paths.
var errHostUnreachable = errors.New("upstream host unreachable")

const dialTimeout = 10 * time.Second

// ValidateAndDial is the single gate for all outbound TCP through the proxy.
// Both `handleConnect` (HTTP-CONNECT path) and `handleSOCKS5` (raw-TCP path)
// call this — sharing the primitive prevents the two codepaths from drifting
// on either the allowlist policy or the audit shape.
//
// Order of operations:
//  1. Localhost → dial directly, no matcher check. Matches the pre-existing
//     CONNECT path exactly (localhost has always been an implicit exemption).
//  2. Reject non-standard IP literals early via `ValidateHostIP` — the same
//     octal / hex / packed-decimal guard the RoundTripper enforces.
//  3. Match against BOTH the hostname matcher (`DomainMatcher`, HTTP-shared)
//     AND the port-aware TCP matcher. A target passes if EITHER allows it.
//     This means an HTTP-allowed hostname is reachable over CONNECT/SOCKS5
//     without a redundant `allowed_tcp` entry — the reverse of "allowlist
//     duplicated across two config keys."
//  4. Fire the audit hook exactly once with the (host, port) pair and the
//     decision. Same shape for HTTP and SOCKS5 flows.
//  5. On allow, dial via `SafeDialer` (SSRF + private-CIDR + strict-IP
//     guard). On deny, return a policy error — no dial, no audit fanout.
//
// The context is threaded through to `SafeDialContext` so the dial respects
// downstream cancellation (agent tool timeout, session shutdown).
func (p *EgressProxy) ValidateAndDial(ctx context.Context, host, port string) (net.Conn, error) {
	// SOCKS5 callers record the full host:port in the audit — the whole
	// point of raw-TCP egress is per-port policy, so per-port audit follows.
	return p.validateAndDialWithIdentity(ctx, host, port, net.JoinHostPort(host, port), egressIdentity{})
}

// fireAttemptRaw emits one audit event per dial attempt with the exact
// domain string the caller decides on — hostname-only for HTTP (pre-#337
// audit shape) or host:port for SOCKS5 (raw-TCP path where port matters).
// Downstream consumers keyed by hostname keep working; consumers reading
// SOCKS5 events see the full destination.
func (p *EgressProxy) fireAttemptRaw(auditDomain string, allowed bool, id egressIdentity) {
	if p.OnAttempt == nil {
		return
	}
	p.OnAttempt(EgressAttempt{
		Domain:        auditDomain,
		Allowed:       allowed,
		TaskID:        id.taskID,
		CorrelationID: id.correlationID,
	})
}

// validateAndDialWithIdentity is the identity-carrying variant of
// ValidateAndDial. HTTP handlers recover the identity from
// Proxy-Authorization and pass it; SOCKS5 has no channel for identity and
// uses the bare ValidateAndDial.
//
// The auditDomain parameter controls the string recorded on OnAttempt:
// callers pass `host` for HTTP (pre-#337 shape, hostname-only — keeps
// downstream consumers that key by hostname working) or
// `net.JoinHostPort(host, port)` for the SOCKS5 raw-TCP path (which
// genuinely needs port granularity to be useful).
func (p *EgressProxy) validateAndDialWithIdentity(ctx context.Context, host, port, auditDomain string, id egressIdentity) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Localhost fires an "allowed" audit event so downstream audit consumers
	// see the CONNECT attempt (with task/correlation IDs on the HTTP path).
	// Matches the pre-refactor behavior of `checkDomain` + `fireCallback`.
	if IsLocalhost(host) {
		p.fireAttemptRaw(auditDomain, true, id)
		return net.DialTimeout("tcp", net.JoinHostPort(host, port), dialTimeout)
	}

	if err := ValidateHostIP(host); err != nil {
		p.fireAttemptRaw(auditDomain, false, id)
		return nil, fmt.Errorf("egress: %w", err)
	}

	allowed := p.matcher.IsAllowed(host) || (p.tcpMatcher != nil && p.tcpMatcher.IsAllowed(host, port))
	p.fireAttemptRaw(auditDomain, allowed, id)
	if !allowed {
		return nil, fmt.Errorf("egress: %s not in allowlist", net.JoinHostPort(host, port))
	}

	conn, err := p.safeDialer.SafeDialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errHostUnreachable, err)
	}
	return conn, nil
}
