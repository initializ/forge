package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-core/validate"
)

// TestB6_NewServer_RejectsUnknownAuthType pins the review-B6 fix.
// A typoed auth.Type (capital "B" in "Bearer", or any other
// unknown value) used to fall through buildAuthFn and produce a
// silently-unauthenticated transport — the only signal was a
// distant 401/403 from the real server. NewServer now rejects
// the spec at construction time.
func TestB6_NewServer_RejectsUnknownAuthType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		authT   string
		wantSub string
	}{
		{"capitalized Bearer", "Bearer", "unknown auth.type"},
		{"capitalized OAuth", "OAuth", "unknown auth.type"},
		{"trailing space", "bearer ", "unknown auth.type"},
		{"empty when block set", "", "auth.type is required"},
		{"plural typo", "bearers", "unknown auth.type"},
		{"capital-S Static", "Static", "unknown auth.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewServer(types.MCPServer{
				Name: "typo", Transport: "http", URL: "http://x",
				Auth:  &types.MCPAuth{Type: tc.authT, TokenEnv: "T"},
				Tools: types.MCPToolFilter{Allow: []string{"x"}},
			}, ServerDeps{HTTPClient: http.DefaultClient})
			if err == nil {
				t.Fatalf("expected error for auth.type=%q", tc.authT)
			}
			if !errors.Is(err, ErrProtocolError) {
				t.Errorf("err should wrap ErrProtocolError, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err lacks %q: %v", tc.wantSub, err)
			}
		})
	}
}

// TestB6_NewServer_RequiresTokenEnvForBearerStatic — adjacent silent-
// no-auth path: type=bearer/static with empty TokenEnv would have
// silently produced "" Authorization headers.
func TestB6_NewServer_RequiresTokenEnvForBearerStatic(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"bearer", "static"} {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			_, err := NewServer(types.MCPServer{
				Name: "x", Transport: "http", URL: "http://x",
				Auth:  &types.MCPAuth{Type: typ},
				Tools: types.MCPToolFilter{Allow: []string{"x"}},
			}, ServerDeps{HTTPClient: http.DefaultClient})
			if err == nil {
				t.Fatalf("expected error for type=%s with empty TokenEnv", typ)
			}
			if !strings.Contains(err.Error(), "token_env is required") {
				t.Errorf("err lacks hint: %v", err)
			}
		})
	}
}

// TestB6_NewServer_RejectsPartialOAuthEndpoints covers the adjacent
// failure mode after #316: the endpoints may be discovered, so the trio
// is no longer required — but a PARTIAL endpoint config (one of
// authorize_url/token_url set, the other empty) is still rejected.
func TestB6_NewServer_RejectsPartialOAuthEndpoints(t *testing.T) {
	t.Parallel()
	full := &types.MCPAuth{
		Type: "oauth", ClientID: "c",
		AuthorizeURL: "https://x/a", TokenURL: "https://x/t",
	}
	cases := []struct {
		name    string
		mutate  func(*types.MCPAuth)
		wantSub string
	}{
		{"only token_url", func(a *types.MCPAuth) { a.AuthorizeURL = "" }, "must be set together"},
		{"only authorize_url", func(a *types.MCPAuth) { a.TokenURL = "" }, "must be set together"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := *full
			tc.mutate(&a)
			_, err := NewServer(types.MCPServer{
				Name: "oa", Transport: "http", URL: "http://x",
				Auth:  &a,
				Tools: types.MCPToolFilter{Allow: []string{"x"}},
			}, ServerDeps{HTTPClient: http.DefaultClient, OAuth: NewOAuthFlow()})
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err lacks %q: %v", tc.wantSub, err)
			}
		})
	}
}

// TestB6_NewServer_AcceptsDiscoveryOAuth: with #316, an oauth server
// that omits client_id AND both endpoints (relying on discovery from
// the url) constructs successfully.
func TestB6_NewServer_AcceptsDiscoveryOAuth(t *testing.T) {
	t.Parallel()
	_, err := NewServer(types.MCPServer{
		Name: "oa", Transport: "http", URL: "https://mcp.example.com/mcp",
		Auth:  &types.MCPAuth{Type: "oauth", Scopes: []string{"read"}},
		Tools: types.MCPToolFilter{Allow: []string{"x"}},
	}, ServerDeps{HTTPClient: http.DefaultClient, OAuth: NewOAuthFlow()})
	if err != nil {
		t.Fatalf("discovery-based oauth should construct, got: %v", err)
	}
}

