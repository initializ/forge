# LLM Runtime Engine

> Part of [Forge Documentation](../README.md)

The runtime engine powers `forge run` — executing agent tasks via LLM providers with tool calling, conversation memory, and lifecycle hooks.

## Agent Loop

The core agent loop follows a simple pattern:

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

### Q&A Nudge Suppression

When the agent finishes with `stop` and no workflow phases are configured, the loop checks whether edit or git tools were used. If only explore-phase tools were invoked (e.g., `web_search`, `file_read`), the conversation is classified as informational/Q&A — the agent's text response is the final answer and no continuation nudge ("You stopped…") is sent. This prevents the agent from re-summarizing answers to simple questions.

## LLM Providers

Forge supports multiple LLM providers with automatic fallback:

| Provider | Default Model | Auth |
|----------|--------------|------|
| `openai` | `gpt-5.2-2025-12-11` | API key or OAuth; optional Organization ID |
| `anthropic` | `claude-sonnet-4-20250514` | API key |
| `gemini` | `gemini-2.5-flash` | API key |
| `ollama` | `llama3` | None (local) |
| Custom | Configurable | API key |

### Configuration

```yaml
model:
  provider: openai
  name: gpt-4o
```

Or override with environment variables:

```bash
export FORGE_MODEL_PROVIDER=anthropic
export ANTHROPIC_API_KEY=sk-ant-...
forge run
```

Provider is auto-detected from available API keys if not explicitly set. Provider configuration is resolved via `ResolveModelConfig()` in priority order:

1. **CLI flag** `--provider` (highest priority)
2. **Environment variables**: `FORGE_MODEL_PROVIDER`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`
3. **forge.yaml** `model` section (lowest priority)

### OpenAI OAuth

For OpenAI, Forge supports browser-based OAuth login (matching the Codex CLI flow) as an alternative to API keys:

```bash
forge init my-agent
# Select "OpenAI" -> "Login with browser (OAuth)"
# Browser opens for authentication
```

OAuth tokens are stored in `~/.forge/credentials/openai.json` and automatically refreshed.

#### OAuth for Skill Scripts

When `OPENAI_API_KEY` is set to the sentinel value `__oauth__`, the `SkillCommandExecutor` resolves OAuth credentials at execution time and injects:
- The real access token as `OPENAI_API_KEY`
- The Codex base URL as `OPENAI_BASE_URL`
- The configured model name as `REVIEW_MODEL`

Skill scripts (e.g., `code-review-diff.sh`) detect `OPENAI_BASE_URL` and automatically use the OpenAI Responses API with streaming instead of the standard Chat Completions API.

### Secret Reuse Detection

At startup, the runtime validates that secret values are not reused across different purpose categories. Sharing the same token between unrelated services (e.g., using an OpenAI API key as a Telegram bot token) is blocked with an error.

Categories: `llm` (OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY, etc.), `search` (TAVILY_API_KEY, PERPLEXITY_API_KEY), `telegram` (TELEGRAM_BOT_TOKEN), `slack` (SLACK_APP_TOKEN, SLACK_BOT_TOKEN). Same-category reuse (e.g., two LLM keys with the same value) is allowed.

### Organization ID (OpenAI Enterprise)

Enterprise OpenAI accounts can set an Organization ID to route API requests to the correct org:

```yaml
model:
  provider: openai
  name: gpt-4o
  organization_id: "org-xxxxxxxxxxxxxxxxxxxxxxxx"
```

Or via environment variable (overrides YAML):

```bash
export OPENAI_ORG_ID=org-xxxxxxxxxxxxxxxxxxxxxxxx
```

The `OpenAI-Organization` header is sent on all OpenAI API requests (chat, embeddings, responses). Fallback providers inherit the primary org ID unless overridden per-fallback. The org ID is also injected into skill subprocess environments as `OPENAI_ORG_ID`.

### Fallback Chains

Configure fallback providers for automatic failover when the primary provider is unavailable:

```yaml
model:
  provider: openai
  name: gpt-4o
  fallbacks:
    - provider: anthropic
      name: claude-sonnet-4-20250514
    - provider: gemini
