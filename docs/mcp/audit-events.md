---
title: "MCP — Audit Events"
description: "Seven event types emitted by the MCP subsystem; their fields and reason codes."
order: 4
---

Every MCP audit event is NDJSON to stderr alongside the existing
Forge audit stream. **No event ever carries argument or result
bytes** — only sizes, durations, server/tool names, and stable
reason codes. The grep-test
`TestMCPTool_Audit_NeverLogsBytes` pins this invariant.

## Event matrix

| Event                  | When                                       | Fields                                          |
|------------------------|--------------------------------------------|-------------------------------------------------|
| `mcp_server_started`   | A server reaches `Ready`                   | `name`, `transport`, `tool_count`               |
| `mcp_server_failed`    | A server reaches terminal `Failed`         | `name`, `phase`, `reason`                       |
| `mcp_server_degraded`  | Transport error mid-call; entering backoff | `name`, `attempt`, `backoff_ms`                 |
| `mcp_tool_call`        | Before every `tools/call`                  | `server`, `tool`, `args_size`                   |
| `mcp_tool_result`      | After every `tools/call`                   | `server`, `tool`, `duration_ms`, `result_size`, `ok`, `reason?` |
| `mcp_tool_conflict`    | Registry rejects a tool name               | `incoming_name`, `error`                        |
| `mcp_token_refresh`    | Every OAuth refresh attempt                | `server`, `ok`, `reason`                        |

Every event also carries the standard top-level fields: `ts`,
`event`, `correlation_id` (when scoped to a request).

## Reason codes

### `mcp_tool_result.reason` (only when `ok=false`)

| Reason         | Cause                                                          |
|----------------|----------------------------------------------------------------|
| `unavailable`  | 5xx / network error / DNS / TLS / timeout                      |
| `protocol`     | 4xx, malformed JSON-RPC frame, JSON-RPC error response         |
| `revoked`      | OAuth refresh denied (`invalid_grant`, `expired_token`)        |
| `canceled`     | Caller cancelled `ctx` (deadline exceeded or explicit cancel)  |
| `tool_error`   | MCP server set `isError: true` in `CallToolResult`             |
| `unknown`      | Anything else — investigate                                    |

### `mcp_server_failed.phase`

| Phase       | Meaning                                                  |
|-------------|----------------------------------------------------------|
| `connect`   | HTTP dial / TCP refused / DNS / TLS                      |
| `initialize`| MCP `initialize` handshake; includes version mismatch    |
| `discover`  | `tools/list` failed or returned a malformed input schema |
| `runtime`   | Anything else (transport error after Ready)              |

### `mcp_server_failed.reason`

| Reason               | Meaning                                                  |
|----------------------|----------------------------------------------------------|
| `backoff_exhausted`  | 5 reconnect attempts failed (1s/2s/4s/8s/16s schedule)   |
| `version_mismatch`   | Server's `protocolVersion` ≠ `2025-06-18` (pinned)       |

### `mcp_token_refresh.reason`

| Reason            | Meaning                                                |
|-------------------|--------------------------------------------------------|
| `refreshed`       | New access token persisted                             |
| `refresh_denied`  | Auth server returned `invalid_grant` / `expired_token` |
| `transport`       | Network / 5xx talking to the token endpoint            |
| `store_error`     | Failed to persist the refreshed token                  |

## What to dashboard

For routine ops, three Grafana-style queries are enough:

1. **Per-server availability:** `count(mcp_server_started) - count(mcp_server_failed)`
2. **Tool latency:** `histogram_quantile(0.95, mcp_tool_result.duration_ms by tool)`
3. **OAuth refresh failures:** `count(mcp_token_refresh{ok="false"})` — page when non-zero.

## What NOT to log

Do not log `args` or result `text/data` content. The audit stream
intentionally omits these — Forge has no way to know whether a tool
argument is PII, secrets, or operationally sensitive data, so it
treats every byte as untrustworthy for logging.
