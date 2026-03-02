# Memory

> Part of [Forge Documentation](../README.md)

Forge provides two layers of memory management: session persistence for multi-turn conversations and long-term memory for cross-session knowledge.

## Session Persistence

Sessions are automatically persisted to disk across requests, enabling multi-turn conversations:

```yaml
memory:
  persistence: true          # default: true
  sessions_dir: ".forge/sessions"
```

- Sessions are saved as JSON files with atomic writes (temp file + fsync + rename)
- Automatic cleanup of sessions older than 7 days at startup
- Session recovery on subsequent requests (disk snapshot supersedes task history)

## Context Window Management

Forge automatically manages context window usage based on model capabilities:

| Model | Context Window | Character Budget |
|-------|---------------|-----------------|
| `gpt-4o` / `gpt-5` | 128K tokens | ~435K chars |
| `claude-sonnet` / `claude-opus` | 200K tokens | ~680K chars |
| `gemini-2.5` | 1M tokens | ~3.4M chars |
| `llama3` | 8K tokens | ~27K chars |
| `llama3.1` | 128K tokens | ~435K chars |

When context grows too large, the **Compactor** automatically:
1. Takes the oldest 50% of messages
2. Flushes tool results and decisions to long-term memory (if enabled)
3. Summarizes via LLM (with extractive fallback)
4. Replaces old messages with the summary

Research tool results receive special handling during compaction: they are preserved with a higher extraction limit (5000 vs 2000 characters) and tagged distinctly in long-term memory logs (e.g., `[research][tool:tavily_research]`) so research insights persist across sessions.

```yaml
memory:
  char_budget: 200000       # override auto-detection
  trigger_ratio: 0.6        # compact at 60% of budget (default)
```

## Long-Term Memory

Enable cross-session knowledge persistence with hybrid vector + keyword search:

```yaml
memory:
  long_term: true
  memory_dir: ".forge/memory"
  vector_weight: 0.7
  keyword_weight: 0.3
  decay_half_life_days: 7
```

Or via environment variable:

```bash
export FORGE_MEMORY_LONG_TERM=true
```

When enabled, Forge:
- Creates a `.forge/memory/` directory with a `MEMORY.md` template for curated facts
- Indexes all `.md` files into a hybrid search index (vector similarity + keyword overlap + temporal decay)
- Registers `memory_search` and `memory_get` tools for the agent to use
- Automatically flushes compacted conversation context to daily log files (`YYYY-MM-DD.md`)

## Embedding Providers

Embedding providers power the vector search component of long-term memory:

| Provider | Default Model | Notes |
|----------|--------------|-------|
| `openai` | `text-embedding-3-small` | Standard OpenAI embeddings API |
| `gemini` | `text-embedding-3-small` | OpenAI-compatible endpoint |
| `ollama` | `nomic-embed-text` | Local embeddings |

Falls back to keyword-only search if no embedding provider is available (e.g., when using Anthropic as the primary provider without a fallback).

## Configuration

Full memory configuration in `forge.yaml`:

```yaml
memory:
  persistence: true
  sessions_dir: ".forge/sessions"
  char_budget: 200000
  trigger_ratio: 0.6
  long_term: false
  memory_dir: ".forge/memory"
  embedding_provider: ""      # Auto-detect from LLM provider
  embedding_model: ""         # Provider default
  vector_weight: 0.7
  keyword_weight: 0.3
  decay_half_life_days: 7
```

Environment variables:

| Variable | Description |
|----------|-------------|
| `FORGE_MEMORY_PERSISTENCE` | Set `false` to disable session persistence |
| `FORGE_MEMORY_LONG_TERM` | Set `true` to enable long-term memory |
| `FORGE_EMBEDDING_PROVIDER` | Override embedding provider |

---
← [Runtime](runtime.md) | [Back to README](../README.md) | [Channels](channels.md) →
