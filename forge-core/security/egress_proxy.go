package security

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// EgressProxy is a localhost-only forward proxy that validates outbound
// destinations before forwarding traffic. It runs TWO listeners on distinct
// ports:
//
//   - An HTTP forward proxy (plain HTTP + HTTPS via CONNECT). Clients reach it
//     via HTTP_PROXY / HTTPS_PROXY. This is the pre-existing surface.
//   - A SOCKS5 CONNECT proxy for raw-TCP flows (databases, message brokers).
//     Clients reach it via ALL_PROXY / SOCKS_PROXY. Started only when
//     `allowed_tcp` is non-empty — one listener less to reason about at
//     deploy time when raw-TCP isn't configured. See issue #337.
//
// Both listeners share the same enforcement primitive (`ValidateAndDial`) so
// the allowlist policy and audit shape can't drift between HTTP and TCP.
type EgressProxy struct {
	matcher       *DomainMatcher
	tcpMatcher    *TCPMatcher // nil-safe; empty when allowed_tcp is unconfigured
	safeDialer    *SafeDialer
	safeTransport *http.Transport
	listener      net.Listener
	srv           *http.Server
	addr          string // "127.0.0.1:<port>" — HTTP listener
	socksListener net.Listener
	socksAddr     string // "127.0.0.1:<port>" — SOCKS5 listener (empty when disabled)
	OnAttempt     func(EgressAttempt)
}

// EgressAttempt describes a single egress decision for audit correlation.
// TaskID and CorrelationID are recovered from the Proxy-Authorization header
// the subprocess sends (see identityFromRequest) — the caller injects them as
// userinfo in the HTTP_PROXY URL, which HTTP clients replay as Basic proxy
// credentials on every request and CONNECT. They are empty when the client
// doesn't send credentials (arbitrary binaries), which degrades gracefully to
// the pre-#338 behaviour: a domain-only event with no task attribution.
type EgressAttempt struct {
	Domain        string
	Allowed       bool
	TaskID        string
	CorrelationID string
}

// egressIdentity carries the per-request task/invocation IDs recovered from the
// proxy credentials, threaded from the request handler down to the callback.
type egressIdentity struct {
	taskID        string
	correlationID string
}

// NewEgressProxy creates a new EgressProxy that validates domains using the
// given DomainMatcher. Call Start to bind and begin serving. allowedPrivateCIDRs
// narrows the private-IP block: when allowPrivateIPs is false, IPs inside any
// of the listed CIDRs bypass the private block. Pass nil for pre-CIDR defaults.
//
// Raw-TCP allowlist entries live on the returned proxy via SetTCPMatcher —
// separating them from the constructor keeps the call sites that don't need
// SOCKS5 (browser capability, dev-open mode, tests) unchanged.
func NewEgressProxy(matcher *DomainMatcher, allowPrivateIPs bool, allowedPrivateCIDRs []*net.IPNet) *EgressProxy {
	sd := NewSafeDialer(nil, allowPrivateIPs, allowedPrivateCIDRs)
	return &EgressProxy{
		matcher:       matcher,
		safeDialer:    sd,
		safeTransport: NewSafeTransport(nil, allowPrivateIPs, allowedPrivateCIDRs),
	}
}

// SetTCPMatcher installs a port-aware allowlist for raw-TCP egress. When the
// matcher is non-nil and non-empty, Start also binds a SOCKS5 listener and
// SOCKSURL() returns a non-empty URL. Must be called before Start.
func (p *EgressProxy) SetTCPMatcher(m *TCPMatcher) {
	p.tcpMatcher = m
}

