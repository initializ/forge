---
title: "Environment Variables"
description: "All environment variables supported by Forge."
order: 3
---

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
| `FORGE_CORS_ORIGINS` | Comma-separated CORS allowed origins for A2A server |
| `FORGE_AUTH_URL` | External auth provider URL for token validation |
| `FORGE_AUTH_ORG_ID` | Organization ID sent to external auth provider |
| `FORGE_GUARDRAILS_DB` | MongoDB URI for DB-backed guardrails config + audit |
| `FORGE_AGENT_ID` | Agent identifier for DB guardrails (falls back to `agent_id` in YAML) |
| `FORGE_ORG_ID` | Organization identifier for DB guardrails |
| `FORGE_PASSPHRASE` | Passphrase for encrypted secrets file |
