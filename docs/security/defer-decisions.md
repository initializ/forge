# Deferred authorization (governance R4c)

Forge can pause the executor mid-task on a high-risk tool call,
notify an external decision-maker (typically a human on Slack /
Telegram / Teams), and resume when a decision arrives. This closes
the fifth and final `PolicyDecision` — `DEFER` — from the R4
governance taxonomy.

Where the other four decisions are terminal (ALLOW proceeds, DENY
refuses, MODIFY rewrites, STEP_UP demands re-authentication),
DEFER is unique: the executor is **paused** — not failed — until
an operator or approver arrives to make the call.

## When to use it

DEFER is the right tool when:

- The action is legitimate under the right circumstances but too
  dangerous to automate (`rm -rf /var/prod`, `aws s3 rm --recursive`).
- The policy engine can't decide from static rules and needs a
  human in the loop.
- Auditability requires a named approver on the action.

DEFER is **not** the right tool for:

- Actions the agent should never take (use DENY / guardrail
  `deny_commands`).
- Actions that need rewriting for safety (use MODIFY / `deny_output`).
- Sessions where the whole flow needs higher-assurance auth (use
  STEP_UP / R4b).

## How it works

1. **Config** (`forge.yaml`) — declare per-tool defer parameters:
   ```yaml
   security:
     defer:
       enabled: true
       tools:
         cli_execute:
           to: channel:slack:#oncall
           timeout: 10m
           context_template: "Agent wants to run {tool} with args: {args}"
       default_timeout: 10m
   ```

2. **`BeforeToolExec` hook fires** on a matching tool call:
   - Registers a pending deferral with the `defer` engine.
   - Flips the task's A2A status to `deferred` in the store, so
     parallel `GET /tasks/{id}` polls see the pause.
   - Emits `task_deferred` audit event (fields: `tool`, `to`,
     `timeout_ms`, `context`).
   - **Blocks the calling goroutine** on the deferral's decision
     channel. The HTTP request handling the current tasks/send
     stays open through the wait.

3. **Approver decides** via `POST /tasks/{id}/decisions`:
   ```
   POST /tasks/task-abc/decisions
   Content-Type: application/json

   {"decision":"approve","approver":"alice@example.com","note":"ok, one-off"}
   ```
   Endpoint validates the task exists in a deferred state (404 if
   not) and the decision string is `approve` or `reject` (400 if
   not). On success the pending deferral is resolved and the
   blocked executor goroutine wakes.

4. **Resume behavior:**
   - `approve` → task status restored to `working`, the tool runs
     normally, the tasks/send response returns as if nothing
     paused. Audit event: `task_deferred_decision {decision:"approve",
     approver, note, wait_ms}`.
   - `reject` → tool call fails with `defer: rejected by <approver>: <note>`;
     task ends `failed`. Audit event: `task_deferred_decision
     {decision:"reject", ...}`.
   - `timeout` (no decision within `timeout`) → tool call fails
     with `defer: no decision within <duration> (auto-deny)`.
     Audit event: `task_deferred_timeout {timeout_ms, wait_ms}`.

## Context template

The `context_template` string is rendered with `{tool}` and `{args}`
placeholders substituted at hook time. The result is what the
notify adapter shows the approver. Example:

```yaml
context_template: "Agent {tool} wants to execute: {args}"
```

Rendered for `cli_execute` with `args={"binary":"aws","args":["s3","rm"]}`:

```
Agent cli_execute wants to execute: {"binary":"aws","args":["s3","rm"]}
```

Truncated to 512 runes on the audit event to bound sink pressure.

## Wire coverage

- **POST /tasks/{id}/decisions** — REST endpoint that accepts
  `{decision, approver, note}`. Returns 200 on resolve, 404 on
  unknown task, 400 on invalid decision, 409 on race (another
  decision landed first).
