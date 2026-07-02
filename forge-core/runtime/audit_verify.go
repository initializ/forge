package runtime

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
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

	// BadEvent is the parsed body of the bad event (best effort —
	// may be empty if the line failed to parse).
	BadEvent AuditEvent

	// Reason is a short, human-readable classification of the failure.
	// Empty when OK.
	Reason string

	// SigChecked counts how many events had their Ed25519 signature
	// verified (only when the caller supplied a pubkey source).
	SigChecked int

	// ChainChecked counts how many events had their prev_hash link
	// verified against the previous line's computed hash.
	ChainChecked int

	// GenesisSeen is true when the first well-formed event carried
	// PrevHash == AuditChainGenesis. A false value on a non-empty
	// stream signals HEAD truncation (the run's initial event has
	// been stripped) — surfaced via Errors as a soft warning, not
	// a hard failure, because a partial-stream fragment is a
	// legitimate use case for external SIEMs that ingest continuously.
	GenesisSeen bool

	// Errors accumulates non-fatal issues that don't stop verification.
	// Populated even on OK() streams — used for head-truncation
	// warnings and "signed events but no --pubkey" notes.
	Errors []string
}

// OK reports whether the stream verified end-to-end.
func (r VerifyResult) OK() bool { return r.FirstBadLine == 0 }

// VerifyOptions configures VerifyAuditLog.
type VerifyOptions struct {
	// Pubkeys is a map from Kid to Ed25519 public key. When empty,
	// signature verification is skipped entirely. Events that carry
	// a Sig field are still walked structurally + chain-verified, and
	// a soft-warning note is added to Errors so operators know the
	// signatures were not checked.
	Pubkeys map[string]ed25519.PublicKey

	// SkipChain, when true, verifies signatures only and does NOT
	// walk the hash chain. Useful for tooling that ingests a stream
	// mid-flight (SIEM tail) where the head-of-stream genesis is
	// out of view. Default false — full tamper-evidence checks
	// require both chain and signature verification.
	SkipChain bool
}

// VerifyAuditLog walks an NDJSON audit stream and reports the first
// integrity failure it can detect:
//
//   - Malformed JSON on any line.
//   - `prev_hash` mismatch (chain break) — the current event's
//     prev_hash doesn't equal sha256 of the previous line's raw
//     bytes (excluding the trailing newline). This catches
//     tampering (altered fields, added bytes) and deletion
//     (dropped events).
//   - Ed25519 signature mismatch (when Pubkeys is non-empty and
//     the event carries a Sig field).
//
// Hashing is over the RAW line bytes as read from the stream, not
// over a re-marshaled event — the producer already committed to
// specific bytes and the verifier should not reconstruct them.
// This closes the "large-integer precision" hole where
// json.Marshal(json.Unmarshal(x)) is not a fixed point when Fields
// carries values > 2^53 (they decode to float64 and re-marshal
// differently).
//
// Never panics on malformed input. Reads to EOF or until the first
// failure; malformed lines are treated as hard failures because
// audit consumers must see them (they signal either producer bugs
// or intentional tampering).
func VerifyAuditLog(r io.Reader, opts VerifyOptions) (VerifyResult, error) {
	var res VerifyResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		prevLineHash string
		firstEvent   = true
		signedSeen   bool
	)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := bytes.TrimRight(scanner.Bytes(), "\n")
		if len(raw) == 0 {
			continue
		}

		var evt AuditEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			res.FirstBadLine = lineNum
			res.Reason = fmt.Sprintf("malformed JSON: %v", err)
			return res, nil
		}
		res.EventCount++

		// Chain check (unless skipped).
		if !opts.SkipChain {
			if firstEvent {
				// First event of the stream. Record whether it's the
				// genesis event (well-defined stream head) or a
				// mid-stream tail (SIEM ingesting from an offset).
				// Either is acceptable — head-truncation is a soft
				// warning only, since a partial stream still has
				// internally-consistent chain state.
				if evt.PrevHash == AuditChainGenesis {
					res.GenesisSeen = true
				}
				res.ChainChecked++
			} else {
				if evt.PrevHash != prevLineHash {
					res.FirstBadLine = lineNum
					res.BadEvent = evt
					res.Reason = fmt.Sprintf(
						"prev_hash mismatch: want %s got %s",
						shortHash(prevLineHash), shortHash(evt.PrevHash))
					return res, nil
				}
				res.ChainChecked++
			}
		}

		// Signature check.
		if evt.Sig != "" {
			signedSeen = true
			if len(opts.Pubkeys) > 0 {
				// Reject unknown canonicalization schemes explicitly.
				// A tamperer who rewrites Sigp to a value we don't
				// recognize would otherwise get "signature verify
				// failed" — the specific "unsupported sigp" message
				// is more actionable for operators.
				if evt.Sigp != "" && evt.Sigp != SigCanonicalizationJCS1 {
					res.FirstBadLine = lineNum
					res.BadEvent = evt
					res.Reason = fmt.Sprintf("unsupported sigp scheme %q", evt.Sigp)
					return res, nil
				}
				pub, ok := opts.Pubkeys[evt.Kid]
				if !ok {
					res.FirstBadLine = lineNum
					res.BadEvent = evt
					res.Reason = fmt.Sprintf("no pubkey for kid %q", evt.Kid)
					return res, nil
				}
				canonical, cerr := canonicalBytesForSigning(evt)
				if cerr != nil {
					res.FirstBadLine = lineNum
					res.BadEvent = evt
					res.Reason = fmt.Sprintf("canonicalizing for verify: %v", cerr)
					return res, nil
				}
				if err := VerifySignature(pub, canonical, evt.Sig); err != nil {
					res.FirstBadLine = lineNum
					res.BadEvent = evt
					res.Reason = fmt.Sprintf("signature verify: %v", err)
					return res, nil
				}
				res.SigChecked++
			}
		}

		// Roll chain state forward.
		sum := sha256.Sum256(raw)
		prevLineHash = hex.EncodeToString(sum[:])
		firstEvent = false
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("reading audit stream: %w", err)
	}

	// Soft warnings — populated on OK() streams so operators can
	// still act on partial-verification signals.
	if !opts.SkipChain && res.EventCount > 0 && !res.GenesisSeen {
		res.Errors = append(res.Errors,
			"first event does not carry the genesis prev_hash — head of stream may be truncated")
	}
	if signedSeen && len(opts.Pubkeys) == 0 {
		res.Errors = append(res.Errors,
			"stream contains signed events but --pubkey was not provided; signatures not verified")
	}
	return res, nil
}

// shortHash renders the first 12 hex characters of a hash for
// human-readable error messages. The full hash is available on
// BadEvent.PrevHash for a caller that wants the untruncated value.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12] + "…"
}
