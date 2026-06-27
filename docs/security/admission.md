---
title: "Platform Admission Hook"
description: "Pre-dispatch gate that asks a platform API whether to admit each new inbound A2A invocation. Used for cost-ceiling / quota enforcement above the request-rate limiter."
order: 9
---

# Platform admission hook

Forge measures LLM token usage per call and per invocation (FWS-3 / [issue #87](https://github.com/initializ/forge/issues/87)) so a platform can compute a spend ceiling externally. The admission hook (issue #201) lets the platform tell the agent process to **stop accepting new invocations** when that ceiling is hit — distinct from auth (HTTP 401 on bad credentials) and from the per-IP rate limiter (HTTP 429 on request-rate burst).

The hook is **off by default**. Self-hosted Forge deploys see no change. When engaged it sits between the auth middleware and the dispatcher, calls a platform endpoint at most once every 5 seconds per agent, and returns HTTP 402 Payment Required to the caller when the platform says "deny."

## Configuration

| Env var | Behavior |
|---|---|
| `FORGE_ADMISSION_URL` | Platform admission endpoint. Unset → admission off (silent no-op). |
| `FORGE_PLATFORM_TOKEN` | Bearer token sent on every admission call. Unset → admission off + warn at startup. |

Both must be set to engage. If only one is set, Forge logs a single warn line at startup and runs without admission — the misconfiguration is visible without breaking traffic.

There are **no other knobs** — no timeout override, no cache TTL knob, no fail-mode switch. The contract is intentionally small.

Existing tenancy env vars from [issue #157](https://github.com/initializ/forge/issues/157) are forwarded as request headers when set:

| Env var | Becomes outbound header |
|---|---|
| `FORGE_ORG_ID` | `Org-Id` |
| `FORGE_WORKSPACE_ID` | `Workspace-Id` |

Empty value → header omitted entirely (not sent as the literal empty string). Lets the platform distinguish "self-hosted deploy without tenancy" from "platform deploy with malformed tenancy."

## Wire shape

### Request — issued once per inbound request that misses the 5s cache

```
GET /v1/admission?agent_id=my-agent HTTP/1.1
Authorization: Bearer <FORGE_PLATFORM_TOKEN>
Org-Id: <FORGE_ORG_ID>
Workspace-Id: <FORGE_WORKSPACE_ID>
```

`agent_id` from the agent's `cfg.AgentID`. Method is `GET` because the call is an idempotent read; the platform can front it with a CDN or HTTP-layer cache if it wants.

### Response — HTTP 200 when the platform reached a decision

```json
{
  "decision": "admit" | "deny",
  "reason": "cost_limit_exceeded",
  "scope": "agent" | "workspace" | "org",
  "window": "daily",
  "reset_at": "2026-06-28T14:00:00Z"
}
```

| Field | Meaning |
|---|---|
| `decision` | `admit` or `deny`. Anything else → Forge logs warn + fail-open admit. |
| `reason` | Platform-defined failure code on deny. Forge does not enum the vocabulary — `cost_limit_exceeded`, `billing_overdue`, `rate_limit_exhausted`, … are all fine. |
| `scope` | Which level in the platform's hierarchy tripped — `agent`, `workspace`, `org`, or `""`. Purely informational for audit + SRE routing. |
| `window` | Which quota window tripped — `hourly`, `daily`, `monthly`, `billing_cycle`, … Platform-defined string. |
| `reset_at` | RFC 3339 timestamp when the deny clears. Drives the `Retry-After` header Forge sends to the caller. |

## What the caller sees

**Admit** (cache hit or fresh) — request proceeds, no observable change.

**Deny** (cache hit or fresh):

```http
HTTP/1.1 402 Payment Required
Retry-After: 7142
Content-Type: application/json

{
  "error": "admission_denied",
  "reason": "cost_limit_exceeded",
  "scope": "workspace",
  "window": "daily",
  "reset_at": "2026-06-28T14:00:00Z"
}
```

402 is the right status code: this is *auth-passed-but-quota-exhausted*, distinct from 401 (auth failed) and 429 (Forge's own rate limiter tripped). `Retry-After` seconds derived from `reset_at`, clamped to 0 on a stale (past) reset_at.

## Fail-open everywhere

Any failure path → log warn + admit + cache the admit for the TTL:

- Network failure (timeout, connection refused, DNS error)
- HTTP 4xx (bad / expired token, 404 unknown agent)
- HTTP 5xx (platform error)
- Body parse failure, missing required fields, unknown `decision` value

The fallback admit is **cached** for the full 5s TTL so a platform outage produces one call per agent per 5s, not one per inbound request. Without that the outage would amplify into a request flood against an already-struggling platform.

Each fallback admit logs one greppable warn line:

```json
{"level":"warn","msg":"admission: call failed, admitting","agent_id":"my-agent","error":"context deadline exceeded","cached_until":"2026-06-27T08:15:27Z"}
```

Operators alert on this line. There is no env knob to flip the default to fail-closed — if you need hard enforcement on platform outage, do it at a different layer (ingress, K8s NetworkPolicy).

## Audit + tracing

### Audit event `task_admission_denied`

Emitted on every denial response Forge sends, whether cached or fresh:

```json
{
  "event": "task_admission_denied",
  "ts": "2026-06-27T08:15:22Z",
  "correlation_id": "9b3d…",
  "task_id": "task-42",
  "entity_id": "my-agent",
  "entity_type": "agent",
  "org_id": "org-7",
  "workspace_id": "ws-3",
  "fields": {
    "reason": "cost_limit_exceeded",
    "scope": "workspace",
    "window": "daily",
    "reset_at": "2026-06-28T14:00:00Z",
    "cached": false
  }
}
```

`cached: false` distinguishes "platform actively denied" from "Forge is serving a 4-second-old cached deny" — useful when debugging propagation lag.

### Span `admission.check`

Opened on every middleware fire. Sibling of `auth.verify` (from [issue #187](https://github.com/initializ/forge/issues/187)); child of `a2a.<method>` in the trace tree.

| Attribute | Values |
|---|---|
| `forge.admission.decision` | `admit` / `deny` |
| `forge.admission.reason` | platform-provided |
| `forge.admission.scope` | `agent` / `workspace` / `org` / `""` |
| `forge.admission.window` | platform-provided (`hourly` / `daily` / `monthly` / `billing_cycle` …) |
| `forge.admission.cached` | `true` / `false` |
| `forge.admission.fallback` | `true` when an `admit` was forced by a call failure |

Status = `Error` on deny. The HTTP call to the platform nests under the span as an `http.client` child — operators see total admission latency from the span, end-to-end platform latency from the child.

## Pipeline placement

```
inbound HTTP
  → rate_limit_middleware            (per-IP — exists, FWS-10)
  → seq_counter_middleware           (per-invocation seq — exists, FWS-8)
  → auth_middleware                  (auth.verify span — exists, issue #187)
  → admission_middleware             (admission.check span; 402 on deny)
  → dispatcher (a2a.<method>)
```

Auth runs before admission so the platform call never burns on unauthenticated traffic. Admission runs before the dispatcher so a denied invocation never reaches the executor / LLM / tool stack — no expensive work on the deny path.

## What the platform owns

| Responsibility | Where |
|---|---|
| Verify the bearer token Forge sends | platform |
| Decide the hierarchy precedence (agent → workspace → org) | platform |
| Define the `window` vocabulary | platform |
| Reset windows (hourly / daily / billing cycle timing) | platform |
| Aggregate Forge's audit stream into per-agent / per-workspace / per-org spend | platform |
| Per-agent overrides, grace periods, free-tier handling | platform |

Forge is the dumb yes/no asker. The whole platform contract is `GET /admission?agent_id=…` with bearer + tenancy headers returning a 5-field JSON. No webhooks, no streams, no SDK. Curl-testable.

## In-flight invocations

When the platform flips an agent to deny, in-flight invocations keep running. The admission hook gates *new* work; *running* work is the orchestrator's `tasks/cancel` problem.

An orchestrator that wants to halt in-flight work too can fire `tasks/cancel` with `reason: cost_limit_exceeded` against the agent's running task IDs (it already knows them — it issued the `tasks/send` that started them).

## Operational notes

**Cache window** is fixed at 5 seconds. At steady state Forge issues at most one admission call per agent per 5s, so the platform's load is `(num_agents / 5) calls/sec` regardless of inbound RPS.

**Overrun window** is bounded by `5s × steady-state RPS × per-invocation cost`. If a deny propagates the moment the platform flips state, the worst case is 5 seconds of cached admits — the platform can absorb that by setting its "deny threshold" slightly below the actual quota ceiling.

**Tenancy headers** use the names `Org-Id` and `Workspace-Id` — **without** the `X-Forge-` prefix. This is deliberately distinct from the inbound `X-Forge-Org-ID` / `X-Forge-Workspace-ID` tenancy stamps Forge accepts (issue #157). The outbound convention is what the platform's parser expects; the inbound convention is unchanged.

## Related

- [Audit logging](audit-logging.md) — `task_admission_denied` event reference
- [Observability — Tracing](../core-concepts/observability-tracing.md) — `admission.check` span hierarchy and attributes
- [Authentication](authentication.md) — runs before admission in the middleware pipeline
- [Issue #201](https://github.com/initializ/forge/issues/201) — the design discussion that locked this contract
- [Issue #187](https://github.com/initializ/forge/issues/187) — the `auth.verify` span that admission.check parallels
- [Issue #157](https://github.com/initializ/forge/issues/157) — `FORGE_ORG_ID` / `FORGE_WORKSPACE_ID` env vars sourced for the outbound tenancy headers
- FWS-3 / [Issue #87](https://github.com/initializ/forge/issues/87) — the token-usage telemetry the platform aggregates to make admission decisions
