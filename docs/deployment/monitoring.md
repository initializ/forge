---
title: "Monitoring"
description: "Monitor Forge agents with structured audit events and logging."
order: 4
---

## Audit Events

All runtime security events are emitted as structured NDJSON to stderr with correlation IDs for end-to-end tracing.

### Event Types

| Event | Description |
|-------|-------------|
| `session_start` | New task session begins |
| `session_end` | Task session completes (with final state) |
| `tool_exec` | Tool execution start/end (with tool name) |
| `egress_allowed` | Outbound request allowed (with domain, mode) |
| `egress_blocked` | Outbound request blocked (with domain, mode) |
| `llm_call` | LLM API call completed (with token count) |
| `guardrail_check` | Guardrail evaluation result |
| `schedule_fire` | Scheduled task triggered |
| `schedule_complete` | Scheduled task completed |

### Example

```json
{"ts":"2026-02-28T10:00:00Z","event":"session_start","correlation_id":"a1b2c3d4","task_id":"task-1"}
{"ts":"2026-02-28T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"start"}}
{"ts":"2026-02-28T10:00:01Z","event":"egress_allowed","correlation_id":"a1b2c3d4","fields":{"domain":"api.tavily.com","mode":"allowlist","source":"proxy"}}
{"ts":"2026-02-28T10:00:05Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"end"}}
{"ts":"2026-02-28T10:00:06Z","event":"session_end","correlation_id":"a1b2c3d4","fields":{"state":"completed"}}
```

The `source` field distinguishes in-process enforcer events from subprocess proxy events.

## Streaming Events

The runtime emits real-time progress events via SSE (Server-Sent Events) when using the A2A HTTP server:

- **`status`** events carry incremental text
- **`progress`** events carry tool execution updates
- **`result`** events carry the final response

These events enable live progress indicators in the [Web Dashboard](/docs/reference/web-dashboard) and channel adapters.

## Health Checks

When running as a daemon via `forge serve`, the agent exposes health information:

```bash
# Check daemon status (PID, uptime, health)
forge serve status

# View recent logs
forge serve logs
```

See [Audit Logging](/docs/security/audit-logging) for details on the event format and DB mode audit storage.

## Distributed Tracing (OpenTelemetry)

As of OTel Tracing v1 (#108), Forge exports OTLP spans for every A2A
request — dispatcher, executor loop, LLM completions, tool calls, and
outbound HTTP. Multi-hop A2A flows display as a single connected
trace, and every audit row carries the active span's `trace_id` +
`span_id` for cross-pipeline pivoting.

Enable in `forge.yaml`:

```yaml
observability:
  tracing:
    enabled: true
    endpoint: https://otel-collector.monitoring.svc.cluster.local:4318/v1/traces
    sampler: parentbased_always_on
```

Or via env / CLI:

```bash
forge run --otel-enabled \
  --otel-endpoint http://localhost:4318/v1/traces \
  --otel-sampler always_on
```

The collector hostname is auto-added to the egress allowlist at build
time (`forge package`) and at runtime, so no second egress edit is
needed. Compatible with any OTLP-aware backend (Tempo, Jaeger,
Honeycomb, Datadog, Grafana Cloud).

See [Observability — Tracing](/docs/core-concepts/observability-tracing)
for the full reference (span hierarchy, attributes, propagation,
audit cross-link, samplers).
