package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Expect: YAML wins, legacy verifier never consulted, AND a warning
	// is emitted (review #12.1 — previously asserted only the chain
	// behavior; the "we logged a warning" half of the contract was
	// invisible because the test used the nop logger).
	srvYAML := fakeVerifierFor(t, "yaml-only")
	srvLegacy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("legacy verifier should not be called when YAML auth: is present")
	}))
	defer srvLegacy.Close()

	// Use a capturing logger so we can assert the warning emission too.
	logger := &recordLogger{}
	r := &Runner{logger: logger}
	r.cfg.Config = &types.ForgeConfig{Auth: types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "http_verifier", Settings: map[string]any{"url": srvYAML.URL}},
		},
	}}
	r.cfg.AuthURL = srvLegacy.URL

	chain, err := r.buildUserAuthChain()
	if err != nil {
		t.Fatalf("buildUserAuthChain: %v", err)
	}
	if _, err := chain.Verify(context.Background(), "yaml-only", nil); err != nil {
		t.Errorf("yaml-only rejected: %v", err)
	}
	if _, err := chain.Verify(context.Background(), "legacy-only", nil); !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("legacy-only token err = %v, want ErrTokenRejected", err)
	}

	// The contract: when both YAML auth and --auth-url are configured,
	// we warn that --auth-url is being ignored. Without this assertion
	// a refactor that silently drops the warning would never surface.
	if !logger.warnedAbout("--auth-url and forge.yaml") {
		t.Errorf("expected warning when both sources configured; got: %+v", logger.warnings)
	}
}

// --- Full resolveAuth + YAML auth + loopback prepend (review #12.3) ---
//
// auth_chain_test.go exercises buildLegacyAuthChain. This test goes one
// level higher and exercises resolveAuth() end-to-end with a forge.yaml
// `auth:` block configured AND an internal token minted by
// ResolveAuth(). The assertion: the loopback static_token sits at the
// chain head, AHEAD of the user-configured providers, so channel
// adapters short-circuit before the OIDC/http_verifier roundtrip.

func TestResolveAuth_YAMLAuth_LoopbackPrependedAtChainHead(t *testing.T) {
	tmp := t.TempDir()

	// Fake verifier for the YAML-configured http_verifier provider.
	srvYAML := fakeVerifierFor(t, "external-token")

	logger := &recordLogger{}
	r := &Runner{logger: logger}
	r.cfg.Config = &types.ForgeConfig{Auth: types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "http_verifier", Settings: map[string]any{"url": srvYAML.URL}},
		},
	}}
	r.cfg.NoAuth = false
	r.cfg.Host = "127.0.0.1" // local — passes the localhost gate in ResolveAuth
	r.cfg.WorkDir = tmp      // ResolveAuth writes runtime.token here

	opts, err := r.resolveAuth(nil)
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if opts.Chain == nil {
		t.Fatal("resolveAuth returned a nil Chain — loopback not prepended")
	}
	chain, ok := opts.Chain.(*auth.ChainProvider)
	if !ok {
		t.Fatalf("Chain is not a *ChainProvider; got %T", opts.Chain)
	}
	providers := chain.Providers()
	if len(providers) < 2 {
		t.Fatalf("chain has %d providers; want ≥ 2 (loopback + YAML)", len(providers))
	}

	// Head MUST be the loopback static_token — channel adapter
	// short-circuit depends on this. If a refactor inserts a YAML
	// provider before it, channels start round-tripping to the
	// external verifier on every callback.
	if providers[0].Name() != "static_token" {
		t.Errorf("chain.providers[0].Name() = %q, want %q (loopback)", providers[0].Name(), "static_token")
	}
	if providers[1].Name() != "http_verifier" {
		t.Errorf("chain.providers[1].Name() = %q, want %q (YAML)", providers[1].Name(), "http_verifier")
	}

	// And smoke-test that the loopback token is actually the one
	// ResolveAuth minted: Verify with r.authToken must succeed without
	// touching the YAML verifier.
	id, err := chain.Verify(context.Background(), r.authToken, nil)
	if err != nil {
		t.Fatalf("loopback verify failed: %v", err)
	}
	if id.Source != "internal" {
		t.Errorf("loopback identity Source = %q, want %q", id.Source, "internal")
	}
}

