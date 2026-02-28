package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestEgressEnforcerAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		domains []string
		url     string
		allowed bool
	}{
		{
			name:    "exact match allowed",
			domains: []string{"api.openai.com"},
			url:     "https://api.openai.com/v1/chat",
			allowed: true,
		},
		{
			name:    "exact match blocked",
			domains: []string{"api.openai.com"},
			url:     "https://evil.com/steal",
			allowed: false,
		},
		{
			name:    "wildcard match subdomain",
			domains: []string{"*.github.com"},
			url:     "https://api.github.com/repos",
			allowed: true,
		},
		{
			name:    "wildcard does not match bare domain",
			domains: []string{"*.github.com"},
			url:     "https://github.com/repos",
			allowed: false,
		},
		{
			name:    "wildcard does not match unrelated",
			domains: []string{"*.github.com"},
			url:     "https://notgithub.com/repos",
			allowed: false,
		},
		{
			name:    "port stripping",
			domains: []string{"api.openai.com"},
			url:     "https://api.openai.com:443/v1/chat",
			allowed: true,
		},
		{
			name:    "case insensitive",
			domains: []string{"api.openai.com"},
			url:     "https://API.OpenAI.COM/v1/chat",
			allowed: true,
		},
		{
			name:    "localhost always allowed",
			domains: []string{},
			url:     "http://localhost:8080/test",
			allowed: true,
		},
		{
			name:    "127.0.0.1 always allowed",
			domains: []string{},
			url:     "http://127.0.0.1:9090/test",
			allowed: true,
		},
		{
			name:    "ipv6 loopback always allowed",
			domains: []string{},
			url:     "http://[::1]:8080/test",
			allowed: true,
		},
		{
			name:    "multiple domains",
			domains: []string{"api.openai.com", "api.tavily.com"},
			url:     "https://api.tavily.com/search",
			allowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a test server to avoid real network calls for allowed requests
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer ts.Close()

			enforcer := NewEgressEnforcer(http.DefaultTransport, ModeAllowlist, tt.domains)

			req, err := http.NewRequest("GET", tt.url, nil)
			if err != nil {
				t.Fatalf("creating request: %v", err)
			}

			_, err = enforcer.RoundTrip(req)
			if tt.allowed && err != nil {
				// Allowed requests may fail with DNS/connection errors
				// but should NOT fail with "egress blocked"
				if strings.Contains(err.Error(), "egress blocked") {
					t.Errorf("expected request to be allowed, got egress blocked: %v", err)
				}
			}
			if !tt.allowed {
				if err == nil {
					t.Error("expected request to be blocked, got nil error")
				} else if !strings.Contains(err.Error(), "egress blocked") {
					t.Errorf("expected egress blocked error, got: %v", err)
				}
			}
		})
	}
}

func TestEgressEnforcerDenyAll(t *testing.T) {
	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeDenyAll, nil)

	req, _ := http.NewRequest("GET", "https://api.openai.com/v1/chat", nil)
	_, err := enforcer.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "egress blocked") {
		t.Errorf("deny-all should block everything, got: %v", err)
	}
}

func TestEgressEnforcerDenyAllAllowsLocalhost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeDenyAll, nil)

	req, _ := http.NewRequest("GET", ts.URL+"/test", nil)
	resp, err := enforcer.RoundTrip(req)
	if err != nil {
		t.Fatalf("localhost should be allowed even in deny-all mode: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
}

func TestEgressEnforcerDevOpen(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeDevOpen, nil)

	req, _ := http.NewRequest("GET", ts.URL+"/test", nil)
	resp, err := enforcer.RoundTrip(req)
	if err != nil {
		t.Fatalf("dev-open should allow everything: %v", err)
	}
	resp.Body.Close() //nolint:errcheck
}

func TestEgressEnforcerOnAttemptCallback(t *testing.T) {
	var mu sync.Mutex
	var calls []struct {
		domain  string
		allowed bool
	}

	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeAllowlist, []string{"api.openai.com"})
	enforcer.OnAttempt = func(_ context.Context, domain string, allowed bool) {
		mu.Lock()
		calls = append(calls, struct {
			domain  string
			allowed bool
		}{domain, allowed})
		mu.Unlock()
	}

	// Allowed request
	req1, _ := http.NewRequest("GET", "https://api.openai.com/v1/chat", nil)
	enforcer.RoundTrip(req1) //nolint:errcheck

	// Blocked request
	req2, _ := http.NewRequest("GET", "https://evil.com/steal", nil)
	enforcer.RoundTrip(req2) //nolint:errcheck

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 callback calls, got %d", len(calls))
	}
	if calls[0].domain != "api.openai.com" || !calls[0].allowed {
		t.Errorf("first call: expected (api.openai.com, true), got (%s, %v)", calls[0].domain, calls[0].allowed)
	}
	if calls[1].domain != "evil.com" || calls[1].allowed {
		t.Errorf("second call: expected (evil.com, false), got (%s, %v)", calls[1].domain, calls[1].allowed)
	}
}

func TestEgressEnforcerDevOpenCallback(t *testing.T) {
	var called bool
	enforcer := NewEgressEnforcer(http.DefaultTransport, ModeDevOpen, nil)
	enforcer.OnAttempt = func(_ context.Context, domain string, allowed bool) {
		called = true
		if !allowed {
			t.Error("dev-open should report allowed=true")
		}
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/test", nil)
	resp, err := enforcer.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	if !called {
		t.Error("OnAttempt callback should fire in dev-open mode")
	}
}

func TestEgressContextRoundTrip(t *testing.T) {
	client := &http.Client{Transport: http.DefaultTransport}
	ctx := WithEgressClient(context.Background(), client)

	got := EgressClientFromContext(ctx)
	if got != client {
		t.Error("EgressClientFromContext should return the stored client")
	}

	transport := EgressTransportFromContext(ctx)
	if transport != http.DefaultTransport {
		t.Error("EgressTransportFromContext should return the client's transport")
	}
}

func TestEgressContextMissing(t *testing.T) {
	ctx := context.Background()

	got := EgressClientFromContext(ctx)
	if got != http.DefaultClient {
		t.Error("EgressClientFromContext should return http.DefaultClient when missing")
	}

	transport := EgressTransportFromContext(ctx)
	if transport != nil {
		t.Error("EgressTransportFromContext should return nil when missing")
	}
}

func TestEgressEnforcerNilBase(t *testing.T) {
	enforcer := NewEgressEnforcer(nil, ModeAllowlist, []string{"example.com"})
	if enforcer.base == nil {
		t.Error("nil base should be replaced with http.DefaultTransport")
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"example.com", false},
		{"192.168.1.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := IsLocalhost(tt.host); got != tt.expected {
				t.Errorf("IsLocalhost(%q) = %v, want %v", tt.host, got, tt.expected)
			}
		})
	}
}
