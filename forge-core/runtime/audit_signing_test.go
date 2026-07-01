package runtime

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generateTestKey returns a fresh Ed25519 key pair the tests can
// throw around without touching disk.
func generateTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

// pkcs8DER encodes a private key as PKCS#8 DER — the format the env
// var loader expects.
func pkcs8DER(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return der
}

func TestLoadEd25519KeyFromEnv_UnsetReturnsNil(t *testing.T) {
	t.Setenv("FORGE_TEST_KEY", "")
	got, err := LoadEd25519KeyFromEnv("FORGE_TEST_KEY", "FORGE_TEST_KID")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil LoadedKey when env is unset, got %+v", got)
	}
}

func TestLoadEd25519KeyFromEnv_Base64PKCS8(t *testing.T) {
	_, priv := generateTestKey(t)
	der := pkcs8DER(t, priv)
	t.Setenv("FORGE_TEST_KEY", base64.StdEncoding.EncodeToString(der))
	t.Setenv("FORGE_TEST_KID", "test-kid-1")

	got, err := LoadEd25519KeyFromEnv("FORGE_TEST_KEY", "FORGE_TEST_KID")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil LoadedKey")
	}
	if got.Kid != "test-kid-1" {
		t.Errorf("kid: got %q want %q", got.Kid, "test-kid-1")
	}
	if !priv.Equal(got.Private) {
		t.Errorf("loaded private key differs from source")
	}
}

func TestLoadEd25519KeyFromEnv_PEMInline(t *testing.T) {
	_, priv := generateTestKey(t)
	der := pkcs8DER(t, priv)
	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	t.Setenv("FORGE_TEST_KEY", string(pemBlock))

	got, err := LoadEd25519KeyFromEnv("FORGE_TEST_KEY", "FORGE_TEST_KID")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil LoadedKey")
	}
	if got.Kid != "forge-audit-v1" {
		t.Errorf("default kid: got %q want %q", got.Kid, "forge-audit-v1")
	}
}

func TestLoadEd25519KeyFromEnv_InvalidBase64(t *testing.T) {
	t.Setenv("FORGE_TEST_KEY", "not!base64!!")
	_, err := LoadEd25519KeyFromEnv("FORGE_TEST_KEY", "FORGE_TEST_KID")
	if err == nil {
		t.Fatal("expected error on invalid base64")
	}
	if !strings.Contains(err.Error(), "not base64") {
		t.Errorf("error should mention base64: %v", err)
	}
}

func TestLoadEd25519KeyFromEnv_RSARejected(t *testing.T) {
	// A non-Ed25519 PKCS#8 payload must be rejected — we never want
	// the operator to accidentally boot Forge signing with an RSA
	// key.
	badPKCS8 := []byte{0x30, 0x02, 0x30, 0x00} // truncated garbage
	t.Setenv("FORGE_TEST_KEY", base64.StdEncoding.EncodeToString(badPKCS8))
	_, err := LoadEd25519KeyFromEnv("FORGE_TEST_KEY", "FORGE_TEST_KID")
	if err == nil {
		t.Fatal("expected error on non-Ed25519 key")
	}
}

func TestLoadEd25519KeyFromFile(t *testing.T) {
	_, priv := generateTestKey(t)
	der := pkcs8DER(t, priv)
	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.pem")
	if err := os.WriteFile(path, pemBlock, 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	got, err := LoadEd25519KeyFromFile(path, "file-kid")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Kid != "file-kid" {
		t.Errorf("kid: got %q want %q", got.Kid, "file-kid")
	}
	if !priv.Equal(got.Private) {
		t.Errorf("loaded key differs from source")
	}
}

func TestLoadEd25519KeyFromFile_MissingFile(t *testing.T) {
	_, err := LoadEd25519KeyFromFile("/definitely/not/a/real/path.pem", "")
	if err == nil {
		t.Fatal("expected error on missing file")
	}
}

func TestAuditSigner_SignVerifyRoundTrip(t *testing.T) {
	pub, priv := generateTestKey(t)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "kid-rt"})
	payload := []byte(`{"event":"session_start","seq":1}`)
	sigB64 := signer.Sign(payload)
	if sigB64 == "" {
		t.Fatal("empty signature")
	}
	if err := VerifySignature(pub, payload, sigB64); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestVerifySignature_RejectsTamperedContent(t *testing.T) {
	pub, priv := generateTestKey(t)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "k"})
	original := []byte(`{"event":"tool_exec","tool":"cli_execute"}`)
	sig := signer.Sign(original)
	tampered := []byte(`{"event":"tool_exec","tool":"rm_rf_root"}`)
	if err := VerifySignature(pub, tampered, sig); err == nil {
		t.Fatal("expected verify to fail on tampered content")
	}
}

