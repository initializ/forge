# Secrets Management

> Part of [Forge Documentation](../../README.md)

Forge provides encrypted secret management with per-agent isolation and interactive passphrase prompting.

## Encrypted Storage

Secrets are stored in AES-256-GCM encrypted files with Argon2id key derivation. The file format is `salt(16) || nonce(12) || ciphertext`, with the plaintext being a JSON key-value map.

```bash
# Store a secret (prompts for value securely)
forge secret set OPENAI_API_KEY

# Store with inline value
forge secret set SLACK_BOT_TOKEN xoxb-...

# Retrieve a secret (shows source: encrypted-file or env)
forge secret get OPENAI_API_KEY

# List all secret keys
forge secret list

# Delete a secret
forge secret delete OLD_KEY
```

## Per-Agent Secrets

Each agent can have its own encrypted secrets file at `<agent-dir>/.forge/secrets.enc`, separate from the global `~/.forge/secrets.enc`. Use the `--local` flag to operate on agent-local secrets:

```bash
cd my-agent

# Store a secret in the agent-local file
forge secret set OPENAI_API_KEY sk-agent1-key --local

# Different agent, different key
cd ../other-agent
forge secret set OPENAI_API_KEY sk-agent2-key --local
```

At runtime, secrets are resolved in order: **agent-local** -> **global** -> **environment variables**. This lets you override global defaults per agent.

## Runtime Passphrase Prompting

When `forge run` encounters encrypted secrets and no `FORGE_PASSPHRASE` environment variable is set, it prompts interactively:

```
$ forge run
Enter passphrase for encrypted secrets: ****
```

In non-interactive environments (CI/CD), set the passphrase via environment variable:

```bash
export FORGE_PASSPHRASE="my-passphrase"
forge run
```

## Smart Init Passphrase

`forge init` detects whether `~/.forge/secrets.enc` already exists:

- **First time**: prompts for passphrase + confirmation (new setup)
- **Subsequent**: prompts once and validates by attempting to decrypt the existing file

## Configuration

```yaml
secrets:
  providers:
    - encrypted-file          # AES-256-GCM encrypted file
    - env                     # Environment variables (fallback)
```

Secret files are automatically excluded from git (`.forge/` in `.gitignore`) and Docker builds (`*.enc` in `.dockerignore`).

## File Safety

- `.forge/` directories are automatically added to `.gitignore`
- `*.enc` files are excluded in `.dockerignore`
- Secret files never appear in container images

---
← [Egress Security](egress.md) | [Back to README](../../README.md) | [Build Signing](signing.md) →
