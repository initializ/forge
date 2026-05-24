package gcp_iap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/initializ/forge/forge-core/auth"
)

// --- test signer harness ---

// es256Signer is a self-contained ES256 signer + JWKS server used by all
// provider/JWKS tests. Lives here (not in a separate test-harness file)
// to keep PR 3 self-contained.
type es256Signer struct {
	priv   *ecdsa.PrivateKey
	pub    *ecdsa.PublicKey
	kid    string
	jwksMu *http.ServeMux
	srv    *httptest.Server
}

func newES256Signer(t *testing.T) *es256Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	s := &es256Signer{
		priv: priv,
		pub:  &priv.PublicKey,
		kid:  "test-kid-1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		s.writeJWKS(w)
	})
	s.jwksMu = mux
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *es256Signer) writeJWKS(w http.ResponseWriter) {
	x := base64.RawURLEncoding.EncodeToString(s.pub.X.Bytes())
	y := base64.RawURLEncoding.EncodeToString(s.pub.Y.Bytes())
	doc := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "EC",
				"crv": "P-256",
				"kid": s.kid,
				"alg": "ES256",
				"x":   x,
				"y":   y,
			},
		},
	}
	_ = json.NewEncoder(w).Encode(doc)
}

func (s *es256Signer) sign(claims map[string]any) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims(claims))
	tok.Header["kid"] = s.kid
	str, err := tok.SignedString(s.priv)
	if err != nil {
		panic(err)
	}
	return str
}

func (s *es256Signer) URL() string { return s.srv.URL + "/jwks" }

// --- helpers ---

func newProviderPointingAt(t *testing.T, signer *es256Signer, audience string) *Provider {
	t.Helper()
	p, err := New(Config{
		Audience:       audience,
		JWKSRefreshTTL: time.Hour,
		HTTPTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Swap the JWKS cache for one pointed at our test signer instead
	// of the real IAP URL. This is the per-package equivalent of the
	// aws_sigv4 sts_endpoint override.
	p.jwks = NewIAPJWKSCache(signer.URL(), time.Hour, 5*time.Second)
	return p
}

func validClaims(audience string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   iapIssuer,
		"aud":   audience,
		"sub":   "1234567890",
		"email": "alice@example.com",
		"hd":    "example.com",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
}

// --- Tests ---

func TestProvider_NoIAPHeader_YieldsToChain(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")
	_, err := p.Verify(context.Background(), "", auth.Headers{"Authorization": "Bearer foo"})
	if !errors.Is(err, auth.ErrTokenNotForMe) {
		t.Errorf("err = %v, want ErrTokenNotForMe", err)
	}
}

func TestProvider_HappyPath(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")

	tok := signer.sign(validClaims("test-aud"))
	id, err := p.Verify(context.Background(), "", auth.Headers{
		"X-Goog-Iap-Jwt-Assertion": tok,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Source != "gcp_iap" {
		t.Errorf("Source = %q", id.Source)
	}
	if id.UserID != "1234567890" {
		t.Errorf("UserID = %q", id.UserID)
	}
	if id.Email != "alice@example.com" {
		t.Errorf("Email = %q", id.Email)
	}
	if id.Claims["hd"] != "example.com" {
		t.Errorf("Claims[hd] = %v", id.Claims["hd"])
	}
}

func TestProvider_WrongIssuer_Rejected(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")

	claims := validClaims("test-aud")
	claims["iss"] = "https://accounts.google.com" // common bug: regular Google token, not IAP
	tok := signer.sign(claims)

	_, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok})
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestProvider_WrongAudience_Rejected(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "expected-aud")

	tok := signer.sign(validClaims("different-aud"))
	_, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok})
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestProvider_AudienceAsArray(t *testing.T) {
	// JWT spec allows aud as []string. Verify we handle that shape too —
	// IAP currently uses string but the contract is broader.
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "wanted-aud")
	claims := validClaims("placeholder")
	claims["aud"] = []string{"other", "wanted-aud"}
	tok := signer.sign(claims)
	if _, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok}); err != nil {
		t.Errorf("expected success with aud=[]string, got %v", err)
	}
}

func TestProvider_MissingSub_Invalid(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")
	claims := validClaims("test-aud")
	delete(claims, "sub")
	tok := signer.sign(claims)

	_, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok})
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestProvider_MissingEmail_Invalid(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")
	claims := validClaims("test-aud")
	delete(claims, "email")
	tok := signer.sign(claims)

	_, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok})
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestProvider_ExpiredToken_Rejected(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")
	claims := validClaims("test-aud")
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	tok := signer.sign(claims)

	_, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok})
	if !errors.Is(err, auth.ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}

func TestProvider_RS256Token_Rejected(t *testing.T) {
	// Algorithm-confusion defense: an RS256 token MUST be rejected
	// BEFORE key lookup — gcp_iap accepts ES256 only.
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")

	rsaPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims(validClaims("test-aud")))
	tok.Header["kid"] = signer.kid
	str, _ := tok.SignedString(rsaPriv)

	_, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": str})
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken (alg whitelist)", err)
	}
}

func TestFactory_RejectsMissingAudience(t *testing.T) {
	if _, err := auth.Build("gcp_iap", map[string]any{}); err == nil {
		t.Fatal("expected error when audience is missing")
	}
}

