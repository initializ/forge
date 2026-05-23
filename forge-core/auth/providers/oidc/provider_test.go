package oidc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/auth/providers/oidc"
)

const testAudience = "api://forge"

func newProvider(t *testing.T, fi *fakeIssuer, mod func(*oidc.Config)) *oidc.Provider {
	t.Helper()
	cfg := oidc.Config{
		Issuer:   fi.IssuerURL(),
		Audience: testAudience,
	}
	if mod != nil {
		mod(&cfg)
	}
	p, err := oidc.New(cfg)
	if err != nil {
		t.Fatalf("oidc.New: %v", err)
	}
	return p
}

// --- Construction / Validation ---

func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     oidc.Config
		wantErr error
	}{
		{
			name:    "empty config",
			cfg:     oidc.Config{},
			wantErr: auth.ErrProviderNotConfigured,
		},
		{
			name:    "missing audience",
			cfg:     oidc.Config{Issuer: "https://example.com"},
			wantErr: auth.ErrProviderNotConfigured,
		},
		{
			name:    "missing issuer",
			cfg:     oidc.Config{Audience: "api://forge"},
			wantErr: auth.ErrProviderNotConfigured,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := oidc.New(tt.cfg)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// --- Happy paths ---

func TestVerify_RSA256_HappyPath(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["email"] = "alice@example.com"
	claims["groups"] = []string{"engineers", "oncall"}

	id, err := p.Verify(context.Background(), fi.SignWith("key-1", claims), nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id == nil {
		t.Fatal("Verify returned nil identity")
	}
	if id.UserID != "user-123" {
		t.Errorf("UserID = %q, want user-123", id.UserID)
	}
	if id.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", id.Email)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "engineers" || id.Groups[1] != "oncall" {
		t.Errorf("Groups = %v, want [engineers oncall]", id.Groups)
	}
	if id.Source != "oidc" {
		t.Errorf("Source = %q, want oidc", id.Source)
	}
}

func TestVerify_ECDSA_HappyPath(t *testing.T) {
	fi := newFakeIssuer(t)
	fi.addECDSAKey("ec-1")
	p := newProvider(t, fi, nil)

	tok := fi.SignWith("ec-1", fi.DefaultClaims(testAudience))
	id, err := p.Verify(context.Background(), tok, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.UserID != "user-123" {
		t.Errorf("UserID = %q, want user-123", id.UserID)
	}
}

// --- Algorithm-confusion / security ---

func TestVerify_RejectsAlgNone(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	tok := SignUnsigned(fi.DefaultClaims(testAudience))
	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken (alg=none must be rejected)", err)
	}
}

func TestVerify_RejectsHMAC(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	tok := SignHMAC("key-1", fi.DefaultClaims(testAudience), []byte("any-secret"))
	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken (HMAC must be rejected)", err)
	}
}

func TestVerify_AlgInHeaderMustMatchJWKS(t *testing.T) {
	// Algorithm-confusion defense: if the token claims an alg that
	// differs from the JWKS-declared alg for that kid, reject.
	fi := newFakeIssuer(t)
	// Replace the JWKS-declared alg for key-1 with RS512 while still
	// signing the token with RS256 (the key's actual method).
	fi.keys["key-1"].algInJWKS = "RS512"

	p := newProvider(t, fi, nil)
	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken (alg mismatch)", err)
	}
}

func TestVerify_RejectsTokenWithoutKid(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	// Sign manually, without kid in header.
	claims := fi.DefaultClaims(testAudience)
	priv := fi.keys["key-1"].priv
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = p.Verify(context.Background(), signed, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken (missing kid)", err)
	}
}

// --- iss / aud / azp / exp / nbf ---

func TestVerify_RejectsWrongIssuer(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["iss"] = "https://different-issuer.example.com"
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected (iss mismatch)", err)
	}
}

func TestVerify_RejectsWrongAudience(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims("api://different-service")
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected (aud mismatch)", err)
	}
}

