package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

// delegatedServer is a fake platform token endpoint for the type=user
// path (#317): it echoes the requested subject into the token, counts
// calls, and can 403 a named subject to simulate "no grant yet".
func delegatedServer(t *testing.T, calls *atomic.Int32, denySubject string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		body, _ := io.ReadAll(r.Body)
		var in struct{ Server, Subject string }
		_ = json.Unmarshal(body, &in)
		if in.Subject == "" {
			t.Errorf("delegated request missing subject: %s", body)
		}
		if in.Subject == denySubject {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-for-" + in.Subject,
			"expires_in":   3600,
		})
	}))
}

// TestDelegatedTokenSource_PerSubjectFetchAndCache: distinct users get
// distinct tokens (subject sent in the body), and the same user's token is
// cached — one fetch per subject.
func TestDelegatedTokenSource_PerSubjectFetchAndCache(t *testing.T) {
	var calls atomic.Int32
	srv := delegatedServer(t, &calls, "")
	defer srv.Close()

	d := newDelegatedTokenSource(PlatformSourceConfig{
		TokenEndpoint: srv.URL, AgentIdentity: "agent-cred", Ref: "atlassian", HTTPClient: srv.Client(),
	})
	ctx := context.Background()

	a, err := d.TokenForSubject(ctx, "alice@corp.com")
	if err != nil || a != "tok-for-alice@corp.com" {
		t.Fatalf("alice: %q %v", a, err)
	}
	b, err := d.TokenForSubject(ctx, "bob@corp.com")
	if err != nil || b != "tok-for-bob@corp.com" {
		t.Fatalf("bob: %q %v", b, err)
	}
	if a == b {
		t.Fatal("distinct users must get distinct tokens")
	}
	// alice again — served from the per-subject cache, no new fetch.
	if _, err := d.TokenForSubject(ctx, "alice@corp.com"); err != nil {
		t.Fatalf("alice cached: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("platform hit %d times, want 2 (one per distinct subject; alice cached)", n)
	}
}

// TestDelegatedTokenSource_NoGrantIsAuthRequired: a platform 403 for a
// subject (no grant yet) surfaces as ErrNoToken — auth-required, lazy —
// not a hard error.
func TestDelegatedTokenSource_NoGrantIsAuthRequired(t *testing.T) {
	srv := delegatedServer(t, nil, "carol@corp.com")
	defer srv.Close()
	d := newDelegatedTokenSource(PlatformSourceConfig{
		TokenEndpoint: srv.URL, AgentIdentity: "c", Ref: "r", HTTPClient: srv.Client(),
	})
	if _, err := d.TokenForSubject(context.Background(), "carol@corp.com"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no grant must be ErrNoToken (auth-required), got: %v", err)
	}
}

// TestBuildAuthFn_User_ResolvesSubjectFromContext: the type=user authFn
// pulls the requesting user from the request ctx and resolves that user's
// token; with no user in ctx it fails lazily with ErrNoToken.
func TestBuildAuthFn_User_ResolvesSubjectFromContext(t *testing.T) {
	srv := delegatedServer(t, nil, "")
	defer srv.Close()

	spec := types.MCPServer{
		Name: "atlassian-read", URL: "https://mcp.atlassian.com/mcp",
		Auth: &types.MCPAuth{Type: "user", Ref: "mcp.atlassian"},
	}
	deps := ServerDeps{
		HTTPClient: srv.Client(),
		Platform:   &types.PlatformConfig{TokenEndpoint: srv.URL, AgentIdentity: "agent-cred"},
	}
	authFn := buildAuthFn(spec, deps)

	// With an authenticated user in ctx → that user's token.
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Email: "dave@corp.com"})
	tok, err := authFn(ctx)
	if err != nil || tok != "tok-for-dave@corp.com" {
		t.Fatalf("resolved token = %q err=%v, want dave's token", tok, err)
	}

	// No user in ctx → lazy auth-required.
	if _, err := authFn(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no user in ctx must fail lazily with ErrNoToken, got: %v", err)
	}
}
