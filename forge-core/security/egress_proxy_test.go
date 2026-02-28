package security

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestEgressProxyAllowedHTTP(t *testing.T) {
	// Start upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	defer upstream.Close()

	// Parse upstream URL to get host
	upstreamURL, _ := url.Parse(upstream.URL)

	matcher := NewDomainMatcher(ModeAllowlist, []string{upstreamURL.Hostname()})
	proxy := NewEgressProxy(matcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	// HTTP client using the proxy
	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	resp, err := client.Get(upstream.URL + "/test")
	if err != nil {
		t.Fatalf("allowed request failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("expected body 'ok', got %q", string(body))
	}
}

func TestEgressProxyBlockedHTTP(t *testing.T) {
	matcher := NewDomainMatcher(ModeAllowlist, []string{"allowed.com"})
	proxy := NewEgressProxy(matcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	resp, err := client.Get("http://blocked.com/evil")
	if err != nil {
		// Connection errors are acceptable for blocked domains
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", resp.StatusCode)
	}
}

func TestEgressProxyLocalhostAlwaysAllowed(t *testing.T) {
	// Even with deny-all, localhost should pass
	matcher := NewDomainMatcher(ModeDenyAll, nil)
	proxy := NewEgressProxy(matcher)

	// Start a local test server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("local")) //nolint:errcheck
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	resp, err := client.Get(upstream.URL + "/test")
	if err != nil {
		t.Fatalf("localhost request failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "local" {
		t.Errorf("expected 'local', got %q", string(body))
	}
}

func TestEgressProxyCONNECTBlocked(t *testing.T) {
	matcher := NewDomainMatcher(ModeAllowlist, []string{"allowed.com"})
	proxy := NewEgressProxy(matcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec
			},
		},
		Timeout: 5 * time.Second,
	}

	_, err = client.Get("https://blocked.com/evil")
	if err == nil {
		t.Error("CONNECT to blocked domain should fail")
	}
}

func TestEgressProxyCONNECTAllowed(t *testing.T) {
	// Start a TLS upstream
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tls-ok")) //nolint:errcheck
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	host, port, _ := net.SplitHostPort(upstreamURL.Host)

	matcher := NewDomainMatcher(ModeAllowlist, []string{host})
	proxy := NewEgressProxy(matcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("https://" + host + ":" + port + "/test")
	if err != nil {
		t.Fatalf("CONNECT to allowed domain failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "tls-ok" {
		t.Errorf("expected 'tls-ok', got %q", string(body))
	}
}

func TestEgressProxyDevOpenPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("open")) //nolint:errcheck
	}))
	defer upstream.Close()

	// dev-open should allow everything
	matcher := NewDomainMatcher(ModeDevOpen, nil)
	proxy := NewEgressProxy(matcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	resp, err := client.Get(upstream.URL + "/test")
	if err != nil {
		t.Fatalf("dev-open request failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "open" {
		t.Errorf("expected 'open', got %q", string(body))
	}
}

func TestEgressProxyOnAttemptCallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)

	matcher := NewDomainMatcher(ModeAllowlist, []string{upstreamURL.Hostname()})
	proxy := NewEgressProxy(matcher)

	var mu sync.Mutex
	var calls []struct {
		domain  string
		allowed bool
	}
	proxy.OnAttempt = func(domain string, allowed bool) {
		mu.Lock()
		calls = append(calls, struct {
			domain  string
			allowed bool
		}{domain, allowed})
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proxy.Stop() //nolint:errcheck

	proxyURL, _ := url.Parse(proxyAddr)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	// Allowed request
	resp, err := client.Get(upstream.URL + "/test")
	if err != nil {
		t.Fatalf("allowed request failed: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	// Blocked request
	client.Get("http://blocked.example.com/evil") //nolint:errcheck

	mu.Lock()
	defer mu.Unlock()

	if len(calls) < 1 {
		t.Fatal("expected at least 1 callback call")
	}
	// First call should be the localhost (upstream is on localhost)
	if !calls[0].allowed {
		t.Errorf("first call should be allowed (localhost), got allowed=%v", calls[0].allowed)
	}
}

func TestEgressProxyStop(t *testing.T) {
	matcher := NewDomainMatcher(ModeDevOpen, nil)
	proxy := NewEgressProxy(matcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := proxy.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stop should succeed
	if err := proxy.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}

	// Stop again should be safe
	if err := proxy.Stop(); err != nil {
		t.Errorf("double Stop: %v", err)
	}
}

func TestEgressProxyURL(t *testing.T) {
	proxy := NewEgressProxy(NewDomainMatcher(ModeDevOpen, nil))
	if proxy.ProxyURL() != "" {
		t.Error("ProxyURL should be empty before Start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxyAddr, _ := proxy.Start(ctx)
	defer proxy.Stop() //nolint:errcheck

	if proxyAddr == "" {
		t.Fatal("Start should return non-empty URL")
	}
	if proxy.ProxyURL() != proxyAddr {
		t.Errorf("ProxyURL() = %q, want %q", proxy.ProxyURL(), proxyAddr)
	}
}
