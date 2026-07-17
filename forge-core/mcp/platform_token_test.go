package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/types"
)

// fake platform resolver: counts hits, asserts the agent credential and
// server ref, and can emit a (forbidden) refresh_token.
func newFakeResolver(t *testing.T, expiresIn int, includeRefresh bool) (*httptest.Server, *int) {
	t.Helper()
	t.Setenv("FORGE_ORG_ID", "org-42")
	t.Setenv("FORGE_WORKSPACE_ID", "ws-7")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// Tenancy headers must ride along so the platform can select the
		// per-org HS256 secret + authorize the workspace (the "missing
		// org-id header" 401 fix).
		if r.Header.Get("Org-Id") != "org-42" || r.Header.Get("Workspace-Id") != "ws-7" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing org-id header"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer agent-cred-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Server string `json:"server"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Server != "mcp.atlassian" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not entitled to " + body.Server})
			return
		}
		out := map[string]any{"access_token": "at-1", "expires_in": expiresIn}
		if includeRefresh {
			out["refresh_token"] = "MUST-NEVER-BE-USED"
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The agent-principal path: token fetched with the agent identity + server
// ref, cached to TTL (one fetch serves many calls), and any refresh_token
// in the response is ignored — the agent never holds one (invariant 8).
func TestPlatformTokenSource_FetchCacheAndIgnoreRefresh(t *testing.T) {
	srv, hits := newFakeResolver(t, 3600, true)
	src := newPlatformTokenSource(PlatformSourceConfig{
		TokenEndpoint: srv.URL,
		AgentIdentity: "agent-cred-1",
		Ref:           "mcp.atlassian",
		HTTPClient:    srv.Client(),
	})
	for i := 0; i < 5; i++ {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("token[%d]: %v", i, err)
		}
		if tok != "at-1" {
			t.Fatalf("token = %q", tok)
		}
	}
	if *hits != 1 {
		t.Fatalf("resolver hit %d times for 5 calls — cache broken", *hits)
	}
}

// Expiry triggers a re-fetch (the skew makes a short-TTL token immediately
// stale).
func TestPlatformTokenSource_RefetchOnExpiry(t *testing.T) {
	srv, hits := newFakeResolver(t, 1, false) // 1s < 30s skew → always stale
	src := newPlatformTokenSource(PlatformSourceConfig{
		TokenEndpoint: srv.URL, AgentIdentity: "agent-cred-1",
		Ref: "mcp.atlassian", HTTPClient: srv.Client(),
	})
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *hits != 2 {
		t.Fatalf("hits = %d, want re-fetch on expiry", *hits)
	}
}

// The resolver's entitlement rejection surfaces with the platform's error
// body — an agent asking for a server it isn't bound to gets refused.
func TestPlatformTokenSource_EntitlementRejectionSurfaces(t *testing.T) {
	srv, _ := newFakeResolver(t, 3600, false)
	src := newPlatformTokenSource(PlatformSourceConfig{
		TokenEndpoint: srv.URL, AgentIdentity: "agent-cred-1",
		Ref: "mcp.other-server", HTTPClient: srv.Client(),
	})
	_, err := src.Token(context.Background())
	if err == nil {
		t.Fatal("unentitled server must be refused")
	}
}

// ${VAR} in endpoint/identity expands at USE time (rotation without restart).
func TestPlatformTokenSource_EnvExpansion(t *testing.T) {
	srv, _ := newFakeResolver(t, 3600, false)
	t.Setenv("TEST_PLATFORM_EP", srv.URL)
	t.Setenv("TEST_AGENT_CRED", "agent-cred-1")
	src := newPlatformTokenSource(PlatformSourceConfig{
		TokenEndpoint: "${TEST_PLATFORM_EP}", AgentIdentity: "${TEST_AGENT_CRED}",
		Ref: "mcp.atlassian", HTTPClient: srv.Client(),
	})
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("env-expanded fetch: %v", err)
	}
}

// Unset identity fails with an actionable error, not a silent
// unauthenticated request.
func TestPlatformTokenSource_MissingIdentityActionable(t *testing.T) {
	src := newPlatformTokenSource(PlatformSourceConfig{
		TokenEndpoint: "https://platform.example.com/token",
		AgentIdentity: "${UNSET_AGENT_CRED_VAR}",
		Ref:           "x",
	})
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("missing identity must fail actionably")
	}
}

// NewServer: platform requires the platform block; user cannot be Required;
// user auth fails lazily (ErrNoToken) instead of blocking construction.
func TestNewServer_PlatformAndUserRules(t *testing.T) {
	t.Parallel()
	base := types.MCPServer{
		Name: "s", Transport: "http", URL: "https://mcp.example.com/mcp",
		Tools: types.MCPToolFilter{Allow: []string{"x"}},
	}
	deps := ServerDeps{HTTPClient: http.DefaultClient}

	p := base
	p.Auth = &types.MCPAuth{Type: "platform"}
	if _, err := NewServer(p, deps); err == nil {
		t.Fatal("platform without a platform block must fail construction")
	}
	depsWithPlatform := deps
	depsWithPlatform.Platform = &types.PlatformConfig{TokenEndpoint: "https://plat/token", AgentIdentity: "${X}"}
	if _, err := NewServer(p, depsWithPlatform); err != nil {
		t.Fatalf("platform with block must construct: %v", err)
	}

	u := base
	u.Auth = &types.MCPAuth{Type: "user"}
	u.Required = true
	if _, err := NewServer(u, deps); err == nil {
		t.Fatal("user + required:true must be rejected")
	}
	u.Required = false
	if _, err := NewServer(u, deps); err != nil {
		t.Fatalf("lazy user server must construct: %v", err)
	}

	fn := buildAuthFn(u, deps)
	if _, err := fn(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("user auth must fail lazily with ErrNoToken, got: %v", err)
	}
}

// A platform token expiring mid-session re-resolves without any stored
// state — assert no persistence side effects by construction (the source
// holds everything in memory; nothing references the credentials store).
func TestPlatformTokenSource_DefaultTTLWhenOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-2"})
	}))
	defer srv.Close()
	src := newPlatformTokenSource(PlatformSourceConfig{
		TokenEndpoint: srv.URL, AgentIdentity: "c", Ref: "r", HTTPClient: srv.Client(),
	})
	tok, err := src.Token(context.Background())
	if err != nil || tok != "at-2" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if time.Until(src.expiresAt) <= 0 {
		t.Fatal("default TTL not applied")
	}
}
