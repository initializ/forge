---
title: "forge.yaml Schema"
description: "Complete YAML schema reference for Forge agent configuration."
order: 2
---

All Forge agent configuration lives in `forge.yaml` at the project root.

## Full Schema

```yaml
agent_id: "my-agent"                # Required
version: "1.0.0"                    # Required
framework: "forge"                  # forge (default), crewai, langchain
registry: "ghcr.io/org"             # Container registry
entrypoint: "agent.py"              # Required for crewai/langchain, omit for forge

model:
  provider: "openai"                # openai, anthropic, gemini, ollama, custom
  name: "gpt-4o"                    # Model name
  organization_id: "org-xxx"        # OpenAI Organization ID (enterprise, optional)
  fallbacks:                        # Fallback providers (optional)
    - provider: "anthropic"
      name: "claude-sonnet-4-20250514"
      organization_id: ""           # Per-fallback org ID override (optional)

tools:
  - name: "web_search"
  - name: "cli_execute"
    config:
      allowed_binaries: ["git", "curl"]
      env_passthrough: ["GITHUB_TOKEN"]

channels:
  - "telegram"
  - "slack"

egress:
  profile: "strict"                 # strict, standard, permissive
  mode: "allowlist"                 # deny-all, allowlist, dev-open
  allowed_domains:                  # Explicit domains
    - "api.example.com"
    - "*.github.com"
  capabilities:                     # Capability bundles
    - "slack"
  allow_private_ips: false          # Allow RFC 1918 IPs (auto: true in containers)

cors_origins:                       # CORS allowed origins for A2A server
  - "https://app.example.com"      # (default: localhost variants)

package:
  alpine: false                     # Prefer Alpine base image
  slim: false                       # Minimize image size
  bin_overrides:                    # Per-binary install overrides
    forge:
      local: "/path/to/linux/forge" # Host path to local binary file
    jq:
      apt: "jq"                     # APT package name
    custom-tool:
      url: "https://example.com/tool.tar.gz"  # Direct download URL
      dest: "/usr/local/bin/custom-tool"       # Install destination
      chmod: "0755"                            # File permissions

secrets:
  providers:                        # Secret providers (order matters)
    - "encrypted-file"              # AES-256-GCM encrypted file
    - "env"                         # Environment variables

memory:
  persistence: true                 # Session persistence (default: true)
  sessions_dir: ".forge/sessions"
  char_budget: 200000               # Context budget override
  trigger_ratio: 0.6                # Compaction trigger ratio
  long_term: false                  # Long-term memory (default: false)
  memory_dir: ".forge/memory"
  embedding_provider: ""            # Auto-detect from LLM provider
  embedding_model: ""               # Provider default
  vector_weight: 0.7                # Hybrid search vector weight
  keyword_weight: 0.3               # Hybrid search keyword weight
  decay_half_life_days: 7           # Temporal decay half-life

guardrails_path: "guardrails.json"  # Path to guardrails config (default: "guardrails.json")

schedules:                          # Recurring scheduled tasks (optional)
  - id: "daily-report"
    cron: "@daily"
    task: "Generate daily status report"
    skill: ""                       # Optional skill to invoke
    channel: "telegram"             # Optional channel for delivery
    channel_target: "-100123456"    # Destination chat/channel ID
```
