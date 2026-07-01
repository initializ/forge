package runtime

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// VerifyResult summarizes the outcome of walking an NDJSON audit
// stream against its hash chain. See VerifyAuditLog.
type VerifyResult struct {
	// EventCount is the total number of well-formed events read.
	EventCount int

	// GenesisSeen is true when the first event's PrevHash matched
	// AuditChainGenesis. A stream whose first event doesn't start at
	// genesis was either truncated at the head or produced by a
	// non-conforming writer.
	GenesisSeen bool

	// FirstTamperedLine is the 1-indexed input line at which the
	// chain first broke. Zero when the whole stream verifies.
	FirstTamperedLine int

	// TamperedEvent is the parsed body of the tampered event (best
	// effort — may be empty if the line failed to parse).
	TamperedEvent AuditEvent

	// ExpectedPrevHash is the hash the failing event SHOULD have
	// carried in its `prev_hash` field, computed as sha256 of the
	// previous line's canonical JSON. Empty on non-hash failures.
	ExpectedPrevHash string

	// ActualPrevHash is the value that was actually present.
	ActualPrevHash string

	// Errors accumulates non-fatal issues (malformed JSON on a line
	// that isn't the tampering line, missing schema_version, etc.).
	Errors []string
}

// OK reports whether the stream verified end-to-end.
func (r VerifyResult) OK() bool { return r.FirstTamperedLine == 0 }

// VerifyAuditLog walks an NDJSON audit stream forward and confirms
// each event's PrevHash matches sha256 of the previous event's JSON
// bytes. Returns a VerifyResult describing what it found and never
// panics on malformed input.
//
// The verifier does NOT stop at the first parse error — it keeps
// reading so operators see the full extent of damage — but it DOES
// short-circuit on the first hash-chain break because subsequent
// events can't be meaningfully verified from that point.
//
// Canonical form for hashing is the same one AuditLogger.Emit writes:
// json.Marshal(event) bytes, no trailing newline. The verifier must
// re-marshal each event through the same code path so producer and
// consumer agree on field order.
func VerifyAuditLog(r io.Reader) (VerifyResult, error) {
	var res VerifyResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // audit lines can be large

	expected := AuditChainGenesis
	var lineNum int
	for scanner.Scan() {
		lineNum++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var evt AuditEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			res.Errors = append(res.Errors,
				fmt.Sprintf("line %d: malformed JSON: %v", lineNum, err))
			// Can't advance the chain from a line we can't parse; abort.
			if res.FirstTamperedLine == 0 {
				res.FirstTamperedLine = lineNum
			}
			return res, nil
		}
		res.EventCount++
		if lineNum == 1 && evt.PrevHash == AuditChainGenesis {
			res.GenesisSeen = true
		}
		if evt.PrevHash != expected {
			res.FirstTamperedLine = lineNum
			res.TamperedEvent = evt
			res.ExpectedPrevHash = expected
			res.ActualPrevHash = evt.PrevHash
			return res, nil
		}
		// Re-marshal to compute the NEXT expected hash. We do NOT rely
		// on the raw bytes on disk — a malicious editor could have
		// added whitespace that the parser tolerates but the producer
		// never emitted. Re-marshaling forces canonical bytes.
		canonical, err := json.Marshal(evt)
		if err != nil {
			res.Errors = append(res.Errors,
				fmt.Sprintf("line %d: re-marshal failed: %v", lineNum, err))
			if res.FirstTamperedLine == 0 {
				res.FirstTamperedLine = lineNum
			}
			return res, nil
		}
		sum := sha256.Sum256(canonical)
		expected = hex.EncodeToString(sum[:])
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("reading audit stream: %w", err)
	}
	return res, nil
}
