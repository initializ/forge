---
title: "Secret Management"
description: "AES-256-GCM encrypted secret storage with per-agent isolation."
order: 4
---

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

## Provider Chain Validation

When the chain is built, each candidate encrypted-file provider is eagerly validated before being admitted:

| Candidate state | Result | Operator-visible signal |
|---|---|---|
| File absent (e.g. you never ran `forge secret set --global`) | Silently skipped | None |
| File present and decrypts with the active passphrase | Admitted; cache populated so subsequent reads reuse the cleartext | Normal operation |
| File present but decryption fails (wrong passphrase, corruption) | **Dropped from the chain** with a warning | `forge: skipping secrets provider that failed to load (path=..., error=...)` |

The drop-with-warning behavior prevents a stale `~/.forge/secrets.enc` — one encrypted with a passphrase you've since forgotten or from an unrelated project — from poisoning the chain and hiding the keys your agent-local file declares. The local file's keys still flow through to the agent. The warning tells you exactly which file to delete or re-encrypt.

This validation runs once per `forge run`, in both `OverlaySecretsToEnv` (pre-runner startup) and `Runner.buildSecretProvider` (in-runner).

## Skill-Declared Secrets

Skills declare env var requirements in `SKILL.md` (`metadata.forge.requires.env`). At startup the runtime overlays each declared key from the configured secret provider chain into the process environment, so the skill's script or `cli_execute` invocation finds it via `os.Getenv`.

The overlay key set is the union of:

- **Builtin keys** — LLM (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, etc.), search (`TAVILY_API_KEY`, `PERPLEXITY_API_KEY`), and channel tokens (`SLACK_*`, `TELEGRAM_BOT_TOKEN`). Always attempted even when the provider can't enumerate (e.g. the `env` provider).
- **Provider-enumerated keys** — every key the provider exposes via `List()`. The encrypted-file provider returns whatever you've stored via `forge secret set`, so a skill declaring `ACME_API_TOKEN` works without any code change in forge.

```yaml
# skills/my-skill/SKILL.md
metadata:
  forge:
    requires:
      env:
        required: [ACME_API_TOKEN]
```

```bash
# Store the value once; runtime overlays it on every `forge run`.
forge secret --local set ACME_API_TOKEN my-secret-value
forge run
```

Existing values in the process environment are never overwritten — set `ACME_API_TOKEN` in your shell to override the encrypted store for one session.

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
