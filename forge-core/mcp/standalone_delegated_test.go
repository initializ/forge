package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

// standaloneUserSpec is a valid standalone (#332) delegated server: type=user,
// no platform block, explicit endpoints + client_id, authorization_code grant.
func standaloneUserSpec() types.MCPServer {
	return types.MCPServer{
		Name: "atlassian", URL: "https://mcp.atlassian.example/mcp",
		Auth: &types.MCPAuth{
			Type:         "user",
			ClientID:     "forge-client",
			AuthorizeURL: "https://idp.example/authorize",
			TokenURL:     "https://idp.example/token",
			Scopes:       []string{"read", "write"},
		},
	}
}

// TestStandaloneUser_ResolverReadsSubjectStore proves the standalone resolver:
// no user → ErrNoToken; user with an empty store → ErrNoToken (trips the gate);
// user with a stored token → that token.
func TestStandaloneUser_ResolverReadsSubjectStore(t *testing.T) {
	store := newMemSubjectTokenStore(0)
	authFn := buildAuthFn(standaloneUserSpec(), ServerDeps{
		HTTPClient:   http.DefaultClient,
		SubjectStore: store,
	})
	if authFn == nil {
		t.Fatal("buildAuthFn returned nil for standalone type=user")
	}

	// No requesting user → lazy ErrNoToken.
	if _, err := authFn(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no user in ctx must fail with ErrNoToken, got %v", err)
	}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Email: "dave@corp.com"})

	// User but no grant yet → ErrNoToken (the auth-required gate parks here).
	if _, err := authFn(ctx); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no stored grant must fail with ErrNoToken, got %v", err)
	}

	// Consent completes → the callback populates the store → resolver returns it.
	store.Put("dave@corp.com", "access-dave", time.Hour)
	tok, err := authFn(ctx)
	if err != nil || tok != "access-dave" {
		t.Fatalf("after Put: token=%q err=%v, want access-dave", tok, err)
	}

	// Per-subject isolation: a different user still has no grant.
	other := auth.WithIdentity(context.Background(), &auth.Identity{Email: "eve@corp.com"})
	if _, err := authFn(other); !errors.Is(err, ErrNoToken) {
		t.Fatalf("unrelated subject must still fail ErrNoToken, got %v", err)
	}
}

// TestStandaloneUser_NewServerValidation pins NewServer's accept/reject rules
// for standalone type=user (no platform block).
func TestStandaloneUser_NewServerValidation(t *testing.T) {
	store := newMemSubjectTokenStore(0)
	baseDeps := ServerDeps{HTTPClient: http.DefaultClient, SubjectStore: store}

	t.Run("valid standalone is accepted", func(t *testing.T) {
		if _, err := NewServer(standaloneUserSpec(), baseDeps); err != nil {
			t.Fatalf("valid standalone type=user rejected: %v", err)
		}
	})

	t.Run("missing explicit endpoints rejected", func(t *testing.T) {
		spec := standaloneUserSpec()
		spec.Auth.AuthorizeURL = "" // no discovery at runtime
		if _, err := NewServer(spec, baseDeps); err == nil {
			t.Fatal("standalone type=user without explicit endpoints must be rejected")
		}
	})

	t.Run("client_credentials grant rejected", func(t *testing.T) {
		spec := standaloneUserSpec()
		spec.Auth.Grant = "client_credentials"
		if _, err := NewServer(spec, baseDeps); err == nil {
			t.Fatal("standalone type=user with grant client_credentials must be rejected")
		}
	})

	t.Run("missing SubjectStore rejected", func(t *testing.T) {
		if _, err := NewServer(standaloneUserSpec(), ServerDeps{HTTPClient: http.DefaultClient}); err == nil {
			t.Fatal("standalone type=user without a SubjectStore must be rejected")
		}
	})

	t.Run("required:true still rejected", func(t *testing.T) {
		spec := standaloneUserSpec()
		spec.Required = true
		if _, err := NewServer(spec, baseDeps); err == nil {
			t.Fatal("type=user + required:true must be rejected")
		}
	})

	t.Run("explicit authorization_code grant accepted", func(t *testing.T) {
		spec := standaloneUserSpec()
		spec.Auth.Grant = "authorization_code"
		if _, err := NewServer(spec, baseDeps); err != nil {
			t.Fatalf("explicit authorization_code grant rejected: %v", err)
		}
	})
}
