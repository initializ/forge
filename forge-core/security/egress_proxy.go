package security

import (
	"context"
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
	matcher   *DomainMatcher
	listener  net.Listener
	srv       *http.Server
	addr      string // "127.0.0.1:<port>"
	OnAttempt func(domain string, allowed bool)
}

// NewEgressProxy creates a new EgressProxy that validates domains using the
// given DomainMatcher. Call Start to bind and begin serving.
func NewEgressProxy(matcher *DomainMatcher) *EgressProxy {
	return &EgressProxy{
		matcher: matcher,
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

	if !p.checkDomain(host) {
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

	resp, err := http.DefaultTransport.RoundTrip(outReq)
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

	if !p.checkDomain(host) {
		http.Error(w, fmt.Sprintf("egress proxy: domain %q blocked", host), http.StatusForbidden)
		return
	}

	// Dial the upstream
	upstream, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
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
func (p *EgressProxy) checkDomain(host string) bool {
	// Localhost is always allowed
	if IsLocalhost(host) {
		p.fireCallback(host, true)
		return true
	}

	allowed := p.matcher.IsAllowed(host)
	p.fireCallback(host, allowed)
	return allowed
}

func (p *EgressProxy) fireCallback(domain string, allowed bool) {
	if p.OnAttempt != nil {
		p.OnAttempt(domain, allowed)
	}
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
