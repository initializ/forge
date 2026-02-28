# Forge

Turn a `SKILL.md` into a portable, secure, runnable AI agent.

Forge is a portable runtime for building and running secure AI agents from simple skill definitions. It takes Agent Skills and makes them:

* A runnable AI agent with tool calling
* A portable, containerized bundle
* A local HTTP / A2A service
* A Slack or Telegram bot
* A secure, restricted execution environment

No Docker required. No inbound tunnels required. No cloud lock-in.

---

## Why Forge?

**Instant Agent From a Single Command**

Write a SKILL.md. Run `forge init`. Your agent is live.

The wizard configures your model provider, validates your API key,
connects Slack or Telegram, picks skills, and starts your agent.
Zero to running in under 5 minutes.

**Secure by Default**

Forge is designed for safe execution:

* Does NOT create public tunnels
* Does NOT expose webhooks automatically
* Uses outbound-only connections (Slack Socket Mode, Telegram polling)
* Enforces outbound domain allowlists at both build-time and runtime, including subprocess HTTP via a local egress proxy
* Encrypts secrets at rest (AES-256-GCM) with per-agent isolation
* Signs build artifacts (Ed25519) for supply chain integrity
* Supports restricted network profiles with audit logging

No accidental exposure. No hidden listeners.

---

## Get Started in 60 Seconds

```bash
# Install
curl -sSL https://github.com/initializ/forge/releases/latest/download/forge-$(uname -s)-$(uname -m).tar.gz | tar xz
sudo mv forge /usr/local/bin/

# Initialize a new agent (interactive wizard)
forge init my-agent

# Run locally
cd my-agent && forge run

# Run with Telegram
forge run --with telegram
```

The `forge init` wizard walks you through model provider, API key, fallback providers, tools, skills, and channel setup. Use `--non-interactive` with flags for scripted setups.

---

## Install

### macOS (Homebrew)
```bash
brew install initializ/tap/forge
```

### Linux / macOS (binary)
```bash
curl -sSL https://github.com/initializ/forge/releases/latest/download/forge-$(uname -s)-$(uname -m).tar.gz | tar xz
sudo mv forge /usr/local/bin/
```

### Windows