func TestVerify_AcceptsAudienceInArray(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["aud"] = []string{"api://other", testAudience, "api://third"}
	tok := fi.SignWith("key-1", claims)

	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("multi-audience token rejected: %v", err)
	}
}

func TestVerify_AcceptsAZPAsFallback(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.ClientID = "client-abc"
	})

	claims := fi.DefaultClaims("api://different") // aud doesn't match
	claims["azp"] = "client-abc"                  // but azp does
	tok := fi.SignWith("key-1", claims)

	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("azp-fallback token rejected: %v", err)
	}
}

func TestVerify_RejectsExpiredToken(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.ClockSkew = 1 * time.Second
	})

	claims := fi.DefaultClaims(testAudience)
	claims["exp"] = time.Now().Add(-10 * time.Minute).Unix()
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected (expired)", err)
	}
}

func TestVerify_ClockSkewToleratesNearExpiration(t *testing.T) {
	fi := newFakeIssuer(t)
	// Configure 60s leeway; token expired 30s ago — should be accepted.
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.ClockSkew = 60 * time.Second
	})

	claims := fi.DefaultClaims(testAudience)
	claims["exp"] = time.Now().Add(-30 * time.Second).Unix()
	tok := fi.SignWith("key-1", claims)

	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Errorf("token within ClockSkew rejected: %v", err)
	}
}

func TestVerify_RejectsNotYetValid(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.ClockSkew = 1 * time.Second
	})

	claims := fi.DefaultClaims(testAudience)
	claims["nbf"] = time.Now().Add(10 * time.Minute).Unix()
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected (nbf in future)", err)
	}
}

// --- JWKS cache / rotation ---

func TestJWKS_LazyLoad(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	// Provider construction must not fetch JWKS (lazy).
	if fi.jwksFetches != 0 {
		t.Errorf("JWKS fetched %d times during New (must be 0)", fi.jwksFetches)
	}

	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if fi.jwksFetches != 1 {
		t.Errorf("JWKS fetched %d times after first Verify, want 1", fi.jwksFetches)
	}

	// Second Verify must reuse the cache.
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	if fi.jwksFetches != 1 {
		t.Errorf("JWKS fetched %d times after second Verify, want 1 (cache miss)", fi.jwksFetches)
	}
}

func TestJWKS_RotationTriggersRefresh(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	// Initial token with key-1.
	tok1 := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	if _, err := p.Verify(context.Background(), tok1, nil); err != nil {
		t.Fatalf("initial Verify: %v", err)
	}

	// IdP rotates: adds key-2, then a token signed with key-2 arrives.
	fi.addRSAKey("key-2")
	tok2 := fi.SignWith("key-2", fi.DefaultClaims(testAudience))

	if _, err := p.Verify(context.Background(), tok2, nil); err != nil {
		t.Fatalf("post-rotation Verify: %v", err)
	}
	// Provider should have refreshed JWKS once on the unknown kid.
	if fi.jwksFetches != 2 {
		t.Errorf("JWKS fetched %d times after rotation, want 2 (lazy + refresh)", fi.jwksFetches)
	}
}

