# Audit event signing

Forge can Ed25519-sign every audit event it emits so an offline
verifier can prove which events came from a given Forge instance and
that the payloads haven't been altered. Signing is opt-in: when no
key is configured, the emitted NDJSON is byte-identical to the
pre-#213 wire shape.

## Enabling signing

Set two env vars before starting the runtime:

```sh
export FORGE_AUDIT_SIGNING_KEY_B64="$(base64 < audit-key.pkcs8.der)"
export FORGE_AUDIT_SIGNING_KID="acme-prod-2026-06"   # optional; defaults to "forge-audit-v1"
```

`FORGE_AUDIT_SIGNING_KEY_B64` accepts either:

- Base64-standard-encoded **PKCS#8 DER** — the format `openssl` emits
  without additional wrapping.
- An inline **PEM** block (heredoc / `secretRef` from Kubernetes).

Generate a key with `openssl`:

```sh
openssl genpkey -algorithm Ed25519 -out audit-key.pem
openssl pkcs8 -topk8 -nocrypt -in audit-key.pem -out audit-key.pkcs8.pem
openssl pkey  -in audit-key.pkcs8.pem -outform DER -out audit-key.pkcs8.der
```

Only Ed25519 keys are accepted — RSA or ECDSA private keys are
rejected at load time so an operator can't accidentally start with
a weaker algorithm.

## Wire shape

When signing is on, each NDJSON event carries two new fields:

```json
{"event":"tool_exec","seq":42,"kid":"acme-prod-2026-06","sig":"...base64..."}
```

- `kid`: the operator-supplied key identifier.
- `sig`: base64-standard-encoded Ed25519 signature over the event's
  canonical JSON with the `sig` field emptied.

Because `sig` is `omitempty`, unsigned events do not include either
field — deployments that never turned signing on see no change.

## Verification

### Runtime JWKS endpoint

The runtime advertises its public keys at:

```
GET /.well-known/forge-audit-keys
```

Media type `application/jwk-set+json`; RFC 8037 shape (`kty=OKP`,
`crv=Ed25519`, `alg=EdDSA`). When signing is off, the endpoint
returns `{"keys":[]}` — consumers can probe for capability without a
version check.

### Offline `forge audit verify`

```sh
# Fetch the JWKS once
curl -sSf https://agent.example/.well-known/forge-audit-keys > audit-keys.jwks

# Verify a captured stream
forge audit verify --pubkey audit-keys.jwks ./sink.ndjson
```

Exit codes:

- `0` — every signed event verified; unsigned events pass through
  the structural check.
- non-zero — the first integrity failure is printed with line
  number, best-effort event snippet, and reason. Verification stops
  at the first failure.

Omitting `--pubkey` performs the structural check only: malformed
JSON is caught, but signatures are not verified. A warning is
printed on stderr when the stream contains signed events but no
JWKS was supplied.

## Key rotation

Rotation is process-scoped: change the env vars and restart the
runtime. New events carry the new `kid`; old events remain
verifiable as long as consumers cache the previous JWKS.
Multi-key JWKS output is a follow-up (tracked separately) — today
the endpoint advertises the single active key.

## Threat model

Signing addresses "the log lied" — an attacker who tampers with a
persisted NDJSON stream can't produce a valid signature without
the private key. It does **not** address:

- Compromise of the signing key itself (protect via secret
  management, K8s secretRef, HSM if available).
- Omission of events during collection (use the hash-chain
  extension #212 to detect deletions).
- Post-hoc reordering across restarts (also covered by the hash
  chain when both features are in effect).

Combine signing (#213) + hash chaining (#212) for full
tamper-evidence.
