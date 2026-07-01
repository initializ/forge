package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// writeAuditFixture emits a signed NDJSON stream to a temp file and
// returns the file path + the JWKS the operator would ship next to
// it. All keys stay in memory — no os.Setenv.
func writeAuditFixture(t *testing.T, dir, kid string, tamper bool) (auditPath, jwksPath string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer := coreruntime.NewAuditSigner(coreruntime.LoadedKey{
		Private: priv, Public: pub, Kid: kid,
	})

	var buf bytes.Buffer
	logger := coreruntime.NewAuditLogger(&buf)
	logger.SetSigner(signer)
	logger.Emit(coreruntime.AuditEvent{Event: "session_start"})
	logger.Emit(coreruntime.AuditEvent{Event: "tool_exec"})
	logger.Emit(coreruntime.AuditEvent{Event: "session_end"})

	data := buf.Bytes()
	if tamper {
		// Length-preserving edit on line 2 breaks the signature.
		data = bytes.Replace(data, []byte(`"tool_exec"`), []byte(`"tool_exeC"`), 1)
	}

	auditPath = filepath.Join(dir, "audit.ndjson")
	if err := os.WriteFile(auditPath, data, 0o600); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	jwksPath = filepath.Join(dir, "audit.jwks")
	jwks := coreruntime.PublicJWKS(coreruntime.LoadedKey{
		Private: priv, Public: pub, Kid: kid,
	})
	jwksBytes, err := json.Marshal(jwks)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	if err := os.WriteFile(jwksPath, jwksBytes, 0o600); err != nil {
		t.Fatalf("write jwks: %v", err)
	}
	return auditPath, jwksPath
}

func TestAuditVerifyCLI_CleanStreamExits0(t *testing.T) {
	dir := t.TempDir()
	auditPath, jwksPath := writeAuditFixture(t, dir, "cli-kid", false)

	// Reset the flag between subtests — cobra's global flag state
	// persists across cmd.Execute invocations.
	auditVerifyPubKeyFile = jwksPath
	defer func() { auditVerifyPubKeyFile = "" }()

	var stdout, stderr bytes.Buffer
	auditVerifyCmd.SetOut(&stdout)
	auditVerifyCmd.SetErr(&stderr)
	if err := auditVerifyRun(auditVerifyCmd, []string{auditPath}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "OK:") {
		t.Errorf("stdout should say OK: got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "3 signatures checked") {
		t.Errorf("expected sig count in output: %q", stdout.String())
	}
}

func TestAuditVerifyCLI_TamperedStreamFails(t *testing.T) {
	dir := t.TempDir()
	auditPath, jwksPath := writeAuditFixture(t, dir, "cli-kid", true)

	auditVerifyPubKeyFile = jwksPath
	defer func() { auditVerifyPubKeyFile = "" }()

	var stdout, stderr bytes.Buffer
	auditVerifyCmd.SetOut(&stdout)
	auditVerifyCmd.SetErr(&stderr)
	err := auditVerifyRun(auditVerifyCmd, []string{auditPath})
	if err == nil {
		t.Fatal("expected non-nil error for tampered stream")
	}
	if !strings.Contains(err.Error(), "audit verify failed") {
		t.Errorf("error should mention verify failure: %v", err)
	}
	if !strings.Contains(stdout.String(), "FAILED at line") {
		t.Errorf("stdout should announce failure: %q", stdout.String())
	}
}

func TestAuditVerifyCLI_SignedWithoutPubkeyWarns(t *testing.T) {
	dir := t.TempDir()
	auditPath, _ := writeAuditFixture(t, dir, "cli-kid", false)

	// Deliberately no --pubkey.
	auditVerifyPubKeyFile = ""

	var stdout, stderr bytes.Buffer
	auditVerifyCmd.SetOut(&stdout)
	auditVerifyCmd.SetErr(&stderr)
	if err := auditVerifyRun(auditVerifyCmd, []string{auditPath}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("stderr should carry warning about missing pubkey: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "OK:") {
		t.Errorf("stdout should still say OK when only signatures unverified: %q", stdout.String())
	}
}

func TestAuditVerifyCLI_BadJWKSFile(t *testing.T) {
	dir := t.TempDir()
	auditPath, _ := writeAuditFixture(t, dir, "cli-kid", false)
	bogus := filepath.Join(dir, "bogus.jwks")
	if err := os.WriteFile(bogus, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write bogus: %v", err)
	}

	auditVerifyPubKeyFile = bogus
	defer func() { auditVerifyPubKeyFile = "" }()

	var stdout, stderr bytes.Buffer
	auditVerifyCmd.SetOut(&stdout)
	auditVerifyCmd.SetErr(&stderr)
	err := auditVerifyRun(auditVerifyCmd, []string{auditPath})
	if err == nil {
		t.Fatal("expected error on malformed JWKS")
	}
	if !strings.Contains(err.Error(), "loading pubkeys") {
		t.Errorf("error should mention pubkey loading: %v", err)
	}
}

// TestLoadJWKSFile_RoundTrip cross-checks that the file→map loader
// produces a pubkey that verifies signatures the corresponding
// signer produces.
func TestLoadJWKSFile_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	dir := t.TempDir()
	jwksPath := filepath.Join(dir, "rt.jwks")
	jwks := coreruntime.PublicJWKS(coreruntime.LoadedKey{Private: priv, Public: pub, Kid: "rt"})
	data, err := json.Marshal(jwks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(jwksPath, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	keys, err := loadJWKSFile(jwksPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	loadedPub, ok := keys["rt"]
	if !ok {
		t.Fatalf("kid 'rt' missing from loaded map")
	}
	// Sign then verify via the loader-supplied key.
	sig := ed25519.Sign(priv, []byte("payload"))
	if !ed25519.Verify(loadedPub, []byte("payload"), sig) {
		t.Errorf("loaded pubkey failed to verify signature")
	}
}
