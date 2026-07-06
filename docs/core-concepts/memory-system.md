---
title: "Memory System"
description: "Session persistence, context management, and long-term memory."
order: 5
---

Forge provides two layers of memory management: session persistence for multi-turn conversations and long-term memory for cross-session knowledge.

## Session Persistence

Sessions are automatically persisted to disk across requests, enabling multi-turn conversations:

```yaml
memory:
  persistence: true          # default: true
  sessions_dir: ".forge/sessions"
```

- Sessions are saved as JSON files with atomic writes (temp file + fsync + rename)
- Orphaned tool calls (assistant tool_calls without matching tool results) are stripped on both save and recovery, preventing API rejection errors
- Automatic cleanup of sessions older than 7 days at startup
- Session recovery on subsequent requests (disk snapshot supersedes task history)
- **Session max age** (default 30 minutes): stale sessions are discarded on recovery to prevent poisoned error context from blocking tool retries. When an LLM accumulates repeated tool failures in a session, it may stop retrying altogether. The max age ensures these poisoned sessions expire, giving the agent a fresh start.

Configure via `forge.yaml` or environment variable:

```yaml
memory:
  session_max_age: "30m"   # default; use "1h", "15m", etc.
```

```bash
export FORGE_SESSION_MAX_AGE=1h
```

### Session Store Backends

Session persistence has two backends, selected by `memory.session_store`:

| Backend | When | Behavior |
|---------|------|----------|
| `file` (default) | Single pod / dev | Local JSON under `sessions_dir` (`.forge/sessions/*.json`). Durable only on that pod's filesystem. |
| `remote` | Stateless / multi-pod | Snapshots are pushed to a platform **session service** over HTTP, so any replica can resume any task with no shared volume — no PVC. |

The `remote` backend keeps agent pods stateless: a task started on one pod resumes on another. It:

- **Pulls before each turn** with a conditional GET (`If-None-Match`) — the service answers `304` (the pod's cached snapshot is current) or `200` (fresh state), so unchanged sessions aren't re-downloaded.
- **Commits with compare-and-swap** (`If-Match`) — a concurrent writer's intervening commit is detected as a `412`. On conflict the stale writer **yields** (the newer state wins) rather than clobbering it; the model is never re-run.
- **Pulls lazily** — a cold pod fetches a session only when its task is first touched, not all sessions at once.

```yaml
memory:
  session_store: remote                       # "file" (default) | "remote"
  session_store_url: "https://sessions.example/api/v1/agent-sessions"
```

The pod authenticates to the service with its platform token, reusing the same env the admission client reads — `FORGE_PLATFORM_TOKEN` plus the `FORGE_ORG_ID` / `FORGE_WORKSPACE_ID` tenancy stamps. A `remote` selection missing its URL or token warns and falls back to the `file` backend, so session memory is never silently dropped.

```bash
export FORGE_SESSION_STORE=remote
export FORGE_SESSION_STORE_URL=https://sessions.example/api/v1/agent-sessions
```

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
  session_max_age: "30m"      # discard sessions idle longer than this
  session_store: "file"       # "file" (default) | "remote"
  session_store_url: ""       # required when session_store: remote
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
| `FORGE_SESSION_MAX_AGE` | Session idle timeout, e.g. `30m`, `1h` (default: `30m`) |
| `FORGE_SESSION_STORE` | Session backend: `file` (default) or `remote` |
| `FORGE_SESSION_STORE_URL` | Platform session-service URL (required for `remote`) |
| `FORGE_MEMORY_LONG_TERM` | Set `true` to enable long-term memory |
| `FORGE_EMBEDDING_PROVIDER` | Override embedding provider |