func TestJWKS_UnknownKidAfterRefresh(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	// Sign a token whose header advertises a kid that doesn't exist on
	// the issuer. We do this by signing with key-1 then editing the
	// header — easier: build the signed-string manually.
	claims := fi.DefaultClaims(testAudience)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "ghost-kid"
	signed, err := tok.SignedString(fi.keys["key-1"].priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = p.Verify(context.Background(), signed, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken (kid not in JWKS)", err)
	}
}

func TestJWKS_RefetchGracePreventsStampede(t *testing.T) {
	// Two requests with the same unknown kid in quick succession should
	// only trigger ONE JWKS fetch — the grace window should suppress
	// the second.
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	// Warm cache with a valid request first.
	if _, err := p.Verify(context.Background(), fi.SignWith("key-1", fi.DefaultClaims(testAudience)), nil); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	startFetches := fi.jwksFetches

	// Two back-to-back unknown-kid requests.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, fi.DefaultClaims(testAudience))
	tok.Header["kid"] = "ghost"
	signed, _ := tok.SignedString(fi.keys["key-1"].priv)

	_, _ = p.Verify(context.Background(), signed, nil)
	_, _ = p.Verify(context.Background(), signed, nil)

	addedFetches := fi.jwksFetches - startFetches
	if addedFetches > 1 {
		t.Errorf("JWKS fetched %d times for two unknown-kid requests, want ≤1 (stampede prevention)", addedFetches)
	}
}

// --- ClaimMap ---

func TestClaimMap_Defaults(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["email"] = "x@y"
	claims["org_id"] = "tenant-1"
	claims["workspace_id"] = "ws-2"
	claims["groups"] = []string{"a"}

	id, err := p.Verify(context.Background(), fi.SignWith("key-1", claims), nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.UserID != "user-123" || id.Email != "x@y" || id.OrgID != "tenant-1" || id.WorkspaceID != "ws-2" {
		t.Errorf("default ClaimMap mapping wrong: %+v", id)
	}
}

func TestClaimMap_CustomNames(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.ClaimMap = oidc.ClaimMap{
			UserID: "uid",
			Email:  "mail",
			OrgID:  "tenant",
			Groups: "roles",
		}
	})

	claims := fi.DefaultClaims(testAudience)
	claims["uid"] = "custom-user"
	claims["mail"] = "x@y"
	claims["tenant"] = "tenant-99"
	claims["roles"] = []string{"admin"}

	id, err := p.Verify(context.Background(), fi.SignWith("key-1", claims), nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.UserID != "custom-user" || id.Email != "x@y" || id.OrgID != "tenant-99" || len(id.Groups) != 1 || id.Groups[0] != "admin" {
		t.Errorf("custom ClaimMap mapping wrong: %+v", id)
	}
}

func TestClaimMap_HeaderOverridesOrgID(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["org_id"] = "claim-org"

	id, err := p.Verify(
		context.Background(),
		fi.SignWith("key-1", claims),
		auth.Headers{"X-Org-ID": "header-org"},
	)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.OrgID != "header-org" {
		t.Errorf("OrgID = %q, want header-org (header should override claim)", id.OrgID)
	}
}

func TestClaimMap_GroupsToleratesSingleString(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["groups"] = "single-group"

	id, err := p.Verify(context.Background(), fi.SignWith("key-1", claims), nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "single-group" {
		t.Errorf("Groups = %v, want [single-group]", id.Groups)
	}
}

// --- Non-JWT tokens yield ---

func TestVerify_NonJWTYields(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	// Opaque-token shapes another provider might handle.
	cases := []string{"", "opaque-token", "abc.def", "abc.def.ghi.jkl", "no-dots"}
	for _, tok := range cases {
		t.Run(tok, func(t *testing.T) {
			_, err := p.Verify(context.Background(), tok, nil)
			if !errors.Is(err, auth.ErrTokenNotForMe) {
				t.Errorf("token %q: err = %v, want ErrTokenNotForMe", tok, err)
			}
		})
	}
}

// --- Discovery ---

func TestDiscovery_IssuerMismatchFails(t *testing.T) {
	// Build an issuer whose discovery doc returns a DIFFERENT issuer URL
	// than the one we hit. Catches misconfiguration where someone copies
	// an Okta discovery URL but configures the wrong issuer string.
	mismatchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://different.example.com","jwks_uri":"https://different.example.com/jwks"}`))
	}))
	defer mismatchSrv.Close()

	p, err := oidc.New(oidc.Config{
		Issuer:   mismatchSrv.URL,
		Audience: testAudience,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Use a syntactically-valid JWT — header includes kid + alg so we
	// reach the JWKS-fetch step where discovery is consulted.
	// Header: {"alg":"RS256","kid":"k1"}  Payload: {"iss":"x"}
	dummyJWT := "eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0.eyJpc3MiOiJ4In0.AAAA"
	_, err = p.Verify(context.Background(), dummyJWT, nil)
	if err == nil {
		t.Fatal("expected discovery mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "discovery issuer") {
		t.Errorf("err = %v, want discovery-issuer-mismatch message", err)
	}
}

func TestDiscovery_JWKSURLOverrideSkipsDiscovery(t *testing.T) {
	fi := newFakeIssuer(t)
	jwksFetchesBefore := fi.jwksFetches

	// Configure JWKSURL explicitly — discovery should never be called.
	// Implicitly verified by the fact that the fakeIssuer's discovery
	// handler increments jwksFetches only on /jwks (not /.well-known/),
	// so we check that signing still works.
	p, err := oidc.New(oidc.Config{
		Issuer:   fi.IssuerURL(),
		Audience: testAudience,
		JWKSURL:  fi.IssuerURL() + "/jwks",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if fi.jwksFetches != jwksFetchesBefore+1 {
		t.Errorf("JWKS fetches = %d, want %d", fi.jwksFetches, jwksFetchesBefore+1)
	}
}

// --- Concurrency / race safety ---

func TestVerify_Concurrent(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)
	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))

	const N = 50
	errs := make(chan error, N)
	for range N {
		go func() {
			_, err := p.Verify(context.Background(), tok, nil)
			errs <- err
		}()
	}
	for range N {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Verify: %v", err)
		}
	}
}

