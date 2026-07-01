package runtime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// TestServeJWKS_WithSigningKey verifies the endpoint returns the
// operator's public key in RFC 8037 shape. Consumers depend on this
// to build the offline verifier.
func TestServeJWKS_WithSigningKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	r := &Runner{
		auditSigningKey: &coreruntime.LoadedKey{
			Private: priv,
			Public:  pub,
			Kid:     "endpoint-kid",
		},
	}
	req := httptest.NewRequest("GET", "/.well-known/forge-audit-keys", nil)
	rec := httptest.NewRecorder()
	r.serveJWKS(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/jwk-set+json" {
		t.Errorf("content-type: got %q want application/jwk-set+json", ct)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("expected Cache-Control header")
	}

	var jwks coreruntime.JWKS
	if err := json.Unmarshal(rec.Body.Bytes(), &jwks); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(jwks.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(jwks.Keys))
	}
	k := jwks.Keys[0]
	if k.Kid != "endpoint-kid" || k.Kty != "OKP" || k.Crv != "Ed25519" || k.Alg != "EdDSA" || k.Use != "sig" {
		t.Errorf("wrong JWK shape: %+v", k)
	}

	// The advertised key must actually verify a signature from the
	// stored private key — the whole point of the endpoint.
	extracted, err := coreruntime.PublicKeyFromJWK(k)
	if err != nil {
		t.Fatalf("extract pub: %v", err)
	}
	sig := ed25519.Sign(priv, []byte("hello"))
	if !ed25519.Verify(extracted, []byte("hello"), sig) {
		t.Error("advertised pubkey does not verify signature from stored privkey")
	}
}

// TestServeJWKS_NoSigningKey confirms the endpoint returns a well-
// formed empty set (not a 404, not `null`). Consumers with cached
// keys must still be able to poll and see the deployment has none.
func TestServeJWKS_NoSigningKey(t *testing.T) {
	r := &Runner{auditSigningKey: nil}
	req := httptest.NewRequest("GET", "/.well-known/forge-audit-keys", nil)
	rec := httptest.NewRecorder()
	r.serveJWKS(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	body := rec.Body.String()
	// The literal wire shape matters for downstream parsers — a null
	// keys slice would deserialize to nil in most languages and
	// surprise consumers. Assert exactly.
	if !containsJSONKeysEmpty(body) {
		t.Errorf("expected {\"keys\":[]} shape, got: %s", body)
	}
}

// containsJSONKeysEmpty tests whether the body has a `keys` array
// with zero entries, tolerating whitespace differences.
func containsJSONKeysEmpty(body string) bool {
	var jwks struct {
		Keys []any `json:"keys"`
	}
	if err := json.Unmarshal([]byte(body), &jwks); err != nil {
		return false
	}
	// nil keys is NOT allowed — must be an empty slice.
	if jwks.Keys == nil {
		return false
	}
	return len(jwks.Keys) == 0
}