// --- --no-auth vs forge.yaml auth: block (review #4) ---
//
// Phase 1 + PR3 let --no-auth and the YAML auth block be set
// independently. Review #4 cross-checks them at resolveAuth time so
// a contradictory combination fails loudly instead of silently
// granting anonymous access.

// makeRunnerWithNoAuth returns a Runner with NoAuth=true and the given
// AuthConfig in forge.yaml. Captures logger output via a recording
// logger so warning assertions can read what the runner emitted.
func makeRunnerWithNoAuth(t *testing.T, authYAML types.AuthConfig) (*Runner, *recordLogger) {
	t.Helper()
	logger := &recordLogger{}
	r := &Runner{logger: logger}
	r.cfg.Config = &types.ForgeConfig{Auth: authYAML}
	r.cfg.NoAuth = true
	return r, logger
}

func TestResolveAuth_NoAuthWithRequiredFails(t *testing.T) {
	r, _ := makeRunnerWithNoAuth(t, types.AuthConfig{
		Required: true,
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{"issuer": "https://x", "audience": "y"}},
		},
	})
	_, err := r.resolveAuth(nil)
	if err == nil {
		t.Fatal("expected error when --no-auth conflicts with auth.required: true")
	}
	if !strings.Contains(err.Error(), "--no-auth conflicts with") {
		t.Errorf("err = %q, want descriptive --no-auth/required mismatch message", err)
	}
}

func TestResolveAuth_NoAuthWithProvidersWarns(t *testing.T) {
	r, logger := makeRunnerWithNoAuth(t, types.AuthConfig{
		// Required: false — the operator hasn't asserted that auth is
		// mandatory; --no-auth should still work but it surprises them
		// that the YAML providers are ignored, so we warn.
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{"issuer": "https://x", "audience": "y"}},
		},
	})
	opts, err := r.resolveAuth(nil)
	if err != nil {
		t.Fatalf("resolveAuth returned error, want warning only: %v", err)
	}
	if !opts.AllowAnonymous {
		t.Error("expected AllowAnonymous=true under --no-auth")
	}
	if !logger.warnedAbout("--no-auth overrides") {
		t.Errorf("expected --no-auth-overrides warning; got: %+v", logger.warnings)
	}
}

func TestResolveAuth_NoAuthWithEmptyYAMLConfig_NoWarning(t *testing.T) {
	// Regression: --no-auth alone (no auth: block at all) must still
	// work without spamming a warning.
	r, logger := makeRunnerWithNoAuth(t, types.AuthConfig{})
	opts, err := r.resolveAuth(nil)
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if !opts.AllowAnonymous {
		t.Error("expected AllowAnonymous=true under --no-auth")
	}
	if len(logger.warnings) > 0 {
		t.Errorf("unexpected warnings emitted: %+v", logger.warnings)
	}
}

func TestResolveAuth_NoAuthWithRequiredFalseAndProviders_WarnsNotFails(t *testing.T) {
	// Explicit Required: false + Providers set — operator knows what
	// they're doing; we warn but don't refuse.
	r, logger := makeRunnerWithNoAuth(t, types.AuthConfig{
		Required: false,
		Providers: []types.AuthProvider{
			{Type: "oidc", Settings: map[string]any{"issuer": "https://x", "audience": "y"}},
		},
	})
	if _, err := r.resolveAuth(nil); err != nil {
		t.Fatalf("resolveAuth should not fail when Required=false: %v", err)
	}
	if !logger.warnedAbout("--no-auth overrides") {
		t.Error("expected override warning")
	}
}

// recordLogger captures Warn/Info/Debug/Error calls for assertions.
// Mirrors the no-op nopLogger pattern in auth_config_test.go but
// records messages so tests can assert on them.
type recordLogger struct {
	warnings []string
	infos    []string
}

func (r *recordLogger) Debug(msg string, _ map[string]any) {}
func (r *recordLogger) Info(msg string, _ map[string]any)  { r.infos = append(r.infos, msg) }
func (r *recordLogger) Warn(msg string, _ map[string]any)  { r.warnings = append(r.warnings, msg) }
func (r *recordLogger) Error(msg string, _ map[string]any) {}
func (r *recordLogger) warnedAbout(substr string) bool {
	for _, w := range r.warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
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
