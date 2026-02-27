package security

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEgressEnforcerIntegration(t *testing.T) {
	// Start a real test server
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	defer ts.Close()

	// Build an enforcer that allows only localhost (test server is localhost)
	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeAllowlist, []string{"api.openai.com"})

	var attemptLog []struct {
		domain  string
		allowed bool
	}
	enforcer.OnAttempt = func(_ context.Context, domain string, allowed bool) {
		attemptLog = append(attemptLog, struct {
			domain  string
			allowed bool
		}{domain, allowed})
	}

	client := &http.Client{Transport: enforcer}

	// Request to test server (localhost) — should succeed
	resp, err := client.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("localhost request should succeed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if string(body) != "ok" {
		t.Errorf("expected body 'ok', got %q", string(body))
	}

	// Request to blocked domain — should fail
	_, err = client.Get("https://evil.example.com/steal")
	if err == nil {
		t.Fatal("request to blocked domain should fail")
	}

	// Verify attempt log
	if len(attemptLog) != 2 {
		t.Fatalf("expected 2 attempt log entries, got %d", len(attemptLog))
	}
	// First: localhost (allowed)
	if !attemptLog[0].allowed {
		t.Errorf("first attempt (localhost) should be allowed")
	}
	// Second: evil.example.com (blocked)
	if attemptLog[1].domain != "evil.example.com" || attemptLog[1].allowed {
		t.Errorf("second attempt should be (evil.example.com, false), got (%s, %v)",
			attemptLog[1].domain, attemptLog[1].allowed)
	}

	// Verify test server was actually hit
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("expected 1 hit to test server, got %d", atomic.LoadInt32(&hits))
	}
}

func TestEgressEnforcerDenyAllIntegration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeDenyAll, nil)
	client := &http.Client{Transport: enforcer}

	// Localhost should still work even in deny-all
	resp, err := client.Get(ts.URL + "/test")
	if err != nil {
		t.Fatalf("localhost should be allowed even in deny-all: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
}

func TestEgressEnforcerWildcardIntegration(t *testing.T) {
	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeAllowlist, []string{"*.example.com"})

	var attempts []struct {
		domain  string
		allowed bool
	}
	enforcer.OnAttempt = func(_ context.Context, domain string, allowed bool) {
		attempts = append(attempts, struct {
			domain  string
			allowed bool
		}{domain, allowed})
	}

	client := &http.Client{Transport: enforcer}

	// api.example.com should be allowed (but will fail at DNS — that's fine, we check the enforcer decision)
	_, _ = client.Get("https://api.example.com/test")
	// other.com should be blocked
	_, _ = client.Get("https://other.com/test")

	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	if !attempts[0].allowed {
		t.Error("api.example.com should be allowed by wildcard")
	}
	if attempts[1].allowed {
		t.Error("other.com should be blocked")
	}
}
