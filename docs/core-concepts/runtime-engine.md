---
title: "Runtime Engine"
description: "The LLM runtime engine powering tool calling, memory, and hooks."
order: 6
---

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

The loop terminates when `len(ToolCalls) == 0`. `FinishReason` is intentionally ignored — some providers return `"stop"` even when tool calls are present. Only the tool call list determines whether execution continues.

### Session Recovery Deduplication

When a session is recovered from the [session store](memory-system.md#session-store-backends) (e.g., after a premature loop exit, on any pod for the remote backend), the executor checks whether the recovered conversation already ends with an identical user message. If so, the duplicate is skipped to prevent the same message from appearing twice in the context window. This handles the common case where a user retries the same prompt after a crash or timeout.

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
| Custom URL | Configurable | API key (OpenAI or Anthropic shape); AWS SigV4 via `auth_scheme: aws_sigv4` for Bedrock; or a gateway key header via `auth_scheme: apikey_header` (e.g. Kong `key-auth`) |

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

### Secret Overlay and Reuse Detection

At startup, the runtime overlays secret values from the configured provider chain into the process environment so downstream skill scripts and `cli_execute` invocations can read them via `os.Getenv`. The overlay set is the union of forge's builtin LLM/search/channel keys and whatever the provider enumerates via `List()` — so a skill declaring a custom env var name (e.g. `ACME_API_TOKEN`) flows through without any code change. See [Secret Management — Skill-Declared Secrets](../security/secret-management.md#skill-declared-secrets).

Before chain assembly, each encrypted-file candidate is eagerly validated. Files that don't exist are silently skipped; files that fail to decrypt (wrong passphrase, corruption) are dropped from the chain with a warning that names the file path. This prevents a stale global `~/.forge/secrets.enc` from poisoning `ChainProvider.Get` / `List` and hiding the agent-local file's keys. See [Provider Chain Validation](../security/secret-management.md#provider-chain-validation).

Once overlaid, the runtime also validates that secret values are not reused across different purpose categories. Sharing the same token between unrelated services (e.g., using an OpenAI API key as a Telegram bot token) is blocked with an error. Categories: `llm` (OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY, etc.), `search` (TAVILY_API_KEY, PERPLEXITY_API_KEY), `telegram` (TELEGRAM_BOT_TOKEN), `slack` (SLACK_APP_TOKEN, SLACK_BOT_TOKEN). Cross-category reuse detection is scoped to these builtin categories — custom skill-declared keys have no defined category and are not part of the check. Same-category reuse (e.g., two LLM keys with the same value) is allowed.

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

### Custom URL Endpoints

Custom URL endpoints (OpenRouter, vLLM, litellm, Together.ai, Anyscale, self-hosted Llama / Kimi proxies, Bedrock OpenAI compat front-ends, …) reuse the OpenAI or Anthropic provider with a base-URL override. The forge init wizard's "Custom URL" option asks which wire format the endpoint speaks and writes the matching provider into `forge.yaml` — the generated config never carries `provider: custom`.

| Wire format | Scaffolded as | Env vars emitted |
|---|---|---|
| OpenAI Chat Completions | `provider: openai` | `OPENAI_BASE_URL`, `OPENAI_API_KEY` |
| Anthropic Messages | `provider: anthropic` | `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY` |

Both shapes flow through the same client construction path; only the wire format and env var names differ. Issue #202 Phase 1.

### AWS Bedrock (SigV4 Outbound)

Bedrock uses AWS SigV4 signing instead of a static API key, so `model.auth_scheme: aws_sigv4` swaps the default `Authorization: Bearer …` / `x-api-key: …` header for a hand-rolled SigV4 signature over the outbound HTTP request (forge-core/llm/providers/sigv4_transport.go). The signer is symmetric across the `openai` and `anthropic` providers — pick whichever wire format the Bedrock endpoint speaks.

```yaml
model:
  provider: anthropic                            # or openai, for Bedrock's OpenAI compat
  name: anthropic.claude-sonnet-4-20250514-v1:0
  base_url: https://bedrock-runtime.us-east-1.amazonaws.com
  auth_scheme: aws_sigv4
  aws_region: us-east-1
```

Credentials come from the standard AWS env chain: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optional `AWS_SESSION_TOKEN`. The signer matches the inbound-auth posture (forge-core/auth/providers/aws_sigv4) — same stdlib-only crypto, no aws-sdk-go-v2 dependency. Issue #202 Phase 2.

Today this is a passthrough — Forge speaks the wire format the endpoint exposes and signs the request. Native Bedrock URL/body rewriting (`POST /model/<id>/invoke` with the event-stream framing) is tracked separately under issue #205; today operators front Bedrock with a compat proxy (e.g. litellm) when calling models that don't expose the OpenAI or Anthropic shape.

### API-gateway key header (`apikey_header`)

Some API gateways authenticate with a fixed header name rather than the provider-native scheme. Kong AI Gateway's `key-auth` plugin, for example, reads the consumer key from an `apikey` header and ignores `Authorization` / `x-api-key`, so an agent pointed at a Kong-fronted `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` otherwise fails every LLM call with `401 {"message":"No API key found in request"}`.

`model.auth_scheme: apikey_header` sends the API key in the gateway header **in addition to** the provider-native header — additive, so it stays safe against non-gateway endpoints (the gateway consumes `apikey`; Kong's ai-proxy replaces/injects the upstream provider header). The header name defaults to `apikey` (Kong `key-auth`'s default `key_names`) and is overridable via `auth_header_name` for gateways with custom key names:

```yaml
model:
  provider: openai                                 # or anthropic — symmetric
  name: gpt-4o
  base_url: https://kong-gateway.internal/openai
  auth_scheme: apikey_header
  # auth_header_name: x-gateway-key                # optional; default: apikey
```

The key comes from the usual `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` env var. Issue #302.

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
| `--auth-url` | — | External auth provider URL for token validation |

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

## External Authentication

When `--auth-url` is set (or `FORGE_AUTH_URL` env var), the runtime delegates token validation to an external auth provider. On each request, the bearer token is forwarded to the external URL for verification.

```bash
# Via CLI flag
forge run --auth-url https://auth.example.com/verify

# Via environment variable (useful in containers)
docker run -e FORGE_AUTH_URL=https://auth.example.com/verify my-agent
```

The middleware checks tokens in two layers: an internal token is accepted first (used by channel adapter loopback calls), then the external auth provider is consulted. This ensures channel adapters (Slack, Telegram) can reach the A2A server without needing a valid external token.

## KUBECONFIG Materialization

The runtime supports passing kubeconfig content directly via the `KUBECONFIG` environment variable. If `KUBECONFIG` contains inline YAML (detected by newlines or `apiVersion:` markers), the runtime automatically writes it to a file and updates `KUBECONFIG` to point to that file. This is useful for container deployments where mounting files is inconvenient:

```bash
docker run -e KUBECONFIG="$(cat ~/.kube/config)" my-agent
```

## File Output Directory

The runtime configures a `FilesDir` for tool-generated files (e.g., from `file_create`). This directory defaults to `<WorkDir>/.forge/files/` and is injected into the execution context so tools can write files that other tools can reference by path.

```
<WorkDir>/
  .forge/
    files/        ← file_create output (patches.yaml, reports, etc.)
    sessions/     ← conversation persistence
    memory/       ← long-term memory
```

The `FilesDir` is set via `LLMExecutorConfig.FilesDir` and made available to tools through `runtime.FilesDirFromContext(ctx)`. See [Tools — File Create](tools-and-builtins.md#file-create) for details.

## Conversation Memory

For details on session persistence, context window management, compaction, and long-term memory, see [Memory](memory-system.md).

## Context Compression

When `compression.enabled` is set, the runner wires reversible context compression (ctxzip) into the loop at three points: an `AfterToolExec` hook compresses large tool outputs once, before they enter memory (registered after the guardrail hooks, so it compresses redacted output); the LLM client is wrapped in a compressing decorator below the fallback chain (so retries and compactor summarization calls are covered too); and the `context_expand` retrieval tool is registered so the model can recover offloaded content by marker hash. A constant system-prompt directive teaches the model what `<<ctxzip:...>>` markers are — individual skills need no awareness. Compression output is deterministic across turns and never touches the system prompt or recent messages, keeping provider prompt caches warm; `compression.cache_hints` additionally injects the provider's native cache primitives (anthropic `cache_control` breakpoints, openai `prompt_cache_key`). See [Context Compression](context-compression.md).

## Hooks

The engine fires hooks at key points in the loop. See [Hooks](hooks.md) for details.

The runner registers five hook groups: logging, audit, progress, global guardrail hooks, and skill guardrail hooks. Global guardrails use the `GuardrailChecker` interface backed by the `github.com/initializ/guardrails` library — the `AfterToolExec` hook scans tool output for secrets and PII, redacting or blocking before results enter the LLM context. Guardrail config is loaded from `guardrails.json` (file mode) or MongoDB (DB mode). Skill guardrail hooks enforce domain-specific rules declared in `SKILL.md` — blocking commands, redacting output, intercepting capability enumeration probes, and replacing binary-enumerating responses. Skill guardrails are loaded from build artifacts or parsed directly from `SKILL.md` at runtime (no `forge build` required). See [Guardrails](../security/guardrails.md) for full details.

## Streaming

The LLM tool-calling loop runs non-streaming internally. `ExecuteStream` calls `Execute` and emits the final response on a channel. However, the **UI chat proxy** (`forge-ui/chat.go`) streams A2A SSE events to the browser in real-time — `status` events carry incremental text, `progress` events carry tool execution updates, and `result` events carry the final response. The frontend renders text and tool progress as each event arrives.