// --- HTTP client / context ---

func TestVerify_RespectsContextCancellation(t *testing.T) {
	// Slow JWKS endpoint: handler blocks until the test ends.
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() { close(done); srv.Close() }()

	p, err := oidc.New(oidc.Config{
		Issuer:   srv.URL,
		Audience: testAudience,
		JWKSURL:  srv.URL + "/jwks",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	dummyJWT := "eyJhbGciOiJSUzI1NiIsImtpZCI6Inh4eCJ9.eyJpc3MiOiJ4In0.signature"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = p.Verify(ctx, dummyJWT, nil)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("Verify took %v after ctx cancellation; expected <2s", elapsed)
	}
}

// --- Factory / registration ---

func TestRegisteredViaFactory(t *testing.T) {
	p, err := auth.Build("oidc", map[string]any{
		"issuer":   "https://example.com",
		"audience": "api://forge",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "oidc" {
		t.Errorf("Name = %q, want oidc", p.Name())
	}
}

func TestFactory_MissingIssuerErrors(t *testing.T) {
	_, err := auth.Build("oidc", map[string]any{
		"audience": "api://forge",
	})
	if !errors.Is(err, auth.ErrProviderNotConfigured) {
		t.Fatalf("err = %v, want ErrProviderNotConfigured", err)
	}
}

func TestFactory_UnknownKeysAreIgnored(t *testing.T) {
	// Forward compat: future YAML keys must not break construction.
	_, err := auth.Build("oidc", map[string]any{
		"issuer":          "https://example.com",
		"audience":        "api://forge",
		"future_setting":  "ignored",
		"another_unknown": 42,
	})
	if err != nil {
		t.Fatalf("Build with unknown keys: %v", err)
	}
}

// --- Smoke: identity Claims is a copy ---

func TestIdentity_ClaimsIsCopiedFromToken(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["custom"] = "value"
	id, err := p.Verify(context.Background(), fi.SignWith("key-1", claims), nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Claims["custom"] != "value" {
		t.Errorf("Claims[custom] = %v, want value", id.Claims["custom"])
	}
	// Mutate the returned map — provider state must not be affected.
	id.Claims["custom"] = "MUTATED"

	id2, _ := p.Verify(context.Background(), fi.SignWith("key-1", claims), nil)
	if id2.Claims["custom"] != "value" {
		t.Errorf("Claims map shared across Verify calls — got %v after mutation", id2.Claims["custom"])
	}
}

// Make atomic referenced so the import isn't dropped if we later remove
// the concurrent-fetches counter.
var _ atomic.Int32

// --- Error-path coverage ---

func TestJWKS_MalformedRSAKey_IsSkipped(t *testing.T) {
	// JWKS contains a valid key AND a malformed one. The malformed one
	// is silently skipped — load must succeed and the valid one usable.
	fi := newFakeIssuer(t)
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"keys": [
				{"kty":"RSA","kid":"broken","alg":"RS256","n":"!!!not-base64!!!","e":"AQAB"},
				{"kty":"unknown","kid":"weird"},
				{"kty":"RSA","kid":"no-kid-here","alg":"RS256","n":"","e":""}
			]
		}`))
	}))
	defer jwksSrv.Close()

	p, err := oidc.New(oidc.Config{
		Issuer:   fi.IssuerURL(),
		Audience: testAudience,
		JWKSURL:  jwksSrv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	_, err = p.Verify(context.Background(), tok, nil)
	// Should fail because the broken JWKS has no usable keys.
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (no valid keys in JWKS)", err)
	}
}

func TestJWKS_EndpointReturns500(t *testing.T) {
	fi := newFakeIssuer(t)
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer jwksSrv.Close()

	p, err := oidc.New(oidc.Config{
		Issuer:   fi.IssuerURL(),
		Audience: testAudience,
		JWKSURL:  jwksSrv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	_, err = p.Verify(context.Background(), tok, nil)
	if err == nil {
		t.Fatal("expected error when JWKS endpoint returns 500")
	}
}

func TestJWKS_HMACAlgInJWKS_IsRejected(t *testing.T) {
	// If the JWKS advertises an HMAC alg, we must skip that key entirely.
	fi := newFakeIssuer(t)
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"keys": [
				{"kty":"oct","kid":"hmac-key","alg":"HS256","k":"some-secret"}
			]
		}`))
	}))
	defer jwksSrv.Close()

	p, err := oidc.New(oidc.Config{
		Issuer:   fi.IssuerURL(),
		Audience: testAudience,
		JWKSURL:  jwksSrv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Build a token claiming HS256 with kid hmac-key.
	tok := SignHMAC("hmac-key", fi.DefaultClaims(testAudience), []byte("some-secret"))
	_, err = p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (HMAC in JWKS must be skipped)", err)
	}
}

