---
title: "Audit tamper evidence"
description: "Hash-chained NDJSON audit stream — how it works and how to verify a captured log."
order: 8
---

Every event Forge emits carries a `prev_hash` that pins it to the
previous event's canonical JSON bytes. Together with the per-emit
re-computation of the tail hash, this forms a sha256 chain over the
audit stream. Any post-hoc alteration to a prior event — a changed
field, an added byte, a deleted line — breaks the chain at the point
of tampering.

This is Forge's answer to governance requirement **R5 (tamper-evident
receipts)** and closes issue [#212](https://github.com/initializ/forge/issues/212).

## The chain

Every `AuditEvent` gains one field:

```json
{
  "ts": "2026-05-28T22:07:31Z",
  "event": "tool_exec",
  "seq": 42,
  "prev_hash": "9c4a5e7f8b1d2c3e...",
  ...
}
```

- **Genesis**: the first event of a process lifetime carries
  `prev_hash: "00…00"` (32 zero bytes hex-encoded, exposed as
  `runtime.AuditChainGenesis`). Verifiers treat that value as
  "no predecessor" and start the walk there.
- **Progression**: every subsequent event carries
  `prev_hash = sha256(previous_event_canonical_json)`.
- **Canonical form**: bytes produced by `json.Marshal(event)` — the
  same bytes written to the sink, minus the trailing newline. The
  verifier re-marshals through the same code path, so producer and
  consumer agree on field order and encoding.

The `prev_hash` field is emitted on **every** event (no `omitempty`);
absence is itself a tampering signal.

## Concurrency

Two concurrent `Emit` calls do NOT race. The chain-mint + marshal +
tail-hash update + sink write execute under a single mutex, so
events land on the sink in the same order they were chained.
`TestHashChain_ConcurrentEmitsProduceValidChain` exercises 200
concurrent emitters and asserts the resulting stream verifies.

## Verifying a captured stream

`forge audit verify` walks an NDJSON stream, recomputes each event's
canonical hash, and asserts the next event's `prev_hash` matches.
Exits 0 on clean; non-zero with a report on tampering.

```sh
forge audit verify /var/log/forge/audit.ndjson
# → OK: 12471 events, hash chain intact

forge audit verify tampered.ndjson
# → TAMPERING DETECTED at line 8341 (event "tool_exec")
#     expected prev_hash: 9c4a5e7f…8b1d2c3e
#     actual prev_hash:   ffffffff…00000000
#     events verified before break: 8340
```

The tool also reads from stdin — useful in pipelines:

```sh
kubectl logs my-agent-pod | jq -c 'select(.event)' | forge audit verify -
```

## What tamper-evidence buys you

- **Detection.** Any content change to a past event, any deletion,
  any insertion is caught. The verifier reports the exact line and
  the mismatch.
- **Trust boundary shifts.** You no longer need to trust the file
  system between event emission and audit review. A malicious
  root user (or a compromised log-forwarder) that alters events
  gets caught by the next verify pass.
- **Compliance narrative.** Framework requirements for tamper-
  evident receipts (governance R5) are satisfied by hash chaining;
  R6 layers per-event Ed25519 signatures on top for identity
  binding (see #213).

## What it does NOT buy you

- **Confidentiality.** The audit stream contains what it contained
  before. Hashing doesn't hide field values.
- **Availability.** A tamperer who wholesale-deletes the audit
  file destroys the evidence too — hash chaining detects
  post-hoc alteration, not deletion. Forward the stream to an
  append-only sink (SIEM / immutable-storage backend) for that.
- **Non-repudiation without the R6 signature layer.** Without
  cryptographic signing, an operator who controls the emitter
  could forge a clean chain from scratch. R6 (issue #213) adds
  Ed25519 event signatures so a verifier can prove that only the
  agent identity's private key could have produced the stream.

## Testing

- Unit: `forge-core/runtime/audit_hash_chain_test.go` — genesis,
  progression, tampering, deletion, concurrency, malformed lines.
- CLI: `forge-cli/cmd/audit_test.go` — clean and tampered
  end-to-end through `forge audit verify`.

## Interop with SIEM

The stream shape is unchanged apart from the new `prev_hash` field
— SIEM parsers that ignore unknown fields will continue to work.
Consumers that want to leverage the chain can call
`runtime.VerifyAuditLog` directly (Go) or reimplement the walk in
any language (sha256 + json.Marshal-equivalent canonical form; the
Go implementation is <100 LOC in `audit_verify.go`).
