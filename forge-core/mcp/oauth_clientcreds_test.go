package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// ccTokenServer is a stand-in token endpoint for the client_credentials
// grant (#324). It records call count and asserts the request shape.
func ccTokenServer(t *testing.T, calls *atomic.Int32, deny bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", r.FormValue("grant_type"))
		}
		w.Header().Set("Content-Type", "application/json")
		if deny {
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"bad secret"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"cc-token","token_type":"bearer","expires_in":3600}`))
	}))
}

// TestBearerToken_ClientCredentials_MintsAndCaches: with no stored login
// token, the 2LO path mints on demand from client_id + secret (no
// ErrNoToken), caches, and does not re-mint within the expiry window.
func TestBearerToken_ClientCredentials_MintsAndCaches(t *testing.T) {
	withTempCredsDir(t)
	var calls atomic.Int32
	srv := ccTokenServer(t, &calls, false)
	defer srv.Close()

	f := NewOAuthFlow()
	cfg := OAuthServerConfig{
		Grant: grantClientCredentials, ClientID: "agent-1",
		ClientSecret: "sekret", TokenURL: srv.URL, Scopes: []string{"read"},
	}
	tok, err := f.BearerToken(context.Background(), "svc", cfg)
	if err != nil || tok != "cc-token" {
		t.Fatalf("mint: tok=%q err=%v (agent-principal must mint without a prior login)", tok, err)
	}
	tok2, err := f.BearerToken(context.Background(), "svc", cfg)
	if err != nil || tok2 != "cc-token" {
		t.Fatalf("cached: tok=%q err=%v", tok2, err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (second call served from cache)", n)
	}
}

// TestBearerToken_ClientCredentials_Denied: a denied grant surfaces as
// ErrTokenRevoked (invalid_client), not a transient transport error.
func TestBearerToken_ClientCredentials_Denied(t *testing.T) {
	withTempCredsDir(t)
	srv := ccTokenServer(t, nil, true)
	defer srv.Close()

	f := NewOAuthFlow()
	cfg := OAuthServerConfig{Grant: grantClientCredentials, ClientID: "a", ClientSecret: "wrong", TokenURL: srv.URL}
	if _, err := f.BearerToken(context.Background(), "svc", cfg); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("want ErrTokenRevoked for invalid_client, got %v", err)
	}
}

// TestBuildAuthFn_ClientCredentials_ResolvesSecretFromEnv exercises the
// full server path: buildAuthFn resolves the client secret from the
// named env var at call time (so a rotated Secret takes effect) → mints.
func TestBuildAuthFn_ClientCredentials_ResolvesSecretFromEnv(t *testing.T) {
	withTempCredsDir(t)
	t.Setenv("MY_MCP_SECRET", "env-secret")
	var gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSecret = r.FormValue("client_secret")
		_, _ = w.Write([]byte(`{"access_token":"t","token_type":"bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	flow := NewOAuthFlow()
	spec := types.MCPServer{
		Name: "svc", URL: "https://x",
		Auth: &types.MCPAuth{
			Type: "oauth", Grant: "client_credentials",
			ClientID: "a", ClientSecretEnv: "MY_MCP_SECRET", TokenURL: srv.URL,
		},
	}
	authFn := buildAuthFn(spec, flow)
	if authFn == nil {
		t.Fatal("buildAuthFn returned nil for a client_credentials server")
	}
	tok, err := authFn(context.Background())
	if err != nil || tok != "t" {
		t.Fatalf("authFn: tok=%q err=%v", tok, err)
	}
	if gotSecret != "env-secret" {
		t.Errorf("client_secret sent = %q, want the value from $MY_MCP_SECRET", gotSecret)
	}
}

// TestBuildAuthFn_ClientCredentials_EmptySecret: when the named secret env
// var resolves to "" (the common headless misconfig — platform didn't
// inject it), the authFn fails closed with a clear cause and never hits
// the token endpoint (#325 review finding 1).
func TestBuildAuthFn_ClientCredentials_EmptySecret(t *testing.T) {
	withTempCredsDir(t)
	t.Setenv("UNSET_MCP_SECRET", "") // configured by name, empty value
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"x","token_type":"bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	flow := NewOAuthFlow()
	spec := types.MCPServer{
		Name: "svc", URL: "https://x",
		Auth: &types.MCPAuth{
			Type: "oauth", Grant: "client_credentials",
			ClientID: "a", ClientSecretEnv: "UNSET_MCP_SECRET", TokenURL: srv.URL,
		},
	}
	_, err := buildAuthFn(spec, flow)(context.Background())
	if err == nil || !strings.Contains(err.Error(), "resolved to an empty value") {
		t.Fatalf("want a clear empty-secret error, got %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("token endpoint hit %d times, want 0 (should fail before minting)", n)
	}
}
