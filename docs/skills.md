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

The `metadata.forge.requires` block declares:
- **`bins`** — Binary dependencies that must be in `$PATH` at runtime
- **`env.required`** — Environment variables that must be set
- **`env.one_of`** — At least one of these environment variables must be set
- **`env.optional`** — Optional environment variables for extended functionality

Frontmatter is parsed by `ParseWithMetadata()` in `forge-core/skills/parser.go` and feeds into the compilation pipeline.

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
│  Skill Tools    │  tavily_research, ...         │  ← auto-registered from scripts
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

Skills can declare a `category` and `tags` in their frontmatter for organization and filtering:

```markdown
---
name: k8s-incident-triage
category: sre
tags:
  - kubernetes
  - incident-response
  - triage
---
```

Categories and tags must be lowercase kebab-case. Use them to filter skills:

```bash
# List skills by category
forge skills list --category sre

# Filter by tags (AND semantics — skill must have all listed tags)
forge skills list --tags kubernetes,incident-response
```

## Built-in Skills

| Skill | Description | Scripts |
|-------|-------------|---------|
| `tavily-research` | Deep multi-source research via Tavily API | `tavily-research.sh`, `tavily-research-poll.sh` |
| `k8s-incident-triage` | Read-only Kubernetes incident triage using kubectl | — (binary-backed) |

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

---
← [Architecture](architecture.md) | [Back to README](../README.md) | [Tools](tools.md) →
