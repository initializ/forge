package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/initializ/forge/forge-core/security"
)

// requireChromium locates a browser binary or skips the test. Gating is by
// binary presence (exec.LookPath / os.Stat via ResolveBinary), never GOOS, so
// these tests run on any machine with Chrome and auto-skip in bare CI.
func requireChromium(t *testing.T) string {
	t.Helper()
	bin, err := ResolveBinary()
	if err != nil {
		t.Skipf("no chromium binary found (set FORGE_BROWSER_BIN); skipping: %v", err)
	}
	return bin
}

// startProxy spins up a real EgressProxy with the given matcher and records
// OnAttempt calls. Returns the proxy URL and the attempts map (guarded by mu).
func startProxy(t *testing.T, matcher *security.DomainMatcher) (string, *sync.Mutex, map[string]bool) {
	t.Helper()
	proxy := security.NewEgressProxy(matcher, false)
	mu := &sync.Mutex{}
	attempts := map[string]bool{}
	proxy.OnAttempt = func(domain string, allowed bool) {
		mu.Lock()
		attempts[domain] = allowed
		mu.Unlock()
	}
	proxyURL, err := proxy.Start(context.Background())
	if err != nil {
		t.Fatalf("start egress proxy: %v", err)
	}
	t.Cleanup(func() { proxy.Stop() }) //nolint:errcheck
	return proxyURL, mu, attempts
}

// TestManagerNavigateThroughProxy is the M0 de-risk spike: it proves the full
// chain of headless=new launch, --proxy-server honoring, <-loopback> bypass
// disabling (loopback traffic MUST go through the proxy), and page evaluation.
func TestManagerNavigateThroughProxy(t *testing.T) {
	bin := requireChromium(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>forge spike</title></head><body><h1 id="hd">hello</h1></body></html>`)) //nolint:errcheck
	}))
	defer ts.Close()

	// Empty allowlist: only localhost (always allowed) is reachable.
	matcher := security.NewDomainMatcher(security.ModeAllowlist, nil)
	proxyURL, mu, attempts := startProxy(t, matcher)

	m, err := NewManager(Config{
		BinaryPath: bin,
		Headless:   true,
		ProxyURL:   proxyURL,
		WorkDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Stop()

	var title, heading string
	err = m.run(45*time.Second,
		chromedp.Navigate(ts.URL),
		chromedp.Title(&title),
		chromedp.Text("#hd", &heading),
	)
	if err != nil {
		t.Fatalf("navigate through proxy: %v", err)
	}
	if title != "forge spike" {
		t.Errorf("title = %q, want %q", title, "forge spike")
	}
	if heading != "hello" {
		t.Errorf("heading = %q, want %q", heading, "hello")
	}

	// The load-bearing assertion: the request reached the httptest server VIA
	// the proxy. If Chrome silently bypassed the proxy for loopback, OnAttempt
	// never fires and the egress enforcement story is broken.
	mu.Lock()
	allowed, seen := attempts["127.0.0.1"]
	mu.Unlock()
	if !seen {
		t.Fatalf("proxy OnAttempt never saw 127.0.0.1 — Chrome bypassed the proxy for loopback (attempts: %v)", attempts)
	}
	if !allowed {
		t.Errorf("127.0.0.1 was blocked by the proxy, expected always-allowed localhost")
	}
}

// TestManagerBlockedDomain proves a non-allowlisted HTTPS domain is refused at
// the CONNECT stage: the proxy denies the tunnel before any DNS/dial happens,
// and Chrome surfaces ERR_TUNNEL_CONNECTION_FAILED.
func TestManagerBlockedDomain(t *testing.T) {
	bin := requireChromium(t)

	matcher := security.NewDomainMatcher(security.ModeAllowlist, nil)
	proxyURL, mu, attempts := startProxy(t, matcher)

	m, err := NewManager(Config{
		BinaryPath: bin,
		Headless:   true,
		ProxyURL:   proxyURL,
		WorkDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Stop()

	err = m.run(30*time.Second, chromedp.Navigate("https://blocked.example.test/"))
	if err == nil {
		t.Fatal("navigation to non-allowlisted domain succeeded, want tunnel failure")
	}
	if !strings.Contains(err.Error(), "ERR_TUNNEL_CONNECTION_FAILED") && !strings.Contains(err.Error(), "ERR_PROXY_CONNECTION_FAILED") {
		t.Errorf("unexpected navigation error (want tunnel/proxy failure): %v", err)
	}

	mu.Lock()
	allowed, seen := attempts["blocked.example.test"]
	mu.Unlock()
	if !seen {
		t.Errorf("proxy OnAttempt never saw blocked.example.test (attempts: %v)", attempts)
	}
	if allowed {
		t.Errorf("blocked.example.test was allowed, want blocked")
	}
}

// TestNewManagerRefusesUnproxied enforces the fail-closed invariant.
func TestNewManagerRefusesUnproxied(t *testing.T) {
	_, err := NewManager(Config{BinaryPath: "/usr/bin/true", WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "ProxyURL") {
		t.Errorf("NewManager without ProxyURL: err = %v, want ProxyURL requirement error", err)
	}
}

// TestManagerStopRemovesProfile verifies the throwaway profile is cleaned up.
func TestManagerStopRemovesProfile(t *testing.T) {
	bin := requireChromium(t)

	matcher := security.NewDomainMatcher(security.ModeAllowlist, nil)
	proxyURL, _, _ := startProxy(t, matcher)

	workDir := t.TempDir()
	m, err := NewManager(Config{BinaryPath: bin, Headless: true, ProxyURL: proxyURL, WorkDir: workDir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var one int
	if err := m.run(30*time.Second, chromedp.Evaluate("1", &one)); err != nil {
		t.Fatalf("launch: %v", err)
	}

	m.mu.Lock()
	profile := m.profileDir
	m.mu.Unlock()
	if profile == "" {
		t.Fatal("profileDir empty after launch")
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("profile dir missing while running: %v", err)
	}

	m.Stop()
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("profile dir still exists after Stop: %v", err)
	}
}
