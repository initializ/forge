---
title: "Workflow Correlation IDs"
description: "Orchestrator workflow / stage / step / invocation-caller identifiers threaded from inbound A2A headers through context.Context onto every audit event."
order: 7
---

# Workflow correlation IDs

Forge agents accept orchestration headers on every inbound A2A call and tag every audit event emitted during that invocation with the matching workflow / stage / step / invocation-caller identifiers. This lets a platform orchestrator (initializ Command, or any A2A-compatible orchestrator) correlate audit events across the multiple agents that participate in one workflow run.

Direct A2A invocations (no orchestrator) leave the fields unset — emitted JSON matches the pre-correlation shape exactly, so existing audit consumers keep working.

## Headers

| Header | Audit field | Identifies |
|---|---|---|
| `X-Workflow-ID` | `workflow_id` | The orchestrator-level workflow run |
| `X-Workflow-Stage-ID` | `stage_id` | A stage within the workflow (a group of steps that may run in parallel) |
| `X-Workflow-Step-ID` | `step_id` | The specific step that invoked this agent |
| `X-Invocation-Caller` | `invocation_caller` | The upstream caller (orchestrator identity, or upstream agent in an agent-to-agent flow) |

All four are optional. Forge extracts whichever are present and leaves the rest empty. The header names are vendor-neutral by design — any A2A-compatible orchestrator (initializ Command, custom registries, third-party platforms) can drive Forge's correlation surface without adopting a vendor prefix.

## How it flows

```
Inbound request
  ↓
A2A dispatcher (forge-cli/server/a2a_server.go)
  ├─ runtime.WorkflowContextFromHTTPHeaders(r.Header)
  └─ runtime.WithWorkflowContext(ctx, wc)
  ↓
JSON-RPC / REST handler receives ctx with WorkflowContext baked in
  ↓
auditLogger.EmitFromContext(ctx, event)
  ├─ reads WorkflowContext from ctx
  └─ stamps workflow_id / stage_id / step_id / invocation_caller onto event
```

Every event from `session_start` through `session_end`, every `tool_exec` / `llm_call` / `egress_allowed` / `egress_blocked` emitted under that ctx carries the same workflow tags — letting an audit consumer reconstruct the full workflow-step → agent-execution → tool-call tree.

## Audit event shape

`session_start` from a workflow-orchestrated invocation:

```json
{
  "ts": "2026-06-04T15:21:09Z",
  "event": "session_start",
  "correlation_id": "9b3d…",
  "task_id": "task-42",
  "workflow_id": "wf-deploy-prod-001",
  "stage_id": "rollout",
  "step_id": "canary-bake",
  "invocation_caller": "initializ-orchestrator"
}
```

Same agent invoked directly (no orchestrator):

```json
{
  "ts": "2026-06-04T15:21:09Z",
  "event": "session_start",
  "correlation_id": "9b3d…",
  "task_id": "task-42"
}
```

Workflow fields are absent (`omitempty`) — byte-identical to pre-correlation audit consumers.

## Outbound propagation (agent-to-agent)

When a Forge agent calls another agent (or any peer), the workflow context is available via `runtime.WorkflowContextFromContext(ctx)`. To propagate, copy the headers onto the outbound request:

```go
wc := runtime.WorkflowContextFromContext(ctx)
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, peerURL, body)
wc.ApplyToHTTPHeaders(req.Header)
client.Do(req)
```

**Auto-propagation is deliberately off by default.** The `X-Workflow-*` / `X-Invocation-Caller` headers identify the workflow, so blanket-stamping them on every outbound HTTP request would leak workflow identity to third-party APIs (the egress proxy can't tell a peer agent from a vendor endpoint). Tools that know their target is a workflow peer call `ApplyToHTTPHeaders` explicitly.

## Backward compatibility

- Direct A2A invocations (no headers) → audit JSON byte-for-byte identical to pre-FWS-2.
- Existing emitters that construct `AuditEvent` literals and call `Emit(...)` continue to work unchanged.
- New `EmitFromContext(ctx, event)` is the per-request preferred path — only adds workflow fields when ctx carries a non-zero `WorkflowContext`; fields already set on the event take precedence over the ctx fallback.

## Where it's wired

| File | Role |
|---|---|
| `forge-core/runtime/workflow.go` | `WorkflowContext` type, `WithWorkflowContext` / `WorkflowContextFromContext` ctx helpers, `WorkflowContextFromHTTPHeaders` + `ApplyToHTTPHeaders` header marshalers |
| `forge-core/runtime/audit.go` | `AuditEvent` extended with `workflow_id` / `stage_id` / `step_id` / `invocation_caller` fields; `AuditLogger.EmitFromContext` auto-tags from ctx |
| `forge-cli/server/a2a_server.go` | JSON-RPC dispatcher extracts headers at boundary and injects WorkflowContext into ctx |
| `forge-cli/runtime/runner.go` | REST `POST /tasks/send` + `POST /tasks/sendSubscribe` handlers extract headers; in-request audit emit sites migrated to `EmitFromContext` |
| auth audit callback | Pulls headers directly from `req.Header` (runs before the dispatcher in middleware order) and stamps the four fields onto `auth_verify` / `auth_fail` events |
