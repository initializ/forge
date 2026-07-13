---
title: "Hooks"
description: "Hook into the agent loop for logging, enforcement, and auditing."
order: 7
---

The hook system allows custom logic to run at key points in the LLM agent loop. Hooks can observe, modify context, or block execution.

## Overview

Hooks fire synchronously during the agent loop and can:

- **Log** interactions for debugging or auditing
- **Block** execution by returning an error
- **Inspect** messages, responses, and tool activity

## Hook Points

| Hook Point | When It Fires | Available Data |
|-----------|---------------|------------------|
| `BeforeLLMCall` | Before each LLM API call | `Messages`, `TaskID`, `CorrelationID` |
| `AfterLLMCall` | After each LLM API call | `Messages`, `Response`, `TaskID`, `CorrelationID` |
| `BeforeToolExec` | Before each tool execution | `ToolName`, `ToolInput`, `TaskID`, `CorrelationID` |
| `AfterToolExec` | After each tool execution | `ToolName`, `ToolInput`, `ToolOutput` (mutable), `Error`, `TaskID`, `CorrelationID` |
| `OnError` | When an LLM call fails | `Error`, `TaskID`, `CorrelationID` |
| `OnProgress` | During tool execution | `Phase`, `ToolName`, `StatusMessage` |

## HookContext

The `HookContext` struct carries data available at each hook point:

```go
type HookContext struct {
    Messages   []llm.ChatMessage  // Current conversation messages
    Response   *llm.ChatResponse  // LLM response (AfterLLMCall only)
    ToolName   string             // Tool being executed
    ToolInput  string             // Tool input arguments (JSON)
    ToolOutput string             // Tool result (AfterToolExec only)
    Error      error              // Error that occurred
}
```

## Writing Hooks

Hooks implement the `Hook` function signature:

```go
type Hook func(ctx context.Context, hctx *HookContext) error
```

### Logging Hook Example

```go
hooks := engine.NewHookRegistry()

hooks.Register(engine.BeforeLLMCall, func(ctx context.Context, hctx *engine.HookContext) error {
    log.Printf("LLM call with %d messages", len(hctx.Messages))
    return nil
})

hooks.Register(engine.AfterToolExec, func(ctx context.Context, hctx *engine.HookContext) error {
    log.Printf("Tool %s returned: %s", hctx.ToolName, hctx.ToolOutput)
    return nil
})
```

### Enforcement Hook Example

```go
hooks.Register(engine.BeforeToolExec, func(ctx context.Context, hctx *engine.HookContext) error {
    if hctx.ToolName == "dangerous_tool" {
        return fmt.Errorf("tool %q is blocked by policy", hctx.ToolName)
    }
    return nil
})
```

## Output Redaction

`AfterToolExec` hooks can modify `hctx.ToolOutput` to redact sensitive content before it enters the LLM context. The agent loop reads back `ToolOutput` from the `HookContext` after all hooks fire.

