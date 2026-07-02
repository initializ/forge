package runtime

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
)

// VerifyResult summarizes the outcome of walking an NDJSON audit
// stream. See VerifyAuditLog.
type VerifyResult struct {
	// EventCount is the total number of well-formed events read.
	EventCount int

	// FirstBadLine is the 1-indexed input line at which verification
	// first failed. Zero when the whole stream verifies.
	FirstBadLine int

	// BadEvent is the parsed body of the bad event (best effort — may
	// be empty if the line failed to parse).
	BadEvent AuditEvent

	// Reason is a short, human-readable classification of the failure.
	// Empty when OK.
	Reason string

	// SigChecked counts how many events had their Ed25519 signature
	// verified (only when the caller supplied a pubkey source).
	SigChecked int

	// Errors accumulates non-fatal issues that don't stop verification.
	Errors []string
}

// OK reports whether the stream verified end-to-end.
func (r VerifyResult) OK() bool { return r.FirstBadLine == 0 }

// VerifyOptions configures VerifyAuditLog.
type VerifyOptions struct {
	// Pubkeys is a map from Kid to Ed25519 public key. When empty,
	// signature verification is skipped entirely (events without a
	// signature pass through; events with a signature are checked
	// only if the caller supplies at least one pubkey). This mirrors
	// the deployment reality: some Forge instances sign, some don't,
	// and a verifier tool used offline shouldn't require the
	// deployment's key to succeed on unsigned streams.
	//
	// When Pubkeys is non-empty AND an event carries a Sig field,
	// the signature MUST verify or the event is flagged.
	Pubkeys map[string]ed25519.PublicKey
}

// VerifyAuditLog walks an NDJSON audit stream and reports any
// integrity failure it can detect: malformed JSON, or (when a
// pubkey is supplied and the event is signed) a bad signature.
// Never panics on malformed input.
//
// This function checks signatures only. A separate hash-chain
// verifier ships alongside #212 (governance R5); when both features
// are merged, `forge audit verify` runs both checks per event.
func VerifyAuditLog(r io.Reader, opts VerifyOptions) (VerifyResult, error) {
	var res VerifyResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var lineNum int
	for scanner.Scan() {
		lineNum++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var evt AuditEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			if res.FirstBadLine == 0 {
				res.FirstBadLine = lineNum
				res.Reason = fmt.Sprintf("malformed JSON: %v", err)
			}
			return res, nil
		}
		res.EventCount++

		if evt.Sig != "" && len(opts.Pubkeys) > 0 {
			pub, ok := opts.Pubkeys[evt.Kid]
			if !ok {
				res.FirstBadLine = lineNum
				res.BadEvent = evt
				res.Reason = fmt.Sprintf("no pubkey for kid %q", evt.Kid)
				return res, nil
			}
			canonical, err := canonicalBytesForSigning(evt)
			if err != nil {
				res.FirstBadLine = lineNum
				res.BadEvent = evt
				res.Reason = fmt.Sprintf("canonicalizing for verify: %v", err)
				return res, nil
			}
			if err := VerifySignature(pub, canonical, evt.Sig); err != nil {
				res.FirstBadLine = lineNum
				res.BadEvent = evt
				res.Reason = fmt.Sprintf("signature verify: %v", err)
				return res, nil
			}
			res.SigChecked++
		} else if evt.Sig != "" && len(opts.Pubkeys) == 0 {
			// Event is signed but caller didn't supply keys. That's a
			// weaker verification, not a failure — surface it once via
			// the errors slice so operators know the sig wasn't checked.
			if len(res.Errors) == 0 {
				res.Errors = append(res.Errors,
					"stream contains signed events but --pubkey was not provided; signatures not verified")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("reading audit stream: %w", err)
	}
	return res, nil
}
