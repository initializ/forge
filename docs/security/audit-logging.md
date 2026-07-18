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
| `invocation_complete` | A2A invocation finished (auth → dispatch → engine → response). Carries `duration_ms` (wall-clock) plus aggregated `input_tokens_total` / `output_tokens_total` / `llm_call_count` / `model` / `provider`. When [context compression](../core-concepts/context-compression.md) is enabled it also carries `compression_saved_tokens_total` — REALIZED savings: tokens this invocation's LLM calls did not send because compression markers rode in place of originals, compounding on every resend of compressed history (this is the number that matches the provider bill) — plus `compression_event_saved_tokens` (the one-time per-compression deltas, matching the sum of this invocation's `context_compressed` events), `compression_count`, and `expansion_count` when nonzero. Accumulated per invocation by correlation ID so concurrent tasks never cross-contaminate. |
| `invocation_cancelled` | A2A invocation cancelled mid-flight via `tasks/cancel` (or internal cancellation like parent ctx deadline). Carries `fields.reason` (one of `workflow_failure` / `cost_limit_exceeded` / `timeout` / `external_signal`), `duration_ms` up to cancellation, and any partial token totals consumed before the signal. See [Cancellation](#cancellation). |
| `task_admission_denied` | A new inbound `tasks/send` was rejected by the platform admission middleware (issue #201; opt-in via `FORGE_ADMISSION_URL` + `FORGE_PLATFORM_TOKEN`). Carries `fields.reason` (platform-defined: `cost_limit_exceeded`, `billing_overdue`, …), `fields.scope` (`agent` / `workspace` / `org`), `fields.window` (`hourly` / `daily` / `monthly` / `billing_cycle`), `fields.reset_at` (RFC 3339), and `fields.cached` (`true` when served from the 5s per-agent cache). Caller observes HTTP 402 Payment Required with `Retry-After`. Since admission sits between auth and dispatch and emits via `EmitFromContext`, it carries the ingress-minted `correlation_id` (#278) — so admission denials group with the `auth_verify` of the same request in per-invocation views. See [Platform Admission Hook](admission.md). |
| `guardrail_check` | Guardrail mask / block / warn decision. Carries `fields.gate` (`input` / `context` / `tool_call` / `output` / `stream` — sourced from the library `Result.Gate`), `fields.decision` (`masked` / `warned` / `blocked`), `fields.guardrail` + `fields.category` from the triggering violation, and `fields.violation_count`. `fields.tool` is present on `tool_call` and on `output` events for tool return text. With `FORGE_GUARDRAIL_CAPTURE_EVIDENCE=true` operators also opt into `fields.evidence` carrying the redacted + truncated triggering text. **Platform command denial (#238):** when a call matches a platform-policy `denied_command_patterns` entry, this event fires with `fields.source: "platform"`, `fields.guardrail: "platform_command_deny"`, `fields.pattern`, `fields.layer` (first-denying layer), `fields.policy_source` (file path), and the operator `fields.message` — the operator-authored, org-wide command control from [Platform Policy — Runtime command denial](platform-policy.md#runtime-command-denial). See [Guardrails — Audit Events](guardrails.md#audit-events). |
| `context_compressed` | [Context compression](../core-concepts/context-compression.md) shrank content before it reached the LLM. Carries `fields.seam` (`tool_output` from the AfterToolExec hook / `request` from the client wrapper), `fields.tool`, `tokens_before` / `tokens_after` / `saved_tokens`, plus running totals `total_saved_tokens` / `total_compressions` / `total_expansions` so any single event shows the cumulative picture. Token figures are tokenizer estimates; billed truth stays in `llm_call.input_tokens`. |
| `context_expanded` | The model retrieved offloaded content via the `context_expand` tool. Carries `fields.hash`, `hit` (`false` = expired/evicted), `bytes`, the producing `tool`, `candidates` (top keep-pattern tokens mined from the retrieved content, ≤5 — lets a platform consuming the audit stream aggregate [learning](../core-concepts/context-compression.md#the-learning-loop) fleet-wide, immune to pod restarts), and the same running totals — expansions are the cost side auditors net against savings. |
| `context_pattern_suggested` | The [compression learning loop](../core-concepts/context-compression.md#the-learning-loop) surfaced a `keep_patterns` candidate: a domain-state token retrieved via `context_expand` in 3+ distinct expansions that the keep floor does not already protect. Fired once per pattern. Carries `fields.pattern`, `expansions`, `tools` (array). Review via `forge compression suggestions`. |
| `auth_verify` | Inbound request authenticated successfully (with `provider`, `user_id`, `org_id`, `token_kind`). Carries the invocation `correlation_id` (minted at ingress, before auth — see below) and, for orchestrator-dispatched calls, `workflow_execution_id` — so it groups with the task events that follow it in the same request. |
| `auth_fail` | Inbound request rejected (with `reason`, `token_kind`). No `task_id` (none is ever created), but carries `workflow_execution_id` when the request had the execution header — so a rejected request is still attributable to its workflow run (#278). |
| `agent_card_published` | Agent Card finalized at startup or hot-reload (with `name`, `version`, `protocol_version`, `url`, `skill_count`, `capabilities`, `security_schemes`, `card_size_bytes`, `card_sha256`). See [Agent Card reference](../reference/a2a-agent-card.md). |
| `policy_loaded` | One per non-empty policy layer at startup (system / user / workspace). Carries `fields.layer`, `source` (file path), deny-list size counts, and max bounds. See [Platform Policy](platform-policy.md). |
| `policy_violation_at_build_time` | One per violation when `forge.yaml` conflicts with any policy layer. Agent refuses to start. Carries `fields.violation_kind` / `offending_value` / `forge_yaml_field` plus `layer` + `source` identifying the enforcing file. See [Platform Policy](platform-policy.md). |
| `channel_denied_by_policy` | One per channel adapter skipped at startup because a policy layer's `denied_channels` list names it. Non-fatal; the agent runs with the remaining channels. Carries `fields.channel`, `layer` (`system` / `user` / `workspace`), and `source` (file path). See [Platform Policy — Channels](platform-policy.md#channels). |
| `audit_export_status` | Per-sink export-health heartbeat, emitted on a **hybrid** cadence (#280): immediately when a sink's `connected` flag flips and otherwise as a slow keepalive (15 min by default, `AUDIT_STATUS_KEEPALIVE_INTERVAL` overrides, read at startup). Carries `fields.reason` (`state_change` \| `keepalive`) and `fields.sinks[]`, one entry per sink with `name`, `writes_ok`, `drops_timeout`, `drops_dial`, `connected`. The `drops_*` counters ride in the payload but deliberately don't drive the edge (they self-amplify during an outage — see [Sink health](#sink-health-audit_export_status)). Operators tail the stream to confirm export health; a missing keepalive past the interval is itself alertable. See [Audit Event Export (FWS-7)](#audit-event-export-fws-7). |
| `intent_alignment` | Emitted per `BeforeToolExec` when `security.intent_alignment` is enabled (R3 / #208). Carries `fields.tool`, `fields.decision` (`allow` / `warn` / `deny`), `fields.score` (cosine similarity ∈ [-1,1] as a float, or the string `"NaN"` on fail-closed paths), and `fields.reason`. Payload never carries the LLM prompt or tool args — only the scored decision. See [Intent Alignment](intent-alignment.md). |
| `intent_drift` | Emitted on state transitions of the R7 rolling-window drift analyzer (R7 / #214). Carries `fields.tool`, `fields.severity` (`mean_below_threshold` / `monotone_decrease` / `both` / `recovered`), `fields.transition` (`entered` / `recovered`), `fields.mean` (rolling window mean at trip time), and `fields.window` (configured window size). One event on entry, one on recovery — never per-call flooded across a long drift stretch. See [Intent Alignment — Drift tracking](intent-alignment.md). |
| `auth_step_up_required` | Emitted when the R4b step-up hook refuses a tool call because the caller's identity lacks the required `acr` claim (R4b / #210). Carries `fields.tool`, `fields.required_acr`, `fields.presented_acr` (empty when no acr was presented), `fields.reason`. The REST handler translates this into HTTP 401 with an RFC 9470 `WWW-Authenticate: Bearer error="step_up_required", acr_values="<value>"` challenge. See [Step-up Authorization](step-up-auth.md). |
| `task_deferred` | Emitted when the R4c defer hook pauses the executor mid-task (R4c / #211). Carries `fields.tool`, `fields.to` (deferral target — channel / human / URL), `fields.timeout_ms`, and `fields.context` (truncated approver context). The task's A2A status flips to `deferred` for the duration of the wait. See [Deferred authorization](defer-decisions.md). |
| `task_deferred_decision` | Emitted when a decision arrives at `POST /tasks/{id}/decisions` for a pending deferral. Carries `fields.tool`, `fields.decision` (`approve` / `reject`), `fields.approver`, `fields.note` (optional), and `fields.wait_ms` (time between defer and decision). On `approve` the tool proceeds; on `reject` the tool call fails with a defer-denied error. |
| `task_deferred_timeout` | Emitted when the defer engine's timer fires before any decision arrives. Carries `fields.tool` and `fields.timeout_ms`. The tool call auto-denies and the task ends in `failed`. |
| `credential_issued` | Emitted when the R9 JIT credential injector materializes credentials for a tool call (R9 / #215) — in-tool at the tool's `Execute` (the injector is wired onto `cli_execute` / `http_request`), **not** from a `BeforeToolExec` hook. Carries `fields.provider` (plugin name — `static` / `sts_assume_role` / …), `fields.tool`, `fields.ttl`, and any provider-specific scope metadata. **Never carries the credential material itself** — only its metadata. See [Least-privilege credentials](least-privilege-credentials.md). |
| `credential_revoked` | Emitted on `AfterToolExec` when a revocable credential is revoked. Carries `fields.provider`, `fields.tool`, `fields.revoked` (`true` when the provider actively revoked; `false` when nothing to revoke), and `fields.self_expiring` (`true` for providers whose credentials expire on their own — e.g. `static`, `sts_assume_role`). Even self-expiring providers emit this event so operators have a complete lifecycle. |
| `credential_failed` | Emitted when the injector could not materialize credentials for a tool call. Carries `fields.provider`, `fields.tool`, and `fields.reason`. The tool call fails closed. |

### Example

```json
{"ts":"2026-02-28T10:00:00Z","event":"session_start","correlation_id":"a1b2c3d4","task_id":"task-1"}
{"ts":"2026-02-28T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"start"}}
{"ts":"2026-02-28T10:00:01Z","event":"egress_allowed","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"domain":"api.tavily.com","mode":"allowlist","source":"proxy"}}
{"ts":"2026-02-28T10:00:05Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"end"}}
{"ts":"2026-02-28T10:00:06Z","event":"session_end","correlation_id":"a1b2c3d4","fields":{"state":"completed"}}
```

The `source` field distinguishes in-process enforcer events from subprocess proxy events. Both carry `correlation_id` and `task_id`: the in-process enforcer reads them from the request context, while the subprocess proxy recovers them from the `Proxy-Authorization` credentials the subprocess replays (the runner stamps the task/invocation IDs into the `HTTP_PROXY` URL userinfo). A subprocess binary that ignores proxy credentials still produces an enforced, audited event — just without the identity fields (issue #338).

### Workflow correlation

When the inbound A2A request carries the orchestrator's correlation headers (`X-Workflow-ID`, `X-Workflow-Execution-ID`, `X-Workflow-Stage-ID`, `X-Workflow-Step-ID`, `X-Invocation-Caller`), every audit event emitted during that invocation is tagged with the matching `workflow_id` / `workflow_execution_id` / `stage_id` / `step_id` / `invocation_caller` fields. `workflow_id` is the workflow DEFINITION (stable across runs); `workflow_execution_id` is the per-run instance (split from the formerly-overloaded header in FORGE-2 / issue #185). Header names are vendor-neutral so any A2A-compatible orchestrator can populate them. Direct A2A invocations (no orchestrator) omit the fields entirely — emitted JSON is byte-identical to the pre-correlation shape. See [Workflow correlation IDs](workflow-correlation.md) for the full reference, including outbound propagation for agent-to-agent flows.

### Tenancy stamping

For deployments where one or more agents serve multiple orgs or workspaces, every audit event can be stamped with `org_id` and `workspace_id` top-level fields so downstream consumers can filter by tenancy without joining against `auth_verify`. Two layers, highest precedence first:

| Layer | Source | When it wins |
|-------|--------|--------------|
| Per-request override | `X-Forge-Org-ID` / `X-Forge-Workspace-ID` request headers | Always — when present, override the static stamp |
| Deployment-time stamp | `FORGE_ORG_ID` / `FORGE_WORKSPACE_ID` env vars | When the request carries no override headers |

The deployment-time stamp is read once at agent startup and applied via `AuditLogger.WithTenancy(...)`. It covers every emitted event — startup banners (`agent_card_published`, `policy_loaded`, `audit_export_status`) AND per-invocation events (`session_start`, `llm_call`, `guardrail_check`, `invocation_complete`, etc.). The per-request override only kicks in inside the request scope; startup banners always reflect the env stamp.

```yaml
# Initializ platform deployment manifest — static-tenancy case
env:
  - name: FORGE_ORG_ID
    value: "org_abc123"
  - name: FORGE_WORKSPACE_ID
    value: "ws_xyz789"
```

```sh
# Multi-tenant routing case — the orchestrator picks per request
curl -X POST https://agent.example.com/ \
  -H 'X-Forge-Org-ID: org_def456' \
  -H 'X-Forge-Workspace-ID: ws_pqr012' \
  ...
```

Both fields use `omitempty`. Deployments that set neither env nor header keep emitting the pre-tenancy JSON shape verbatim — no schema version bump.

The top-level `org_id` is distinct from `auth_verify.fields.org_id`, which carries whatever the inbound auth token claimed (provider-derived). The top-level value is the operator's declared tenancy, trusted because the deployment / orchestrator set it. Both can be present on the same `auth_verify` event when they're different identifiers (e.g., the token came from a federated identity but the agent is deployed into a specific workspace).

### Entity stamping (`entity_id` / `entity_type`)

Every audit event also carries the entity identifier the event came from:

| Layer | Source |
|-------|--------|
| Per-event explicit | `AuditEvent.EntityID` / `AuditEvent.EntityType` |
| Deployment-time stamp | `FORGE_AGENT_ID` env → forge.yaml `agent_id` → `entity_id`; `entity_type` hardcoded to `"agent"` |

```yaml
env:
  - name: FORGE_AGENT_ID
    value: "aibuilderdemo"        # or just set forge.yaml agent_id
```

Emits land as:

```json
{
  "ts": "...",
  "event": "session_start",
  "entity_id": "aibuilderdemo",
  "entity_type": "agent",
  ...
}
```

**Entity identity.** Each event carries `entity_id` + `entity_type`, sourced from `FORGE_AGENT_ID` / `cfg.AgentID` at startup. These match the field names + values the guardrails library uses (`EntityType` constants: `agent` / `workflow` / `assistant`). Forge only runs `entity_type: "agent"` today; the other values are future-compatible.

Entity identity has no per-request override — agent identity is fixed at process startup. The tenancy layer above (`org_id` / `workspace_id`) covers the multi-tenant routing case.

See [Tenancy stamping reference](tenancy.md) for the precedence rules and the agent-to-agent propagation helper.

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

Workflow correlation fields (`workflow_id` / `workflow_execution_id` / `stage_id` / `step_id` / `invocation_caller` from FWS-2) also auto-tag every `llm_call` / `tool_exec` / `invocation_complete` event when the inbound request carried orchestrator headers — billing and audit consumers can attribute cost not just to a task but to a specific workflow run / stage / step. `workflow_id` rollups answer "cost per workflow definition over time"; `workflow_execution_id` joins answer "cost for this specific run."

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

The runtime emits `audit_export_status` events carrying per-sink
counters. The event flows through the same fan-out so operators tail
the audit stream itself to confirm export health.

**Hybrid cadence (#280).** Emitting once a minute produced ~1,440
near-identical "still fine" rows per agent per day, which dominated the
audit collection at fleet scale. The runtime instead:

- polls sink health frequently (`AuditExportStatusPollInterval`, 15s) and
  emits **immediately** when a sink's `connected` flag flips (a dial
  failure holds it at 0; a write timeout disconnects the sink, so both
  failure modes read 0 on the next poll) — so a failure surfaces within
  one poll interval; and
- otherwise emits a slow **keepalive** so liveness stays provable. The
  keepalive interval defaults to 15 minutes and is overridable via the
  `AUDIT_STATUS_KEEPALIVE_INTERVAL` env var (a Go duration, e.g. `"5m"`,
  `"1h"`), read once at process start — a deploy-time knob, not
  live-tunable. A missing keepalive past the interval is itself alertable.

The edge signal is `connected` — a **level** the sink maintains — and
deliberately **not** the cumulative `drops_*` counters: the status event
flows through every sink including a failing one, so its own write bumps
that sink's drop counter, and on an idle agent the status emits are the
only writes. Diffing drops would therefore self-amplify — one emit per
poll for the whole outage. The `drops_*` counters still ride in every
event's `sinks[]` payload for anyone tracking totals; they just don't
drive the edge.

Steady-state volume drops from ~1,440/day to a handful. Every event
carries `fields.reason` — `state_change` (a `connected` flip) or
`keepalive` (still alive) — so consumers can distinguish the two. A
keepalive baseline is emitted at startup so a healthy pipeline is
visible from t=0.

```json
{
  "ts": "2026-06-06T18:30:00Z",
  "event": "audit_export_status",
  "fields": {
    "reason": "state_change",
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
| `connected` | Live health level: `1` when the last write succeeded, `0` after any failure. Both network sinks maintain it — the UDS sink clears it on dial/timeout/write error, and the HTTP sink clears it on request-build / transport / timeout / non-2xx (#280) — which is what the hybrid cadence's `connected`-flip edge relies on. Sticky `0` for fire-and-forget sinks (writerSink), which never hold a connection |

### Why a separate path from OTel

Audit cannot be sampled (every policy decision and cost-relevant event
must land). OTel traces can be sampled. Audit needs separate retention
from observability. Failure-domain isolation: if OTel export breaks,
audit must continue, and vice versa.

The two pipelines share signal sources in Forge — when something
interesting happens, instrumentation emits to OTel **and** to audit at
the same call site. They are deliberately not coupled at the export
level (one breaking does not break the other), but as of OTel v1
(#108 / Phase 4) every audit event emitted from a request-scoped
context carries the active span's `trace_id` + `span_id`. See
[trace cross-link](#trace-cross-link-otel-v1-105) below for the
join-key semantics.

### Trace cross-link (OTel v1, #105)

When OpenTelemetry tracing is enabled (see
[Observability — Tracing](../core-concepts/observability-tracing.md)),
`EmitFromContext` automatically stamps the active span's `trace_id`
and `span_id` on every audit event. Operators paste either value
directly into a trace backend's search box to pivot between the two
streams:

| Pivot direction | How |
|---|---|
| **audit row → trace** | Paste the row's `trace_id` into Tempo / Jaeger / Honeycomb to land on the matching trace. Paste the `span_id` to jump directly to the span (an `llm_call` row's `span_id` resolves to the `llm.completion` span carrying matching `gen_ai.usage.*` tokens). |
| **trace → audit row** | Copy `trace_id` from a trace browser; grep the audit log for the corresponding row to get the FWS-8 payload metadata the trace does not carry. |

Format: lowercase hex matching W3C `traceparent` semantics — 32-char
(128-bit) `trace_id`, 16-char (64-bit) `span_id`.

**Backward compatibility:** both fields use `omitempty`. When tracing
is off (default), audit JSON is byte-identical to the pre-Phase-4
shape — no `trace_id` / `span_id` keys appear. The
`AuditSchemaVersion` is NOT bumped: adding optional fields is a
schema-compatible change per the policy above.

### Content-capture parity

When `observability.tracing.capture_content: true` is set, prompt /
completion / tool-args / tool-result content appears on **both** the
linked OTel span and the FWS-8 audit row for the same logical event.
The two pipelines run the captured content through the same redact-
then-truncate helper (`runtime.PrepareSpanContent` /
`runtime.TruncateForAudit`) so:

- The redaction marker is identical (`[REDACTED]`) — operators
  grepping either sink for vendor secret-token shapes see the same
  match.
- The truncation marker is byte-identical (`…[truncated:N]` where
  `N` is the original byte length of the input). Grepping
  `[truncated:` across audit rows and span attributes returns
  aligned, comparable results.
- The redact patterns mirror the runtime guardrails CustomRules
  defaults (Anthropic / OpenAI / GitHub / AWS / Slack / private key
  blocks / Telegram bot tokens). Adding a new vendor pattern to one
  pipeline implies adding it to the other.

The audit pipeline's byte cap (16 KiB per field, see
`AuditPayloadCapture.Cap*Bytes`) is intentionally larger than the
span cap (4 KiB — below the soft attribute-length limit most
observability backends apply). The two caps are independent: a single
event may be truncated on the span side and survive intact on the
audit side. The trailing marker shape is the same either way.

See [Observability — Span content capture](../core-concepts/observability-tracing.md#span-content-capture) for the
span-side attribute keys and opt-in switches.

## Streams (FWS-9)

`forge run` / `forge serve` use the OS streams as a stream-level
audit-vs-ops split, so container log collectors and SIEM pipelines
can route the two concerns separately without parsing any payload:

| Stream | Carries | Consumer |
|---|---|---|
| **stdout** | Ops logs — startup banner, request lines, runtime errors emitted via the structured `JSONLogger` (`r.logger.Info/Warn/Error`). | Container log collector / local debugging. |
| **stderr** | Audit NDJSON — every `event` constant defined in the table above. | SIEM pipeline today. After FWS-7, also lands on the dedicated UDS / HTTP sink in parallel (stderr stays as the safety-net fallback). |
| UDS / HTTP sink (FWS-7) | Audit NDJSON (primary, when configured). | initializ platform sidecar / customer SIEM. |

Migration note: pre-FWS-9, ops logs and audit both went to stderr —
SIEM rules had to filter by the presence of the `event` JSON field.
After FWS-9, the split is clean. Operators who used to redirect
`forge run 2> ops.log` for ops capture must switch to
`forge run > ops.log` (and `2> audit.log` for audit). Container
deployments that capture both streams via the runtime's standard
log collector are unaffected.

Interactive CLI commands (`forge init`, `forge build`, `forge channel`)
keep writing warnings and errors to stderr — those are user-facing UX
messages, not server ops logs, and the stream-split policy doesn't
apply to them.

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
| `workflow_id` / `workflow_execution_id` / `stage_id` / `step_id` / `invocation_caller` | string | optional | Populated when the request carried `X-Workflow-*` headers (FWS-2). `workflow_id` is the workflow definition (stable across runs); `workflow_execution_id` is the per-run instance (FORGE-2 / #185 split). |
| `model` / `provider` | string | optional | LLM call attribution (FWS-3) |
| `input_tokens` / `output_tokens` / `tokens_unavailable` | int / bool | optional | LLM call usage (FWS-3) |
| `duration_ms` | int64 | optional | Wall-clock duration (FWS-3) |
| `request_id` | string | optional | Provider-specific call identifier (FWS-3) |
| `trace_id` / `span_id` | string | tracing-on only | W3C-format lowercase hex (32/16 chars) of the OTel span active at emit time. Pivots audit row ↔ trace tree. See [trace cross-link](#trace-cross-link-otel-v1-105). |
| `fields` | map | optional | Per-event structured metadata (see each event type) |
| `prev_hash` | string | yes | sha256 of the previous event's line (hash chain). Stamped on **every** event unconditionally since R5 (#212); the first event of a process carries the genesis value. See [audit-signing.md](audit-signing.md) / [audit-tamper-evidence.md](audit-tamper-evidence.md). |
| `kid` / `sigp` / `sig` | string | signing-on only | Ed25519 signature envelope — `kid` (key id), `sigp` (`"jcs-1"`), `sig` (signature over the JCS-canonicalized event, covering `prev_hash`). Present **iff** `FORGE_AUDIT_SIGNING_KEY_B64` is set (R6 / #213); absent otherwise. See [audit-signing.md](audit-signing.md). |

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

#### Counter + correlation-id installation order

Both the per-invocation `SequenceCounter` **and** the `correlation_id`
are installed on `r.Context()` at ingress by
`installIngressContextMiddleware`, which wraps the auth middleware so
both are on context before the auth chain runs. This puts
`auth_verify` / `auth_fail` first in the sequence (`seq=1`) **and**
gives them the same `correlation_id` the task events will carry — so
the entire invocation, auth event included, is one gap-free,
single-id timeline under the `(correlation_id, task_id)` group.

Minting the `correlation_id` at ingress rather than at task creation is
the key change from issue #278: auth runs before a task exists, so
before this the pre-admission auth events had no invocation id and fell
into an "unattributed" bucket in per-invocation views. Emission order is
preserved (auth genuinely precedes admission — no backfilling).

The runner's request entry calls `coreruntime.EnsureSequenceCounter`
and `coreruntime.EnsureCorrelationID` — each reuses the ingress-installed
value when present and installs a fresh one on the `--no-auth` path (and
schedule fires, which have no ingress), so no path loses seq or
correlation stamping. Pinned by `TestAuthAudit_SeqStampedWhenCounterInstalled`,
`TestAuthAudit_CarriesIngressCorrelationID`, and the `Ensure*_ReusesExisting`
tests (issues #174, #278).

#### Emit invariant

The seq counter is picked up by `AuditLogger.EmitFromContext(ctx, ...)`
(and the typed helpers built on top of it — `EmitLLMCall`,
`EmitToolExec`, `EmitInvocationComplete`, `EmitInvocationCancelled`,
the egress and guardrail emit paths). Plain `AuditLogger.Emit` skips
the counter and the trace cross-link — so every audit emission that
happens inside an invocation scope MUST go through `EmitFromContext`.
This was the regression behind issues #173 (three sites — the
`BeforeToolExec` / `AfterToolExec` hook callbacks and the
outbound-guardrail-failure `session_end` emit — had drifted to plain
`Emit` and lost seq on `tool_exec` + that branch's `session_end`) and
#174 (the auth callback couldn't use `EmitFromContext` until the
counter was installed upstream of the auth middleware). Pinned by
`TestToolExecAudit_CarriesSequenceFromContext`. Sites that still call
plain `Emit` are explicitly outside any invocation scope and are
documented inline:

| Site | Why plain `Emit` |
|---|---|
| Egress proxy `OnAttempt` with `source=proxy` | Subprocess HTTP `CONNECT` has no Go ctx tying back to the A2A request, so it can't use `EmitFromContext`. Since #338 it still recovers `task_id` + `correlation_id` out-of-band from the `Proxy-Authorization` creds and sets them on the event manually; seq + trace cross-link remain unavailable (no ctx). |
| MCP server startup events (`mcp_server_started` / `_failed` / `_degraded`) | Pre-invocation; no scope |
| Scheduler tick (`schedule_fire` / `schedule_complete` / `schedule_skip` / `schedule_modify`) | Runs on its own timer outside any A2A request |
| Startup banners (`policy_loaded`, `agent_card_published`, `audit_export_status`) | Pre-invocation; no scope |

Issue #175 tracks a follow-up vet/lint pass to catch future
`Emit`-instead-of-`EmitFromContext` drift on per-invocation events.

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
supervised-learning corpora, compliance replay) opt in field by field.
Operators configure capture via `forge.yaml`, env vars, or programmatic
runner config; the three layers stack with the following precedence:

| Layer | Knob | Wins over |
|---|---|---|
| `forge.yaml` `audit.capture` | per-field `*bool`, `max_bytes` | env + default |
| `FORGE_AUDIT_CAPTURE_*` env | per-field bool, `MAX_BYTES` | default |
| Built-in default | all flags off, `Redact=true` | — |

### `forge.yaml` block

```yaml
audit:
  capture:
    tool_args: true         # capture raw tool input on tool_exec start
    tool_result: true       # capture raw tool output on tool_exec end
    llm_messages: false     # capture chat messages on llm_call
    llm_response: false     # capture completion text on llm_call
    redact: true            # scrub vendor-secret token shapes (ON by default)
    max_bytes: 16384        # per-field byte cap (16 KiB default)
```

Every flag in the block is optional. An omitted field falls through to
the env layer; an explicit `false` overrides env. The default-deploy
case (no block at all) is metadata-only auditing — byte-for-byte
identical to pre-#163 output.

### Env vars

| Env var | Type | Default | Meaning |
|---|---|---|---|
| `FORGE_AUDIT_CAPTURE_TOOL_ARGS` | bool | `false` | Capture raw tool input on `tool_exec phase=start` |
| `FORGE_AUDIT_CAPTURE_TOOL_RESULT` | bool | `false` | Capture raw tool output on `tool_exec phase=end` |
| `FORGE_AUDIT_CAPTURE_LLM_MESSAGES` | bool | `false` | Capture chat-messages array on `llm_call` |
| `FORGE_AUDIT_CAPTURE_LLM_RESPONSE` | bool | `false` | Capture completion text on `llm_call` |
| `FORGE_AUDIT_CAPTURE_REDACT` | bool | `true` | Vendor-secret regex scrub before emission |
| `FORGE_AUDIT_CAPTURE_MAX_BYTES` | int | `16384` | Single-knob per-field byte cap |

`MAX_BYTES` is a single knob: when set it applies uniformly across all
four `CapXxxBytes` fields. Operators who need divergent per-field caps
embed Forge as a library and set `AuditPayloadCapture` programmatically.

### Programmatic (library) config

```go
RunnerConfig{
  AuditPayloadCapture: coreruntime.AuditPayloadCapture{
    LLMMessages: true,
    LLMResponse: true,
    ToolArgs:    true,
    ToolResult:  true,
    Redact:      true,
    // Per-field byte caps; 0 = use DefaultPayloadCaptureCapBytes (16 KiB)
    CapLLMMessagesBytes: 32 << 10,
    CapToolResultBytes:  64 << 10,
  },
}
```

### What gets scrubbed

When `redact: true` (the default), captured fields run through
`coreruntime.PrepareCapturedContent` which scrubs known vendor token
shapes before truncation. The same regex set protects OTel span
content (#130) and guardrail evidence (#155 / #156) — fix once,
flow everywhere. Current shapes:

| Shape | Pattern (illustrative) | Replacement |
|---|---|---|
| Anthropic API key | `sk-ant-…` | `[REDACTED]` |
| OpenAI API key | `sk-…` (20+ chars) | `[REDACTED]` |
| GitHub PAT / OAuth / server / fine-grained | `ghp_…` `gho_…` `ghs_…` `github_pat_…` | `[REDACTED]` |
| AWS access key | `AKIA…` | `[REDACTED]` |
| Slack bot / user tokens | `xoxb-…` `xoxp-…` | `[REDACTED]` |
| Private-key PEM block | `-----BEGIN … KEY-----…-----END … KEY-----` | `[REDACTED]` |
| Telegram bot token | `<digits>:…` | `[REDACTED]` |

Redact runs BEFORE truncation so the truncation cut cannot split a
`[REDACTED]` marker mid-string.

Disable redact (`redact: false` / `FORGE_AUDIT_CAPTURE_REDACT=false`)
ONLY when a downstream sink runs its own scrubber — typically a
platform-side SIEM normalizer or a sidecar that mutates events before
storage.

Captured strings are truncated to the configured per-field byte cap
with a `…[truncated:N]` marker so a runaway prompt or gigabyte tool
output can't bloat one audit event.

### Verbosity guidance

Capture is expensive. The same agent that emits ~1 MB / day of
metadata-only audit can emit 25–80 MB / day with both `tool_args` and
`tool_result` on — a 25–80× factor depending on payload size.

| Posture | Per `tool_exec` event |
|---|---|
| Default (metadata only) | ~150–300 bytes |
| `tool_args` + `tool_result` on | up to ~32 KiB (capped) |
| Realistic average for a tool-heavy agent | 5–15 KiB |

For a tool-heavy agent doing 1000 invocations/day with 5 tool calls each:

- Metadata-only: ~1 MB/day
- Both captures on: 25–80 MB/day

Recommended usage patterns:

- **Debug a misbehaving tool**: turn `tool_args + tool_result` on for
  the affected session only, then turn off. Don't ship it as
  always-on.
- **Compliance evidence**: `tool_args` is usually enough (the inputs
  the agent produced); `tool_result` is rarely needed and is the
  largest of the four captures.
- **Long-running production**: leave default off unless a specific
  audit need surfaces. The size-only metadata (`args_size`,
  `result_size`, `prompt_messages_count`) is still emitted, so
  observability dashboards keep working without capture.

### Security note

Even with `redact: true`, a captured payload may carry PII, customer
data, or secrets the regex set doesn't recognize. The transport (FWS-7
sink or the stderr safety net) lands captured payloads verbatim.
Operators are responsible for routing the audit stream to a store
appropriate to the captured payloads' sensitivity. `redact: false`
means the regex set is bypassed entirely; reach for it only when a
downstream scrubber is known to run.

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
- ~~**Per-agent capture flags in `forge.yaml`.** Capture is set via
  `RunnerConfig` programmatically today. A YAML surface can be added
  if customers ask; the runtime semantics are already in place.~~
  *Shipped in issue #163 — see [Payload capture](#payload-capture-fws-8)
  above for the `forge.yaml` + env-var operator surface.*
