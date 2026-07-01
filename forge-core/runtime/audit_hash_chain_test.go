package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestHashChain_GenesisAndProgression pins the two structural
// invariants of #212 (governance R5):
//
//	(a) the first emitted event carries AuditChainGenesis as prev_hash
//	(b) every subsequent event carries sha256(previous event JSON) as
//	    prev_hash
func TestHashChain_GenesisAndProgression(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)

	a.Emit(AuditEvent{Event: "one"})
	a.Emit(AuditEvent{Event: "two"})
	a.Emit(AuditEvent{Event: "three"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if got := len(lines); got != 3 {
		t.Fatalf("wrote %d lines, want 3", got)
	}

	var first, second, third AuditEvent
	mustDecode(t, lines[0], &first)
	mustDecode(t, lines[1], &second)
	mustDecode(t, lines[2], &third)

	if first.PrevHash != AuditChainGenesis {
		t.Errorf("first event prev_hash = %q, want %q", first.PrevHash, AuditChainGenesis)
	}
	if got := hashOf(t, lines[0]); second.PrevHash != got {
		t.Errorf("second event prev_hash = %q, want %q", second.PrevHash, got)
	}
	if got := hashOf(t, lines[1]); third.PrevHash != got {
		t.Errorf("third event prev_hash = %q, want %q", third.PrevHash, got)
	}
}

// TestHashChain_VerifyWalksCleanly proves the round-trip: a stream
// produced by Emit verifies via VerifyAuditLog with no errors.
func TestHashChain_VerifyWalksCleanly(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	for i := range 20 {
		a.Emit(AuditEvent{Event: "e", Fields: map[string]any{"i": i}})
	}
	res, err := VerifyAuditLog(&buf)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if !res.OK() {
		t.Fatalf("verify failed at line %d (expected %q, got %q)",
			res.FirstTamperedLine, res.ExpectedPrevHash, res.ActualPrevHash)
	}
	if res.EventCount != 20 {
		t.Errorf("event count = %d, want 20", res.EventCount)
	}
	if !res.GenesisSeen {
		t.Errorf("GenesisSeen = false; stream should start at genesis")
	}
}

// TestHashChain_TamperingDetected exercises the primary contract:
// altering any byte of a previously-written event breaks the chain
// at the NEXT event (whose prev_hash no longer matches the tampered
// event's recomputed hash).
func TestHashChain_TamperingDetected(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	a.Emit(AuditEvent{Event: "alpha"})
	a.Emit(AuditEvent{Event: "beta"})
	a.Emit(AuditEvent{Event: "gamma"})

	// Tamper with event #2 — rewrite "beta" → "BETA".
	tampered := strings.Replace(buf.String(), `"event":"beta"`, `"event":"BETA"`, 1)
	res, err := VerifyAuditLog(strings.NewReader(tampered))
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if res.OK() {
		t.Fatal("expected verification to fail after tampering")
	}
	if res.FirstTamperedLine != 3 {
		// The tampered event is line 2, but the break shows up at
		// line 3 (its prev_hash no longer matches the recomputed
		// hash of the tampered line 2).
		t.Errorf("tampering detected at line %d, want line 3", res.FirstTamperedLine)
	}
}

// TestHashChain_DeletionDetected — dropping an event breaks the
// chain at the successor.
func TestHashChain_DeletionDetected(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	a.Emit(AuditEvent{Event: "a"})
	a.Emit(AuditEvent{Event: "b"})
	a.Emit(AuditEvent{Event: "c"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// Drop line 2.
	trimmed := lines[0] + "\n" + lines[2] + "\n"
	res, err := VerifyAuditLog(strings.NewReader(trimmed))
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if res.OK() {
		t.Fatal("expected verification to fail after deletion")
	}
	if res.FirstTamperedLine != 2 {
		t.Errorf("deletion detected at line %d, want line 2", res.FirstTamperedLine)
	}
}

// TestHashChain_ConcurrentEmitsProduceValidChain checks the
// concurrency contract: many goroutines emitting simultaneously must
// still produce a chain that verifies. If ordering leaked, the
// chain would break.
func TestHashChain_ConcurrentEmitsProduceValidChain(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			a.Emit(AuditEvent{Event: "concurrent", Fields: map[string]any{"i": i}})
		}()
	}
	wg.Wait()

	res, err := VerifyAuditLog(&buf)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if !res.OK() {
		t.Fatalf("chain broke under concurrent emits at line %d", res.FirstTamperedLine)
	}
	if res.EventCount != N {
		t.Errorf("event count = %d, want %d", res.EventCount, N)
	}
}

// TestHashChain_GenesisConstantShape guards against accidental
// modification of the AuditChainGenesis value — it's part of the
// wire contract for downstream verifiers.
func TestHashChain_GenesisConstantShape(t *testing.T) {
	t.Parallel()
	if len(AuditChainGenesis) != 64 {
		t.Fatalf("AuditChainGenesis length = %d, want 64", len(AuditChainGenesis))
	}
	if strings.Trim(AuditChainGenesis, "0") != "" {
		t.Fatalf("AuditChainGenesis = %q, want all zeros", AuditChainGenesis)
	}
}

// TestHashChain_VerifyDetectsMalformedLine — a line the parser can't
// decode is reported cleanly, not a panic.
func TestHashChain_VerifyDetectsMalformedLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	a.Emit(AuditEvent{Event: "clean"})
	// Append a garbage line.
	buf.WriteString("{this is not json}\n")
	res, err := VerifyAuditLog(&buf)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if res.OK() {
		t.Fatal("expected verification to flag the malformed line")
	}
	if res.FirstTamperedLine != 2 {
		t.Errorf("malformed line reported at %d, want 2", res.FirstTamperedLine)
	}
}

// TestHashChain_PrevHashAlwaysWritten pins the "no omitempty" choice
// — the field must appear on every line so absence is a signal, not
// a Go-JSON quirk.
func TestHashChain_PrevHashAlwaysWritten(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	a.Emit(AuditEvent{Event: "x"})
	if !bytes.Contains(buf.Bytes(), []byte(`"prev_hash":`)) {
		t.Errorf("emitted event omitted prev_hash — expected always-present\n%s", buf.String())
	}
}

// ─── helpers ─────────────────────────────────────────────────────

func mustDecode(t *testing.T, line string, into *AuditEvent) {
	t.Helper()
	if err := json.Unmarshal([]byte(line), into); err != nil {
		t.Fatalf("decode: %v (line=%q)", err, line)
	}
}

func hashOf(t *testing.T, line string) string {
	t.Helper()
	// The producer serialized via json.Marshal(event). To match, we
	// must round-trip: decode → re-encode → sha256. The verifier does
	// exactly the same in production.
	var e AuditEvent
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatalf("decode for hash: %v", err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal for hash: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
