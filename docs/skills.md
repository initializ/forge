# Skills

> Part of [Forge Documentation](../README.md)

Skills are a progressive disclosure mechanism for defining agent capabilities in a structured, human-readable format. They compile into container artifacts during `forge build`.

## Overview

Skills bridge the gap between high-level capability descriptions and the tool-calling system. A `SKILL.md` file in your project root defines what the agent can do, and Forge compiles these into JSON artifacts and prompt text for the container.

## SKILL.md Format

Skills are defined in a Markdown file (default: `SKILL.md`). The file supports optional YAML frontmatter and two body formats.

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

- **`bins`** — Binary dependencies that must be in `$PATH` at runtime
- **`env.required`** — Environment variables that must be set
- **`env.one_of`** — At least one of these environment variables must be set
- **`env.optional`** — Optional environment variables for extended functionality

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

`forge skills add` copies the skill's SKILL.md and any associated scripts into your project's `skills/` directory. It validates binary and environment requirements, checks for existing values in your environment, `.env` file, and encrypted secrets, and prompts only for truly missing values with a suggestion to use `forge secrets set` for sensitive keys.

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
- **Configurable timeout**: Each skill declares a `timeout_hint` in its YAML frontmatter (e.g., 300s for research)
- **No shell execution**: Scripts run via `bash <script> <json-input>`, not through a shell interpreter
- **Egress proxy enforcement**: When egress mode is `allowlist` or `deny-all`, a local HTTP/HTTPS proxy is started and `HTTP_PROXY`/`HTTPS_PROXY` env vars are injected into subprocess environments, ensuring `curl`, `wget`, Python `requests`, and other HTTP clients route through the same domain allowlist used by in-process tools (see [Egress Security](security/egress.md))

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

## Built-in Skills

| Skill | Icon | Category | Description | Scripts |
|-------|------|----------|-------------|---------|
| `github` | 🐙 | developer | Create issues, PRs, and query repositories | — (binary-backed) |
| `weather` | 🌤️ | utilities | Get weather data for a location | — (binary-backed) |
| `tavily-search` | 🔍 | research | Search the web using Tavily AI search API | `tavily-search.sh` |
| `tavily-research` | 🔬 | research | Deep multi-source research via Tavily API | `tavily-research.sh`, `tavily-research-poll.sh` |
| `k8s-incident-triage` | ☸️ | sre | Read-only Kubernetes incident triage using kubectl | — (binary-backed) |
| `k8s-pod-rightsizer` | ⚖️ | sre | Analyze workload metrics and produce CPU/memory rightsizing recommendations | — (binary-backed) |
| `code-review` | 🔎 | developer | AI-powered code review for diffs and files | `code-review-diff.sh`, `code-review-file.sh` |
| `code-review-standards` | 📏 | developer | Initialize and manage code review standards | — (template-based) |
| `code-review-github` | 🐙 | developer | Post code review results to GitHub PRs | — (binary-backed) |
| `codegen-react` | ⚛️ | developer | Scaffold and iterate on Vite + React apps | `codegen-react-scaffold.sh`, `codegen-react-read.sh`, `codegen-react-write.sh`, `codegen-react-run.sh` |
| `codegen-html` | 🌐 | developer | Scaffold standalone Preact + HTM apps (zero dependencies) | `codegen-html-scaffold.sh`, `codegen-html-read.sh`, `codegen-html-write.sh` |

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

### Kubernetes Incident Triage Skill

The `k8s-incident-triage` skill performs read-only triage of Kubernetes workloads using `kubectl`:

```bash
forge skills add k8s-incident-triage
```

This registers a single tool:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `k8s_triage` | Diagnose unhealthy workloads, pods, or namespaces | Runs read-only kubectl commands, produces a structured triage report |

The skill accepts two input modes:

- **Human mode** — natural language like `"triage payments-prod"` or `"why are pods pending in checkout-prod?"`
- **Automation mode** — structured JSON with namespace, workload, pod, and diagnostic options

**Triage process:**

1. Verify cluster access (kubectl version, cluster-info)
2. Fast health snapshot (pods, deployments, statefulsets)
3. Events timeline (FailedScheduling, probe failures, evictions)
4. Describe pods & workloads (container state, restart counts, probes)
5. Node diagnostics (optional — NotReady, memory/disk pressure)
6. Logs (optional — with previous container logs for CrashLoopBackOff)
7. Metrics (optional — via metrics-server)

**Detection heuristics** classify issues into: CrashLoop, OOMKilled, Image Pull Failure, Scheduling Constraint, Probe Failure, PVC/Volume Failure, Node Pressure/Eviction, Rollout Stuck. Each finding includes a hypothesis, evidence, confidence score (0.0-1.0), and recommended next commands.

**Safety:** This skill is strictly read-only. It never executes `apply`, `patch`, `delete`, `exec`, `port-forward`, `scale`, or `rollout restart`. It never prints Secret values.

Requires: `kubectl`, optional `KUBECONFIG`, `K8S_API_DOMAIN`, `DEFAULT_NAMESPACE` environment variables.

### Kubernetes Pod Rightsizer Skill

The `k8s-pod-rightsizer` skill analyzes real workload metrics (Prometheus or metrics-server fallback) and produces policy-constrained CPU/memory rightsizing recommendations:

```bash
forge skills add k8s-pod-rightsizer
```

This skill operates in three modes:

| Mode | Purpose | Mutates Cluster |
|------|---------|-----------------|
| `dry-run` | Report recommendations only (default) | No |
| `plan` | Generate strategic merge patch YAMLs | No |
| `apply` | Execute patches with rollback bundle | Yes (requires `i_accept_risk: true`) |

**Key features:**

