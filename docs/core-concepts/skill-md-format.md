---
title: "SKILL.md Format"
description: "Define agent skills using the SKILL.md format with YAML frontmatter and tool definitions."
order: 2
---

## SKILL.md Format

Skills are defined in Markdown files inside `skills/<skill-name>/SKILL.md`. Each file supports optional YAML frontmatter and two body formats.

```markdown
---
name: weather
icon: 🌤️
category: utilities
tags:
  - weather
  - forecast
  - api
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

### YAML Frontmatter

Top-level fields:

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Skill identifier (kebab-case) |
| `icon` | yes | Emoji displayed in the TUI skill picker |
| `category` | yes | Grouping for `forge skills list --category` (e.g., `sre`, `developer`, `research`, `utilities`) |
| `tags` | yes | Discovery keywords for `forge skills list --tags` (kebab-case) |
| `description` | yes | One-line summary |

The `metadata.forge.requires` block declares runtime dependencies:

- **`bins`** — Binary dependencies that must be in `$PATH` at runtime. Each entry is either a scalar name (matched against the embedded registry) or a mapping with its own install method (`url:`, `run:`, `apt:`, `apk:`). See [Binary Dependencies](binary-dependencies.md) for the resolution pipeline, install methods, and the four ways to add a binary.
- **`env.required`** — Environment variables that must be set
- **`env.one_of`** — At least one of these environment variables must be set
- **`env.optional`** — Optional environment variables for extended functionality

The `metadata.forge.runtime` field selects how the skill's tool is executed (issue #182):

| Value | Behavior |
|---|---|
| `script` (default; empty = `script`) | Skill body is materialized as a bash script at `skills/<dir>/scripts/<tool>.sh` and invoked as `bash <scriptPath> <jsonArgs>`. |
| `binary` | The first `metadata.forge.requires.bins` entry IS the executable. The runtime resolves it via `exec.LookPath` and invokes `<binary> <jsonArgs>` directly — no bash fork, no script file required. Skill body is documentation only. |

```yaml
# Binary skill — wraps the `infil` binary directly. OTel-instrumented
# binaries inherit the parent agent's `tool.<name>` span via TRACEPARENT
# env (see observability-tracing.md § Subprocess propagation).
metadata:
  forge:
    runtime: binary
    requires:
      bins:
        - name: infil
          version: ">=0.4.0"
```

Both runtimes receive the same env passthrough (skill-declared `env.optional`, provider base URLs, `TRACEPARENT` + curated `OTEL_*` for tracing) — the binary path just removes the wrapper hop.

Frontmatter is parsed by `ParseWithMetadata()` in `forge-skills/parser/parser.go` and feeds into the compilation pipeline.

### Legacy List Format

```markdown
# Agent Skills

- translate
- summarize
- classify
```

Single-word list items (no spaces, max 64 characters) create name-only skill entries. This format is simpler but provides less metadata.

## Skill Registry

Forge ships with a built-in skill registry. Add skills to your project with a single command:

```bash
# Add a skill from the registry
forge skills add tavily-research

# Validate skill requirements
forge skills validate

# Audit skill security
forge skills audit --embedded
```

`forge skills add` copies the skill's SKILL.md and any associated scripts into your project's `skills/` directory. It validates binary and environment requirements, checks for existing values in your environment, `.env` file, and encrypted secrets, and prompts only for truly missing values with a suggestion to use `forge secrets set` for sensitive keys. If the skill declares `egress_domains`, they are automatically merged into the `forge.yaml` `egress.allowed_domains` list (deduplicated and sorted).

## Skills as First-Class Tools

Script-backed skills are automatically registered as **first-class LLM tools** at runtime. When a skill has scripts in `skills/scripts/`, Forge:

1. Parses the skill's SKILL.md for tool definitions, descriptions, and input schemas
2. Creates a named tool for each `## Tool:` entry (e.g., `tavily_research` becomes a tool the LLM can call directly)
3. Executes the skill's shell script with JSON input when the LLM invokes it

This means the LLM sees skill tools alongside builtins like `web_search` and `http_request` — no generic `cli_execute` indirection needed.

For skills **without** scripts (binary-backed skills like `k8s-incident-triage`), Forge injects the full skill instructions into the system prompt. The complete SKILL.md body — including triage steps, detection heuristics, output structure, and safety constraints — is included inline so the LLM follows the skill protocol without needing an extra tool call. Skills are invoked via `cli_execute` with the declared binary dependencies.

```
┌─────────────────────────────────────────────────┐
│                LLM Tool Registry                │
├─────────────────┬───────────────────────────────┤
│  Builtins       │  web_search, http_request     │
│  Skill Tools    │  tavily_research, codegen_*   │  ← auto-registered from scripts
│  read_skill     │  load any SKILL.md on demand  │
│  cli_execute    │  run approved binaries        │
├─────────────────┴───────────────────────────────┤
│  System Prompt: full skill instructions inline  │  ← binary-backed skills
└─────────────────────────────────────────────────┘
```

## Skill Execution Security

Skill scripts run in a restricted environment via `SkillCommandExecutor`:

- **Isolated environment**: Only `PATH`, `HOME`, and explicitly declared env vars are passed through
- **OAuth token resolution**: When `OPENAI_API_KEY` is set to `__oauth__`, the executor resolves OAuth credentials and injects the access token, `OPENAI_BASE_URL`, and the configured model as `REVIEW_MODEL`
- **Configurable timeout**: Each skill declares a `timeout_hint` in its YAML frontmatter (e.g., 300s for research)
- **No shell execution**: Scripts run via `bash <script> <json-input>`, not through a shell interpreter
- **Egress proxy enforcement**: When egress mode is `allowlist` or `deny-all`, a local HTTP/HTTPS proxy is started and `HTTP_PROXY`/`HTTPS_PROXY` env vars are injected into subprocess environments, ensuring `curl`, `wget`, Python `requests`, and other HTTP clients route through the same domain allowlist used by in-process tools (see [Egress Security](../security/egress-control.md))

### Symlink Escape Detection

The skill scanner validates symlinks when a filesystem root path is available. Symlinks that resolve outside the root directory are skipped with a warning log. This prevents malicious symlinks in skill directories from escaping the project boundary. The scanner exposes `ScanWithRoot(fsys, rootPath)` for callers that need symlink validation, while the original `Scan(fsys)` remains backward-compatible.

### Trust Policy Defaults

The default trust policy requires checksum verification (`RequireChecksum: true`). Skills loaded without a signature emit a warning log at scan time. Signature verification remains opt-in (`RequireSignature: false`).

## Skill Categories & Tags

All embedded skills must declare `category`, `tags`, and `icon` in their frontmatter. Categories and tags must be lowercase kebab-case.

```markdown
---
name: k8s-incident-triage
icon: ☸️
category: sre
tags:
  - kubernetes
  - incident-response
  - triage
---
```

Use categories and tags to filter skills:

```bash
# List skills by category
forge skills list --category sre

# Filter by tags (AND semantics — skill must have all listed tags)
forge skills list --tags kubernetes,incident-response
```
