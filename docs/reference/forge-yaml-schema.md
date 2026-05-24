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

auth:                               # a2a HTTP-server auth chain (optional)
  required: true                    # 401 every unauthenticated request
  providers:                        # ordered; first match wins (fail-closed on rejection)
    - type: "static_token"          # local dev / shared-secret
      settings:
        token_env: "FORGE_AUTH_TOKEN"  # env var name (preferred over literal `token:`)
    - type: "oidc"                  # any IdP with OIDC discovery
      settings:
        issuer:   "https://login.example.com/auth/realms/forge"
        audience: "api://forge"
        client_id: ""               # optional azp fallback
        jwks_url: ""                # overrides discovery
        jwks_cache_ttl: "1h"
        clock_skew: "30s"
        claim_map: {groups: "roles"}
    - type: "http_verifier"         # legacy external /verify endpoint
      settings:
        url:         "https://auth.example.com/verify"
        default_org: "acme"
        timeout:     "10s"
    - type: "aws_sigv4"             # Phase 2: AWS IAM via pre-signed STS URL
      settings:
        region:    "us-east-1"      # required
        audience:  "api://forge"    # informational, emitted in audit Claims
        allowed_accounts:           # ergonomic: "anyone in these AWS accounts"
          - "412664885516"
        allowed_principals:         # explicit globs (path.Match)
          - "arn:aws:sts::412664885516:assumed-role/ci-deploy/*"
        identity_cache_ttl: "60s"
        max_token_expires:  "15m"   # caps caller's X-Amz-Expires claim
        clock_skew:         "5m"
    - type: "gcp_iap"               # Phase 2: GCP IAP-fronted Forge
      settings:
        audience: "/projects/PNUM/global/backendServices/BACKEND_ID"
        jwks_refresh_ttl: "1h"
    - type: "azure_ad"              # Phase 2: Microsoft Entra ID
      settings:
        tenant_id: "00000000-1111-..."   # required unless allow_multi_tenant
        audience:  "api://forge"
        allow_multi_tenant: false
        allowed_tenants:                  # required when multi-tenant + want allowlist
          - "55555555-6666-..."
        groups_mode:   "claim"            # "claim" | "graph"
        graph_timeout: "5s"

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