- Deterministic formulas — no LLM-based guessing for recommendations
- Policy model with per-namespace and per-workload overrides (safety factors, min/max bounds, step constraints)
- Prometheus p95 metrics with metrics-server fallback
- Automatic rollback bundle generation in apply mode
- Workload classification: over-provisioned, under-provisioned, right-sized, limit-bound, insufficient-data

**Apply workflow:** The skill's built-in `mode=apply` handles rollback bundles, strategic merge patches via `kubectl patch`, and rollout verification. Do not manually run `kubectl apply -f` — use `mode=apply` with `i_accept_risk: true` instead.

Requires: `bash`, `kubectl`, `jq`, `curl`. Optional: `KUBECONFIG`, `K8S_API_DOMAIN`, `PROMETHEUS_URL`, `PROMETHEUS_TOKEN`, `POLICY_FILE`, `DEFAULT_NAMESPACE`.

### Codegen React Skill

The `codegen-react` skill scaffolds and iterates on **Vite + React** applications with Tailwind CSS:

```bash
forge skills add codegen-react
```

This registers four tools:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `codegen_react_scaffold` | Create a new project | Generates package.json, Vite config, React components with Tailwind CSS and Forge dark theme |
| `codegen_react_run` | Start the dev server | Runs `npm install` + `npm run dev`, auto-opens browser, returns server URL and PID |
| `codegen_react_read` | Read project files | Returns file content or directory listing (excludes `node_modules/`, `.git/`) |
| `codegen_react_write` | Write/update files | Creates or updates files with path traversal prevention; Vite hot-reloads automatically |

**Iteration workflow:**

1. Scaffold the project with `codegen_react_scaffold`
2. Start the dev server with `codegen_react_run` — installs deps, opens browser
3. Read/write files with `codegen_react_read` / `codegen_react_write` — Vite hot-reloads on save
4. Repeat step 3 to iterate on the UI

**Scaffold output:** `package.json` (React 19, Vite 6), `vite.config.js`, `index.html` (with Tailwind CDN), `src/main.jsx`, `src/App.jsx` (Tailwind utility classes), `src/App.css`, `.gitignore`.

**Safety:** Output directories must be under `$HOME` or `/tmp`. Path traversal (`..`, absolute paths) is rejected. Non-empty directories require `force: true`.

Requires: `node`, `npx`, `jq`. Egress: `registry.npmjs.org`, `cdn.jsdelivr.net`, `cdn.tailwindcss.com`.

### Codegen HTML Skill

The `codegen-html` skill scaffolds standalone **Preact + HTM** applications with zero local dependencies:

```bash
forge skills add codegen-html
```

This registers three tools:

| Tool | Purpose | Behavior |
|------|---------|----------|
| `codegen_html_scaffold` | Create a new project | Generates HTML with Preact + HTM via CDN and Tailwind CSS; supports single-file and multi-file modes |
| `codegen_html_read` | Read project files | Returns file content or directory listing |
| `codegen_html_write` | Write/update files | Creates or updates files with path traversal prevention |

**Two scaffold modes:**

| Mode | Files | Use Case |
|------|-------|----------|
| `single-file` | One `index.html` with inline JS | Quick prototypes, shareable demos |
| `multi-file` | `index.html`, `app.js`, `components/Counter.js` | Larger apps with component separation |

**Key differences from codegen-react:** No Node.js required. No build step. No `npm install`. Just open `index.html` in a browser. Uses `class` (not `className`) since HTM maps directly to DOM attributes.

**Safety:** Same restrictions as codegen-react — output under `$HOME` or `/tmp`, path traversal prevention, `force: true` for non-empty directories.

Requires: `jq`. Egress: `cdn.tailwindcss.com`, `esm.sh`.

## Skill Instructions in System Prompt

Forge injects the **full body** of each skill's SKILL.md into the LLM system prompt. This means all detailed operational instructions — triage steps, detection heuristics, output structure, safety constraints — are directly available in the LLM's context without requiring an extra `read_skill` tool call.

For skills with extensive instructions (like `k8s-incident-triage` with ~150 lines of triage procedures), this ensures the LLM follows the complete skill protocol from the first interaction.

## Compilation Pipeline

The skill compilation pipeline has three stages:

1. **Parse** — Reads `SKILL.md` and extracts `SkillEntry` values with name, description, input spec, and output spec. When YAML frontmatter is present, `ParseWithMetadata()` additionally extracts `SkillMetadata` and `SkillRequirements` (binary deps, env vars).

2. **Compile** — Converts entries into `CompiledSkills` with:
   - A JSON-serializable skill list
   - A human-readable prompt catalog
   - Version identifier (`agentskills-v1`)

3. **Write Artifacts** — Outputs to the build directory:
   - `compiled/skills/skills.json` — Machine-readable skill definitions
   - `compiled/prompt.txt` — LLM-readable skill catalog

## Build Stage Integration

The `SkillsStage` runs as part of the build pipeline:

1. Resolves the skills file path (default: `SKILL.md` in work directory)
2. Skips silently if the file doesn't exist
3. Parses, compiles, and writes artifacts
4. Updates the `AgentSpec` with `skills_spec_version` and `forge_skills_ext_version`
5. Records generated files in the build manifest

## Configuration

In `forge.yaml`:

```yaml
skills:
  path: SKILL.md  # default, can be customized
```

## CLI Workflow

```bash
# Initialize a project with skills support
forge init my-agent --from-skills

# Build compiles skills automatically
forge build
```

## Skill Builder (Web UI)

The [Web Dashboard](dashboard.md#skill-builder) includes an AI-powered Skill Builder that generates valid SKILL.md files and helper scripts through a conversational interface. It uses the agent's own LLM provider and includes server-side validation before saving to the agent's `skills/` directory.

---
← [Architecture](architecture.md) | [Back to README](../README.md) | [Tools](tools.md) →
