package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

// E2E coverage for PR3's forge.yaml `auth:` block → ChainProvider wiring.

// fakeVerifierFor is a test verifier that accepts `goodToken` and rejects
// everything else.
func fakeVerifierFor(t *testing.T, goodToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["token"] == goodToken {
			_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "user_id": "u"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": false})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildChainFromConfig_Empty(t *testing.T) {
	chain, err := buildChainFromConfig(types.AuthConfig{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if chain != nil {
		t.Errorf("chain = %v, want nil for empty config", chain)
	}
}

func TestBuildChainFromConfig_SingleHTTPVerifier(t *testing.T) {
	srv := fakeVerifierFor(t, "good")
	chain, err := buildChainFromConfig(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "http_verifier", Settings: map[string]any{"url": srv.URL}},
		},
	})
	if err != nil {
		t.Fatalf("buildChainFromConfig: %v", err)
	}
	id, err := chain.Verify(context.Background(), "good", nil)
	if err != nil || id == nil {
		t.Errorf("good token rejected: id=%v err=%v", id, err)
	}
	if _, err := chain.Verify(context.Background(), "bad", nil); !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("bad token err = %v, want ErrTokenRejected", err)
	}
}

func TestBuildChainFromConfig_StaticToken(t *testing.T) {
	t.Setenv("FORGE_TEST_STATIC_TOKEN", "dev-secret")

	chain, err := buildChainFromConfig(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "static_token", Settings: map[string]any{"token_env": "FORGE_TEST_STATIC_TOKEN"}},
		},
	})
	if err != nil {
		t.Fatalf("buildChainFromConfig: %v", err)
	}
	if _, err := chain.Verify(context.Background(), "dev-secret", nil); err != nil {
		t.Errorf("dev-secret rejected: %v", err)
	}
}

func TestBuildChainFromConfig_OrderedChain(t *testing.T) {
	// Verify first-match-wins ordering: a static_token at position 1
	// short-circuits an http_verifier at position 2.
	verifierHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		verifierHit = true
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "user_id": "external"})
	}))
	defer srv.Close()

	t.Setenv("FORGE_TEST_LOOPBACK_TOKEN", "loopback")

	chain, err := buildChainFromConfig(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "static_token", Settings: map[string]any{"token_env": "FORGE_TEST_LOOPBACK_TOKEN"}},
			{Type: "http_verifier", Settings: map[string]any{"url": srv.URL}},
		},
	})
	if err != nil {
		t.Fatalf("buildChainFromConfig: %v", err)
	}

	// Loopback token matches first provider — verifier must NOT be called.
	id, err := chain.Verify(context.Background(), "loopback", nil)
	if err != nil {
		t.Fatalf("loopback verify: %v", err)
	}
	if id.Source != "static_token" {
		t.Errorf("identity.Source = %q, want static_token", id.Source)
	}
	if verifierHit {
		t.Error("http_verifier was called despite static_token match (chain ordering broken)")
	}

	// Different token falls through to verifier.
	if _, err := chain.Verify(context.Background(), "other", nil); err != nil {
		t.Fatalf("fallthrough verify: %v", err)
	}
	if !verifierHit {
		t.Error("http_verifier was not called for non-loopback token (chain ordering broken)")
	}
}

func TestBuildChainFromConfig_InvalidProviderType(t *testing.T) {
	_, err := buildChainFromConfig(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "ldap"}, // not registered
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown provider type")
	}
}

func TestBuildChainFromConfig_FactoryError(t *testing.T) {
	_, err := buildChainFromConfig(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc"}, // missing issuer + audience
		},
	})
	if err == nil {
		t.Fatal("expected error for oidc without issuer/audience")
	}
	if !errors.Is(err, auth.ErrProviderNotConfigured) {
		t.Errorf("err = %v, want wrapped ErrProviderNotConfigured", err)
	}
}

func TestBuildLegacyHTTPVerifierChain_HappyPath(t *testing.T) {
	srv := fakeVerifierFor(t, "ok")
	chain, err := buildLegacyHTTPVerifierChain(srv.URL, "default-org")
	if err != nil {
		t.Fatalf("buildLegacyHTTPVerifierChain: %v", err)
	}
	if _, err := chain.Verify(context.Background(), "ok", nil); err != nil {
		t.Errorf("verify failed: %v", err)
	}
}