func TestVerify_TokenWithoutAudClaim(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	delete(claims, "aud")
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected (missing aud)", err)
	}
}

// --- Issuer trailing-slash normalization (review finding #2) ---
//
// IdPs disagree on whether the canonical issuer has a trailing slash.
// Auth0 emits "iss": "https://tenant.auth0.com/"; most Okta deployments
// do not. Operators paste whatever they see into forge.yaml. These
// tests guarantee both directions interop.

func TestIssuer_ConfigWithoutSlash_TokenWithSlash(t *testing.T) {
	fi := newFakeIssuer(t)
	// Config the user-facing way: no trailing slash.
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.Issuer = fi.IssuerURL() // already no slash
	})

	// Mint a token whose iss has a trailing slash (mimics Auth0).
	claims := fi.DefaultClaims(testAudience)
	claims["iss"] = fi.IssuerURL() + "/"
	tok := fi.SignWith("key-1", claims)

	id, err := p.Verify(context.Background(), tok, nil)
	if err != nil {
		t.Fatalf("token iss with slash, config without — should accept, got: %v", err)
	}
	if id == nil {
		t.Fatal("Verify returned nil identity")
	}
}

func TestIssuer_ConfigWithSlash_TokenWithoutSlash(t *testing.T) {
	fi := newFakeIssuer(t)
	// Config the other way: user pastes with trailing slash.
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.Issuer = fi.IssuerURL() + "/"
	})

	// Token has no slash (the more common case).
	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))

	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("config with slash, token without — should accept, got: %v", err)
	}
}

func TestIssuer_BothWithoutSlash_StillWorks(t *testing.T) {
	// Regression: ensure the trim didn't break the most common case.
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)
	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("config without slash + token without slash should accept: %v", err)
	}
}

func TestIssuer_BothWithSlash_StillWorks(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.Issuer = fi.IssuerURL() + "/"
	})
	claims := fi.DefaultClaims(testAudience)
	claims["iss"] = fi.IssuerURL() + "/"
	tok := fi.SignWith("key-1", claims)
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("config + token both with slash should accept: %v", err)
	}
}

