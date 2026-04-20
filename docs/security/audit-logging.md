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
| `llm_call` | LLM API call completed (with token count) |
| `guardrail_check` | Guardrail evaluation result |

### Example

```json
{"ts":"2026-02-28T10:00:00Z","event":"session_start","correlation_id":"a1b2c3d4","task_id":"task-1"}
{"ts":"2026-02-28T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"start"}}
{"ts":"2026-02-28T10:00:01Z","event":"egress_allowed","correlation_id":"a1b2c3d4","fields":{"domain":"api.tavily.com","mode":"allowlist","source":"proxy"}}
{"ts":"2026-02-28T10:00:05Z","event":"tool_exec","correlation_id":"a1b2c3d4","fields":{"tool":"tavily_research","phase":"end"}}
{"ts":"2026-02-28T10:00:06Z","event":"session_end","correlation_id":"a1b2c3d4","fields":{"state":"completed"}}
```

The `source` field distinguishes in-process enforcer events from subprocess proxy events.
