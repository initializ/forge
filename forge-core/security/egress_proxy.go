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

// EgressProxy is a localhost-only HTTP/HTTPS forward proxy that validates
// outbound domains against a DomainMatcher before forwarding requests.
// It is used to enforce egress rules on subprocesses (skill scripts) that
// cannot use the Go-level EgressEnforcer RoundTripper.
type EgressProxy struct {
	matcher       *DomainMatcher
	safeDialer    *SafeDialer
	safeTransport *http.Transport
	listener      net.Listener
	srv           *http.Server
	addr          string // "127.0.0.1:<port>"
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
func NewEgressProxy(matcher *DomainMatcher, allowPrivateIPs bool, allowedPrivateCIDRs []*net.IPNet) *EgressProxy {
	sd := NewSafeDialer(nil, allowPrivateIPs, allowedPrivateCIDRs)
	return &EgressProxy{
		matcher:       matcher,
		safeDialer:    sd,
		safeTransport: NewSafeTransport(nil, allowPrivateIPs, allowedPrivateCIDRs),
	}
}

// Start binds to 127.0.0.1:0 (random port) and begins serving.
// Returns the proxy URL (e.g., "http://127.0.0.1:54321").
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

	return p.ProxyURL(), nil
}

// Stop gracefully shuts down the proxy with a 5-second timeout.
func (p *EgressProxy) Stop() error {
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

// handleConnect handles HTTPS CONNECT tunneling. It validates the destination
// hostname, then blind-relays bytes without decrypting TLS.
func (p *EgressProxy) handleConnect(w http.ResponseWriter, req *http.Request) {
	host := extractHost(req.Host)
	id := identityFromRequest(req)

	if !p.checkDomain(host, id) {
		http.Error(w, fmt.Sprintf("egress proxy: domain %q blocked", host), http.StatusForbidden)
		return
	}

	// Dial the upstream. Use safe dialer for non-localhost, standard dial for localhost
	// (safe dialer blocks loopback IPs for DNS rebinding protection).
	var upstream net.Conn
	var err error
	if IsLocalhost(host) {
		upstream, err = net.DialTimeout("tcp", req.Host, 10*time.Second)
	} else {
		ctx := req.Context()
		upstream, err = p.safeDialer.SafeDialContext(ctx, "tcp", req.Host)
	}
	if err != nil {
		http.Error(w, "egress proxy: failed to connect upstream", http.StatusBadGateway)
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

	// Blind relay between client and upstream
	go func() {
		defer upstream.Close()        //nolint:errcheck
		defer clientConn.Close()      //nolint:errcheck
		io.Copy(upstream, clientConn) //nolint:errcheck
	}()
	go func() {
		defer upstream.Close()        //nolint:errcheck
		defer clientConn.Close()      //nolint:errcheck
		io.Copy(clientConn, upstream) //nolint:errcheck
	}()
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