The runner registers a guardrail hook that scans tool output for secrets and PII patterns. The hook passes `hctx.ToolName` to the guardrail engine, enabling per-tool exemptions via `allow_tools` config. See [Tool Output Scanning](../security/guardrails.md#tool-output-scanning) for details.

```go
hooks.Register(engine.AfterToolExec, func(ctx context.Context, hctx *engine.HookContext) error {
    hctx.ToolOutput = strings.ReplaceAll(hctx.ToolOutput, secret, "[REDACTED]")
    return nil
})
```

## Skill Guardrail Hooks

When skills declare guardrails in their `SKILL.md` frontmatter, the runner registers four hooks that enforce skill-specific security policies across the entire agent loop:

| Hook Point | Guardrail Type | Behavior |
|------------|---------------|----------|
| `BeforeLLMCall` | `deny_prompts` | Blocks user messages that probe agent capabilities (e.g., "what tools can you run") |
| `AfterLLMCall` | `deny_responses` | Replaces LLM responses that enumerate internal binary names |
| `BeforeToolExec` | `deny_commands` | Blocks `cli_execute` commands matching deny patterns (e.g., `kubectl get secrets`) |
| `AfterToolExec` | `deny_output` | Blocks or redacts `cli_execute` output matching deny patterns (e.g., Secret manifests) |

These hooks complement the global guardrail hooks (secrets/PII scanning) and fire in addition to them. Skill guardrails are loaded from build artifacts or parsed at runtime from `SKILL.md` — no `forge build` step is required.

For pattern syntax and configuration, see [Skill Guardrails](../security/guardrails.md#skill-guardrails).

## Audit Logging

The runner registers `AfterLLMCall` hooks that emit structured audit events for each LLM interaction. Audit fields include:

| Field | Description |
|-------|-------------|
| `provider` | LLM provider name |
| `model` | Model identifier |
| `input_tokens` | Prompt token count |
| `output_tokens` | Completion token count |
| `organization_id` | OpenAI Organization ID (when set) |

These events are logged via `slog` at Info level and can be consumed by external log aggregators for cost tracking and compliance.

## Progress Tracking

The runner automatically registers progress hooks that emit real-time status updates during tool execution. Progress events include the tool name, phase (`tool_start` / `tool_end`), and a human-readable status message. These events are streamed to clients via SSE when using the A2A HTTP server, enabling live progress indicators in web and chat UIs.

## Governance hooks (R3 / R7 / R4b / R4c / R9)

Forge's governance framework layers its policy engines on top of the hook system. All are OPT-IN through `forge.yaml`; when the corresponding block is absent the engine is not wired and the wire shape stays unchanged. Most are `BeforeToolExec` hooks; R7 folds into the R3 hook, and R9 is not a hook at all (see its row).

| Hook | Config block | Fires at | Fail behavior |
|---|---|---|---|
| Intent alignment (R3) | `security.intent_alignment` | `BeforeToolExec` | Deny → error → tool body never runs. Emits `intent_alignment` audit event. See [intent-alignment.md](../security/intent-alignment.md). |
| Intent drift (R7) | `security.intent_drift` (requires R3) | Same `BeforeToolExec` — folded into the R3 hook's Score call | Telemetry only; never changes the decision. Emits `intent_drift` on state transitions. |
| Step-up (R4b) | `security.step_up` | `BeforeToolExec` | Missing / weak `acr` claim → returns `*stepup.RequiredError` → REST handler emits HTTP 401 + RFC 9470 `WWW-Authenticate` challenge. Emits `auth_step_up_required`. See [step-up-auth.md](../security/step-up-auth.md). |
| Defer (R4c) | `security.defer` | `BeforeToolExec` | Blocks the executor goroutine on a decision channel; task status flips to `deferred`. On approve → resumes and runs. On reject / timeout → error → tool body never runs. Emits `task_deferred` / `task_deferred_decision` / `task_deferred_timeout`. See [defer-decisions.md](../security/defer-decisions.md). |
| JIT credentials (R9) | top-level `credentials:` | In-tool at `Execute` — **not a registered hook**. The injector is wired onto `cli_execute` / `http_request` via `WithCredentialInjector`; `Materialize` runs inside the tool's `Execute`, and the credential is revoked via a deferred `Close`. | Fresh credentials materialized per tool call; injected into the outbound request (headers for HTTP tools, env for `cli_execute`). Emits `credential_issued` / `credential_revoked` / `credential_failed`. **Credential material never appears in audit event payloads.** See [least-privilege-credentials.md](../security/least-privilege-credentials.md). |

Order: the governance hooks register AFTER guardrail hooks so a caller whose input is already guardrail-denied doesn't see the step-up challenge or the defer wait unnecessarily. Within the governance hooks themselves the order is R3 → R4b → R4c → R9 as reflected in the audit stream ordering.

## Error Handling

- Hooks fire **in registration order** for each hook point
- If a hook returns an **error**, execution stops immediately
- The error propagates up to the `Execute` caller
- For `BeforeToolExec`, returning an error prevents the tool from running
- For `OnError`, the error from the LLM call is available in `hctx.Error`

## Registration

```go
hooks := engine.NewHookRegistry()
hooks.Register(engine.BeforeLLMCall, myHook)
hooks.Register(engine.AfterToolExec, myOtherHook)

exec := engine.NewLLMExecutor(engine.LLMExecutorConfig{
    Client: client,
    Tools:  tools,
    Hooks:  hooks,
})
```

If no `HookRegistry` is provided, an empty one is created automatically.
