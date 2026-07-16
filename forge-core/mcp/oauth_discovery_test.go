package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-core/llm/oauth"
)

// withTempCredsDir points the oauth store at a throwaway dir so the
// discovery/registration persistence tests don't touch ~/.forge.
func withTempCredsDir(t *testing.T) {
	t.Helper()
	oauth.SetCredentialsDir(t.TempDir())
	t.Cleanup(func() { oauth.SetCredentialsDir("") })
}

// discoveryServer stands in for an MCP server + its authorization
// server. It serves RFC 9728 protected-resource metadata, RFC 8414
// auth-server metadata, and an RFC 7591 registration endpoint. Knobs
// let individual tests suppress pieces to exercise the fallbacks.
type discoveryServer struct {
	srv           *httptest.Server
	regCalls      atomic.Int32
	noPRWellKnown bool // suppress /.well-known/oauth-protected-resource (force WWW-Authenticate path)
	noRegEndpoint bool // omit registration_endpoint from AS metadata
}

func newDiscoveryServer(t *testing.T) *discoveryServer {
	t.Helper()
	d := &discoveryServer{}
	mux := http.NewServeMux()
	d.srv = httptest.NewServer(mux)
	base := d.srv.URL

	prMeta := func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protectedResourceMetadata{
			Resource:             base + "/mcp",
			AuthorizationServers: []string{base},
		})
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		if d.noPRWellKnown {
			http.NotFound(w, r)
			return
		}
		prMeta(w, r)
	})
	// An ALTERNATE protected-resource metadata URL the 401 points at —
	// always served, so the WWW-Authenticate fallback works even when the
	// default well-known path is suppressed (RFC 9728 allows a custom URL).
	mux.HandleFunc("/alt-resource-metadata", prMeta)

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		m := authServerMetadata{
			Issuer:                base,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
			ScopesSupported:       []string{"read", "write"},
		}
		if !d.noRegEndpoint {
			m.RegistrationEndpoint = base + "/register"
		}
		_ = json.NewEncoder(w).Encode(m)
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		d.regCalls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"client_id":"dyn-client-123"}`))
	})

	// The MCP endpoint itself: 401 with a WWW-Authenticate pointer, so
	// the WWW-Authenticate discovery path can be exercised.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate",
			`Bearer resource_metadata="`+base+`/alt-resource-metadata"`)
		w.WriteHeader(http.StatusUnauthorized)
	})

	t.Cleanup(d.srv.Close)
	return d
}

func (d *discoveryServer) url() string { return d.srv.URL + "/mcp" }

// TestResolveOAuthConfig_Discovery: with no endpoints and no client_id,
// resolve runs 9728 → 8414 → 7591, populates the config, and persists a
// registration record that a second call reuses (no re-registration).
func TestResolveOAuthConfig_Discovery(t *testing.T) {
	withTempCredsDir(t)
	d := newDiscoveryServer(t)
	f := NewOAuthFlow()

	cfg := OAuthServerConfig{ServerURL: d.url(), Scopes: []string{"read"}}
	got, err := f.resolveOAuthConfig(context.Background(), "srv", cfg, true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ClientID != "dyn-client-123" {
		t.Errorf("client_id = %q, want the DCR-minted id", got.ClientID)
	}
	if !strings.HasSuffix(got.AuthorizeURL, "/authorize") || !strings.HasSuffix(got.TokenURL, "/token") {
		t.Errorf("endpoints not discovered: %+v", got)
	}

	// Second resolve must reuse the persisted registration — no new DCR.
	got2, err := f.resolveOAuthConfig(context.Background(), "srv", cfg, true)
	if err != nil {
		t.Fatalf("resolve #2: %v", err)
	}
	if got2.ClientID != "dyn-client-123" {
		t.Errorf("reuse: client_id = %q", got2.ClientID)
	}
	if n := d.regCalls.Load(); n != 1 {
		t.Errorf("registration endpoint hit %d times, want 1 (persisted + reused)", n)
	}
}

// TestResolveOAuthConfig_WWWAuthenticateFallback: when the well-known
// protected-resource path is absent, the 401 WWW-Authenticate pointer
// drives discovery instead.
func TestResolveOAuthConfig_WWWAuthenticateFallback(t *testing.T) {
	withTempCredsDir(t)
	d := newDiscoveryServer(t)
	d.noPRWellKnown = true
	f := NewOAuthFlow()

	got, err := f.resolveOAuthConfig(context.Background(), "srv", OAuthServerConfig{ServerURL: d.url()}, true)
	if err != nil {
		t.Fatalf("resolve via WWW-Authenticate: %v", err)
	}
	if got.ClientID == "" || got.TokenURL == "" {
		t.Errorf("discovery via WWW-Authenticate did not populate config: %+v", got)
	}
}

// TestResolveOAuthConfig_ExplicitWins: a fully-explicit config never
// touches the network (discovery would fail against a dead URL).
func TestResolveOAuthConfig_ExplicitWins(t *testing.T) {
	withTempCredsDir(t)
	f := NewOAuthFlow()
	cfg := OAuthServerConfig{
		ServerURL:    "http://127.0.0.1:0/dead",
		ClientID:     "static",
		AuthorizeURL: "https://as/authorize",
		TokenURL:     "https://as/token",
	}
	got, err := f.resolveOAuthConfig(context.Background(), "srv", cfg, true)
	if err != nil {
		t.Fatalf("explicit config must not require discovery: %v", err)
	}
	if got.ClientID != "static" {
		t.Errorf("explicit client_id overridden: %+v", got)
	}
}

// TestResolveOAuthConfig_NoRegistrationFailsClosed: a server advertising
// no registration_endpoint and no configured client_id fails closed with
// a clear message.
func TestResolveOAuthConfig_NoRegistrationFailsClosed(t *testing.T) {
	withTempCredsDir(t)
	d := newDiscoveryServer(t)
	d.noRegEndpoint = true
	f := NewOAuthFlow()

	_, err := f.resolveOAuthConfig(context.Background(), "srv", OAuthServerConfig{ServerURL: d.url()}, true)
	if err == nil || !strings.Contains(err.Error(), "registration_endpoint") {
		t.Fatalf("want a fail-closed no-registration error, got: %v", err)
	}
}

// TestResolveOAuthConfig_RefreshNoDiscovery: the refresh path
// (allowDiscovery=false) never discovers — with no stored registration
// it errors, pointing at `forge mcp login`.
func TestResolveOAuthConfig_RefreshNoDiscovery(t *testing.T) {
	withTempCredsDir(t)
	d := newDiscoveryServer(t)
	f := NewOAuthFlow()

	_, err := f.resolveOAuthConfig(context.Background(), "srv", OAuthServerConfig{ServerURL: d.url()}, false)
	if err == nil || !strings.Contains(err.Error(), "forge mcp login") {
		t.Fatalf("refresh path must not discover; want login hint, got: %v", err)
	}
	if n := d.regCalls.Load(); n != 0 {
		t.Errorf("refresh path hit the registration endpoint %d times, want 0", n)
	}
}

// TestRegisteredOAuthHosts: after discovery persists a registration, the
// egress helper returns the authorize/token/registration hosts.
func TestRegisteredOAuthHosts(t *testing.T) {
	withTempCredsDir(t)
	d := newDiscoveryServer(t)
	f := NewOAuthFlow()
	if _, err := f.resolveOAuthConfig(context.Background(), "srv", OAuthServerConfig{ServerURL: d.url()}, true); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hosts := RegisteredOAuthHosts([]string{"srv"})
	wantHost := hostOf(d.srv.URL)
	found := false
	for _, h := range hosts {
		if h == wantHost {
			found = true
		}
	}
	if !found {
		t.Errorf("RegisteredOAuthHosts = %v, want it to include %q", hosts, wantHost)
	}
	// A server with no stored registration contributes nothing.
	if h := RegisteredOAuthHosts([]string{"never-logged-in"}); h != nil {
		t.Errorf("unregistered server yielded hosts: %v", h)
	}
}

// TestResourceMetadataParam covers the WWW-Authenticate parse edge cases.
func TestResourceMetadataParam(t *testing.T) {
	cases := map[string]string{
		`Bearer resource_metadata="https://as/.well-known/x"`:           "https://as/.well-known/x",
		`Bearer realm="r", resource_metadata="https://as/m", error="x"`: "https://as/m",
		`resource_metadata=https://as/m`:                                "https://as/m",
		`Bearer realm="r"`:                                              "",
		``:                                                              "",
	}
	for header, want := range cases {
		if got := resourceMetadataParam(header); got != want {
			t.Errorf("resourceMetadataParam(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestWellKnown pins the origin-rooted well-known path construction.
func TestWellKnown(t *testing.T) {
	cases := map[string]string{
		"https://as.example.com":            "https://as.example.com/.well-known/oauth-authorization-server",
		"https://as.example.com/":           "https://as.example.com/.well-known/oauth-authorization-server",
		"https://mcp.example.com/mcp?x=1":   "https://mcp.example.com/.well-known/oauth-authorization-server",
		"https://as.example.com/tenant/abc": "https://as.example.com/.well-known/oauth-authorization-server",
	}
	for base, want := range cases {
		if got := wellKnown(base, "oauth-authorization-server"); got != want {
			t.Errorf("wellKnown(%q) = %q, want %q", base, got, want)
		}
	}
}
