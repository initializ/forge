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
| `guardrail_check` | Guardrail evaluation result |
| `auth_verify` | Inbound request authenticated successfully (with `provider`, `user_id`, `org_id`, `token_kind`) |
| `auth_fail` | Inbound request rejected (with `reason`, `token_kind`) |
| `agent_card_published` | Agent Card finalized at startup or hot-reload (with `name`, `version`, `protocol_version`, `url`, `skill_count`, `capabilities`, `security_schemes`, `card_size_bytes`, `card_sha256`). See [Agent Card reference](../reference/a2a-agent-card.md). |

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
