package runtime

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
)

// E2E tests covering the auth chain that the runner builds from legacy
// CLI flags (--auth-token, --auth-url, --auth-org-id). These exist to
// guarantee that the PR1 refactor preserves the pre-refactor behavior
// byte-for-byte. If any of these fail, the refactor changed externally
// observable behavior — investigate before merging.

// fakeVerifier accepts a handler and returns a httptest.Server that
// implements the http_verifier contract.
func newFakeVerifier(t *testing.T, handler func(req map[string]any) (status int, body any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		status, respBody := handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if respBody != nil {
			_ = json.NewEncoder(w).Encode(respBody)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildLegacyAuthChain_NoAuth(t *testing.T) {
	chain, err := buildLegacyAuthChain("", "", "")
	if err != nil {
		t.Fatalf("buildLegacyAuthChain: %v", err)
	}
	if chain != nil {
		t.Errorf("chain = %v, want nil (no providers configured)", chain)
	}
}

func TestBuildLegacyAuthChain_TokenOnly(t *testing.T) {
	chain, err := buildLegacyAuthChain("local-token", "", "")
	if err != nil {
		t.Fatalf("buildLegacyAuthChain: %v", err)
	}
	if chain == nil {
		t.Fatal("chain is nil, want a ChainProvider with static_token")
	}

	// Valid token accepted.
	id, err := chain.Verify(context.Background(), "local-token", nil)
	if err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	if id == nil || id.Source != "internal" {
		t.Errorf("identity = %+v, want Source=internal", id)
	}

	// Wrong token yields with ErrTokenNotForMe (and at chain end, that's
	// what we see — no http_verifier behind to claim it).
	_, err = chain.Verify(context.Background(), "wrong", nil)
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("wrong token err = %v, want ErrTokenNotForMe", err)
	}
}

func TestBuildLegacyAuthChain_AuthURLOnly(t *testing.T) {
	srv := newFakeVerifier(t, func(req map[string]any) (int, any) {
		if req["token"] == "external-token" {
			return http.StatusOK, map[string]any{
				"valid":   true,
				"user_id": "external-user",
				"org_id":  "external-org",
			}
		}
		return http.StatusOK, map[string]any{"valid": false}
	})

	chain, err := buildLegacyAuthChain("", srv.URL, "default-org")
	if err != nil {
		t.Fatalf("buildLegacyAuthChain: %v", err)
	}
	if chain == nil {
		t.Fatal("chain is nil, want http_verifier chain")
	}

	id, err := chain.Verify(context.Background(), "external-token", nil)
	if err != nil {
		t.Errorf("external token rejected: %v", err)
	}
	if id == nil || id.UserID != "external-user" || id.Source != "http_verifier" {
		t.Errorf("identity = %+v", id)
	}

	_, err = chain.Verify(context.Background(), "wrong", nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("wrong token err = %v, want ErrTokenRejected", err)
	}
}

func TestBuildLegacyAuthChain_TokenAndAuthURL_LoopbackShortCircuits(t *testing.T) {
	// This is the pre-refactor behavior: when both an internal Token AND
	// AuthURL are set, the internal token is checked FIRST so loopback
	// calls from channel adapters don't hit the external verifier.
	var verifierCalls atomic.Int32
	srv := newFakeVerifier(t, func(map[string]any) (int, any) {
		verifierCalls.Add(1)
		return http.StatusOK, map[string]any{"valid": true, "user_id": "external"}
	})

	chain, err := buildLegacyAuthChain("loopback-token", srv.URL, "")
	if err != nil {
		t.Fatalf("buildLegacyAuthChain: %v", err)
	}

	// Loopback token: external verifier should NOT be called.
	id, err := chain.Verify(context.Background(), "loopback-token", nil)
	if err != nil {
		t.Fatalf("loopback verify: %v", err)
	}
	if id.UserID != "forge-internal" || id.Source != "internal" {
		t.Errorf("loopback identity = %+v, want forge-internal/internal", id)
	}
	if got := verifierCalls.Load(); got != 0 {
		t.Errorf("external verifier called %d times during loopback (must be 0)", got)
	}

	// Non-loopback token: falls through to external verifier.
	id2, err := chain.Verify(context.Background(), "other-token", nil)
	if err != nil {
		t.Fatalf("external verify: %v", err)
	}
	if id2.Source != "http_verifier" {
		t.Errorf("external identity Source = %q, want http_verifier", id2.Source)
	}
	if got := verifierCalls.Load(); got != 1 {
		t.Errorf("external verifier called %d times, want 1", got)
	}
}

func TestBuildLegacyAuthChain_OrgIDPropagation(t *testing.T) {
	var capturedOrgID atomic.Value
	srv := newFakeVerifier(t, func(req map[string]any) (int, any) {
		capturedOrgID.Store(req["org_id"])
		return http.StatusOK, map[string]any{"valid": true, "user_id": "u"}
	})

	chain, _ := buildLegacyAuthChain("", srv.URL, "default-org")

	// No headers → config default.
	if _, err := chain.Verify(context.Background(), "tok", nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := capturedOrgID.Load(); got != "default-org" {
		t.Errorf("verifier got org_id %v, want default-org", got)
	}

	// X-Org-ID header overrides default.
	if _, err := chain.Verify(context.Background(), "tok", auth.Headers{"X-Org-ID": "header-org"}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := capturedOrgID.Load(); got != "header-org" {
		t.Errorf("verifier got org_id %v, want header-org", got)
	}
}

// --- middleware-level end-to-end ---

// runMiddleware is a small driver that runs an http.Handler chain through
// the auth.Middleware with a given chain and returns the recorded response.
func runMiddleware(t *testing.T, chain auth.Provider, method, path, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	handler := auth.Middleware(auth.MiddlewareOptions{
		Chain:     chain,
		SkipPaths: auth.DefaultSkipPaths(),
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestE2E_NoAuthConfigured_AnonymousAccess(t *testing.T) {
	chain, _ := buildLegacyAuthChain("", "", "")
	// nil chain → middleware passthrough
	rr := runMiddleware(t, chain, "POST", "/tasks/send", "")
	if rr.Code != http.StatusOK {
		t.Errorf("anonymous access blocked: status = %d", rr.Code)
	}
}

func TestE2E_LegacyAuthURL_HappyPath(t *testing.T) {
	srv := newFakeVerifier(t, func(req map[string]any) (int, any) {
		if req["token"] == "good" {
			return http.StatusOK, map[string]any{"valid": true, "user_id": "u"}
		}
		return http.StatusOK, map[string]any{"valid": false}
	})

	chain, _ := buildLegacyAuthChain("", srv.URL, "")
	rr := runMiddleware(t, chain, "POST", "/tasks/send", "Bearer good")
	if rr.Code != http.StatusOK {
		t.Errorf("good token rejected: status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestE2E_LegacyAuthURL_BadTokenReturns401(t *testing.T) {
	srv := newFakeVerifier(t, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"valid": false}
	})

	chain, _ := buildLegacyAuthChain("", srv.URL, "")
	rr := runMiddleware(t, chain, "POST", "/tasks/send", "Bearer bad")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("bad token accepted: status = %d", rr.Code)
	}
}

func TestE2E_SkipPathsBypassAuth(t *testing.T) {
	srv := newFakeVerifier(t, func(map[string]any) (int, any) {
		t.Error("verifier should not be called for skip paths")
		return http.StatusOK, map[string]any{"valid": false}
	})

	chain, _ := buildLegacyAuthChain("", srv.URL, "")

	skipPaths := []struct {
		method, path string
	}{
		{"GET", "/"},
		{"GET", "/.well-known/agent.json"},
		{"GET", "/healthz"},
		{"GET", "/health"},
	}

	for _, sp := range skipPaths {
		t.Run(sp.method+" "+sp.path, func(t *testing.T) {
			rr := runMiddleware(t, chain, sp.method, sp.path, "")
			if rr.Code != http.StatusOK {
				t.Errorf("skip path %s %s blocked: status = %d", sp.method, sp.path, rr.Code)
			}
		})
	}
}

func TestE2E_ChannelLoopback_DoesNotCallVerifier(t *testing.T) {
	// Simulates a Slack/Telegram adapter calling the local A2A server
	// with the internal loopback token. The external verifier must NOT
	// be consulted — that would be wasteful and would break if the
	// verifier is misconfigured/unreachable.
	var verifierCalls atomic.Int32
	srv := newFakeVerifier(t, func(map[string]any) (int, any) {
		verifierCalls.Add(1)
		return http.StatusOK, map[string]any{"valid": true, "user_id": "external"}
	})

	chain, _ := buildLegacyAuthChain("loopback-secret", srv.URL, "")

	rr := runMiddleware(t, chain, "POST", "/tasks/send", "Bearer loopback-secret")
	if rr.Code != http.StatusOK {
		t.Errorf("loopback token rejected: status = %d", rr.Code)
	}
	if got := verifierCalls.Load(); got != 0 {
		t.Errorf("external verifier called %d times for loopback (must be 0)", got)
	}
}

func TestE2E_VerifierUnreachable_ReturnsInvalidNot500(t *testing.T) {
	// When the external verifier is down (port closed), the middleware
	// should return 401, not 500 — preserving the pre-refactor "auth
	// provider error" behavior.
	chain, _ := buildLegacyAuthChain("", "http://127.0.0.1:1/never", "")
	rr := runMiddleware(t, chain, "POST", "/tasks/send", "Bearer x")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("verifier-unreachable status = %d, want 401", rr.Code)
	}
}

func TestE2E_WireFormat_TokenAndOrgID(t *testing.T) {
	// Black-box: confirm the verifier sees exactly `token` and `org_id`
	// JSON fields. Guards against future schema drift.
	var sawToken, sawOrgID atomic.Value
	srv := newFakeVerifier(t, func(req map[string]any) (int, any) {
		sawToken.Store(req["token"])
		sawOrgID.Store(req["org_id"])
		return http.StatusOK, map[string]any{"valid": true, "user_id": "u"}
	})

	chain, _ := buildLegacyAuthChain("", srv.URL, "")
	rr := runMiddleware(t, chain, "POST", "/tasks/send", "Bearer the-token")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := sawToken.Load(); got != "the-token" {
		t.Errorf("verifier got token = %v, want the-token", got)
	}
	// No org headers sent → verifier sees empty org_id.
	if got := sawOrgID.Load(); got != "" {
		t.Errorf("verifier got org_id = %v, want empty", got)
	}
}
