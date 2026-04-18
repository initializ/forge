---
title: "Trust Model"
description: "How Forge evaluates trust for skills, tools, and external services."
order: 3
---

## Trust Evaluation

Forge evaluates trust at multiple levels — skills, build artifacts, and secrets — to ensure integrity from development through production.

### Skill Trust Policy

The default trust policy for skills:

| Setting | Default | Description |
|---------|---------|-------------|
| `RequireChecksum` | `true` | Skills must have valid checksums |
| `RequireSignature` | `false` | Signature verification is opt-in |

Skills loaded without a signature emit a warning log at scan time. The skill scanner also validates symlinks — symlinks that resolve outside the project root directory are skipped with a warning.

### Build Artifact Verification

When a signing key exists, `forge build` automatically:

1. Computes SHA-256 checksums of all generated artifacts
2. Signs the checksums with Ed25519
3. Writes `checksums.json` with checksums, signature, and key ID

At runtime, `forge run` can verify artifacts against trusted keys in `~/.forge/trusted-keys/`.

See [Build Signing](/docs/security/build-signing) for details on key management and verification.

### Secret Reuse Detection

At startup, the runtime detects when the same secret value is shared across different purpose categories. This prevents credential reuse mistakes that could escalate the impact of a single token compromise.

| Category | Keys |
|----------|------|
| `llm` | OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY |
| `search` | TAVILY_API_KEY, PERPLEXITY_API_KEY |
| `telegram` | TELEGRAM_BOT_TOKEN |
| `slack` | SLACK_APP_TOKEN, SLACK_BOT_TOKEN |

Same-category reuse (e.g., two LLM keys with the same value) is allowed. Cross-category reuse is blocked with an error.

### Production Build Checks

Production builds (`--prod`) enforce additional security requirements:

- No `dev-open` egress mode
- No dev-only tools (`local_shell`, `local_file_browser`)
- Secret provider chain must include `env` (not just `encrypted-file`)
- `.dockerignore` must exist if a Dockerfile is generated
