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
| Live status | Real-time state updates (stopped, starting, running, errored) |
| Passphrase unlock | Prompts for `FORGE_PASSPHRASE` when agents have encrypted secrets |
| Auto-rescan | Detects new agents after creation |

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
| Model & Auth | Pick from provider-specific model lists; OpenAI supports API key or browser OAuth login |
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

## Architecture

The dashboard is a single Go module (`forge-ui`) embedded into the `forge` binary:

```
forge-cli/cmd/ui.go          CLI command, injects StartFunc/CreateFunc/OAuthFunc
forge-ui/
  server.go                   HTTP server with CORS, SPA fallback
  handlers.go                 Dashboard API (agents, start/stop, chat, sessions)
  handlers_create.go          Wizard API (create, config, skills, tools, OAuth)
  process.go                  Process manager (start/stop agent goroutines)
  discovery.go                Workspace scanner (finds forge.yaml files)
  sse.go                      Server-Sent Events broker
  chat.go                     A2A chat proxy with streaming
  types.go                    Shared types
  static/dist/                Embedded frontend (Preact + HTM, no build step)
    app.js                    SPA with hash routing
    style.css                 Dark theme styles
    monaco/                   Tree-shaken YAML editor
```

Key design: `forge-cli` imports `forge-ui` (not vice versa). CLI-specific logic (scaffold, config loading, OAuth flow) is injected via function callbacks, keeping `forge-ui` framework-agnostic.

---
← [Configuration](configuration.md) | [Back to README](../README.md) | [Deployment](deployment.md) →
