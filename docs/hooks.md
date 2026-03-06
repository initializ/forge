# Hooks

> Part of [Forge Documentation](../README.md)

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

The runner registers a guardrail hook that scans tool output for secrets and PII patterns. See [Tool Output Scanning](security/guardrails.md#tool-output-scanning) for details.

```go
hooks.Register(engine.AfterToolExec, func(ctx context.Context, hctx *engine.HookContext) error {
    hctx.ToolOutput = strings.ReplaceAll(hctx.ToolOutput, secret, "[REDACTED]")
    return nil
})
```

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

---
← [Scheduling](scheduling.md) | [Back to README](../README.md) | [Commands](commands.md) →
