package runtime

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

// signedStreamFixture emits N events through a signed AuditLogger and
// returns the raw NDJSON bytes + the pubkey. Helper factored out so
// several tests share the same setup.
func signedStreamFixture(t *testing.T, kid string, count int) ([]byte, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: kid})

	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)
	for range count {
		logger.Emit(AuditEvent{Event: "tool_exec", TaskID: "t-1"})
	}
	return buf.Bytes(), pub
}

func TestVerifyAuditLog_CleanStreamOK(t *testing.T) {
	data, pub := signedStreamFixture(t, "kid-1", 5)
	opts := VerifyOptions{Pubkeys: map[string]ed25519.PublicKey{"kid-1": pub}}
	res, err := VerifyAuditLog(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Errorf("expected OK, got FirstBadLine=%d reason=%s", res.FirstBadLine, res.Reason)
	}
	if res.EventCount != 5 {
		t.Errorf("EventCount: got %d want 5", res.EventCount)
	}
	if res.SigChecked != 5 {
		t.Errorf("SigChecked: got %d want 5", res.SigChecked)
	}
}

func TestVerifyAuditLog_UnsignedStreamOK(t *testing.T) {
	// No signer installed → no Sig field → verifier should still pass
	// (structural check only, no pubkey provided).
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.Emit(AuditEvent{Event: "session_start"})
	logger.Emit(AuditEvent{Event: "session_end"})
	res, err := VerifyAuditLog(bytes.NewReader(buf.Bytes()), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Errorf("unexpected failure: %s", res.Reason)
	}
	if res.SigChecked != 0 {
		t.Errorf("no keys supplied, SigChecked should be 0, got %d", res.SigChecked)
	}
}

func TestVerifyAuditLog_TamperedContentFails(t *testing.T) {
	data, pub := signedStreamFixture(t, "kid-1", 3)
	// Flip a character in the middle of the stream — the signed
	// canonical bytes no longer match.
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}
	// Replace 'tool_exec' with 'tool_exeC' in line 2 — length preserved.
	lines[1] = bytes.Replace(lines[1], []byte(`"tool_exec"`), []byte(`"tool_exeC"`), 1)
	tampered := bytes.Join(lines, []byte("\n"))

	opts := VerifyOptions{Pubkeys: map[string]ed25519.PublicKey{"kid-1": pub}}
	res, err := VerifyAuditLog(bytes.NewReader(tampered), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK() {
		t.Fatal("expected verify to fail on tampered content")
	}
	if res.FirstBadLine != 2 {
		t.Errorf("FirstBadLine: got %d want 2", res.FirstBadLine)
	}
	if !strings.Contains(res.Reason, "signature verify") {
		t.Errorf("reason should mention signature: %q", res.Reason)
	}
}

func TestVerifyAuditLog_UnknownKidFails(t *testing.T) {
	data, _ := signedStreamFixture(t, "unknown-kid", 1)
	// Supply a pubkey under a different kid — the verifier should
	// flag the mismatch, not silently pass.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	opts := VerifyOptions{Pubkeys: map[string]ed25519.PublicKey{"different-kid": pub}}
	res, err := VerifyAuditLog(bytes.NewReader(data), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK() {
		t.Fatal("expected verify to fail on unknown kid")
	}
	if !strings.Contains(res.Reason, "no pubkey for kid") {
		t.Errorf("reason should mention missing kid: %q", res.Reason)
	}
}

func TestVerifyAuditLog_SignedStreamWithoutKeysWarnsButPasses(t *testing.T) {
	data, _ := signedStreamFixture(t, "kid-1", 2)
	res, err := VerifyAuditLog(bytes.NewReader(data), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Errorf("expected OK when signed events but no keys: %s", res.Reason)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected a non-fatal warning about missing pubkey")
	}
}

func TestVerifyAuditLog_MalformedJSONFlagged(t *testing.T) {
	broken := []byte("{\"event\":\"ok\"}\n{not valid json}\n")
	res, err := VerifyAuditLog(bytes.NewReader(broken), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK() {
		t.Fatal("expected malformed JSON to fail")
	}
	if res.FirstBadLine != 2 {
		t.Errorf("FirstBadLine: got %d want 2", res.FirstBadLine)
	}
	if !strings.Contains(res.Reason, "malformed JSON") {
		t.Errorf("reason: %q", res.Reason)
	}
}

func TestVerifyAuditLog_EmptyStreamOK(t *testing.T) {
	res, err := VerifyAuditLog(bytes.NewReader(nil), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Errorf("empty stream should verify: %s", res.Reason)
	}
	if res.EventCount != 0 {
		t.Errorf("EventCount: got %d want 0", res.EventCount)
	}
}