func TestVerifySignature_RejectsBadBase64(t *testing.T) {
	pub, _ := generateTestKey(t)
	err := VerifySignature(pub, []byte("hello"), "!!!not base64!!!")
	if err == nil {
		t.Fatal("expected error on invalid base64 signature")
	}
}

func TestPublicJWKS_Roundtrip(t *testing.T) {
	pub, priv := generateTestKey(t)
	key := LoadedKey{Private: priv, Public: pub, Kid: "roundtrip"}
	jwks := PublicJWKS(key)
	if len(jwks.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(jwks.Keys))
	}
	jwk := jwks.Keys[0]
	if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
		t.Errorf("wrong kty/crv: kty=%q crv=%q", jwk.Kty, jwk.Crv)
	}
	if jwk.Alg != "EdDSA" || jwk.Use != "sig" {
		t.Errorf("wrong alg/use: alg=%q use=%q", jwk.Alg, jwk.Use)
	}
	extracted, err := PublicKeyFromJWK(jwk)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !bytes.Equal(extracted, pub) {
		t.Errorf("extracted pubkey differs from source")
	}
}

func TestPublicJWKS_EmptyInputReturnsEmptySet(t *testing.T) {
	jwks := PublicJWKS()
	if jwks.Keys == nil {
		t.Fatal("expected non-nil (empty) keys slice for JSON stability")
	}
	if len(jwks.Keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(jwks.Keys))
	}
	data, err := json.Marshal(jwks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"keys":[]}` {
		t.Errorf("wire shape: got %s want {\"keys\":[]}", string(data))
	}
}

func TestPublicKeyFromJWK_RejectsWrongCurve(t *testing.T) {
	_, err := PublicKeyFromJWK(JWK{Kty: "OKP", Crv: "X25519", X: "abc"})
	if err == nil {
		t.Fatal("expected error on unsupported curve")
	}
}

func TestPublicKeyFromJWK_RejectsShortKey(t *testing.T) {
	// base64url of 4 bytes — way short of ed25519.PublicKeySize.
	_, err := PublicKeyFromJWK(JWK{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString([]byte("abcd"))})
	if err == nil {
		t.Fatal("expected error on short pubkey")
	}
}

// TestSetSigner_EnablesSigOnEmit is the end-to-end proof: install
// a signer on a fresh AuditLogger, emit one event, capture the
// stdout NDJSON, and verify Kid + Sig are present + valid.
func TestSetSigner_EnablesSigOnEmit(t *testing.T) {
	pub, priv := generateTestKey(t)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "e2e"})

	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)

	logger.Emit(AuditEvent{
		Event:  "session_start",
		TaskID: "t-1",
	})

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no event emitted")
	}
	var evt AuditEvent
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		t.Fatalf("parsing emitted event: %v", err)
	}
	if evt.Kid != "e2e" {
		t.Errorf("kid: got %q want %q", evt.Kid, "e2e")
	}
	if evt.Sig == "" {
		t.Fatal("expected sig, got empty")
	}
	// Recompute canonical bytes the way the signer did and verify.
	canonical, err := canonicalBytesForSigning(evt)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if err := VerifySignature(pub, canonical, evt.Sig); err != nil {
		t.Errorf("emitted signature failed to verify: %v", err)
	}
}

// TestUnsignedEmitOmitsKidAndSig covers the backward-compat path:
// without a signer installed, the wire shape is unchanged (Kid + Sig
// absent, per omitempty).
func TestUnsignedEmitOmitsKidAndSig(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)

	logger.Emit(AuditEvent{
		Event:  "session_start",
		TaskID: "t-1",
	})

	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, `"kid"`) {
		t.Errorf("unsigned emit leaked kid field: %s", line)
	}
	if strings.Contains(line, `"sig"`) {
		t.Errorf("unsigned emit leaked sig field: %s", line)
	}
}
