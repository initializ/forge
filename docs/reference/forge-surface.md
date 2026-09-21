---
title: "The forge surface"
description: "Running `forge` with no subcommand opens an interactive builder that shells into Claude Code (wired with forge knowledge + tools) or runs forge's own agent — and can generate an initializ-deploy.yaml."
order: 2
---

# The `forge` surface

Running **`forge` with no subcommand** opens an interactive surface — think "Claude Code, but for building Forge agents." It greets you with the FORGE logo and a short capability list, then lets you either **shell into a coding agent** (Claude Code) wired with forge knowledge + tools, or use **forge's own in-process agent**. It can also **generate an `initializ-deploy.yaml`** for the platform.

All existing subcommands still dispatch normally (`forge run`, `forge build`, `forge --help`, …); the surface is the zero-argument default.

> **Interactive only.** The surface requires a TTY (both stdin and stdout). In a non-interactive context (CI, a piped process, a deployed agent) `forge` prints help and exits — it never launches the surface or its shell headlessly. See [Security](#security).

## The chooser

```
forge  →  ███ F O R G E ███  + capabilities
          Code with:  ◆ Claude Code   ⚒ forge (native)
```

| Choice | What it does |
|---|---|
| **Claude Code** | Registers a durable forge MCP server with your `claude` and launches it, wired with forge knowledge + tools. Optionally routes it through the forge optimizer. |
| **forge (native)** | Runs forge's own in-process builder agent in a terminal REPL. Also the automatic fallback when `claude` isn't on your PATH. |

## Claude Code path

Picking **Claude Code** registers a **durable, user-scope MCP server** with Claude Code and then execs it:

```bash
claude mcp add --scope user forge -- forge mcp-serve
claude --  # launched inheriting your terminal
```

Because the registration is at **user scope**, the forge tools stay available in every Claude Code session — including a later `claude` **resume** — the same way the optimizer keeps `context_expand` available. Remove it any time with `claude mcp remove forge`.

`forge mcp-serve` is a hidden subcommand: the stdio MCP server Claude Code spawns. It exposes:

| Tool | Purpose |
|---|---|
| `forge_docs` | Look up authoritative forge documentation by topic or free-text query (grounded in the embedded knowledge skill). |
| `forge_scaffold`, `forge_validate`, `forge_build`, `forge_run` | Structured wrappers that self-exec the matching `forge` subcommand (argv-based, no shell). |
| `forge_add_channel`, `forge_add_skill`, `forge_import_skill`, `forge_skills_list`, `forge_mcp_list` | More structured forge operations. |
| `forge_cli` | Generic passthrough — run any forge subcommand (refuses long-running/interactive ones). Guarantees a new forge capability is reachable without a new wrapper. |
| `initializ_detect_agent`, `initializ_deploy_generate` | Investigate the project and generate an `initializ-deploy.yaml` (see [Generating a deploy spec](#generating-an-initializ-deploy-spec)). |

The unsandboxed **shell tool is deliberately NOT in this set** — see [Security](#security).

### The optimizer prompt

After choosing Claude Code you're asked whether to route it through the **[forge optimizer](../core-concepts/context-compression.md)** (cache-safe reversible compression + episodic/procedural memory).

**The optimizer is OFF by default and cannot be selected unless a forge settings layer enables it.** Enabling it rewrites `ANTHROPIC_BASE_URL` (routing Claude Code through the local proxy, which sees all prompt context), which cannot be forced under enterprise-managed Claude Code — so it is opt-in via the trusted `optimizer.enabled` [setting](settings.md#optimizer):

```jsonc
// ~/.forge/settings.json (user) — or the enterprise-managed settings layer
{ "optimizer": { "enabled": true } }
```

When enabled and selected, the surface starts (or **adopts** an already-running) optimizer proxy with **in-band expansion**, so there is still exactly **one** forge MCP server. It resolves the setting from **trusted layers only** (user / project-local / CLI / managed) — a checked-in project `.forge/settings.json` cannot enable it, matching the model-gateway trust boundary.

## forge (native) path

Picking **forge (native)** runs forge's own agent loop in-process — no `forge.yaml` required (the point is to create one). It resolves a model credential the same way `forge try` does, then drops you into a REPL:

```
you › build me an agent that summarizes RSS feeds daily
forge › …            # markdown-rendered reply; tool calls shown inline
```

The native agent has file read/write/edit + search tools, a **shell tool** (OS commands + the `forge` CLI on PATH), the structured forge-ops, `forge_docs`, and the initializ generators. Slash commands: `/help`, `/exit` (or Ctrl-D). Per-turn, the relevant forge knowledge topic is injected once by intent.

## Generating an initializ-deploy spec

Both paths can prepare an agent for the initializ platform. The workflow is **detect → confirm → generate** (generation only — you deploy it yourself):

1. **`initializ_detect_agent`** investigates the project: `forge.yaml` → **forge**; a Claude/Strands dependency in `package.json` (node) or `requirements.txt` / `pyproject.toml` (python) → **claude-agent** / **strands**.
2. The agent **confirms** the type (and node/python) with you.
3. **`initializ_deploy_generate`** writes a valid `initializ-deploy.yaml`. `forge` specs read `forge.yaml` and are always A2A; `claude-agent`/`strands` require a name + `model.provider` and pick one exposure — `expose: a2a` (default) or `expose: http` (a plain invoke endpoint).

The generator mirrors the platform's own `deployspec` validation. You deploy the result with `initializ agent deploy -f initializ-deploy.yaml --image <ref> --wait`.

## Security

The surface is a **local-operator tool**, and its capabilities are structurally contained:

- **TTY gate.** `forge` opens the surface only when stdin *and* stdout are TTYs; otherwise it prints help. A deployed agent (piped stdio, no PTY) can't launch it.
- **Shell tool is native-only.** The unsandboxed shell tool (OS + `forge` CLI, like a coding agent's Bash) is wired **only** into the native REPL — never into `mcp-serve`'s toolset (what Claude Code / a deployed agent could reach) and never into a built agent. A regression test enforces this.
- **File/search tools are workspace-confined** (path-traversal barrier); the forge-ops and `forge_cli` are argv-based (no shell string interpolation) and refuse long-running/interactive commands.
- **Optimizer enablement is trusted-layer only**, so a hostile cloned repo can't turn it on.

## See also

- [CLI Reference](cli-reference.md) — every `forge` subcommand
- [Settings](settings.md) — the `optimizer.enabled` setting and the layered settings model
- [Context Compression](../core-concepts/context-compression.md) — what the optimizer does
- [Command Integration](command-integration.md) — the initializ platform
