# Web Dashboard

> Part of [Forge Documentation](../README.md)

Forge includes a local web dashboard for managing agents from the browser — no CLI needed after launch.

## Launch

```bash
# Launch the dashboard
forge ui

# Specify workspace and port
forge ui --dir /path/to/workspace --port 4200

# Launch without auto-opening browser
forge ui --no-open
```

Opens `http://localhost:4200` with a full-featured SPA for the complete agent lifecycle.

## Dashboard

The main view discovers all agents in the workspace directory and shows their status in real-time via SSE (Server-Sent Events).

| Feature | Description |
|---------|-------------|
| Agent discovery | Auto-scans workspace for `forge.yaml` files |
| Start / Stop | Start and stop agents with one click |
| Daemon processes | Agents run as background daemons via `forge serve` — they survive UI shutdown |
| Live status | Real-time state updates (stopped, starting, running, errored) |
| Passphrase unlock | Prompts for `FORGE_PASSPHRASE` when agents have encrypted secrets |
| Startup error display | Shows actual error messages (e.g., missing env vars) in the agent card when startup fails, extracted from `.forge/serve.log` |
| Auto-rescan | Detects new agents after creation |
| Unified management | All agents (UI-started or CLI-started) get identical Start/Stop controls |

### Agent Lifecycle

The UI manages agents as daemon processes using `forge serve start` / `forge serve stop` under the hood. This means:

- **Agents survive UI shutdown** — closing the dashboard does not kill running agents.
- **Restart detection** — restarting the UI auto-discovers running agents via `.forge/serve.json` and TCP probing.
- **PID liveness verification** — after `forge serve start` returns, the UI verifies the child process is still alive via PID probing and TCP port check. If the child crashed (e.g., missing env vars), the error is extracted from `.forge/serve.log` and displayed in the agent card.
- **Unified view** — agents started from the CLI (`forge serve start`) and agents started from the UI appear identically. There is no distinction between "UI-managed" and "CLI-managed" agents.

## Interactive Chat

Click any running agent to open a chat interface that streams responses via the A2A protocol.

| Feature | Description |
|---------|-------------|
| Streaming responses | Real-time token streaming with progress indicators |
| Markdown rendering | Code blocks, tables, lists rendered inline |
| Session history | Browse and resume previous conversations |
| Tool call visibility | See which tools the agent invokes during execution |

## Create Agent Wizard

A multi-step wizard (web equivalent of `forge init`) that walks through the full agent setup:

| Step | What it does |
|------|-------------|
| Name | Set agent name with live slug preview |
| Provider | Select LLM provider (OpenAI, Anthropic, Gemini, Ollama, Custom) with descriptions |
| Model & Auth | Pick from provider-specific model lists; OpenAI supports API key or browser OAuth login, plus optional Organization ID for enterprise accounts |
| Channels | Select Slack/Telegram with inline token collection |
| Tools | Select builtin tools; web_search shows Tavily vs Perplexity provider choice with API key input |
| Skills | Browse registry skills by category with inline required/optional env var collection |
| Fallback | Select backup LLM providers with API keys for automatic failover |
| Env & Security | Add extra env vars; set passphrase for AES-256-GCM secret encryption |
| Review | Summary of all selections before creation |

The wizard collects credentials inline at each step (matching the CLI TUI behavior) and supports all the same options: model selection, OAuth, web search providers, fallback chains, and encrypted secret storage.

## Config Editor

Edit `forge.yaml` for any agent with a Monaco-based YAML editor:

| Feature | Description |
|---------|-------------|
| Syntax highlighting | YAML language support with Monaco editor |
| Live validation | Validate config against the forge schema without saving |
| Save with validation | Server-side validation before writing to disk |
| Keyboard shortcut | Cmd/Ctrl+S to save |
| Restart integration | Restart agent after config changes |
| Fallback editor | Plain textarea if Monaco fails to load |

The Monaco editor is a tree-shaken YAML-only bundle (~615KB) built with esbuild — not the full 4MB distribution.

## Skills Browser

Browse the built-in skill registry with filtering and detail view:

| Feature | Description |
|---------|-------------|
| Grid view | Skill cards showing name, description, category, tags |
| Category filter | Filter skills by category |
| Detail panel | Click a skill to view its full SKILL.md content |
| Env requirements | Shows required, one-of, and optional env vars per skill |

## Skill Builder

An AI-powered conversational tool for creating custom skills. Access it via the **Build Skill** button on any agent card, or navigate to `#/skill-builder/{agent-id}`.

### How It Works