// Start binds to 127.0.0.1:0 (random ports) and begins serving.
// Returns the HTTP proxy URL (e.g., "http://127.0.0.1:54321"). The SOCKS5
// listener, if TCPMatcher is non-empty, is started at the same time and its
// URL is available via SOCKSURL().
func (p *EgressProxy) Start(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("egress proxy listen: %w", err)
	}
	p.listener = ln
	p.addr = ln.Addr().String()

	p.srv = &http.Server{
		Handler: http.HandlerFunc(p.handleRequest),
	}

	go func() {
		<-ctx.Done()
		p.Stop() //nolint:errcheck
	}()

	go p.srv.Serve(ln) //nolint:errcheck

	// SOCKS5 listener — only when raw-TCP egress is configured. Skipping
	// this by default means the deploy surface is unchanged for agents that
	// only need HTTP.
	if p.tcpMatcher != nil && !p.tcpMatcher.Empty() {
		socksLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			// Roll back the HTTP listener so we don't leave the process in a
			// half-started state.
			_ = ln.Close()
			return "", fmt.Errorf("egress proxy socks5 listen: %w", err)
		}
		p.socksListener = socksLn
		p.socksAddr = socksLn.Addr().String()
		go p.acceptSOCKS5(socksLn)
	}

	return p.ProxyURL(), nil
}

// acceptSOCKS5 runs the SOCKS5 accept loop until the listener closes.
func (p *EgressProxy) acceptSOCKS5(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go p.handleSOCKS5(conn)
	}
}

// Stop gracefully shuts down the proxy with a 5-second timeout. Both
// listeners are closed. Safe to call on a not-yet-Started proxy.
func (p *EgressProxy) Stop() error {
	if p.socksListener != nil {
		_ = p.socksListener.Close()
	}
	if p.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

// ProxyURL returns the URL for HTTP_PROXY/HTTPS_PROXY env vars.
func (p *EgressProxy) ProxyURL() string {
	if p.addr == "" {
		return ""
	}
	return "http://" + p.addr
}

// SOCKSURL returns the URL for ALL_PROXY/SOCKS_PROXY env vars, or empty when
// raw-TCP egress isn't configured. Uses the `socks5h://` scheme so clients
// send the destination hostname (not a pre-resolved IP) — the proxy needs
// the hostname to record it in the audit hook.
func (p *EgressProxy) SOCKSURL() string {
	if p.socksAddr == "" {
		return ""
	}
	return "socks5h://" + p.socksAddr
}

// handleRequest dispatches HTTP requests and CONNECT tunnels.
func (p *EgressProxy) handleRequest(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		p.handleConnect(w, req)
		return
	}
	p.handleHTTP(w, req)
}

// handleHTTP forwards plain HTTP requests after domain validation.
func (p *EgressProxy) handleHTTP(w http.ResponseWriter, req *http.Request) {
	host := extractHost(req.URL.Host)
	id := identityFromRequest(req)

	if !p.checkDomain(host, id) {
		http.Error(w, fmt.Sprintf("egress proxy: domain %q blocked", host), http.StatusForbidden)
		return
	}

	// Forward the request
	outReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), req.Body)
	if err != nil {
		http.Error(w, "egress proxy: failed to create request", http.StatusBadGateway)
		return
	}
	outReq.Header = req.Header.Clone()
	// Remove hop-by-hop headers
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authorization")

	// Use http.DefaultTransport for localhost (safe dialer blocks loopback).
	var transport http.RoundTripper = p.safeTransport
	if IsLocalhost(host) {
		transport = http.DefaultTransport
	}
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "egress proxy: upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// handleConnect handles HTTPS CONNECT tunneling. Delegates policy + dial to
// ValidateAndDial (shared with the SOCKS5 handler) and then blind-relays.
func (p *EgressProxy) handleConnect(w http.ResponseWriter, req *http.Request) {
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		http.Error(w, "egress proxy: bad CONNECT target", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(host)

	// The proxy-identity threading is HTTP-only (Proxy-Authorization creds).
	// ValidateAndDial doesn't know about it, so we upgrade the audit event
	// here after the dial with the recovered identity fields. The two audit
	// events (one from ValidateAndDial, one on failure of the upgrade path)
	// stay one-per-attempt because the failure path returns before the dial.
	id := identityFromRequest(req)

	// HTTP-CONNECT audits keep the pre-#337 shape (hostname-only) so
	// downstream consumers that key events by hostname keep working.
	upstream, err := p.validateAndDialWithIdentity(req.Context(), host, port, host, id)
	if err != nil {
		http.Error(w, "egress proxy: "+err.Error(), http.StatusForbidden)
		return
	}

	// Respond 200 to signal the client that the tunnel is established
	w.WriteHeader(http.StatusOK)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close() //nolint:errcheck
		http.Error(w, "egress proxy: hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		upstream.Close() //nolint:errcheck
		return
	}

	// The hijacked conn is now owned by the relay goroutines — handleConnect
	// must return so the http.Server can accept the next connection.
	go relayPair(clientConn, upstream)
}

// relayPair blind-relays bytes between two conns in both directions and
// BLOCKS until both directions finish. Callers that need async behavior
// (HTTP-CONNECT, where the hijacked conn's lifetime is owned by the spawned
// goroutines) wrap this in `go relayPair(...)`. The SOCKS5 handler calls it
// synchronously so its deferred client.Close() doesn't fire mid-relay.
func relayPair(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(b, a) //nolint:errcheck
		_ = b.Close()
		_ = a.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(a, b) //nolint:errcheck
		_ = a.Close()
		_ = b.Close()
		done <- struct{}{}
	}()
	<-done
	<-done
}