```

Or via environment variable:

```bash
export FORGE_MODEL_FALLBACKS="anthropic:claude-sonnet-4-20250514,gemini:gemini-2.5-flash"
```

Fallback behavior:
- **Retriable errors** (rate limits, overloaded, timeouts) try the next provider
- **Non-retriable errors** (auth, billing, bad format) abort immediately
- Per-provider exponential backoff cooldowns prevent thundering herd
- Fallbacks are also auto-detected from available API keys when not explicitly configured

## Executor Types

The runtime supports multiple executor implementations:

| Executor | Use Case |
|----------|----------|
| `LLMExecutor` | Custom agents with LLM-powered tool calling |
| `SubprocessExecutor` | Framework agents (CrewAI, LangChain) running as subprocesses |
| `StubExecutor` | Returns canned responses for testing |

Executor selection happens in `runner.go` based on framework type and configuration.

## Running Modes

### `forge run` — Foreground Server

Run the agent as a foreground HTTP server. Used for development and container deployments.

```bash
# Development (all interfaces, immediate shutdown)
forge run --with slack --port 8080

# Container deployment
forge run --host 0.0.0.0 --shutdown-timeout 30s
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | HTTP server port |
| `--host` | `""` (all interfaces) | Bind address |
| `--shutdown-timeout` | `0` (immediate) | Graceful shutdown timeout |
| `--with` | — | Channel adapters (e.g. `slack,telegram`) |
| `--mock-tools` | `false` | Use mock executor for testing |
| `--model` | — | Override model name |
| `--provider` | — | Override LLM provider |
| `--env` | `.env` | Path to env file |
| `--enforce-guardrails` | `true` | Enforce guardrail violations as errors |
| `--no-guardrails` | `false` | Disable all guardrail enforcement |

### `forge serve` — Background Daemon

Manage the agent as a background daemon process with PID/log management.

```bash
# Start daemon (secure defaults: 127.0.0.1, 30s shutdown timeout)
forge serve

# Start on custom port
forge serve start --port 9090 --host 0.0.0.0

# Stop the daemon
forge serve stop

# Check status (PID, uptime, health)
forge serve status

# View recent logs (last 100 lines)
forge serve logs
```

| Subcommand | Description |
|------------|-------------|
| `start` (default) | Start the daemon in background |
| `stop` | Send SIGTERM (10s timeout, SIGKILL fallback) |
| `status` | Show PID, listen address, health check |
| `logs` | Tail `.forge/serve.log` |

The daemon forks `forge run` in the background with `setsid`, writes state to `.forge/serve.json`, and redirects output to `.forge/serve.log`. Passphrase prompting for encrypted secrets happens in the parent process (which has TTY access) before forking.

## File Output Directory

The runtime configures a `FilesDir` for tool-generated files (e.g., from `file_create`). This directory defaults to `<WorkDir>/.forge/files/` and is injected into the execution context so tools can write files that other tools can reference by path.

```
<WorkDir>/
  .forge/
    files/        ← file_create output (patches.yaml, reports, etc.)
    sessions/     ← conversation persistence
    memory/       ← long-term memory
```

The `FilesDir` is set via `LLMExecutorConfig.FilesDir` and made available to tools through `runtime.FilesDirFromContext(ctx)`. See [Tools — File Create](tools.md#file-create) for details.

## Conversation Memory

For details on session persistence, context window management, compaction, and long-term memory, see [Memory](memory.md).

## Hooks

The engine fires hooks at key points in the loop. See [Hooks](hooks.md) for details.

The runner registers five hook groups: logging, audit, progress, global guardrail hooks, and skill guardrail hooks. The global guardrail `AfterToolExec` hook scans tool output for secrets and PII, redacting or blocking before results enter the LLM context. Skill guardrail hooks enforce domain-specific rules declared in `SKILL.md` — blocking commands, redacting output, intercepting capability enumeration probes, and replacing binary-enumerating responses. Skill guardrails are loaded from build artifacts or parsed directly from `SKILL.md` at runtime (no `forge build` required). See [Tool Output Scanning](security/guardrails.md#tool-output-scanning) and [Skill Guardrails](security/guardrails.md#skill-guardrails).

## Streaming

The LLM tool-calling loop runs non-streaming internally. `ExecuteStream` calls `Execute` and emits the final response on a channel. However, the **UI chat proxy** (`forge-ui/chat.go`) streams A2A SSE events to the browser in real-time — `status` events carry incremental text, `progress` events carry tool execution updates, and `result` events carry the final response. The frontend renders text and tool progress as each event arrives.

---
← [Tools](tools.md) | [Back to README](../README.md) | [Memory](memory.md) →
