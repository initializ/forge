---
title: "FAQ"
description: "Frequently asked questions about Forge."
order: 1
---

## What LLM providers does Forge support?

Forge supports **OpenAI**, **Anthropic**, **Google Gemini**, **Ollama** (local models), and **custom** OpenAI-compatible endpoints. You can configure fallback chains for automatic failover between providers.

## Can I use Forge without an API key?

Yes. Use [Ollama](https://ollama.ai) with a locally-hosted model:

```yaml
model:
  provider: ollama
  name: llama3
```

No API key needed — Ollama runs models entirely on your machine.

## How do I add a skill?

```bash
# Browse available skills
forge skills list

# Add a skill from the registry
forge skills add tavily-research

# Validate requirements
forge skills validate
```

See [Embedded Skills](/docs/skills/embedded-skills) for the full catalog.

## What's the difference between skills and tools?

**Skills** are high-level capability descriptions defined in `SKILL.md` files. They describe what an agent *can do* and compile into container artifacts during `forge build`.

**Tools** are individual functions the LLM can call at runtime — like `web_search`, `http_request`, or `cli_execute`. Skills can register their own tools (script-backed) or use existing builtin tools (binary-backed).

## How does Forge secure my data?

Forge provides multiple security layers:

- **Egress controls** — Restrict which domains agents can access (allowlist, deny-all, or dev-open modes)
- **Encrypted secrets** — AES-256-GCM encryption with Argon2id key derivation
- **Execution sandboxing** — Binary allowlists, argument validation, environment isolation
- **Content guardrails** — PII detection, jailbreak protection, secret scanning
- **Build signing** — Ed25519 signatures for supply chain integrity
- **Audit logging** — Structured NDJSON events for every security-relevant action

See [Security Overview](/docs/security/overview) for the full architecture.

## Can I run Forge in air-gapped environments?

Yes. Use Ollama as the LLM provider and set egress mode to `deny-all`:

```yaml
model:
  provider: ollama
  name: llama3
egress:
  mode: deny-all
```

Pre-install all binary dependencies in the container image. No outbound network access is required.

## How do I connect Slack or Telegram?

```bash
# Add a channel adapter
forge channel add slack
forge channel add telegram

# Run with channels
forge run --with slack,telegram
```

Both channels use **outbound-only connections** — no public URLs or webhooks required. See [Channels](/docs/core-concepts/channels) for setup instructions.

## Does Forge support multi-turn conversations?

Yes. Session persistence is enabled by default — conversations are saved to disk and recovered automatically. Sessions older than 30 minutes are discarded to prevent poisoned context.

See [Memory System](/docs/core-concepts/memory-system) for details.

## Can I schedule recurring tasks?

Yes. Configure schedules in `forge.yaml` or create them dynamically at runtime:

```yaml
schedules:
  - id: daily-report
    cron: "@daily"
    task: "Generate daily status report"
    channel: telegram
```

See [Scheduling](/docs/core-concepts/scheduling) for details.