Download the latest `.zip` from [GitHub Releases](https://github.com/initializ/forge/releases/latest) and add to your PATH.

### Verify
```bash
forge --version
```

---

## How It Works

```
SKILL.md --> Parse --> Discover tools/requirements --> Compile AgentSpec
                                                            |
                                                            v
                                                    Apply security policy
                                                            |
                                                            v
                                                    Run LLM agent loop
                                                   (tool calling + memory)
```

1. You write a `SKILL.md` that describes what the agent can do
2. Forge parses the skill definitions and optional YAML frontmatter (binary deps, env vars)
3. The build pipeline discovers tools, resolves egress domains, and compiles an `AgentSpec`
4. Security policies (egress allowlists, capability bundles) are applied
5. Build artifacts are checksummed and optionally signed (Ed25519)
6. At runtime, encrypted secrets are decrypted and the LLM-powered tool-calling loop executes with session persistence and memory

---

## Skills

Skills are defined in Markdown with optional YAML frontmatter for requirements:

```markdown
---
name: weather
description: Weather data skill
metadata:
  forge:
    requires:
      bins:
        - curl
      env:
        required: []
        one_of: []
        optional: []
---
## Tool: weather_current

Get current weather for a location.

**Input:** location (string) - City name or coordinates
**Output:** Current temperature, conditions, humidity, and wind speed

## Tool: weather_forecast

Get weather forecast for a location.

**Input:** location (string), days (integer: 1-7)
**Output:** Daily forecast with high/low temperatures and conditions
```

Each `## Tool:` heading defines a tool the agent can call. The frontmatter declares binary dependencies and environment variable requirements. Skills compile into JSON artifacts and prompt text during `forge build`.

### Skill Registry

Forge ships with a built-in skill registry. Add skills to your project with a single command:

```bash
# Add a skill from the registry
forge skills add tavily-research

# Validate skill requirements
forge skills validate

# Audit skill security
forge skills audit --embedded
```

`forge skills add` copies the skill's SKILL.md and any associated scripts into your project's `skills/` directory. It validates binary and environment requirements, checks for existing values in your environment, `.env` file, and encrypted secrets, and prompts only for truly missing values with a suggestion to use `forge secrets set` for sensitive keys.

### Skills as First-Class Tools

Script-backed skills are automatically registered as **first-class LLM tools** at runtime. When a skill has scripts in `skills/scripts/`, Forge:

1. Parses the skill's SKILL.md for tool definitions, descriptions, and input schemas
2. Creates a named tool for each `## Tool:` entry (e.g., `tavily_research` becomes a tool the LLM can call directly)
3. Executes the skill's shell script with JSON input when the LLM invokes it

This means the LLM sees skill tools alongside builtins like `web_search` and `http_request` — no generic `cli_execute` indirection needed.

For skills **without** scripts (binary-backed skills like `github`), Forge injects a lightweight catalog into the system prompt. The LLM can then use the `read_skill` builtin tool to load full instructions on demand, and invoke the skill via `cli_execute`.

```
┌─────────────────────────────────────────────┐
│              LLM Tool Registry              │
├─────────────────┬───────────────────────────┤
│  Builtins       │  web_search, http_request │
│  Skill Tools    │  tavily_research, ...     │  ← auto-registered from scripts
│  read_skill     │  lazy-load any SKILL.md   │
│  cli_execute    │  run approved binaries    │
└─────────────────┴───────────────────────────┘
```

### Skill Execution Security

Skill scripts run in a restricted environment via `SkillCommandExecutor`:

- **Isolated environment**: Only `PATH`, `HOME`, and explicitly declared env vars are passed through
- **Configurable timeout**: Each skill declares a `timeout_hint` in its YAML frontmatter (e.g., 300s for research)
- **No shell execution**: Scripts run via `bash <script> <json-input>`, not through a shell interpreter
- **Egress proxy enforcement**: When egress mode is `allowlist` or `deny-all`, a local HTTP/HTTPS proxy is started and `HTTP_PROXY`/`HTTPS_PROXY` env vars are injected into subprocess environments, ensuring `curl`, `wget`, Python `requests`, and other HTTP clients route through the same domain allowlist used by in-process tools (see [Subprocess Egress Proxy](#subprocess-egress-proxy) below)

### Built-in Skills

| Skill | Description | Scripts |
|-------|-------------|---------|
| `tavily-research` | Deep multi-source research via Tavily API | `tavily-research.sh`, `tavily-research-poll.sh` |

### Tavily Research Skill

The `tavily-research` skill demonstrates the **async two-tool pattern** for long-running operations:

```bash
forge skills add tavily-research
```

This registers two tools:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `tavily_research` | Submit a research query | Returns immediately with a `request_id` |
| `tavily_research_poll` | Wait for results | Polls internally for up to ~5 minutes, returns complete report |

The LLM uses them in sequence: submit the research request, inform the user that research is in progress, then call the poll tool which handles all waiting internally. The complete report (1000-3000 words with sources) is returned to the LLM and delivered to the user.

**Research models:**

| Model | Speed | Use Case |
|-------|-------|----------|
| `mini` | ~30s | Quick overviews, simple topics |
| `pro` | ~300s | Comprehensive analysis, complex topics |
| `auto` | Varies | Let the API choose based on query complexity |

Requires: `curl`, `jq`, `TAVILY_API_KEY` environment variable.

---

## Tools

Forge ships with built-in tools, adapter tools, and supports custom tools:

### Built-in Tools

| Tool | Description |
|------|-------------|
| `http_request` | Make HTTP requests (GET, POST, PUT, DELETE) |
| `json_parse` | Parse and query JSON data |
| `csv_parse` | Parse CSV data into structured records |
| `datetime_now` | Get current date and time |
| `uuid_generate` | Generate UUID v4 identifiers |
| `math_calculate` | Evaluate mathematical expressions |
| `web_search` | Search the web for quick lookups and recent information |
| `read_skill` | Load full instructions for an available skill on demand |
| `memory_search` | Search long-term memory (when enabled) |
| `memory_get` | Read memory files (when enabled) |
| `cli_execute` | Execute pre-approved CLI binaries |

### Adapter Tools

| Adapter | Description |
|---------|-------------|
| `mcp_call` | Call tools on MCP servers via JSON-RPC |
| `webhook_call` | POST JSON payloads to webhook URLs |
| `openapi_call` | Call OpenAPI-described endpoints |

### Web Search Providers

The `web_search` tool supports two providers:

| Provider | API Key Env Var | Endpoint |
|----------|----------------|----------|
| Tavily (recommended) | `TAVILY_API_KEY` | `api.tavily.com/search` |
| Perplexity | `PERPLEXITY_API_KEY` | `api.perplexity.ai/chat/completions` |

Provider selection: `WEB_SEARCH_PROVIDER` env var, or auto-detect from available API keys (Tavily first).

### CLI Execute

The `cli_execute` tool provides security-hardened command execution with 7 security layers:

```yaml
tools:
  - name: cli_execute
    config:
      allowed_binaries: ["git", "curl", "jq", "python3"]
      env_passthrough: ["GITHUB_TOKEN"]
      timeout: 120
      max_output_bytes: 1048576
```

Only allowlisted binaries can run. No shell execution. Arguments are validated against injection patterns. Environment is isolated to `PATH`, `HOME`, `LANG` plus explicit passthrough vars.

### Tool Commands

```bash
# List all registered tools
forge tool list

# Show details for a specific tool
forge tool describe web_search
```

Custom tools can be added by placing scripts in a `tools/` directory in your project.

---

## LLM Providers

Forge supports multiple LLM providers with automatic fallback:

| Provider | Default Model | Auth |
|----------|--------------|------|
| `openai` | `gpt-5.2-2025-12-11` | API key or OAuth |
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

Provider is auto-detected from available API keys if not explicitly set.

### OpenAI OAuth

For OpenAI, Forge supports browser-based OAuth login (matching the Codex CLI flow) as an alternative to API keys:

```bash
forge init my-agent
# Select "OpenAI" -> "Login with browser (OAuth)"
# Browser opens for authentication
```

OAuth tokens are stored in `~/.forge/credentials/openai.json` and automatically refreshed.

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

---

## Channel Connectors

Forge connects agents to messaging platforms via channel adapters. Both use **outbound-only connections** — no public URLs, no ngrok, no inbound webhooks.

| Channel | Mode | How It Works |
|---------|------|-------------|
| Slack | Socket Mode | Outbound WebSocket via `apps.connections.open` |
| Telegram | Polling (default) | Long-polling via `getUpdates`, no public URL needed |

```bash
# Add Slack adapter to your project
forge channel add slack

# Run agent with Slack connected
forge run --with slack

# Run with multiple channels
forge run --with slack,telegram
```

Channels can also run standalone as separate services:

```bash
export AGENT_URL=http://localhost:8080
forge channel serve slack
```

### Large Response Handling

When an agent response exceeds 4096 characters (common with research reports), channel adapters automatically split it into a **summary message** and a **file attachment**:

1. A brief summary (first paragraph, up to 600 characters) is sent as a regular message
2. The full report is uploaded as a downloadable Markdown file (`research-report.md`)

This works on both Slack (via `files.upload`) and Telegram (via `sendDocument`). If file upload fails, adapters fall back to chunked messages. Markdown is converted to platform-native formatting (Slack mrkdwn or Telegram HTML).

---

## Security

Forge provides layered security controls at both build-time and runtime.

### Runtime Egress Enforcement

Every outbound HTTP request from tools passes through an `EgressEnforcer` — an `http.RoundTripper` that validates the target domain against the resolved allowlist before forwarding the request.

| Mode | Behavior |
|------|----------|
| `deny-all` | All non-localhost outbound traffic blocked |
| `allowlist` | Only explicitly allowed domains (exact + wildcard) |
| `dev-open` | All traffic allowed (development only) |

Key behaviors:
- **Localhost always allowed** (`127.0.0.1`, `::1`, `localhost`) in all modes
- **Wildcard domains** supported (e.g., `*.github.com` matches `api.github.com`)
- **Tool domains auto-inferred** — declaring `web_search` in tools automatically allows `api.tavily.com` and `api.perplexity.ai`
- **Capability bundles** — declaring `slack` capability adds `slack.com`, `hooks.slack.com`, `api.slack.com`
- Blocked requests return: `egress blocked: domain "X" not in allowlist (mode=allowlist)`

### Subprocess Egress Proxy

The `EgressEnforcer` only works for in-process Go `http.Client` calls. Skill scripts and `cli_execute` subprocesses bypass it because they use external tools like `curl` or `wget`. To close this gap, Forge starts a **local HTTP/HTTPS forward proxy** that validates domains against the same allowlist:

```
┌─────────────────────────────────────────────────────┐
│                   forge run                         │
│                                                     │
│  In-process HTTP ──→ EgressEnforcer (RoundTripper)  │
│                                                     │
│  Subprocesses ──→ HTTP_PROXY ──→ EgressProxy        │
│  (curl, wget,       127.0.0.1:<port>  (validates    │
│   python, etc.)                        domains)     │
└─────────────────────────────────────────────────────┘
```

Key properties:

- **Local-only**: Binds to `127.0.0.1:0` (random port), never exposed externally
- **Per-instance**: Each `forge run` gets its own proxy on a different random port
- **HTTPS CONNECT support**: Validates the destination hostname from the CONNECT line, then blind-relays bytes (no MITM, no custom CA certs needed)
- **Transparent**: Both uppercase (`HTTP_PROXY`, `HTTPS_PROXY`) and lowercase (`http_proxy`, `https_proxy`) env vars are set to cover all common HTTP clients
- **Container-aware**: Skipped when running inside Docker/Kubernetes (detected via `KUBERNETES_SERVICE_HOST` env var or `/.dockerenv`), where `NetworkPolicy` handles egress enforcement instead
- **Mode-aware**: Skipped in `dev-open` mode (no restrictions needed)
- **Audit logged**: Proxy decisions emit the same `egress_allowed`/`egress_blocked` audit events as the in-process enforcer, with `"source": "proxy"` for distinction

The proxy appears in the startup banner when active:

```
  Egress:     strict / allowlist
  Proxy:      http://127.0.0.1:54321
```

### Egress Profiles

| Profile | Description | Default Mode |
|---------|-------------|-------------|
| `strict` | Maximum restriction, deny by default | `deny-all` |
| `standard` | Balanced, allow known domains | `allowlist` |
| `permissive` | Minimal restriction for development | `dev-open` |

### Configuration

```yaml
egress:
  profile: standard
  mode: allowlist
  allowed_domains:
    - api.example.com
    - "*.github.com"
  capabilities:
    - slack
```

### Audit Logging

All runtime events are emitted as structured NDJSON to stderr with correlation IDs for end-to-end tracing:

```json
{"ts":"2026-02-26T10:00:00Z","event":"session_start","correlation_id":"a1b2c3d4","task_id":"task-1"}
{"ts":"2026-02-26T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"tool":"http_request","phase":"start"}}
{"ts":"2026-02-26T10:00:01Z","event":"egress_allowed","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"domain":"api.openai.com","mode":"allowlist"}}
{"ts":"2026-02-26T10:00:01Z","event":"tool_exec","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"tool":"http_request","phase":"end"}}
{"ts":"2026-02-26T10:00:02Z","event":"llm_call","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"tokens":493}}
{"ts":"2026-02-26T10:00:02Z","event":"session_end","correlation_id":"a1b2c3d4","task_id":"task-1","fields":{"state":"completed"}}
```

Event types: `session_start`, `session_end`, `tool_exec`, `egress_allowed`, `egress_blocked`, `llm_call`, `guardrail_check`.

### Build-Time Security

Every `forge build` produces:
- `egress_allowlist.json` — machine-readable domain allowlist
- Kubernetes `NetworkPolicy` manifest — restricts pod egress to allowed domains on ports 80/443

```bash
# Production build rejects dev tools and dev-open egress
forge package --prod
```

### Guardrails

The guardrail engine checks inbound and outbound messages against policy rules:

| Guardrail | Description |
|-----------|-------------|
| `content_filter` | Blocks messages containing configured blocked words |
| `no_pii` | Detects email addresses, phone numbers, and SSNs via regex |
| `jailbreak_protection` | Detects common jailbreak phrases ("ignore previous instructions", etc.) |

Guardrails run in `enforce` mode (blocking) or `warn` mode (logging only), configured via the policy scaffold.

---

## Secrets

Forge provides encrypted secret management with per-agent isolation and interactive passphrase prompting.

### Encrypted Storage

Secrets are stored in AES-256-GCM encrypted files with Argon2id key derivation. The file format is `salt(16) || nonce(12) || ciphertext`, with the plaintext being a JSON key-value map.

```bash
# Store a secret (prompts for value securely)
forge secret set OPENAI_API_KEY

# Store with inline value
forge secret set SLACK_BOT_TOKEN xoxb-...

# Retrieve a secret (shows source: encrypted-file or env)
forge secret get OPENAI_API_KEY

# List all secret keys
forge secret list

# Delete a secret
forge secret delete OLD_KEY
```

### Per-Agent Secrets

Each agent can have its own encrypted secrets file at `<agent-dir>/.forge/secrets.enc`, separate from the global `~/.forge/secrets.enc`. Use the `--local` flag to operate on agent-local secrets:

```bash
cd my-agent

# Store a secret in the agent-local file
forge secret set OPENAI_API_KEY sk-agent1-key --local

# Different agent, different key
cd ../other-agent
forge secret set OPENAI_API_KEY sk-agent2-key --local
```

At runtime, secrets are resolved in order: **agent-local** → **global** → **environment variables**. This lets you override global defaults per agent.

### Runtime Passphrase Prompting

When `forge run` encounters encrypted secrets and no `FORGE_PASSPHRASE` environment variable is set, it prompts interactively:

```
$ forge run
Enter passphrase for encrypted secrets: ****
```

In non-interactive environments (CI/CD), set the passphrase via environment variable:

```bash
export FORGE_PASSPHRASE="my-passphrase"
forge run
```

### Smart Init Passphrase

`forge init` detects whether `~/.forge/secrets.enc` already exists:

- **First time**: prompts for passphrase + confirmation (new setup)
- **Subsequent**: prompts once and validates by attempting to decrypt the existing file

### Configuration

```yaml
secrets:
  providers:
    - encrypted-file          # AES-256-GCM encrypted file
    - env                     # Environment variables (fallback)
```

Secret files are automatically excluded from git (`.forge/` in `.gitignore`) and Docker builds (`*.enc` in `.dockerignore`).

---

## Build Signing & Verification

Forge supports Ed25519 signing of build artifacts for supply chain integrity.

### Key Management

```bash
# Generate an Ed25519 signing keypair
forge key generate
# Output: ~/.forge/signing-key.pem (private) + ~/.forge/signing-key.pub (public)

# Generate with a custom name
forge key generate --name ci-key

# Add a public key to the trusted keyring
forge key trust ~/.forge/signing-key.pub

# List signing and trusted keys
forge key list
```

### Build Signing

When a signing key exists at `~/.forge/signing-key.pem` (or specified via `--signing-key`), `forge build` automatically:

1. Computes SHA-256 checksums of all generated artifacts
2. Signs the checksums with the Ed25519 private key
3. Writes `checksums.json` with checksums, signature, and key ID

### Runtime Verification

At runtime, `forge run` can verify build artifacts against `checksums.json`:

- Validates SHA-256 checksums of all files
- Verifies the Ed25519 signature against trusted keys in `~/.forge/trusted-keys/`
- Verification is optional — if `checksums.json` doesn't exist, it's skipped

### Secret Safety Stage

The build pipeline includes a `secret-safety` stage that:

- Blocks production builds (`--prod`) that only use `encrypted-file` without `env` provider (containers can't use encrypted files at runtime)
- Warns if `.dockerignore` is missing alongside a generated Dockerfile
- Ensures secrets never leak into container images

---

## Memory

Forge provides two layers of memory management:

### Session Persistence

Sessions are automatically persisted to disk across requests, enabling multi-turn conversations:

```yaml
memory:
  persistence: true          # default: true
  sessions_dir: ".forge/sessions"
```

- Sessions are saved as JSON files with atomic writes (temp file + fsync + rename)
- Automatic cleanup of sessions older than 7 days at startup
- Session recovery on subsequent requests (disk snapshot supersedes task history)

### Context Window Management

Forge automatically manages context window usage based on model capabilities:

| Model | Context Window | Character Budget |
|-------|---------------|-----------------|
| `gpt-4o` / `gpt-5` | 128K tokens | ~435K chars |
| `claude-sonnet` / `claude-opus` | 200K tokens | ~680K chars |
| `gemini-2.5` | 1M tokens | ~3.4M chars |
| `llama3` | 8K tokens | ~27K chars |
| `llama3.1` | 128K tokens | ~435K chars |

When context grows too large, the **Compactor** automatically:
1. Takes the oldest 50% of messages
2. Flushes tool results and decisions to long-term memory (if enabled)
3. Summarizes via LLM (with extractive fallback)
4. Replaces old messages with the summary

Research tool results receive special handling during compaction: they are preserved with a higher extraction limit (5000 vs 2000 characters) and tagged distinctly in long-term memory logs (e.g., `[research][tool:tavily_research]`) so research insights persist across sessions.

```yaml
memory:
  char_budget: 200000       # override auto-detection
  trigger_ratio: 0.6        # compact at 60% of budget (default)
```

### Long-Term Memory

Enable cross-session knowledge persistence with hybrid vector + keyword search:

```yaml
memory:
  long_term: true
  memory_dir: ".forge/memory"
  vector_weight: 0.7
  keyword_weight: 0.3
  decay_half_life_days: 7
```

Or via environment variable:

```bash
export FORGE_MEMORY_LONG_TERM=true
```

When enabled, Forge:
- Creates a `.forge/memory/` directory with a `MEMORY.md` template for curated facts
- Indexes all `.md` files into a hybrid search index (vector similarity + keyword overlap + temporal decay)
- Registers `memory_search` and `memory_get` tools for the agent to use
- Automatically flushes compacted conversation context to daily log files (`YYYY-MM-DD.md`)

**Embedding providers** for vector search:

| Provider | Default Model | Notes |
|----------|--------------|-------|
| `openai` | `text-embedding-3-small` | Standard OpenAI embeddings API |
| `gemini` | `text-embedding-3-small` | OpenAI-compatible endpoint |
| `ollama` | `nomic-embed-text` | Local embeddings |

Falls back to keyword-only search if no embedding provider is available (e.g., when using Anthropic as the primary provider without a fallback).

---

## Hooks

The agent loop fires hooks at key points, enabling observability and custom behavior:

| Hook Point | When | Available Data |
|------------|------|---------------|
| `BeforeLLMCall` | Before each LLM API call | Messages, TaskID, CorrelationID |
| `AfterLLMCall` | After each LLM API call | Messages, Response, TaskID, CorrelationID |
| `BeforeToolExec` | Before each tool execution | ToolName, ToolInput, TaskID, CorrelationID |
| `AfterToolExec` | After each tool execution | ToolName, ToolInput, ToolOutput, Error, TaskID, CorrelationID |
| `OnError` | On LLM call errors | Error, TaskID, CorrelationID |
| `OnProgress` | During tool execution | Phase, ToolName, StatusMessage |

Hooks fire in registration order. If any hook returns an error, execution stops (useful for security enforcement).

### Progress Tracking

The runner automatically registers progress hooks that emit real-time status updates during tool execution. Progress events include the tool name, phase (`tool_start` / `tool_end`), and a human-readable status message. These events are streamed to clients via SSE when using the A2A HTTP server, enabling live progress indicators in web and chat UIs.

---

## Running Modes

| | `forge run` | `forge serve` |
|---|------------|--------------|
| **Purpose** | Development | Production service |
| **Channels** | `--with slack,telegram` | Reads from `forge.yaml` |
| **Sessions** | Single session | Multi-session with TTL |
| **Logging** | Human-readable | JSON structured logs |
| **Lifecycle** | Interactive | PID file, graceful shutdown |

```bash
# Development
forge run --with slack --port 8080

# Production
forge serve --port 8080 --session-ttl 30m
```

---

## Packaging & Deployment

```bash
# Build a container image (auto-detects Docker/Podman/Buildah)
forge package

# Production build (rejects dev tools and dev-open egress)
forge package --prod

# Build and push to registry
forge package --registry ghcr.io/myorg --push

# Generate docker-compose with channel sidecars
forge package --with-channels

# Export for Initializ Command platform
forge export --pretty --include-schemas
```

`forge package` generates a Dockerfile, Kubernetes manifests, and NetworkPolicy. Use `--prod` to strip dev tools and enforce strict egress. Use `--verify` to smoke-test the built container.

---

## Configuration Reference

Complete `forge.yaml` schema:

```yaml
agent_id: "my-agent"                # Required
version: "1.0.0"                    # Required
framework: "forge"                  # forge (default), crewai, langchain
registry: "ghcr.io/org"             # Container registry
entrypoint: "agent.py"              # Required for crewai/langchain, omit for forge

model:
  provider: "openai"                # openai, anthropic, gemini, ollama, custom
  name: "gpt-4o"                    # Model name
  fallbacks:                        # Fallback providers (optional)
    - provider: "anthropic"
      name: "claude-sonnet-4-20250514"

tools:
  - name: "web_search"
  - name: "cli_execute"
    config:
      allowed_binaries: ["git", "curl"]
      env_passthrough: ["GITHUB_TOKEN"]

channels:
  - "telegram"
  - "slack"

egress:
  profile: "strict"                 # strict, standard, permissive
  mode: "allowlist"                 # deny-all, allowlist, dev-open
  allowed_domains:                  # Explicit domains
    - "api.example.com"
    - "*.github.com"
  capabilities:                     # Capability bundles
    - "slack"

skills:
  path: "SKILL.md"

secrets:
  providers:                        # Secret providers (order matters)
    - "encrypted-file"              # AES-256-GCM encrypted file
    - "env"                         # Environment variables

memory:
  persistence: true                 # Session persistence (default: true)
  sessions_dir: ".forge/sessions"
  char_budget: 200000               # Context budget override
  trigger_ratio: 0.6                # Compaction trigger ratio
  long_term: false                  # Long-term memory (default: false)
  memory_dir: ".forge/memory"
  embedding_provider: ""            # Auto-detect from LLM provider
  embedding_model: ""               # Provider default
  vector_weight: 0.7                # Hybrid search vector weight
  keyword_weight: 0.3               # Hybrid search keyword weight
  decay_half_life_days: 7           # Temporal decay half-life
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `FORGE_MODEL_PROVIDER` | Override LLM provider |
| `FORGE_MODEL_FALLBACKS` | Fallback chain (e.g., `"anthropic:claude-sonnet-4,gemini"`) |
| `FORGE_MEMORY_PERSISTENCE` | Set `false` to disable session persistence |
| `FORGE_MEMORY_LONG_TERM` | Set `true` to enable long-term memory |
| `FORGE_EMBEDDING_PROVIDER` | Override embedding provider |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Google Gemini API key |
| `TAVILY_API_KEY` | Tavily web search API key |
| `PERPLEXITY_API_KEY` | Perplexity web search API key |
| `WEB_SEARCH_PROVIDER` | Force web search provider (`tavily` or `perplexity`) |
| `OPENAI_BASE_URL` | Override OpenAI base URL |
| `ANTHROPIC_BASE_URL` | Override Anthropic base URL |
| `OLLAMA_BASE_URL` | Override Ollama base URL (default: `http://localhost:11434`) |
| `FORGE_PASSPHRASE` | Passphrase for encrypted secrets file |

---

## Command Reference

| Command | Description |
|---------|-------------|
| `forge init [name]` | Initialize a new agent project (interactive wizard) |
| `forge build` | Compile agent artifacts (AgentSpec, egress allowlist, skills) |
| `forge validate [--strict] [--command-compat]` | Validate agent spec and forge.yaml |
| `forge run [--with slack,telegram] [--port 8080]` | Run agent locally with A2A dev server |
| `forge serve [--port 8080] [--session-ttl 30m]` | Run as production service |
| `forge package [--push] [--prod] [--registry] [--with-channels]` | Build container image |
| `forge export [--pretty] [--include-schemas] [--simulate-import]` | Export for Command platform |
| `forge tool list\|describe` | List or inspect registered tools |
| `forge skills add\|validate\|audit\|sign\|keygen` | Manage agent skills |
| `forge channel add\|serve\|list\|status` | Manage channel adapters |
| `forge secret set\|get\|list\|delete [--local]` | Manage encrypted secrets |
| `forge key generate\|trust\|list` | Manage Ed25519 signing keys |

See [docs/commands.md](docs/commands.md) for full flags and examples.

---

## Architecture

```
forge/
  forge-core/          Core library
    a2a/               A2A protocol types
    llm/               LLM client, fallback chains, OAuth
    memory/            Long-term memory (vector + keyword search)
    runtime/           Agent loop, hooks, compactor, audit logger
    secrets/           Encrypted secret storage (AES-256-GCM + Argon2id)
    security/          Egress resolver, enforcer, proxy, K8s NetworkPolicy
    tools/             Tool registry, builtins, adapters, skill_tool
    types/             Config types
  forge-cli/           CLI application
    cmd/               CLI commands (init, build, run, skills, etc.)
    runtime/           Runner, skill registration, subprocess executor
    internal/tui/      Interactive init wizard (Bubbletea)
    tools/             CLI-specific tools (cli_execute, skill executor)
  forge-plugins/       Channel plugins
    telegram/          Telegram adapter (polling, document upload)
    slack/             Slack adapter (Socket Mode, file upload)
    markdown/          Markdown converter, message splitting
  forge-skills/        Skill system
    contract/          Skill types, registry interface
    local/             Embedded + local skill registries
    requirements/      Requirement aggregation and derivation
    analyzer/          Security audit for skills
    resolver/          Binary and env var resolution
    trust/             Skill signing and verification
```

---

## Philosophy

Running agents that do real work requires more than prompts.

It requires:

### Atomicity

Agents must be packaged as clear, self-contained units:

* Explicit skills
* Defined tools
* Declared dependencies
* Deterministic behavior

No hidden state. No invisible glue code.

### Security

Agents must run safely:

* Restricted outbound access with runtime enforcement
* Explicit capability bundles
* No automatic inbound exposure
* Structured audit trails for every action
* Transparent execution boundaries

If an agent can touch the outside world, it must declare how.

### Portability

Agents should not be locked to a framework, a cloud, or a vendor.

A Forge agent:

- Runs locally
- Runs in containers
- Runs in Kubernetes
- Runs in cloud
- Runs inside **initializ**
- Speaks A2A

*Same agent. Anywhere.*

**Forge is built on a simple belief:**

> Real agent systems require atomicity, security, and portability.

Forge provides those building blocks.

---

## Documentation

- [Architecture](docs/architecture.md) — System design and data flows
- [Commands](docs/commands.md) — CLI reference with all flags and examples
- [Runtime](docs/runtime.md) — LLM agent loop, providers, and memory
- [Tools](docs/tools.md) — Tool system: builtins, adapters, custom tools
- [Skills](docs/skills.md) — Skills definition and compilation
- [Security](docs/security/SECURITY.md) — Complete security architecture
- [Egress Security](docs/security/egress.md) — Egress enforcement deep dive
- [Hooks](docs/hooks.md) — Agent loop hook system
- [Plugins](docs/plugins.md) — Framework plugin system
- [Channels](docs/channels.md) — Channel adapter architecture
- [Contributing](docs/contributing.md) — Development guide and PR process

## License

See [LICENSE](LICENSE) for details.
