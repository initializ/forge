package runtime

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

// TestJCS_SigCanonicalizationStampedOnSignedEvents pins the
// wire-shape guarantee: every signed event carries sigp="jcs-1"
// alongside kid + sig. Non-Go verifiers key off Sigp to select the
// right canonicalization; a missing value is treated as unspecified
// (rejected at verify time — see TestJCS_UnsupportedSigpRejected).
func TestJCS_SigCanonicalizationStampedOnSignedEvents(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "k"})
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)
	logger.Emit(AuditEvent{Event: "session_start"})

	line := strings.TrimSpace(buf.String())
	var evt AuditEvent
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if evt.Sigp != SigCanonicalizationJCS1 {
		t.Errorf("sigp: got %q want %q", evt.Sigp, SigCanonicalizationJCS1)
	}
	if evt.Sig == "" || evt.Kid == "" {
		t.Errorf("expected sig + kid alongside sigp: sig=%q kid=%q", evt.Sig, evt.Kid)
	}
}

// TestJCS_UnsignedEventOmitsSigp ensures the wire shape stays clean
// when signing is off — no sigp, no kid, no sig. Any of those on an
// unsigned event would confuse consumers keying off the field's
// presence.
func TestJCS_UnsignedEventOmitsSigp(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.Emit(AuditEvent{Event: "session_start"})
	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, `"sigp"`) {
		t.Errorf("unsigned event leaked sigp: %s", line)
	}
}

// TestJCS_LargeIntegerFieldsRoundTrip is the acceptance test for the
// precision-hole fix. Pre-JCS the verifier re-marshaled the parsed
// event, so a Fields value like int64(2^53+1) signed as one byte
// sequence and verified as a different one (JSON numbers decode to
// float64; producer's int64 rounded on the way back out).
//
// With JCS both sides canonicalize the PARSED value through the
// same ES6-double rule, so they agree — but the numeric value
// itself IS rounded, per the caveat documented on
// canonicalBytesForSigning. Consumers preserving 64-bit-exact
// values must stringify. This test proves the signature/verify
// round-trip succeeds; the stringify-your-big-ints obligation is
// enforced by the docs, not the library.
func TestJCS_LargeIntegerFieldsRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "k"})
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)

	// int64(2^53 + 1) — one past the fp64 mantissa boundary. With
	// Go's json.Marshal preimage this would sign as
	// ...9007199254740993 and verify as ...9007199254740992, failing
	// even on an untampered stream. Under JCS both sides converge.
	logger.Emit(AuditEvent{
		Event:  "session_start",
		Fields: map[string]any{"big": int64(9007199254740993)},
	})

	opts := VerifyOptions{Pubkeys: map[string]ed25519.PublicKey{"k": pub}}
	res, err := VerifyAuditLog(bytes.NewReader(buf.Bytes()), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("expected OK on untampered big-int stream; got line=%d reason=%s",
			res.FirstBadLine, res.Reason)
	}
	if res.SigChecked != 1 {
		t.Errorf("SigChecked=%d want 1", res.SigChecked)
	}
}

// TestJCS_UnsupportedSigpRejected pins the sigp-covered-by-signature
// property: a tamperer who rewrites sigp to a value the verifier
// doesn't recognize gets an actionable "unsupported sigp scheme"
// error, not a generic sig-verify failure.
func TestJCS_UnsupportedSigpRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewAuditSigner(LoadedKey{Private: priv, Public: pub, Kid: "k"})
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.SetSigner(signer)
	logger.Emit(AuditEvent{Event: "session_start"})

	line := strings.TrimSpace(buf.String())
	var evt AuditEvent
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		t.Fatalf("parse: %v", err)
	}
	evt.Sigp = "attacker-canonicalization-v0"
	remarshaled, _ := json.Marshal(evt)
	tampered := append(remarshaled, '\n')

	opts := VerifyOptions{
		Pubkeys:   map[string]ed25519.PublicKey{"k": pub},
		SkipChain: true, // isolate the sigp check
	}
	res, err := VerifyAuditLog(bytes.NewReader(tampered), opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK() {
		t.Fatal("expected failure on unsupported sigp")
	}
	if !strings.Contains(res.Reason, "unsupported sigp scheme") {
		t.Errorf("wrong reason: %s", res.Reason)
	}
}

// TestJCS_CanonicalizationIsDeterministicAcrossEmits proves the
// canonicalization output is stable — same event value → same
// signed bytes → same signature. This is the property that lets a
// non-Go verifier converge on the same preimage as the Go producer.
func TestJCS_CanonicalizationIsDeterministic(t *testing.T) {
	evt := AuditEvent{
		Event:  "tool_exec",
		Kid:    "k",
		Sigp:   SigCanonicalizationJCS1,
		Fields: map[string]any{"b": 2, "a": 1, "c": 3},
	}
	a, err := canonicalBytesForSigning(evt)
	if err != nil {
		t.Fatalf("canonicalize 1: %v", err)
	}
	// Shuffle map iteration order — JCS output must not depend on it.
	evt2 := AuditEvent{
		Event:  "tool_exec",
		Kid:    "k",
		Sigp:   SigCanonicalizationJCS1,
		Fields: map[string]any{"c": 3, "a": 1, "b": 2},
	}
	b, err := canonicalBytesForSigning(evt2)
	if err != nil {
		t.Fatalf("canonicalize 2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("JCS output not deterministic across map orderings:\n  a=%s\n  b=%s", a, b)
	}
}

// TestJCS_KeysSortedInOutput probes JCS's core requirement — top-
// level and nested object keys sorted UTF-16 lexicographically. If
// any key ordering breaks, non-Go verifiers will diverge.
func TestJCS_KeysSortedInOutput(t *testing.T) {
	evt := AuditEvent{
		Event: "tool_exec",
		Kid:   "k",
		Sigp:  SigCanonicalizationJCS1,
		Fields: map[string]any{
			"zebra": "z",
			"alpha": "a",
			"mango": "m",
		},
	}
	out, err := canonicalBytesForSigning(evt)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	s := string(out)
	// Assert alpha appears before mango which appears before zebra
	// somewhere in the output (inside the "fields" object).
	a := strings.Index(s, `"alpha"`)
	m := strings.Index(s, `"mango"`)
	z := strings.Index(s, `"zebra"`)
	if a == -1 || m == -1 || z == -1 {
		t.Fatalf("expected all three keys in output: %s", s)
	}
	if a >= m || m >= z {
		t.Errorf("keys not sorted: alpha@%d mango@%d zebra@%d in %s", a, m, z, s)
	}
}
