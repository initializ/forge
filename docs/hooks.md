# Hooks

The hook system allows custom logic to run at key points in the LLM agent loop. Hooks can observe, modify context, or block execution.

## Overview

Hooks are defined in `internal/runtime/engine/hooks.go`. They fire synchronously during the agent loop and can:

- **Log** interactions for debugging or auditing
- **Block** execution by returning an error
- **Inspect** messages, responses, and tool activity

## Hook Points

| Hook Point | When It Fires | HookContext Data |
|-----------|---------------|------------------|
| `BeforeLLMCall` | Before each LLM API call | `Messages` |
| `AfterLLMCall` | After each LLM API call | `Messages`, `Response` |
| `BeforeToolExec` | Before each tool execution | `ToolName`, `ToolInput` |
| `AfterToolExec` | After each tool execution | `ToolName`, `ToolInput`, `ToolOutput`, `Error` |
| `OnError` | When an LLM call fails | `Error` |

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

## Related Files

- `internal/runtime/engine/hooks.go` — Hook types, registry, and firing logic
- `internal/runtime/engine/loop.go` — Hook integration in the agent loop
