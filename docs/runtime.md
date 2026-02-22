# LLM Runtime Engine

The runtime engine powers `forge run` — executing agent tasks via LLM providers with tool calling, conversation memory, and lifecycle hooks.

## Agent Loop

The core agent loop is implemented in `internal/runtime/engine/loop.go`. It follows a simple pattern:

1. **Initialize memory** with the system prompt and task history
2. **Append** the user message
3. **Call the LLM** with the conversation and available tool definitions
4. If the LLM returns **tool calls**: execute each tool, append results, go to step 3
5. If the LLM returns a **text response**: return it as the final answer
6. If **max iterations** are exceeded: return an error

```
User message → Memory → LLM → tool_calls? → Execute tools → LLM → ... → text → Done
```

The loop terminates when `FinishReason == "stop"` or `len(ToolCalls) == 0`.

## Executor Types

The runtime supports multiple executor implementations:

| Executor | Use Case |
|----------|----------|
| `LLMExecutor` | Custom agents with LLM-powered tool calling |
| `SubprocessExecutor` | Framework agents (CrewAI, LangChain) running as subprocesses |
| `StubExecutor` | Returns canned responses for testing |

Executor selection happens in `internal/runtime/runner.go` based on framework type and configuration.

## Provider Configuration

Provider configuration is resolved in `internal/runtime/engine/config.go` via `ResolveModelConfig()`. Sources are checked in priority order:

1. **CLI flag** `--provider` (highest priority)
2. **Environment variables**: `FORGE_MODEL_PROVIDER`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `LLM_API_KEY`
3. **forge.yaml** `model` section (lowest priority)

If no provider is explicitly set, the system auto-detects from available API keys.

### Supported Providers

| Provider | Default Model | Base URL Override |
|----------|--------------|-------------------|
| `openai` | `gpt-4o` | `OPENAI_BASE_URL` |
| `anthropic` | `claude-sonnet-4-20250514` | `ANTHROPIC_BASE_URL` |
| `ollama` | `llama3` | `OLLAMA_BASE_URL` |

All providers implement the `llm.Client` interface defined in `internal/runtime/llm/client.go`:

```go
type Client interface {
    Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req *ChatRequest) (<-chan StreamDelta, error)
    ModelID() string
}
```

## Conversation Memory

Memory management is handled by `internal/runtime/engine/memory.go`. Key behaviors:

- **System prompt** is always prepended to the message list (never trimmed)
- **Character budget** defaults to 32,000 characters (~8,000 tokens)
- When over budget, **oldest messages are trimmed first**
- The **most recent message is never trimmed**
- Memory is per-task (created fresh for each `Execute` call)
- Thread-safe via `sync.Mutex`

## Streaming

The current implementation (v1) runs the full tool-calling loop non-streaming. `ExecuteStream` calls `Execute` internally and emits the final response as a single message on a channel. True word-by-word streaming during tool loops is planned for v2.

## Hooks

The engine fires hooks at key points in the loop. See [docs/hooks.md](hooks.md) for details.

## Related Files

- `internal/runtime/engine/loop.go` — Agent loop implementation
- `internal/runtime/engine/memory.go` — Conversation memory
- `internal/runtime/engine/config.go` — Provider configuration resolution
- `internal/runtime/engine/hooks.go` — Hook system
- `internal/runtime/llm/client.go` — LLM client interface
- `internal/runtime/llm/types.go` — Canonical chat types
- `internal/runtime/llm/providers/` — Provider implementations