func TestBuildLegacyHTTPVerifierChain_EmptyURLFails(t *testing.T) {
	if _, err := buildLegacyHTTPVerifierChain("", ""); err == nil {
		t.Error("expected error for empty URL")
	}
}

// --- buildUserAuthChain (precedence rules) ---

// runnerWith returns a Runner whose Config has the given AuthConfig and
// CLI fields. Minimal — only what buildUserAuthChain needs.
func runnerWith(authYAML types.AuthConfig, authURL string) *Runner {
	r := &Runner{logger: nopLogger{}}
	r.cfg.Config = &types.ForgeConfig{Auth: authYAML}
	r.cfg.AuthURL = authURL
	return r
}

// nopLogger is a Logger that does nothing — keeps the warning path out
// of test output for the prefer-YAML-over-flag test below.
type nopLogger struct{}

func (nopLogger) Debug(string, map[string]any) {}
func (nopLogger) Info(string, map[string]any)  {}
func (nopLogger) Warn(string, map[string]any)  {}
func (nopLogger) Error(string, map[string]any) {}

func TestBuildUserAuthChain_NeitherSourcePresent(t *testing.T) {
	r := runnerWith(types.AuthConfig{}, "")
	chain, err := r.buildUserAuthChain()
	if err != nil {
		t.Fatalf("buildUserAuthChain: %v", err)
	}
	if chain != nil {
		t.Errorf("chain = %v, want nil when neither YAML nor flag is set", chain)
	}
}

func TestBuildUserAuthChain_OnlyLegacyURL(t *testing.T) {
	srv := fakeVerifierFor(t, "ok")
	r := runnerWith(types.AuthConfig{}, srv.URL)
	chain, err := r.buildUserAuthChain()
	if err != nil {
		t.Fatalf("buildUserAuthChain: %v", err)
	}
	if _, err := chain.Verify(context.Background(), "ok", nil); err != nil {
		t.Errorf("verify failed: %v", err)
	}
}

func TestBuildUserAuthChain_OnlyYAMLAuth(t *testing.T) {
	srv := fakeVerifierFor(t, "yaml-token")
	r := runnerWith(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "http_verifier", Settings: map[string]any{"url": srv.URL}},
		},
	}, "")
	chain, err := r.buildUserAuthChain()
	if err != nil {
		t.Fatalf("buildUserAuthChain: %v", err)
	}
	if _, err := chain.Verify(context.Background(), "yaml-token", nil); err != nil {
		t.Errorf("yaml-source verifier rejected: %v", err)
	}
}

func TestBuildUserAuthChain_BothSourcesPrefersYAML(t *testing.T) {
	// YAML auth points at srvYAML (accepts "yaml-only").
	// Legacy --auth-url points at srvLegacy (accepts "legacy-only").
	// Expect: YAML wins, legacy verifier never consulted.
	srvYAML := fakeVerifierFor(t, "yaml-only")
	srvLegacy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("legacy verifier should not be called when YAML auth: is present")
	}))
	defer srvLegacy.Close()

	r := runnerWith(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "http_verifier", Settings: map[string]any{"url": srvYAML.URL}},
		},
	}, srvLegacy.URL)

	chain, err := r.buildUserAuthChain()
	if err != nil {
		t.Fatalf("buildUserAuthChain: %v", err)
	}
	// YAML accepts "yaml-only".
	if _, err := chain.Verify(context.Background(), "yaml-only", nil); err != nil {
		t.Errorf("yaml-only rejected: %v", err)
	}
	// Legacy token rejected because YAML verifier doesn't recognize it.
	if _, err := chain.Verify(context.Background(), "legacy-only", nil); !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("legacy-only token err = %v, want ErrTokenRejected", err)
	}
}

func TestBuildUserAuthChain_OIDCFromYAML(t *testing.T) {
	// Builds an oidc provider successfully — confirms the side-effect
	// import wired it into the registry. Network isn't actually exercised
	// because we don't call Verify with a real JWT.
	r := runnerWith(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{
				"issuer":   "https://login.example.com",
				"audience": "api://forge",
			}},
		},
	}, "")
	chain, err := r.buildUserAuthChain()
	if err != nil {
		t.Fatalf("buildUserAuthChain: %v", err)
	}
	if chain == nil {
		t.Fatal("chain is nil; OIDC factory may not be registered")
	}
}
