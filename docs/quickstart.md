# Quick Start

> Part of [Forge Documentation](../README.md)

Get a Forge agent running in under 60 seconds.

## Why Forge?

**Instant Agent From a Single Command**

Write a SKILL.md. Run `forge init`. Your agent is live.

The wizard configures your model provider, validates your API key,
connects Slack or Telegram, picks skills, and starts your agent.
Zero to running in under 60 seconds.

**Secure by Default**

Forge is designed for safe execution:

* Does NOT create public tunnels
* Does NOT expose webhooks automatically
* Uses outbound-only connections (Slack Socket Mode, Telegram polling)
* Enforces outbound domain allowlists at both build-time and runtime, including subprocess HTTP via a local egress proxy
* Encrypts secrets at rest (AES-256-GCM) with per-agent isolation
* Signs build artifacts (Ed25519) for supply chain integrity
* Supports restricted network profiles with audit logging

No accidental exposure. No hidden listeners.

## Get Started in 60 Seconds

```bash
# Install
curl -sSL https://raw.githubusercontent.com/initializ/forge/main/install.sh | bash

# Initialize a new agent (interactive wizard)
forge init my-agent

# Run locally
cd my-agent && forge run

# Run with Telegram
forge run --with telegram
```

The `forge init` wizard walks you through model provider, API key, fallback providers, tools, skills, and channel setup. Use `--non-interactive` with flags for scripted setups.

See [Installation](installation.md) for all installation methods.

---
[Back to README](../README.md) | [Installation](installation.md) →
