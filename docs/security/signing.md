# Build Signing & Verification

> Part of [Forge Documentation](../../README.md)

Forge supports Ed25519 signing of build artifacts for supply chain integrity.

## Key Management

```bash
# Generate an Ed25519 signing keypair
forge key generate
# Output: ~/.forge/signing-key.pem (private) + ~/.forge/signing-key.pub (public)

# Generate with a custom name
forge key generate --name ci-key

# Add a public key to the trusted keyring
forge key trust ~/.forge/signing-key.pub

# List signing and trusted keys
forge key list
```

## Build Signing

When a signing key exists at `~/.forge/signing-key.pem` (or specified via `--signing-key`), `forge build` automatically:

1. Computes SHA-256 checksums of all generated artifacts
2. Signs the checksums with the Ed25519 private key
3. Writes `checksums.json` with checksums, signature, and key ID

## Runtime Verification

At runtime, `forge run` can verify build artifacts against `checksums.json`:

- Validates SHA-256 checksums of all files
- Verifies the Ed25519 signature against trusted keys in `~/.forge/trusted-keys/`
- Verification is optional — if `checksums.json` doesn't exist, it's skipped

## Secret Safety Stage

The build pipeline includes a `secret-safety` stage that:

- Blocks production builds (`--prod`) that only use `encrypted-file` without `env` provider (containers can't use encrypted files at runtime)
- Warns if `.dockerignore` is missing alongside a generated Dockerfile
- Ensures secrets never leak into container images

---
← [Secrets](secrets.md) | [Back to README](../../README.md) | [Guardrails](guardrails.md) →
