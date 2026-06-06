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
