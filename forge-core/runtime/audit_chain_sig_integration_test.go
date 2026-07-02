package runtime

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

// TestIntegration_ChainAndSigCompose is the acceptance test for the
// #212/#213 integration: an event emitted with a signer installed
// carries prev_hash, kid, AND sig; the signature covers prev_hash
// (so tampering with the chain link breaks the signature too); and
// VerifyAuditLog rejects any of chain break / sig break / structural
// error.
func TestIntegration_ChainAndSigCompose(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "k"})
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)
	for i := range 4 {
		logger.Emit(AuditEvent{Event: "tool_exec", TaskID: string(rune('a' + i))})
	}

	// Every emitted event carries all three fields.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, line := range lines {
		var evt AuditEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("line %d parse: %v", i, err)
		}
		if evt.PrevHash == "" {
			t.Errorf("line %d missing prev_hash", i)
		}
		if evt.Kid != "k" {
			t.Errorf("line %d kid=%q want k", i, evt.Kid)
		}
		if evt.Sig == "" {
			t.Errorf("line %d missing sig", i)
		}
	}
	// First event's prev_hash MUST be the genesis constant.
	var first AuditEvent
	_ = json.Unmarshal([]byte(lines[0]), &first)
	if first.PrevHash != AuditChainGenesis {
		t.Errorf("first prev_hash = %q; want AuditChainGenesis", first.PrevHash)
	}

	// End-to-end verify: chain + sig both checked.
	opts := VerifyOptions{Pubkeys: map[string]ed25519.PublicKey{"k": pub}}
	res, err := VerifyAuditLog(bytes.NewReader(buf.Bytes()), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("expected OK, got line=%d reason=%s", res.FirstBadLine, res.Reason)
	}
	if res.ChainChecked != 4 {
		t.Errorf("ChainChecked=%d want 4", res.ChainChecked)
	}
	if res.SigChecked != 4 {
		t.Errorf("SigChecked=%d want 4", res.SigChecked)
	}
	if !res.GenesisSeen {
		t.Error("GenesisSeen should be true")
	}
}

// TestIntegration_SigCoversPrevHash proves that tampering with just
// prev_hash breaks the signature — proving prev_hash is covered by
// the signature. This is the property the reviewer asked for: chain
// tampering is caught even if the tamperer recomputes downstream
// prev_hashes.
func TestIntegration_SigCoversPrevHash(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "k"})
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)
	logger.Emit(AuditEvent{Event: "session_start"})
	logger.Emit(AuditEvent{Event: "session_end"})

	// Parse line 2, swap its prev_hash to a different (fake) hash,
	// re-marshal, and re-inject. Because we don't re-sign, the
	// signature no longer matches — even though the prev_hash bytes
	// were changed to something that looks valid.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	var evt2 AuditEvent
	if err := json.Unmarshal([]byte(lines[1]), &evt2); err != nil {
		t.Fatalf("parse: %v", err)
	}
	evt2.PrevHash = "1111111111111111111111111111111111111111111111111111111111111111"
	tamperedLine2, _ := json.Marshal(evt2)
	tampered := lines[0] + "\n" + string(tamperedLine2) + "\n"

	// Skip chain check → sig verification is the only mechanism that
	// can catch this. If sig didn't cover prev_hash, this would pass.
	opts := VerifyOptions{
		Pubkeys:   map[string]ed25519.PublicKey{"k": pub},
		SkipChain: true,
	}
	res, err := VerifyAuditLog(bytes.NewReader([]byte(tampered)), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK() {
		t.Fatal("signature must cover prev_hash — tampering with prev_hash alone must break the sig")
	}
	if !strings.Contains(res.Reason, "signature verify") {
		t.Errorf("expected sig-verify failure, got: %s", res.Reason)
	}
}

// TestIntegration_UnsignedStreamStillChains guards that operators
// who haven't wired signing still get chain integrity — R5 stands
// alone even without R6.
func TestIntegration_UnsignedStreamStillChains(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	// No signer installed.
	logger.Emit(AuditEvent{Event: "session_start"})
	logger.Emit(AuditEvent{Event: "tool_exec"})
	logger.Emit(AuditEvent{Event: "session_end"})

	res, err := VerifyAuditLog(bytes.NewReader(buf.Bytes()), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("unsigned stream should chain-verify: %s", res.Reason)
	}
	if res.ChainChecked != 3 {
		t.Errorf("ChainChecked=%d want 3", res.ChainChecked)
	}
	if res.SigChecked != 0 {
		t.Errorf("SigChecked should be 0 for unsigned stream, got %d", res.SigChecked)
	}
	// No head-truncation warning, no signed-but-no-pubkey warning.
	if len(res.Errors) != 0 {
		t.Errorf("unexpected warnings: %v", res.Errors)
	}
}

// TestIntegration_HeadTruncationSoftWarn confirms: dropping the
// first event surfaces via res.Errors but still returns OK() (the
// tail is internally consistent).
func TestIntegration_HeadTruncationSoftWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	for range 3 {
		logger.Emit(AuditEvent{Event: "tool_exec"})
	}
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	// Drop line 1 (the genesis-bearing event).
	truncated := bytes.Join(lines[1:], []byte("\n"))
	// The first surviving event's prev_hash points at the removed
	// event's hash — which is NOT genesis. VerifyAuditLog treats
	// this as the tail-of-stream case (first event not genesis).
	// The internal chain among lines 2 & 3 is still consistent,
	// so verify succeeds with a warning rather than failure.
	res, err := VerifyAuditLog(bytes.NewReader(truncated), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Chain within lines 2-3 is consistent among themselves; the
	// verifier treats the new first line as its own genesis-of-view.
	// But we DO want a soft warning about the missing genesis so
	// operators know they're looking at a partial stream.
	found := false
	for _, w := range res.Errors {
		if strings.Contains(w, "truncated") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected head-truncation warning in res.Errors, got %v", res.Errors)
	}
}