// checkDomain validates a host against the matcher, allowing localhost always.
func (p *EgressProxy) checkDomain(host string, id egressIdentity) bool {
	// Reject non-standard IP formats early
	if err := ValidateHostIP(host); err != nil {
		p.fireCallback(host, false, id)
		return false
	}

	// Localhost is always allowed
	if IsLocalhost(host) {
		p.fireCallback(host, true, id)
		return true
	}

	allowed := p.matcher.IsAllowed(host)
	p.fireCallback(host, allowed, id)
	return allowed
}

func (p *EgressProxy) fireCallback(domain string, allowed bool, id egressIdentity) {
	if p.OnAttempt != nil {
		p.OnAttempt(EgressAttempt{
			Domain:        domain,
			Allowed:       allowed,
			TaskID:        id.taskID,
			CorrelationID: id.correlationID,
		})
	}
}

// identityFromRequest recovers the task/invocation IDs the caller stashed in
// the proxy credentials. HTTP clients that see userinfo in the HTTP_PROXY URL
// replay it as a "Proxy-Authorization: Basic base64(user:pass)" header on every
// proxied request and CONNECT, where user/pass are the base64url-encoded task
// and correlation IDs (see proxyURLWithIdentity). The inner base64url encoding
// keeps each half free of the ':' separator, so a task ID containing a raw
// colon can't mis-split its own attribution. Returns a zero identity when the
// header is absent or doesn't match that exact shape — the proxy still enforces
// and audits, just without task attribution (arbitrary binaries that ignore
// proxy creds, or send their own unrelated credentials).
func identityFromRequest(req *http.Request) egressIdentity {
	h := req.Header.Get("Proxy-Authorization")
	if h == "" {
		return egressIdentity{}
	}
	const prefix = "Basic "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return egressIdentity{}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return egressIdentity{}
	}
	encTask, encCorr, found := strings.Cut(string(raw), ":")
	if !found {
		return egressIdentity{}
	}
	task, tErr := base64.RawURLEncoding.DecodeString(encTask)
	corr, cErr := base64.RawURLEncoding.DecodeString(encCorr)
	if tErr != nil || cErr != nil {
		return egressIdentity{}
	}
	return egressIdentity{taskID: string(task), correlationID: string(corr)}
}

// extractHost strips the port from a host:port string.
func extractHost(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		// No port present
		return strings.ToLower(hostPort)
	}
	return strings.ToLower(host)
}