The Skill Builder uses the agent's own LLM provider to power a chat conversation that generates valid SKILL.md files and optional helper scripts. It automatically selects a stronger code-generation model when available (e.g. `gpt-4.1` for OpenAI, `claude-opus-4-6` for Anthropic). API key detection loads the agent's `.env` file and encrypted secrets (if unlocked) in addition to system environment variables.

### Features

| Feature | Description |
|---------|-------------|
| Conversational design | Describe what you want in plain language; the AI asks clarifying questions and generates the skill |
| Live streaming | LLM responses stream token-by-token via SSE |
| Artifact extraction | Automatically parses `skill.md` and `script:` code fences from the LLM response |
| SKILL.md preview | Live preview panel shows the generated SKILL.md with syntax highlighting |
| Script preview | View generated helper scripts alongside the SKILL.md |
| Validation | Server-side validation checks name format, required fields, egress domain declarations, and name uniqueness |
| One-click save | Save the validated skill directly to the agent's `skills/` directory |

### Workflow

1. **Open** the Skill Builder from an agent card
2. **Describe** the skill you want (e.g. "Create a skill that queries Jira issues")
3. **Iterate** — the AI asks about requirements, security constraints, and env vars
4. **Review** — inspect the generated SKILL.md and scripts in the preview panel
5. **Validate** — check for errors and warnings before saving
6. **Save** — writes `skills/{name}/SKILL.md` and `skills/{name}/scripts/` to the agent directory

### Validation Rules

The validator enforces the [SKILL.md format](skills.md):

| Check | Level | Description |
|-------|-------|-------------|
| Name present | Error | `name` is required in frontmatter |
| Name format | Error | Must be lowercase kebab-case, max 64 characters |
| Description present | Error | `description` is required in frontmatter |
| YAML parse | Error | Frontmatter must be valid YAML |
| Tool sections | Warning | Body should contain `## Tool:` sections |
| Category format | Warning | `category` should be lowercase kebab-case |
| Egress domains | Warning | Scripts referencing HTTP(S) URLs should declare them in `egress_domains` |
| Name uniqueness | Warning | Warns if a skill with the same name already exists in the agent |

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/agents/{id}/skill-builder/provider` | Returns the agent's LLM provider, codegen model, and API key status |
| `GET` | `/api/agents/{id}/skill-builder/context` | Returns the system prompt used for skill generation |
| `POST` | `/api/agents/{id}/skill-builder/chat` | Streams an LLM conversation via SSE (accepts `messages` array) |
| `POST` | `/api/agents/{id}/skill-builder/validate` | Validates a SKILL.md and optional scripts |
| `POST` | `/api/agents/{id}/skill-builder/save` | Saves a validated skill to `skills/{name}/` |

## Architecture

The dashboard is a single Go module (`forge-ui`) embedded into the `forge` binary:

```
forge-cli/cmd/ui.go               CLI command, injects ExePath/CreateFunc/OAuthFunc/LLMStreamFunc
forge-ui/
  server.go                        HTTP server with CORS, SPA fallback
  handlers.go                      Dashboard API (agents, start/stop, chat, sessions)
  handlers_create.go               Wizard API (create, config, skills, tools, OAuth)
  handlers_skill_builder.go        Skill Builder API (chat, validate, save, provider)
  skill_builder_context.go         System prompt for the Skill Designer AI
  skill_validator.go               SKILL.md validation and artifact extraction
  process.go                       Process manager (exec forge serve start/stop)
  discovery.go                     Workspace scanner (finds forge.yaml + detects running daemons)
  sse.go                           Server-Sent Events broker
  chat.go                          A2A chat proxy with streaming
  types.go                         Shared types
  static/dist/                     Embedded frontend (Preact + HTM, no build step)
    app.js                         SPA with hash routing
    style.css                      Dark theme styles
    monaco/                        Tree-shaken YAML editor
```

Key design decisions:

- **`forge-cli` imports `forge-ui`** (not vice versa). CLI-specific logic (scaffold, config loading, OAuth flow) is injected via function callbacks, keeping `forge-ui` framework-agnostic.
- **Daemon-based lifecycle** — the UI delegates to `forge serve start/stop` via `exec.Command`, so agents are independent OS processes that survive UI restarts.
- **Scanner as source of truth** — `discovery.go` reads `.forge/serve.json` and does a TCP probe to detect running agents. No in-memory state tracking is needed.
- **Version display** — the sidebar footer shows the Forge version (injected via `-ldflags` at build time) and links to [useforge.ai](https://useforge.ai).

---
← [Configuration](configuration.md) | [Back to README](../README.md) | [Deployment](deployment.md) →
