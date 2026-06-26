---
title: "Workflow Correlation IDs"
description: "Orchestrator workflow / stage / step / invocation-caller identifiers threaded from inbound A2A headers through context.Context onto every audit event."
order: 7
---

# Workflow correlation IDs

Forge agents accept orchestration headers on every inbound A2A call and tag every audit event emitted during that invocation with the matching workflow / execution / stage / step / invocation-caller identifiers. This lets a platform orchestrator (initializ Command, or any A2A-compatible orchestrator) correlate audit events across the multiple agents that participate in one workflow run.

Direct A2A invocations (no orchestrator) leave the fields unset — emitted JSON matches the pre-correlation shape exactly, so existing audit consumers keep working.

## Headers

| Header | Audit field | Identifies |
|---|---|---|
| `X-Workflow-ID` | `workflow_id` | The workflow DEFINITION — stable across every run of the same workflow. Join here for definition-level rollups ("top failing workflows", "latency by workflow definition"). |
| `X-Workflow-Execution-ID` | `workflow_execution_id` | The per-run instance — unique per workflow execution. Join here for per-run timelines ("show me every event in this specific run, across every agent the orchestrator dispatched to"). Added in FORGE-2 / issue #185. |
| `X-Workflow-Stage-ID` | `stage_id` | A stage within the workflow run (a group of steps that may run in parallel) |
| `X-Workflow-Step-ID` | `step_id` | The specific step that invoked this agent |
| `X-Invocation-Caller` | `invocation_caller` | The upstream caller (orchestrator identity, or upstream agent in an agent-to-agent flow) |

All five are optional. Forge extracts whichever are present and leaves the rest empty. The header names are vendor-neutral by design — any A2A-compatible orchestrator (initializ Command, custom registries, third-party platforms) can drive Forge's correlation surface without adopting a vendor prefix.

### Why definition + execution

Pre-FORGE-2, `X-Workflow-ID` was overloaded — the code comment said "orchestrator workflow run" but the name read like a definition identifier. The split disambiguates two distinct queries operators actually want:

- "Show me this specific run" → join on `workflow_execution_id`
- "Show me every run of this workflow" / "Top failing workflows" → group by `workflow_id`

Industry precedent for the split: GitHub Actions (`workflow` + `workflow_run_id`), Tekton (`Pipeline` + `PipelineRun`), Argo (`Workflow` + `WorkflowRun`).

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
  └─ stamps workflow_id / workflow_execution_id / stage_id / step_id / invocation_caller onto event
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
  "workflow_id": "wf-deploy-prod",
  "workflow_execution_id": "wfrun-2026-06-04-canary-001",
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

When a Forge agent calls another agent (or any peer), the workflow context is available via `runtime.WorkflowContextFromContext(ctx)`. Forge supports two complementary propagation paths:

### Explicit propagation (always available)

A tool that knows its target is a workflow peer can stamp the headers itself:

```go
wc := runtime.WorkflowContextFromContext(ctx)
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, peerURL, body)
wc.ApplyToHTTPHeaders(req.Header)
client.Do(req)
```

This is the path used by hand-written A2A tools and any code that wants per-request control.

### Auto-propagation via `forge.yaml` allow-list (issue #186 / FORGE-1)

Adding a `workflow_propagation` block to `forge.yaml` opts specific downstream hosts in to auto-receive the workflow headers without each tool having to call `ApplyToHTTPHeaders`. Every built-in HTTP tool (`http_request`, `webhook_call`, `web_search_*`, future tools) routes outbound requests through the egress transport, and the runner wraps that transport with a small RoundTripper that consults the allow-list per request.

```yaml
workflow_propagation:
  allowed_hosts:
    - "orchestrator.svc"           # exact match
    - "*.agents.internal"          # wildcard subdomain
```

| Pattern | Matches |
|---|---|
| `orchestrator.svc` | exactly `orchestrator.svc` (any port) |
| `*.agents.internal` | any strictly-deeper subdomain like `payments.agents.internal` or `worker.zone-a.agents.internal` |

Match semantics mirror the egress allow-list (`security.DomainMatcher`): lowercase + port-stripped comparison, wildcards match strictly-deeper subdomains (the `*.agents.internal` entry does NOT match the bare `agents.internal` apex), and the matcher safely returns false on a nil receiver / empty list.

**Auto-propagation is deliberately off by default.** The `X-Workflow-*` / `X-Invocation-Caller` headers identify the workflow, so blanket-stamping them on every outbound HTTP request would leak workflow identity to third-party APIs (the egress proxy can't tell a peer agent from a vendor endpoint). The allow-list keeps the safe default while removing the manual per-tool friction inside the trust boundary. A request to a host that isn't on the list still requires an explicit `ApplyToHTTPHeaders` call from the tool — the pre-#186 behavior.

## Backward compatibility

- Direct A2A invocations (no headers) → audit JSON byte-for-byte identical to pre-FWS-2.
- Existing emitters that construct `AuditEvent` literals and call `Emit(...)` continue to work unchanged.
- New `EmitFromContext(ctx, event)` is the per-request preferred path — only adds workflow fields when ctx carries a non-zero `WorkflowContext`; fields already set on the event take precedence over the ctx fallback.

## Where it's wired

| File | Role |
|---|---|
| `forge-core/runtime/workflow.go` | `WorkflowContext` type, `WithWorkflowContext` / `WorkflowContextFromContext` ctx helpers, `WorkflowContextFromHTTPHeaders` + `ApplyToHTTPHeaders` header marshalers |
| `forge-core/runtime/workflow_propagation.go` | `WorkflowPropagationMatcher` (exact + wildcard host matching) and `WrapTransportForWorkflowPropagation` (RoundTripper wrapper that auto-applies headers on allow-listed hosts). FORGE-1 / issue #186. |
| `forge-core/types/config.go` | `WorkflowPropagationConfig` (`forge.yaml` `workflow_propagation` block) |
| `forge-cli/runtime/runner.go` | Builds the matcher from `cfg.WorkflowPropagation.AllowedHosts` and wraps the egress client's transport once at startup; every HTTP tool inherits the auto-apply via `security.EgressTransportFromContext` |
| `forge-core/runtime/audit.go` | `AuditEvent` extended with `workflow_id` / `workflow_execution_id` / `stage_id` / `step_id` / `invocation_caller` fields; `AuditLogger.EmitFromContext` auto-tags from ctx |
| `forge-cli/server/a2a_server.go` | JSON-RPC dispatcher extracts headers at boundary and injects WorkflowContext into ctx |
| `forge-cli/runtime/runner.go` | REST `POST /tasks/send` + `POST /tasks/sendSubscribe` handlers extract headers; in-request audit emit sites migrated to `EmitFromContext` |
| auth audit callback | Pulls headers directly from `req.Header` (runs before the dispatcher in middleware order) and stamps the four fields onto `auth_verify` / `auth_fail` events |
