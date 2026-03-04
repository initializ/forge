# Configuration Reference

> Part of [Forge Documentation](../README.md)

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

skills:
  path: "SKILL.md"

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

schedules:                          # Recurring scheduled tasks (optional)
  - id: "daily-report"
    cron: "@daily"
    task: "Generate daily status report"
    skill: ""                       # Optional skill to invoke
    channel: "telegram"             # Optional channel for delivery
    channel_target: "-100123456"    # Destination chat/channel ID
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `FORGE_MODEL_PROVIDER` | Override LLM provider |
| `FORGE_MODEL_FALLBACKS` | Fallback chain (e.g., `"anthropic:claude-sonnet-4,gemini"`) |
| `FORGE_MEMORY_PERSISTENCE` | Set `false` to disable session persistence |
| `FORGE_MEMORY_LONG_TERM` | Set `true` to enable long-term memory |
| `FORGE_EMBEDDING_PROVIDER` | Override embedding provider |
| `OPENAI_API_KEY` | OpenAI API key |
| `OPENAI_ORG_ID` | OpenAI Organization ID (enterprise); overrides `organization_id` in YAML |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `TAVILY_API_KEY` | Tavily web search API key |
| `PERPLEXITY_API_KEY` | Perplexity web search API key |
| `WEB_SEARCH_PROVIDER` | Force web search provider (`tavily` or `perplexity`) |
| `OPENAI_BASE_URL` | Override OpenAI base URL |
| `ANTHROPIC_BASE_URL` | Override Anthropic base URL |
| `OLLAMA_BASE_URL` | Override Ollama base URL (default: `http://localhost:11434`) |
| `FORGE_PASSPHRASE` | Passphrase for encrypted secrets file |

---
← [Commands](commands.md) | [Back to README](../README.md) | [Dashboard](dashboard.md) →