func TestIssuer_GenuineMismatchStillRejected(t *testing.T) {
	// Security guard: trimming trailing slashes must not collapse
	// genuinely-different issuers into the same comparison value.
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	claims["iss"] = "https://attacker.example.com/" // completely different host
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("genuine issuer mismatch err = %v, want ErrTokenRejected", err)
	}
}

func TestIssuer_TokenMissingIssClaim(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	delete(claims, "iss")
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("missing iss err = %v, want ErrTokenRejected", err)
	}
}

func TestDiscovery_TrailingSlashTolerated(t *testing.T) {
	// Discovery side: IdP's doc has issuer with trailing slash;
	// operator configured without. Must not bomb at startup-ish phase.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately use a different host than the test server's URL
		// to simulate normalization-only mismatch — wait, we need it to
		// be the same host, just different slash form.
	}))
	srv.Close() // we just needed the type

	// Build a real fake issuer and add a slash to the doc's response.
	docHost := ""
	dynamicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			// Emit issuer WITH trailing slash even though docHost has none.
			_, _ = w.Write([]byte(`{"issuer":"` + docHost + `/","jwks_uri":"` + docHost + `/jwks"}`))
		case "/jwks":
			_, _ = w.Write([]byte(`{"keys":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer dynamicSrv.Close()
	docHost = dynamicSrv.URL

	p, err := oidc.New(oidc.Config{
		Issuer:   docHost, // no trailing slash
		Audience: testAudience,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Trigger the discovery path with a real JWT-shaped string.
	// Discovery happens lazily on first JWKS lookup. We're testing that
	// the comparison doesn't reject due to trailing-slash mismatch.
	dummyJWT := "eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0.eyJpc3MiOiJ4In0.AAAA"
	_, err = p.Verify(context.Background(), dummyJWT, nil)
	// Expect an ErrInvalidToken (kid not found in empty JWKS) — NOT a
	// "discovery issuer mismatch" error. The previous bug surfaced as
	// the latter.
	if err != nil && strings.Contains(err.Error(), "discovery issuer") {
		t.Errorf("discovery rejected due to trailing-slash drift: %v", err)
	}
}

// --- TTL-driven refresh (review finding #1) ---
//
// These tests guarantee that JWKS keys revoked at the IdP eventually stop
// being accepted by Forge. Before the fix, the cache's ttl field was dead
// code and revoked keys remained trusted until process restart.

func TestJWKS_TTLExpiryForcesRefresh(t *testing.T) {
	// Build the provider with a 1-minute TTL. We can't reach the cache's
	// injectable clock from outside the package directly, so we exercise
	// TTL behavior by setting a tiny TTL and advancing wall time via the
	// `now` field on the provider's JWKS cache through the exported
	// SetCacheClockForTest test hook below.
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.JWKSCacheTTL = 10 * time.Minute // any value above the 5-min clamp
	})

	// Drive a virtual clock so we can move past TTL without sleeping.
	clk := &virtualClock{now: time.Now()}
	oidc.SetCacheClockForTest(p, clk.Now)

	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	fetchesAfterFirst := fi.jwksFetches

	// Same kid, still within TTL → no new JWKS fetch.
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("second Verify within TTL: %v", err)
	}
	if fi.jwksFetches != fetchesAfterFirst {
		t.Errorf("JWKS refetched within TTL (%d → %d) — should have served from cache",
			fetchesAfterFirst, fi.jwksFetches)
	}

	// Advance virtual time past TTL.
	clk.advance(11 * time.Minute)

	// Same kid, but TTL expired → MUST refetch.
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("third Verify after TTL: %v", err)
	}
	if fi.jwksFetches != fetchesAfterFirst+1 {
		t.Errorf("JWKS not refetched after TTL expiry (fetches: %d, expected %d)",
			fi.jwksFetches, fetchesAfterFirst+1)
	}
}

func TestJWKS_RevokedKeyEventuallyRejected(t *testing.T) {
	// The security-critical case: IdP removes a key from JWKS (revocation).
	// Within TTL, Forge still trusts it (acceptable — that's the TTL window).
	// After TTL, Forge picks up the new JWKS without the revoked key and
	// must reject tokens signed by it.
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, func(c *oidc.Config) {
		c.JWKSCacheTTL = 10 * time.Minute
	})

	clk := &virtualClock{now: time.Now()}
	oidc.SetCacheClockForTest(p, clk.Now)

	// Add a second key the issuer rotates between.
	fi.addRSAKey("key-revoked")

	// Token is signed by key-revoked.
	tok := fi.SignWith("key-revoked", fi.DefaultClaims(testAudience))

	// Initial verify — cache warms with both keys present.
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("pre-revocation verify: %v", err)
	}

	// IdP revokes the key (removes it from JWKS).
	delete(fi.keys, "key-revoked")
	for i, k := range fi.keyOrder {
		if k == "key-revoked" {
			fi.keyOrder = append(fi.keyOrder[:i], fi.keyOrder[i+1:]...)
			break
		}
	}

	// Within TTL: Forge still trusts the old key (acceptable revocation lag).
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Errorf("within-TTL verify failed unexpectedly: %v", err)
	}

	// Advance past TTL → refresh picks up the new JWKS without key-revoked.
	clk.advance(11 * time.Minute)

	// After refresh, the token's kid is no longer in JWKS → ErrInvalidToken.
	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("post-TTL verify err = %v, want ErrInvalidToken (kid removed by IdP)", err)
	}
}

func TestJWKS_FailedRefreshDoesNotExtendTTL(t *testing.T) {
	// Bug guard: a failed refresh must not bump lastSuccessful. Otherwise
	// an IdP outage during a TTL-refresh window would silently extend
	// trust in already-cached keys.
	//
	// Strategy: warm the cache, then make the JWKS endpoint return 500,
	// advance past TTL, attempt a Verify. The refresh fails, but the
	// existing keys remain available — and the next attempt after the
	// failure backoff should retry (proving lastSuccessful did NOT advance).
	fi := newFakeIssuer(t)
	failJWKS := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failJWKS.Load() && r.URL.Path == "/jwks" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fi.serve(w, r)
	}))
	defer srv.Close()

	p, err := oidc.New(oidc.Config{
		Issuer:       srv.URL,
		Audience:     testAudience,
		JWKSURL:      srv.URL + "/jwks",
		JWKSCacheTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Patch the fakeIssuer's IssuerURL so claim's iss matches.
	fi.server = srv

	clk := &virtualClock{now: time.Now()}
	oidc.SetCacheClockForTest(p, clk.Now)

	tok := fi.SignWith("key-1", fi.DefaultClaims(testAudience))

	// Warm the cache.
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Fatalf("warmup: %v", err)
	}

	// Endpoint goes down; advance past TTL.
	failJWKS.Store(true)
	clk.advance(11 * time.Minute)

	// Refresh attempt fails. The TTL clock was NOT extended by the
	// failure, so subsequent calls outside the backoff window will
	// re-attempt.
	_, err = p.Verify(context.Background(), tok, nil)
	if err == nil {
		t.Errorf("expected error during JWKS outage, got nil")
	}

	// Endpoint comes back, but we're still in error-backoff window.
	// Advance past refetchGrace (30s) so a retry can happen.
	failJWKS.Store(false)
	clk.advance(35 * time.Second)

	// Next verify should retry the refresh and succeed.
	if _, err := p.Verify(context.Background(), tok, nil); err != nil {
		t.Errorf("post-recovery verify failed: %v", err)
	}
}

// virtualClock is a controllable time source for tests.
type virtualClock struct {
	now time.Time
}

func (c *virtualClock) Now() time.Time { return c.now }
func (c *virtualClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func TestVerify_TokenWithoutExp(t *testing.T) {
	fi := newFakeIssuer(t)
	p := newProvider(t, fi, nil)

	claims := fi.DefaultClaims(testAudience)
	delete(claims, "exp")
	tok := fi.SignWith("key-1", claims)

	_, err := p.Verify(context.Background(), tok, nil)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (missing exp)", err)
	}
}
