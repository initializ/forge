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

## Notify integration (follow-up)

The `to` field is free-form for now — the runtime doesn't route it
anywhere. Operators wire their own notify path:

- Poll the audit stream for `task_deferred`, forward to Slack/Teams/etc.
- Slack Block Kit approve/reject buttons post back to
  `/tasks/{id}/decisions` (add an auth token — the endpoint honors
  the runner's normal auth middleware).

A first-party channel adapter with approve/reject buttons is
tracked as a follow-up; the plumbing (audit → HTTP roundtrip) is
in place today.

## Combining with other governance controls

- **R3 intent alignment (#208)** fires first; alignment DENY skips the defer path.
- **R4b step-up (#210)** — deployments needing "high-assurance for
  ALL sessions" prefer step-up; DEFER is per-action.
- **R5 hash chain (#212) / R6 signing (#213)** — the three defer
  audit events participate in both when enabled.

Combined governance posture: R3 alignment → R4b step-up →
R4a MODIFY → R4c DEFER → tool executes. A DENY at any layer
short-circuits everything downstream.
