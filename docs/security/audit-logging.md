---
title: "Audit Logging"
description: "Structured NDJSON audit logging for runtime security events."
order: 6
---

## Audit Logging

All runtime security events are emitted as structured NDJSON to stderr with correlation IDs for end-to-end tracing.

### Event Types

| Event | Description |
|-------|-------------|
| `session_start` | New task session begins |
| `session_end` | Task session completes (with final state) |
| `tool_exec` | Tool execution start/end (with tool name) |
| `egress_allowed` | Outbound request allowed (with domain, mode) |
| `egress_blocked` | Outbound request blocked (with domain, mode) |
| `llm_call` | LLM API call completed (with `input_tokens`, `output_tokens`, `model`, `provider`, `duration_ms`, `request_id`). See [Token usage and duration](#token-usage-and-execution-duration). |
| `llm_call_cancelled` | Streaming LLM call cancelled mid-flight; carries partial token counts captured up to cancellation. |
| `invocation_complete` | A2A invocation finished (auth → dispatch → engine → response). Carries `duration_ms` (wall-clock) plus aggregated `input_tokens_total` / `output_tokens_total` / `llm_call_count` / `model` / `provider`. |
| `invocation_cancelled` | A2A invocation cancelled mid-flight via `tasks/cancel` (or internal cancellation like parent ctx deadline). Carries `fields.reason` (one of `workflow_failure` / `cost_limit_exceeded` / `timeout` / `external_signal`), `duration_ms` up to cancellation, and any partial token totals consumed before the signal. See [Cancellation](#cancellation). |
| `guardrail_check` | Guardrail evaluation result |
| `auth_verify` | Inbound request authenticated successfully (with `provider`, `user_id`, `org_id`, `token_kind`) |
| `auth_fail` | Inbound request rejected (with `reason`, `token_kind`) |
| `agent_card_published` | Agent Card finalized at startup or hot-reload (with `name`, `version`, `protocol_version`, `url`, `skill_count`, `capabilities`, `security_schemes`, `card_size_bytes`, `card_sha256`). See [Agent Card reference](../reference/a2a-agent-card.md). |
| `policy_loaded` | One per non-empty policy layer at startup (system / user / workspace). Carries `fields.layer`, `source` (file path), deny-list size counts, and max bounds. See [Platform Policy](platform-policy.md). |
| `policy_violation_at_build_time` | One per violation when `forge.yaml` conflicts with any policy layer. Agent refuses to start. Carries `fields.violation_kind` / `offending_value` / `forge_yaml_field` plus `layer` + `source` identifying the enforcing file. See [Platform Policy](platform-policy.md). |
| `channel_denied_by_policy` | One per channel adapter skipped at startup because a policy layer's `denied_channels` list names it. Non-fatal; the agent runs with the remaining channels. Carries `fields.channel`, `layer` (`system` / `user` / `workspace`), and `source` (file path). See [Platform Policy — Channels](platform-policy.md#channels). |
| `audit_export_status` | One event every 60s when an export sink is configured. Carries `fields.sinks[]`, one entry per registered sink with `name`, `writes_ok`, `drops_timeout`, `drops_dial`, `connected`. Operators tail the audit stream to confirm export health. See [Audit Event Export (FWS-7)](#audit-event-export-fws-7). |

### Example

```json
{"ts":"2026-02-28T10:00:00Z","event":"session_start","correlation_id":"a1b2c3d4","task_id":"task-1"}
{"ts":"2026-02-28T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"start"}}
{"ts":"2026-02-28T10:00:01Z","event":"egress_allowed","correlation_id":"a1b2c3d4","fields":{"domain":"api.tavily.com","mode":"allowlist","source":"proxy"}}
{"ts":"2026-02-28T10:00:05Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"end"}}
{"ts":"2026-02-28T10:00:06Z","event":"session_end","correlation_id":"a1b2c3d4","fields":{"state":"completed"}}
```

The `source` field distinguishes in-process enforcer events from subprocess proxy events.

### Workflow correlation

When the inbound A2A request carries the orchestrator's correlation headers (`X-Workflow-ID`, `X-Workflow-Stage-ID`, `X-Workflow-Step-ID`, `X-Invocation-Caller`), every audit event emitted during that invocation is tagged with the matching `workflow_id` / `stage_id` / `step_id` / `invocation_caller` fields. Header names are vendor-neutral so any A2A-compatible orchestrator can populate them. Direct A2A invocations (no orchestrator) omit the fields entirely — emitted JSON is byte-identical to the pre-correlation shape. See [Workflow correlation IDs](workflow-correlation.md) for the full reference, including outbound propagation for agent-to-agent flows.

### Token usage and execution duration

Every `llm_call` audit event carries the normalized token counts the provider returned in its response metadata, plus the wall-clock time spent in the provider call. Field naming aligns with [OTel GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/) (`gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens`) so audit consumers can correlate Forge audit events with OTel traces without a translation table.

```json
{
  "ts": "2026-06-04T15:21:09Z",
  "event": "llm_call",
  "correlation_id": "9b3d…",
  "task_id": "task-42",
  "model": "claude-sonnet-4-6",
  "provider": "anthropic",
  "input_tokens": 1240,
  "output_tokens": 387,
  "duration_ms": 2150,
  "request_id": "msg_01H8…"
}
```

| Field | Source | Notes |
|---|---|---|
| `input_tokens` | Provider response usage | Maps to `gen_ai.usage.input_tokens` |
| `output_tokens` | Provider response usage | Maps to `gen_ai.usage.output_tokens` |
| `tokens_unavailable` | Audit emitter | `true` when both counts are zero — some self-hosted Ollama setups don't return usage; billing consumers must distinguish "not measured" from "zero tokens used" |
| `model` | Runtime model config | The model identifier the executor was configured with |
| `provider` | Runtime model config | One of `anthropic`, `openai`, `ollama`, `custom` |
| `duration_ms` | Captured at call site | Wall-clock time spent in `client.Chat`, in milliseconds |
| `request_id` | Provider response | Opaque provider call ID (Anthropic `id`, OpenAI `id`) — debug-correlation handle only, never used for billing |

Each `tool_exec` event (phase=end) carries `duration_ms` for the tool execution plus structured arg-shape metadata (`args_size`, `result_size`) — raw arg values are deliberately not included (payload stripping is FWS-8's concern). One `invocation_complete` event closes each A2A invocation with the total wall-clock duration and aggregated token totals across all LLM calls in the invocation.

Workflow correlation fields (`workflow_id` / `stage_id` / `step_id` / `invocation_caller` from FWS-2) also auto-tag every `llm_call` / `tool_exec` / `invocation_complete` event when the inbound request carried orchestrator headers — billing and audit consumers can attribute cost not just to a task but to a specific workflow run / stage / step.

A2A response headers carry the same per-invocation totals inline so an orchestrator can ceiling-check cost during parallel workflow execution without subscribing to the audit stream:

| Header | Value |
|---|---|
| `X-Forge-Tokens-In` | Sum of `input_tokens` across all LLM calls in the invocation |
| `X-Forge-Tokens-Out` | Sum of `output_tokens` across all LLM calls in the invocation |
| `X-Forge-Duration-Ms` | Wall-clock invocation duration (auth → dispatch → engine → response) |
| `X-Forge-Model` | Most-recently-used model |
| `X-Forge-Provider` | Most-recently-used provider |

Headers populate regardless of whether OTel tracing is enabled — they're the orchestration channel, not the observability channel.

**Cost calculation is deliberately not in Forge.** Forge emits token counts; the platform applies price tables to compute dollar amounts. Price tables change frequently and shouldn't require agent redeploys.

### Cancellation

Forge accepts mid-invocation cancellation via the A2A `tasks/cancel` JSON-RPC method. The handler looks up the in-flight invocation in a per-Runner cancellation registry, fires a typed cancel-cause through the executor's `context.Context`, and the loop honors it at the next iteration boundary or between tool calls (whichever comes first). The current LLM call honors cancellation natively — `http.Client.Do` aborts the request on `ctx.Done()`.

Cancellation latency is bounded by the time for the current LLM call or tool call to finish (typically seconds, not minutes). Go's runtime does not support force-terminating a goroutine, so "hard-cancel" semantically means "honor the signal at the next safe checkpoint." The orchestrator-side `cancel + give-up-wait-after-N-seconds` pattern is an A2A-client concern, not a Forge concern.

```json
{
  "ts": "2026-06-04T15:23:47Z",
  "event": "invocation_cancelled",
  "correlation_id": "9b3d…",
  "task_id": "task-42",
  "duration_ms": 1820,
  "fields": {
    "reason": "cost_limit_exceeded",
    "state": "canceled",
    "input_tokens_total": 940,
    "output_tokens_total": 215,
    "llm_call_count": 2,
    "model": "claude-sonnet-4-6",
    "provider": "anthropic"
  }
}
```

| `fields.reason` | Set by | Meaning |
|---|---|---|
| `workflow_failure` | Orchestrator | Sibling step in a parallel stage failed under `fail_workflow`; abandon work. |
| `cost_limit_exceeded` | Orchestrator | Workflow cumulative cost ceiling hit (typically derived from the FWS-3 `X-Forge-Tokens-*` headers). |
| `timeout` | Orchestrator / Forge | Wall-clock budget exhausted. Parent ctx `context.DeadlineExceeded` auto-maps to this reason. |
| `external_signal` | Operator / fallback | Operator-initiated stop, debugging cancel, or any cancellation without a typed reason. |

**Cancel request shape:**

```json
{
  "jsonrpc": "2.0",
  "method": "tasks/cancel",
  "params": { "id": "task-42", "reason": "cost_limit_exceeded" },
  "id": "1"
}
```

`reason` is optional. Unknown reason strings are accepted and forwarded to the audit event verbatim — the audit pipeline is the authority on classification.

**Cancel after complete is idempotent.** A cancel issued for a task that already finished (or was never started) returns the stored task state unchanged — no error. The handler refuses to flip a terminal-state task to `canceled` because that would corrupt audit and orchestrator state.

**Partial usage is preserved.** When LLM calls completed before the cancel signal, `input_tokens_total` / `output_tokens_total` / `llm_call_count` carry the accumulated counts so a downstream cost aggregator bills only for what was consumed. When no LLM call landed, the totals are absent and the event still carries `duration_ms` so wall-clock spend is visible.

### Authentication events

Every inbound request to `/tasks` emits exactly one of `auth_verify` or `auth_fail`.

**Successful authentication:**

```json
{
  "ts":"2026-05-24T00:50:01Z",
  "event":"auth_verify",
  "fields":{
    "method":"POST",
    "path":"/tasks/send",
    "provider":"aws_sigv4",
    "user_id":"arn:aws:sts::412664885516:assumed-role/AWSReservedSSO_PowerUserAccess_.../Naveen",
    "org_id":"412664885516",
    "token_kind":"sigv4",
    "groups_count":0,
    "remote_addr":"[::1]:62297"
  }
}
```

`user_id` is the canonical identifier the verifier returned (ARN for AWS, JWT
`sub` for OIDC/IAP/AAD). `org_id` is the AWS account, Entra tenant GUID, or
OIDC `tid`/`org_id`-mapped claim depending on the provider.

**Failed authentication:**

```json
{"ts":"...","event":"auth_fail","fields":{"reason":"rejected","token_kind":"sigv4","method":"POST","path":"/tasks/send","remote_addr":"[::1]:62200"}}
```

### Reason codes (`auth_fail.fields.reason`)

| Reason | What it means | Operator action |
|---|---|---|
| `missing_token` | No auth-shaped headers at all | Caller forgot to authenticate |
| `not_for_me` | Bearer present but no provider claimed it | Wrong token format for the configured providers |
| `rejected` | Provider recognized + denied (allowlist miss, expired, bad sig, scope mismatch) | Check `allowed_principals` / `tenant_id` / token freshness |
| `invalid` | Token malformed (bad base64, unsupported alg, missing required field) | Token construction bug on the caller side |
| `provider_unavailable` | Verifier endpoint down (STS / JWKS / Graph 5xx, network error) | Provider-side incident; not a token issue |

### Token kind values (`fields.token_kind`)

Structural classification of what bytes were on the wire — safe to log:

| Value | Shape |
|---|---|
| `empty` | No token / no auth-shaped headers |
| `opaque` | Bearer with non-JWT, non-sigv4 shape (channel adapter loopback, custom verifier tokens) |
| `jwt` | Bearer with three base64url segments (`oidc`, `azure_ad`) |
| `sigv4` | Bearer with `forge-aws-v1.` prefix (`aws_sigv4` pre-signed URL token) |
| `iap_jwt` | `X-Goog-Iap-Jwt-Assertion` header present (`gcp_iap`) — also stamped on successful verify even if Bearer was simultaneously present |

### Audit pipeline grep recipes

Who called my agent in the last hour, by ARN/email?

```bash
jq -r 'select(.event=="auth_verify") | .fields.user_id' forge.log | sort | uniq -c
```

Why are requests failing?

```bash
jq -r 'select(.event=="auth_fail") | .fields.reason' forge.log | sort | uniq -c
```

Which agents called this one (in a mesh)?

```bash
jq -r 'select(.event=="auth_verify") | "\(.fields.user_id)"' forge.log | sort -u
```

See [Authentication](authentication.md) for the full provider chain and how
each provider populates these fields.

## Audit Event Export (FWS-7)

By default, audit events go to **stderr only** — the long-standing
NDJSON-on-stderr safety net. FWS-7 (issue #95) adds a parallel export
path so an in-pod sidecar can consume audit at low latency without
parsing every container-log line.

The export sink does NOT replace stderr. Both paths emit
byte-identical NDJSON; the export sink is purely additive. If the
export sink is down, the operator can still grep audit out of the
container logs.

### Configuration

| Flag | Env var | Purpose | Default |
|---|---|---|---|
| `--audit-socket` | `FORGE_AUDIT_SOCKET` | Unix Domain Socket path (preferred) | empty (no export sink) |
| `--audit-http-endpoint` | `FORGE_AUDIT_HTTP_ENDPOINT` | localhost HTTP POST endpoint (fallback when UDS unavailable) | empty |
| `--audit-write-timeout` | `FORGE_AUDIT_WRITE_TIMEOUT` | Per-event sink timeout (Go duration syntax: `50ms`, `200ms`) | `50ms` |

Both `forge run` and `forge serve start` accept these flags; `forge
serve start` forwards them to the daemon process. Env vars flow
through to the daemon via `os.Environ()` even without the flags. When
both `--audit-socket` and `--audit-http-endpoint` are set, the socket
wins.

### Operational model

- **Lazy connect.** The socket need not exist when the agent starts;
  the first emit triggers the dial. Sidecar deploys that come up
  *after* the agent will pick up future events without restarting the
  agent.
- **Per-event timeout.** Each emit at the sink gets up to
  `--audit-write-timeout` (default 50ms) before being dropped and
  counted as a `drops_timeout`. A slow sidecar can never back-pressure
  the agent.
- **Exponential backoff between failed dials.** 100ms → 200ms → 400ms
  → … → 5s cap. During the backoff window, writes drop without
  attempting a dial — so a permanently-down sidecar does not slow the
  emit path beyond a cheap clock check.
- **No buffering on the sink.** Buffering is the sidecar's job. The
  sink is fire-and-forget.
- **No transformation.** Events leaving the export sink are
  byte-identical to events leaving stderr.

### Sink health: `audit_export_status`

Every 60 seconds the runtime emits one `audit_export_status` event
carrying per-sink counters. The event flows through the same fan-out
so operators tail the audit stream itself to confirm export health.

```json
{
  "ts": "2026-06-06T18:30:00Z",
  "event": "audit_export_status",
  "fields": {
    "sinks": [
      {"name": "stderr",      "writes_ok": 4137, "drops_timeout": 0, "drops_dial": 0, "connected": 0},
      {"name": "unix-socket", "writes_ok": 4135, "drops_timeout": 0, "drops_dial": 2, "connected": 1}
    ]
  }
}
```

| Counter | Meaning |
|---|---|
| `writes_ok` | Events successfully delivered to this sink |
| `drops_timeout` | Events dropped because the per-event Write missed its deadline (slow / unresponsive peer) |
| `drops_dial` | Events dropped because the connection was down (sidecar offline or in backoff window) |
| `connected` | `1` when a working connection is held, `0` otherwise. Sticky `0` for fire-and-forget sinks (writerSink) |

### Why a separate path from OTel

Audit cannot be sampled (every policy decision and cost-relevant event
must land). OTel traces can be sampled. Audit needs separate retention
from observability. Failure-domain isolation: if OTel export breaks,
audit must continue, and vice versa.

The two pipelines share signal sources in Forge — when something
interesting happens, instrumentation emits to OTel **and** to audit at
the same call site. They are deliberately not coupled: do not tap one
from the other.

### Companion follow-up: stream separation

Forge currently puts both ops logs (`r.logger.Info(...)` startup
banners, request logs) **and** audit NDJSON on stderr. A SIEM pipeline
that wants audit-only records can split by parsing the `event` field,
but stream-level separation would be cleaner. Tracked as FWS-9 (#100):
*"Move ops logger output from stderr to stdout (stream separation from
audit)."* Independent of FWS-7 in code.

## Schema contract (FWS-8)

The audit event schema is a **stable, versioned contract**. Consumers
(the initializ platform, custom SIEM pipelines, cost-attribution
dashboards) depend on field names and types. Forge treats the schema
as an external interface: backward-compatible additions do not bump
the version; removals or semantic changes do.

Every emitted event carries:

| Field | Type | Always present? | Notes |
|---|---|---|---|
| `ts` | string (RFC3339) | yes | Emission timestamp in UTC |
| `event` | string | yes | Event-type constant — see "Event Types" above |
| `schema_version` | string | yes | Current contract version. `"1.0"` as of FWS-8. |
| `seq` | int64 | per-invocation only | Monotonic per-invocation counter. Absent on startup events (`policy_loaded`, `agent_card_published`, `audit_export_status`). |
| `correlation_id` | string | request-scoped only | Per-invocation ID; groups all events for one A2A invocation |
| `task_id` | string | request-scoped only | A2A task identifier (`params.id` on `tasks/send`) |
| `workflow_id` / `stage_id` / `step_id` / `invocation_caller` | string | optional | Populated when the request carried `X-Workflow-*` headers (FWS-2) |
| `model` / `provider` | string | optional | LLM call attribution (FWS-3) |
| `input_tokens` / `output_tokens` / `tokens_unavailable` | int / bool | optional | LLM call usage (FWS-3) |
| `duration_ms` | int64 | optional | Wall-clock duration (FWS-3) |
| `request_id` | string | optional | Provider-specific call identifier (FWS-3) |
| `fields` | map | optional | Per-event structured metadata (see each event type) |

### Sequence numbers

Every audit event emitted on behalf of an A2A invocation carries a
monotonically increasing `seq` field. Sequences start at `1` for the
first event of an invocation and advance by `1` per emit. Consumers
detect gaps (lost events) and reordering (export-side races) by
inspecting `seq` within a `(correlation_id, task_id)` group.

Sequences are scoped to a single invocation — different invocations
start their own counters. Events emitted outside any invocation scope
(`policy_loaded`, `agent_card_published`, `audit_export_status`) omit
`seq` entirely.

### Schema versioning policy

| Change | Bumps version? |
|---|---|
| Add a new optional field with `omitempty` | No |
| Add a new event type constant | No |
| Add a new `fields[]` key inside an existing event | No |
| Rename a field, drop a field, or change a field's type | Yes (major bump) |
| Change the semantic meaning of an existing field value | Yes (major bump) |

Consumers that don't recognize a `schema_version` should keep
processing — the schema is additive-by-default.

## Payload capture (FWS-8)

By default, audit events are **metadata only** — token counts, sizes,
durations, tool names, provider attribution. No prompt text, no
completion text, no raw tool arguments, no raw tool results. This is
the baseline contract every operator can rely on regardless of
configuration.

Customers who need raw payloads in audit (debugging incidents,
supervised-learning corpora, compliance replay) opt in field by field
via `AuditPayloadCapture` on the runner config:

```go
RunnerConfig{
  AuditPayloadCapture: coreruntime.AuditPayloadCapture{
    LLMMessages: true,             // prompt messages in llm_call
    LLMResponse: true,             // completion text in llm_call
    ToolArgs:    true,             // raw tool input in tool_exec
    ToolResult:  true,             // raw tool output in tool_exec
    // Per-field byte caps; 0 = use DefaultPayloadCaptureCapBytes (16 KiB)
    CapLLMMessagesBytes: 32 << 10,
    CapToolResultBytes:  64 << 10,
  },
}
```

Captured strings are truncated to a per-field byte cap (default 16 KiB)
with a `…[truncated:N]` marker so a runaway prompt or gigabyte tool
output can't bloat one audit event. The cap is enforced by
`coreruntime.TruncateForAudit`.

**Security note.** Once any capture flag is enabled, the audit
transport (FWS-7 sink or stderr safety net) lands captured payloads
verbatim — including any PII or secret that flowed through the prompt
or completion. Operators are responsible for routing the audit stream
to a store appropriate to the captured payloads' sensitivity. Forge
does not redact captured content.

### What each flag turns on

| Flag | Adds to event | Adds field |
|---|---|---|
| `LLMMessages` | `llm_call` / `llm_call_cancelled` | `prompt_messages` (JSON-encoded `[]ChatMessage`), `prompt_messages_count` |
| `LLMResponse` | `llm_call` | `completion_text` (`Response.Message.Content`) |
| `ToolArgs` | `tool_exec` (start hook) | `args` (raw `ToolInput`) |
| `ToolResult` | `tool_exec` (end hook) | `result` (raw `ToolOutput`) |

The default size-only fields (`args_size`, `result_size`,
`prompt_messages_count`) always land regardless of capture
configuration so consumers can size-check even without raw bodies.

### What FWS-8 does NOT include

- **Audit event signing.** The issue's architectural recommendation
  was to defer signing until a customer specifically asks
  (complexity around key management, rotation, and customer-side
  verification). Sequence numbers cover gap detection in the
  meantime. Tracked as a follow-up.
- **Per-agent capture flags in `forge.yaml`.** Capture is set via
  `RunnerConfig` programmatically today. A YAML surface can be added
  if customers ask; the runtime semantics are already in place.