func TestProvider_Name(t *testing.T) {
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")
	if p.Name() != "gcp_iap" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestProvider_RegisteredInRegistry(t *testing.T) {
	p, err := auth.Build("gcp_iap", map[string]any{"audience": "test"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name() != "gcp_iap" {
		t.Errorf("Name = %q", p.Name())
	}
}

// --- JWKS cache tests (use a controllable mux, not the full signer) ---

func TestJWKSCache_StaleGraceOnOutage(t *testing.T) {
	// 1. Bring signer up, prime cache via successful verify.
	// 2. Take JWKS endpoint down.
	// 3. Verify same token still works — stale-grace.
	signer := newES256Signer(t)
	p := newProviderPointingAt(t, signer, "test-aud")

	tok := signer.sign(validClaims("test-aud"))
	if _, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok}); err != nil {
		t.Fatalf("priming Verify: %v", err)
	}

	// Take JWKS down by closing the server.
	signer.srv.Close()
	// Mark cache stale so we go through refresh path.
	p.jwks.mu.Lock()
	p.jwks.lastSuccessful = time.Now().Add(-2 * time.Hour)
	p.jwks.mu.Unlock()

	if _, err := p.Verify(context.Background(), "", auth.Headers{"X-Goog-Iap-Jwt-Assertion": tok}); err != nil {
		t.Errorf("stale-grace failed: %v", err)
	}
}

func TestJWKSCache_DoesNotFollowRedirects(t *testing.T) {
	// Review (sibling of B2/B3): the IAP JWKS URL is hardcoded (§9.4)
	// precisely so we never trust an alternate key source. Auto-
	// following a 302 to attacker bytes would let an attacker substitute
	// their own keys — any token forged with those keys would then
	// verify. Pin: any 3xx is treated as JWKS-unavailable (we never
	// follow).
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Location", "https://attacker.example.com/")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := NewIAPJWKSCache(srv.URL, time.Hour, 5*time.Second)
	err := c.refresh(context.Background())
	if err == nil {
		t.Fatal("expected error on 302; client must not follow")
	}
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
	if hits != 1 {
		t.Errorf("JWKS endpoint hit %d times, want 1 (redirect was followed)", hits)
	}
}

func TestJWKSCache_BackoffBumps(t *testing.T) {
	// Endpoint always 500 — observe backoffDuration grow on each refresh.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewIAPJWKSCache(srv.URL, time.Hour, 5*time.Second)
	if err := c.refresh(context.Background()); err == nil {
		t.Fatal("expected first refresh to fail")
	}
	c.mu.RLock()
	first := c.backoffDuration
	c.mu.RUnlock()
	if first != 5*time.Second {
		t.Errorf("backoff after 1st failure = %v, want 5s", first)
	}

	// Force a second attempt past the backoff window.
	c.mu.Lock()
	c.lastAttempt = time.Now().Add(-10 * time.Second)
	c.mu.Unlock()
	if err := c.refresh(context.Background()); err == nil {
		t.Fatal("expected second refresh to fail")
	}
	c.mu.RLock()
	second := c.backoffDuration
	c.mu.RUnlock()
	if second != 10*time.Second {
		t.Errorf("backoff after 2nd failure = %v, want 10s", second)
	}
}

func TestJWKSCache_BackoffBlocksRefresh(t *testing.T) {
	// During backoff, refresh() returns ErrProviderUnavailable without
	// attempting a network call.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewIAPJWKSCache(srv.URL, time.Hour, 5*time.Second)
	_ = c.refresh(context.Background()) // failure -> backoff
	if calls != 1 {
		t.Fatalf("calls after first refresh = %d", calls)
	}
	err := c.refresh(context.Background()) // should NOT call network
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
	if calls != 1 {
		t.Errorf("network was hit during backoff (calls = %d)", calls)
	}
}

func TestJWKSCache_BodyCap(t *testing.T) {
	// Serve 1 MiB of garbage; parse must fail without OOM.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 256 KiB cap + 1 byte → triggers either truncated parse fail or
		// LimitReader cut.
		buf := make([]byte, 512<<10)
		_, _ = w.Write(buf)
	}))
	defer srv.Close()
	c := NewIAPJWKSCache(srv.URL, time.Hour, 5*time.Second)
	err := c.refresh(context.Background())
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Errorf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestParseECJWKSet_DropsNonES256(t *testing.T) {
	// Build a JWKS with one valid EC key and one RSA-shaped entry.
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x := base64.RawURLEncoding.EncodeToString(priv.X.Bytes())
	y := base64.RawURLEncoding.EncodeToString(priv.Y.Bytes())
	doc := map[string]any{
		"keys": []map[string]any{
			{"kty": "EC", "crv": "P-256", "kid": "good", "alg": "ES256", "x": x, "y": y},
			{"kty": "RSA", "kid": "bad", "alg": "RS256", "n": "ignored", "e": "ignored"},
			{"kty": "EC", "crv": "P-256", "kid": "wrong-alg", "alg": "RS256", "x": x, "y": y},
		},
	}
	raw, _ := json.Marshal(doc)
	keys, err := parseECJWKSet(raw)
	if err != nil {
		t.Fatalf("parseECJWKSet: %v", err)
	}
	if _, ok := keys["good"]; !ok {
		t.Error("good key dropped")
	}
	if _, ok := keys["bad"]; ok {
		t.Error("RSA key not dropped")
	}
	if _, ok := keys["wrong-alg"]; ok {
		t.Error("EC key labeled RS256 not dropped")
	}
}

func TestParseAudience_StringAndArray(t *testing.T) {
	// "aud":"x"
	one, err := parseAudience(json.RawMessage(`"x"`))
	if err != nil || len(one) != 1 || one[0] != "x" {
		t.Errorf("string aud: got %v, %v", one, err)
	}
	// "aud":["x","y"]
	two, err := parseAudience(json.RawMessage(`["x","y"]`))
	if err != nil || len(two) != 2 {
		t.Errorf("array aud: got %v, %v", two, err)
	}
	// missing
	if _, err := parseAudience(nil); err == nil {
		t.Error("missing aud should error")
	}
}

// ensure import not flagged
var (
	_ = io.EOF
	_ = big.NewInt
)