- **`BeforeToolExec` pause** — fires from the same three tasks/send
  paths as R3/R4b (REST, JSON-RPC, SSE). The HTTP client's
  connection stays open through the wait; adjust reverse-proxy
  timeouts (~`timeout` + a margin) accordingly.

  **Choose your transport for the approval window.** Synchronous
  `tasks/send` requires the caller to hold one HTTP request open
  for the entire `timeout`. That's fine for short (~seconds)
  approvals in interactive tools, but any window measured in
  minutes should use `tasks/sendSubscribe` (SSE) or the async
  A2A envelope so the caller can drop and reconnect. If either
  the client's read-timeout or the server's write-timeout fires
  before the approver responds, the ctx cancels — the executor
  cleans up correctly (Handle deregistered, status restored,
  audit line for the abandoned wait), but the approval is
  effectively lost and the operator has to re-drive the task.
  Rule of thumb: `min(client_read_timeout, server_write_timeout,
  reverse_proxy_idle_timeout) > defer.timeout + margin`.
- **Task-status transitions** — the a2a task store flips to
  `deferred` for the duration of the wait; parallel GETs on
  `/tasks/{id}` observe it.

## In-process only (today)

The pause mechanism is a goroutine block — the goroutine's stack IS
the persisted state. A Forge process restart mid-defer abandons all
pending deferrals; the caller's HTTP request will fail cleanly. For
deployments needing pause-across-restart, the `defer.Engine`
interface is the seam a future persistent implementation will
replace.

## Audit event shape

```json
{
  "event": "task_deferred",
  "task_id": "task-abc",
  "correlation_id": "req-xyz",
  "fields": {
    "tool": "cli_execute",
    "to": "channel:slack:#oncall",
    "timeout_ms": 600000,
    "context": "Agent cli_execute wants to execute: {\"binary\":\"aws\"…"
  }
}
```

```json
{
  "event": "task_deferred_decision",
  "task_id": "task-abc",
  "fields": {
    "tool": "cli_execute",
    "decision": "approve",
    "approver": "alice@example.com",
    "note": "ok, one-off",
    "wait_ms": 42371
  }
}
```

```json
{
  "event": "task_deferred_timeout",
  "task_id": "task-abc",
  "fields": {
    "tool": "cli_execute",
    "timeout_ms": 600000,
    "wait_ms": 600000
  }
}
```

Never carries token bytes or full tool inputs beyond the truncated
context template.

## Notify integration

### Native Slack approvals (#310)

When the agent runs with `--with slack` and a deferred tool's `to` is
`channel:slack:<channel>`, Forge **delivers the approval natively**: on a
deferral the Slack adapter posts a **Block Kit** message with **Approve /
Reject** buttons to that channel, and a click resolves the deferral —
the tool proceeds or fails and the message updates with the outcome +
approver.

```yaml
security:
  defer:
    enabled: true
    tools:
      atlassian__jira_create_issue:
        to: channel:slack:#oncall        # channel:<adapter>:<target>
        timeout: 15m
        context_template: "Agent wants to run {tool} with args: {args}"
```

This needs **no inbound exposure to Forge**: the Slack adapter uses
Socket Mode (outbound WebSocket), so the button click arrives over the
agent's existing outbound connection. Under the hood the click is routed
to the same `POST /tasks/{id}/decisions` endpoint an operator would curl.
Delivery is **best-effort** — if Slack is unreachable the deferral still
holds and an approver can POST the decision directly; a delivery failure
never auto-denies.

The `to` value must be `channel:<adapter>:<target>`; an adapter that
doesn't implement interactive approvals (`channels.ApprovalDeliverer`)
can't be a target. Telegram / MS Teams interactive approvals are a
follow-up (same interface).

### Custom notify path

For any other target (or without the Slack adapter), wire your own:

- Poll the audit stream for `task_deferred` (carries `task_id`, `to`,
  `context`), forward to your tool of choice.
- Have it POST `{decision, approver, note}` to `/tasks/{id}/decisions`
  (add an auth token — the endpoint honors the runner's auth middleware).

## Combining with other governance controls

- **R3 intent alignment (#208)** fires first; alignment DENY skips the defer path.
- **R4b step-up (#210)** — deployments needing "high-assurance for
  ALL sessions" prefer step-up; DEFER is per-action.
- **R5 hash chain (#212) / R6 signing (#213)** — the three defer
  audit events participate in both when enabled.

Combined governance posture: R3 alignment → R4b step-up →
R4a MODIFY → R4c DEFER → tool executes. A DENY at any layer
short-circuits everything downstream.
