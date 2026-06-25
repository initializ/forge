---
title: "Web Dashboard"
description: "Local web dashboard for managing agents from the browser."
order: 5
---

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
| Auth | Select a2a auth provider (see below) |
| Egress | Review the outbound allowlist (including auth-provider-derived hosts) |
| Env & Security | Add extra env vars; set passphrase for AES-256-GCM secret encryption |
| Review | Summary of all selections before creation |

The wizard collects credentials inline at each step (matching the CLI TUI behavior) and supports all the same options: model selection, OAuth, web search providers, fallback chains, and encrypted secret storage.

### Auth step

The Auth step exposes the same provider chain as `forge.yaml`'s
`auth.providers[]` block. Picker options:

| Option | Effect |
|---|---|
| **None** | Anonymous access — no `auth:` block written |
| **OIDC (JWT)** | Generic OIDC IdP (Keycloak, Auth0, Okta, Google) — collects issuer + audience |
| **HTTP Verifier** | Legacy — POST tokens to your own `/verify` endpoint |
| **AWS Sigv4 (IAM)** | AWS-IAM-based callers — collects region + optional audience + comma-separated allowed accounts |
| **GCP Identity-Aware Proxy** | Forge behind GCP LB+IAP — collects backend service ID |
| **Azure AD / Entra ID** | Microsoft Entra ID (single-tenant in the wizard; multi-tenant requires editing `forge.yaml` as a deliberate security trade-off) |
| **Custom** | Comment stub — edit `forge.yaml` yourself |

The Auth step runs **before** Egress, so the Egress step's review screen
displays auth-provider-derived hosts (e.g. `sts.us-east-1.amazonaws.com`
when AWS Sigv4 is selected) alongside the model / channel / tool / skill
hosts. The operator sees the full outbound surface for approval in one
place.

The Web UI's `POST /api/create` endpoint additionally filters incoming
auth `settings` through a closed-key whitelist (defined in
`forge-core/validate/auth.go`) before scaffolding — unknown keys are
silently dropped rather than written to disk.

See [Authentication](../security/authentication.md) for the full
provider reference.

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

The Skill Builder uses a **workspace-level LLM** that's independent of any specific agent's runtime LLM. The same configuration applies across every agent in the workspace and works before any agent has been scaffolded.

Configuration is persisted under `<workspace>/.forge/`:

- `ui.yaml` holds the non-secret fields (`provider`, `model`, optional `base_url`, optional `api_key_env`).
- `.env` holds the API key value (mode `0600`, auto-protected by a sibling `.gitignore`).

The status banner above the chat panel surfaces the resolution source — `workspace`, `user` (machine-wide fallback), `agent_fallback` (a deprecated compat shim used when no workspace/user config exists), or `unset`. When `unset`, the banner shows a **Configure** button that opens the settings modal.

See [Skill Builder LLM](../ui/skill-builder-llm.md) for the full configuration reference, three-tier resolution precedence, and trust-boundary rationale.

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
| Egress injection | Automatically merges declared `egress_domains` into `forge.yaml` allowlist on save |
| Env var handling | Writes user-provided env vars to `.env`; prompts for missing required vars after save |

### Workflow

1. **Open** the Skill Builder from an agent card
2. **Describe** the skill you want (e.g. "Create a skill that queries Jira issues")
3. **Iterate** — the AI asks about requirements, security constraints, and env vars
4. **Review** — inspect the generated SKILL.md and scripts in the preview panel
5. **Validate** — check for errors and warnings before saving
6. **Save** — writes `skills/{name}/SKILL.md` and `skills/{name}/scripts/` to the agent directory, merges egress domains into `forge.yaml`, and writes env vars to `.env`
7. **Configure** — if the skill requires env vars that aren't set, the UI shows input fields to provide them and re-save

### Editing an Attached Skill

The Skill Builder also supports iterating on an already-attached custom
skill — created either by the builder or by hand. Real-world bugs in a
skill (wrong input schema, missing egress, brittle error handling) only
surface once the LLM actually calls the tool inside the agent loop, so
the create → test → fix loop needs a first-class edit path.

| Step | What happens |
|------|--------------|
| 1. Open the Skill Builder for an agent that already has custom skills | A **Skills attached to this agent** panel lists each `skills/<name>/SKILL.md` (subdir form) and `skills/<name>.md` (flat form) discovered on disk. |
| 2. Click **Edit** on one of them | The skill's current SKILL.md and helper scripts load into the editor; the chat is seeded with a system message naming the skill; the header shows an **Editing** banner with the skill name. |
| 3. Describe the change in chat | The chat call passes `mode: "edit"` and `editing_name`. The server reloads the on-disk SKILL.md itself (single source of truth) and primes the LLM with an `## Edit Mode` trailer that instructs it to preserve existing `## Tool: <name>` headings, default to minimal patches, and end with a `**Changed:**` summary. |
| 4. Click **Preview changes** | A Monaco side-by-side diff modal shows the editor state vs the on-disk baseline. Tabs cover SKILL.md plus every script that appears in either side — scripts present only in the new state are tagged `new`, scripts present only in the old state are tagged `removed`. |
| 5. Click **Confirm save** | The save call sets `overwrite: true` and `editing_name` matching `skill_name`. The validator suppresses the duplicate-name warning (the skill IS the one being edited). The forge-cli writer wipes `skills/<name>/scripts/` before re-writing the script set so any dropped scripts disappear from disk — they would otherwise linger and the runtime would keep discovering them. |
| 6. Restart prompt | After a successful edit-mode save, a banner offers a **Restart agent** action. The running agent's tool registry is frozen at startup, so a restart is the safe way to pick up the edited SKILL.md and scripts. (The watcher reloads the agent card on file changes, but not the live tool registry — see the [hot-reload note](#hot-reload-note) below.) |
| 7. Click **New skill** to reset | Clears editor state and the chat, drops back into create mode so the next message starts a fresh skill. |