// TestB6_NewServer_AcceptsKnownTypes_SanityCheck pins the inverse
// of the rejection tests: every legal Auth.Type still constructs.
func TestB6_NewServer_AcceptsKnownTypes_SanityCheck(t *testing.T) {
	t.Parallel()
	bearerOrStatic := func(typ string) *types.MCPAuth {
		return &types.MCPAuth{Type: typ, TokenEnv: "MY_TOKEN"}
	}
	oauth := &types.MCPAuth{
		Type: "oauth", ClientID: "c",
		AuthorizeURL: "https://x/a", TokenURL: "https://x/t",
	}
	cases := []struct {
		name string
		auth *types.MCPAuth
		oa   *OAuthFlow
	}{
		{"bearer", bearerOrStatic("bearer"), nil},
		{"static", bearerOrStatic("static"), nil},
		{"oauth", oauth, NewOAuthFlow()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewServer(types.MCPServer{
				Name: "ok", Transport: "http", URL: "http://x",
				Auth:  tc.auth,
				Tools: types.MCPToolFilter{Allow: []string{"x"}},
			}, ServerDeps{HTTPClient: http.DefaultClient, OAuth: tc.oa})
			if err != nil {
				t.Fatalf("%s should construct: %v", tc.name, err)
			}
		})
	}
}

// TestB6_KnownAuthTypes_MatchValidate keeps mcp.knownMCPAuthTypes
// and validate's auth-type set in lockstep. They're physically
// duplicated to avoid an mcp→validate import; this test catches
// any drift between them.
func TestB6_KnownAuthTypes_MatchValidate(t *testing.T) {
	t.Parallel()
	// Probe by running validate against every type mcp accepts and
	// every plausible typo we reject. validate must produce the
	// same accept/reject decision.
	for typ := range knownMCPAuthTypes {
		r := &validate.ValidationResult{}
		validate.ValidateMCPConfig(types.MCPConfig{Servers: []types.MCPServer{{
			Name: "x", Transport: "http", URL: "http://x",
			Tools: types.MCPToolFilter{Allow: []string{"x"}},
			Auth:  &types.MCPAuth{Type: typ, TokenEnv: "T", ClientID: "c", AuthorizeURL: "https://x/a", TokenURL: "https://x/t"},
		}}}, r)
		hasTypeErr := false
		for _, e := range r.Errors {
			if strings.Contains(e, "type "+typ) || strings.Contains(e, "must be one of") {
				hasTypeErr = true
				break
			}
		}
		if hasTypeErr {
			t.Errorf("mcp accepts %q but validate rejects it — packages drifted", typ)
		}
	}
	// Inverse: a clearly-bad typo must be rejected by both.
	for _, bad := range []string{"Bearer", "OAuth", "bearers", "Static"} {
		// mcp side.
		if knownMCPAuthTypes[bad] {
			t.Errorf("mcp.knownMCPAuthTypes unexpectedly contains %q", bad)
		}
		// validate side.
		r := &validate.ValidationResult{}
		validate.ValidateMCPConfig(types.MCPConfig{Servers: []types.MCPServer{{
			Name: "x", Transport: "http", URL: "http://x",
			Tools: types.MCPToolFilter{Allow: []string{"x"}},
			Auth:  &types.MCPAuth{Type: bad, TokenEnv: "T"},
		}}}, r)
		joined := strings.Join(r.Errors, " | ")
		if !strings.Contains(joined, "must be one of") {
			t.Errorf("validate accepts %q (mcp rejects) — packages drifted: %v", bad, r.Errors)
		}
	}
}

// TestB6_BuildAuthFn_UnknownType_FailsOnFirstCall covers the
// defense-in-depth path: even if a future refactor lets an
// unknown Type past NewServer, buildAuthFn returns a func that
// errors out instead of producing "" silently.
func TestB6_BuildAuthFn_UnknownType_FailsOnFirstCall(t *testing.T) {
	t.Parallel()
	fn := buildAuthFn(types.MCPServer{
		Name: "x",
		Auth: &types.MCPAuth{Type: "Bearer"},
	}, ServerDeps{})
	if fn == nil {
		t.Fatal("buildAuthFn returned nil for unknown type — silent no-auth (the B6 regression)")
	}
	_, err := fn(context.Background())
	if err == nil {
		t.Fatal("expected error from defense-in-depth AuthTokenFunc")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("err = %v, want wrap of ErrProtocolError", err)
	}
}

// TestB6_E2E_TypoNeverSendsHeader proves end-to-end that the typo
// cannot reach the wire. The mock server records every
// Authorization header; we attempt to construct + run the Server,
// expect construction failure, expect ZERO HTTP hits.
func TestB6_E2E_TypoNeverSendsHeader(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := NewServer(types.MCPServer{
		Name: "typo", Transport: "http", URL: srv.URL,
		Auth:  &types.MCPAuth{Type: "Bearer", TokenEnv: "MY_TOKEN"}, // typo
		Tools: types.MCPToolFilter{Allow: []string{"x"}},
	}, ServerDeps{HTTPClient: srv.Client()})
	if err == nil {
		t.Fatal("expected construction error for typoed auth.type")
	}
	if hits.Load() != 0 {
		t.Errorf("server received %d HTTP hits despite construction failure — leaked traffic", hits.Load())
	}
}
