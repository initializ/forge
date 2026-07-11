---
title: "Context Compression"
description: "Reversible compression of bulky tool outputs — fewer tokens, nothing lost."
order: 6
---

Forge can compress bulky tool outputs before they reach the LLM — reversibly: everything dropped stays retrievable, so compression is lossy on the wire but lossless end-to-end.

Powered by [ctxzip](https://github.com/initializ/ctxzip). Off by default; enable per agent in `forge.yaml`, per run with a flag, or at scaffold time in the init wizard.

## The problem it solves

Agent tool outputs are dominated by repetition: 149 pods that are `Running` and one that is `CrashLoopBackOff`; hundreds of log lines differing only by timestamp; JSON list responses where the model needs three rows. Without compression these outputs either flood the context window or get **truncated** — destroying whatever fell past the cut, which is frequently the one row that mattered.

Compression inverts the tradeoff: keep what matters (errors, anomalies, query-relevant rows, boundaries), offload the rest to a local store, and let the model retrieve the original if it turns out to need it.

## How it works

```
tool executes
   │
   ▼
AfterToolExec hook ──── output ≥ 2 KB? ──── compress once, at production time
   │                                         dropped content → .forge/ctxzip.db
   │                                         replaced by <<ctxzip:HASH note>> marker
   ▼
Memory (compressed bytes never change → provider prompt caches stay warm)
   │
   ▼
LLM client wrapper ──── compresses the live zone of each request
   │                    (system prompt + recent turns forwarded byte-identical)
   ▼
LLM sees:  [... kept rows, errors intact ...] <<ctxzip:ac998fea694b 149_lines_offloaded>>
   │
   └─ needs the offloaded data? → calls context_expand(hash) → original returned
```

Three pieces, all automatic once enabled:

| Piece | What it does |
|-------|--------------|
| Tool-output hook | Compresses each large tool result once, before it enters session memory. Error results and small outputs are left verbatim. |
| Client wrapper | Compresses the remaining live zone of each outbound request. Deterministic across turns so historic messages always compress to identical bytes. |
| `context_expand` tool | Registered automatically. The model calls it with a marker's hash to get the original content back. A system-prompt directive teaches every agent what markers are — skills need zero awareness. |

## What is never dropped

Fidelity is layered; every layer only ever adds protection:

1. **Error floor** — content matching error vocabulary (`error`, `fail`, `panic`, `timeout`, `crash`, `backoff`, `oomkilled`, `evicted`, …) is kept verbatim.
2. **`keep_patterns`** — your domain's never-drop vocabulary (see below).
3. **Query anchors** — items matching the conversation's ask survive.
4. **Structure** — head/tail windows and one exemplar of each near-duplicate group.
5. **Reversibility** — everything else is offloaded to the store, not deleted.
6. **Source of truth** — after the store TTL (30 min), the disk or the original command still holds the data; a retrieval miss tells the model to re-run the producing tool.

## Configuration

```yaml
# forge.yaml
compression:
  enabled: true                # default: false
  keep_patterns:               # domain vocabulary that must never be dropped
    - CrashLoopBackOff
    - PAYMENT_DECLINED
  # store_path: .forge/ctxzip.db      # offloaded-originals store (bbolt)
  # ttl: 30m                          # how long originals stay retrievable
  # min_tool_output_chars: 2048       # hook floor; smaller outputs untouched
  # cache_hints: true                 # provider prompt-cache hints (defaults to enabled)
```

Precedence (most specific wins):

```
forge run --compression[=false]  >  FORGE_COMPRESSION=true|false  >  compression.enabled  >  off
```

| Surface | Usage |
|---------|-------|
| `forge run --compression` | Enable for one run; `--compression=false` force-disables even when forge.yaml enables it |
| `forge serve --compression[=false]` | Forwarded to the daemon |
| `forge init --compression` | Scaffold a new agent with the block enabled |
| init TUI wizard | "Context Compression" step (between Skills and Auth) |

## Provider prompt-cache hints

Compressing the wrong bytes can *cost* tokens by busting the provider's prompt cache, so compression never touches the system prompt, tool definitions, or recent turns, and its output is deterministic across turns. On top of that, `cache_hints` (on by default when compression is enabled) injects each provider's native cache primitives:

| Provider | Hint |
|----------|------|
| anthropic | `cache_control: {type: ephemeral}` breakpoints on the last tool definition and the system block — caches the stable tools+system prefix across turns. Also applies on the `aws_sigv4` Bedrock-passthrough path. |
| openai / gemini | A stable `prompt_cache_key` derived from (model, system prompt, tool names) — pins cache routing; prefix caching itself is automatic. |

When `cache_hints` is off, provider wire formats are byte-identical to a build without compression.

## Observability

Savings are first-class audit events, not log noise — see [Audit Logging](../security/audit-logging.md) for the event schema:

- `context_compressed` — per compression: seam, tool, tokens before/after/saved, plus running totals.
- `context_expanded` — per retrieval: hash, hit, bytes — the cost side to net against savings.
- `invocation_complete` gains `compression_saved_tokens_total` (**realized** savings — tokens this invocation's LLM calls did not send; a tool output compressed once but resent in history across four calls saves its delta four times), `compression_event_saved_tokens` (one-time per-compression deltas), `compression_count`, and `expansion_count`, accumulated per invocation (concurrent tasks never cross-contaminate).

Token figures are tokenizer estimates (directionally accurate); billed truth remains `llm_call.input_tokens`. A surgical session that produced only small outputs correctly reports `compression_count: 0` — compression is insurance against bulk, not a tax on every call.

## The learning loop

Every `context_expand` retrieval is a signal: compression dropped something a model needed. At expansion time the runtime mines the retrieved content for domain-state tokens (CamelCase / ALLCAPS words like `SchedulingGated` or `QUOTA_EXCEEDED`) that are **not** already protected by the error floor or your `keep_patterns`, and counts them across expansions in `.forge/ctxzip-suggestions.json`. A token retrieved in three distinct expansions is surfaced once — a `context_pattern_suggested` audit event plus a log line — and:

```bash
forge compression suggestions
```

renders the accumulated candidates with a paste-ready `keep_patterns` block. Suggestions are advisory: a token retrieved often is evidence, not proof — review before adopting. The file is bounded (512 entries, lowest-count evicted) and failures are swallowed (the flywheel never affects compression itself).

### Persistence and containers

The suggestions file lives next to the CCR store (`.forge/ctxzip-suggestions.json`, following `store_path`). In a container without a volume, **a pod restart loses the local counting state** — and in a multi-replica Deployment each replica counts independently, so fleet-wide repetition may never cross any single pod's threshold. Two answers, use either or both:

- **Volume**: point `store_path` at a mounted path (per-replica — the store is single-writer). An `emptyDir` survives container crash-restarts; a per-replica PVC survives rescheduling too.
- **Audit stream (recommended for fleets)**: every `context_expanded` event carries the producing `tool` and the top mined `candidates`, and threshold crossings emit `context_pattern_suggested` — so a platform consuming the audit export can **rebuild counting fleet-wide from the stream alone**, immune to restarts and replica dilution. The local file is a single-agent convenience, not the source of truth.

The CCR store itself needs no such care: originals expire after `ttl` anyway, and a post-restart marker miss tells the model to re-run the producing tool.

## Failure posture

Fail-open, always: if the store cannot be opened, a compressor errors, or "compression" would grow a message, the original content is used unchanged. Error tool results are never compressed. An expired retrieval is not a dead end — the model is told to re-run the tool that produced the output.

**Interaction with tool-output truncation.** Forge normally hard-caps tool results at 25% of the context budget *before* anything else sees them. With compression enabled, that cut moves to **after** the compression hook (behind a safety ceiling of 16× the cap, absolute max 4MB): pre-hook truncation both destroys data and breaks the JSON envelopes compression would shrink losslessly. The compressed result is then capped as usual — normally a no-op. If an envelope still arrives cut mid-string (the safety ceiling, or another runtime), ctxzip salvages the intact prefix and adds a `_ctxzip_note` field telling the model the tail was destroyed upstream — not offloaded — so it re-runs the tool rather than calling `context_expand` for bytes that no longer exist.

**Single-writer store.** The bbolt store at `store_path` holds an exclusive file lock — one store per process. A second process pointing at the same file (two replicas on a shared volume, or `forge run` alongside `forge serve` in the same directory) fails to acquire the lock after a 5-second timeout and that process runs uncompressed (fail-open, with a startup warning). Give each replica its own `store_path` — offloaded originals are only ever retrieved by the process that offloaded them, so the store has no reason to be shared.

## Related

- [Runtime Engine](runtime-engine.md) — where the hook and client wrapper sit in the agent loop
- [Tools & Builtins](tools-and-builtins.md) — the `context_expand` tool
- [forge.yaml Schema](../reference/forge-yaml-schema.md) — the `compression` block
- [CLI Reference](../reference/cli-reference.md) — flags and wizard step