#### Renaming a skill

The frontmatter `name:` field IS the on-disk identifier and the tool
name the LLM dispatches against — renaming it is a breaking change for
any agent already wired to the existing tool. The validator surfaces a
warning when the editor `name:` differs from the skill being edited.
Save in that case writes a NEW skill directory under the new name and
leaves the old directory in place; nothing is renamed in-place. Manual
cleanup of the old directory is required.

#### Hot-reload note

The dashboard's file watcher reloads the agent card on SKILL.md
changes, but the agent runtime's tool registry is captured once at
startup and not live-mutated. Restart the agent (the banner that
appears after save does this for you) to make edited skills callable
in the running session.

### Validation Rules

The validator enforces the [SKILL.md format](../core-concepts/skill-md-format.md):

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
| `GET` | `/api/skill-builder/provider` | Returns the resolved workspace-level LLM (provider, model, `source`, `has_key`, deprecation `warning`). Use this for first-run detection before any agent is picked. |
| `GET` | `/api/agents/{id}/skill-builder/provider` | Same shape as above, but consults the agent-fallback tier when no workspace/user config exists. |
| `GET` | `/api/agents/{id}/skill-builder/context` | Returns the system prompt used for skill generation |
| `POST` | `/api/agents/{id}/skill-builder/chat` | Streams an LLM conversation via SSE. Accepts `messages` array; in edit mode also pass `mode: "edit"` and `editing_name` so the server loads the on-disk skill and primes an `## Edit Mode` system-prompt trailer. Returns `400` when the workspace LLM is `unset` or has no resolvable API key. |
| `POST` | `/api/agents/{id}/skill-builder/validate` | Validates a SKILL.md and optional scripts. In edit mode pass `mode: "edit"` and `editing_name` to suppress the duplicate-name warning for the skill being edited. |
| `POST` | `/api/agents/{id}/skill-builder/save` | Saves a validated skill to `skills/{name}/`, merges egress domains, writes env vars; returns `SkillSaveResult` with `path`, `egress_added`, `env_configured`, `env_missing`. In edit mode pass `overwrite: true` and `editing_name` matching `skill_name` to rewrite the existing directory and clear stale scripts. |
| `GET` | `/api/agents/{id}/skill-builder/skills` | Lists custom skills attached to the agent (project-local SKILL.md files under `skills/`). Distinct from `/api/skills` which returns registry/embedded skills. Returns `[]CustomSkillSummary` with `name`, `description`, `category`, `tags`, `path`, `has_scripts`, `tools`. |
| `GET` | `/api/agents/{id}/skill-builder/skills/{name}` | Loads one custom skill's SKILL.md plus any helper scripts so the builder can populate the editor. Returns `CustomSkillContent` with `skill_md`, `scripts` (basename → content), `format` (`subdir`\|`flat`). Rejects invalid names (path traversal, non-kebab-case) with `400`; returns `404` when the skill doesn't exist. |
| `GET` | `/api/settings/skill-builder` | Returns the workspace-level config + metadata for the Settings modal (`source`, `has_key`, `providers` list). The API key value is never echoed back. |
| `PUT` | `/api/settings/skill-builder` | Persists `provider`, `model`, optional `base_url`, optional `api_key_env` to `<workspace>/.forge/ui.yaml`. When an optional `api_key` field is present in the body, writes it to `<workspace>/.forge/.env` (mode `0600`) under `api_key_env` (or the provider default). The key value never leaks to `ui.yaml`. Submit an empty / omitted `api_key` to leave the saved value untouched. |

## Architecture

The dashboard is a single Go module (`forge-ui`) embedded into the `forge` binary:

```
forge-cli/cmd/ui.go               CLI command, injects ExePath/CreateFunc/OAuthFunc/LLMStreamFunc
forge-ui/
  server.go                        HTTP server with CORS, SPA fallback
  handlers.go                      Dashboard API (agents, start/stop, chat, sessions)
  handlers_create.go               Wizard API (create, config, skills, tools, OAuth)
  handlers_skill_builder.go        Skill Builder API (chat, validate, save, provider)
  handlers_settings.go             Workspace-level settings API (skill-builder LLM)
  uiconfig/                        Workspace ui.yaml + .env loader; SkillBuilderLLM
                                   resolution + atomic .env writer with auto-.gitignore
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
