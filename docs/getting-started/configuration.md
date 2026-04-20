---
title: "Configuration"
description: "Configure your Forge agent with forge.yaml and environment variables."
order: 4
---

All Forge agent configuration lives in `forge.yaml` at the project root.

## Quick Start

A minimal `forge.yaml`:

```yaml
agent_id: "my-agent"
version: "1.0.0"

model:
  provider: "openai"
  name: "gpt-4o"

tools:
  - name: "web_search"
  - name: "cli_execute"
    config:
      allowed_binaries: ["git", "curl"]
```

## Key Sections

| Section | Purpose |
|---------|---------|
| `model` | LLM provider, model name, fallback chain |
| `tools` | Builtin and custom tool configuration |
| `channels` | Messaging platform adapters (Slack, Telegram) |
| `egress` | Outbound network access controls |
| `memory` | Session persistence and long-term memory |
| `secrets` | Encrypted secret storage providers |
| `schedules` | Recurring cron-based tasks |

For the complete schema, see [forge.yaml Schema](/docs/reference/forge-yaml-schema).

For all environment variable overrides, see [Environment Variables](/docs/reference/environment-variables).
