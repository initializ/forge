---
title: "Writing Custom Skills"
description: "Create script-backed skills with tools, guardrails, and the compilation pipeline."
order: 2
---

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
- **Egress proxy enforcement**: When egress mode is `allowlist` or `deny-all`, a local HTTP/HTTPS proxy is started and `HTTP_PROXY`/`HTTPS_PROXY` env vars are injected into subprocess environments, ensuring `curl`, `wget`, Python `requests`, and other HTTP clients route through the same domain allowlist used by in-process tools (see [Egress Security](security/egress.md))

### Symlink Escape Detection

The skill scanner validates symlinks when a filesystem root path is available. Symlinks that resolve outside the root directory are skipped with a warning log. This prevents malicious symlinks in skill directories from escaping the project boundary. The scanner exposes `ScanWithRoot(fsys, rootPath)` for callers that need symlink validation, while the original `Scan(fsys)` remains backward-compatible.

### Trust Policy Defaults

The default trust policy requires checksum verification (`RequireChecksum: true`). Skills loaded without a signature emit a warning log at scan time. Signature verification remains opt-in (`RequireSignature: false`).

## Skill Guardrails

Skills can declare domain-specific guardrails in their `SKILL.md` frontmatter to enforce security policies at runtime. These guardrails operate at four interception points in the agent loop, preventing unauthorized commands, data exfiltration, capability enumeration, and binary name disclosure.

### Configuration

Add a `guardrails` block under `metadata.forge` in `SKILL.md`:

```yaml
metadata:
  forge:
    guardrails:
      deny_commands:
        - pattern: '\bget\s+secrets?\b'
          message: "Listing Kubernetes secrets is not permitted"
      deny_output:
        - pattern: 'kind:\s*Secret'
          action: block
        - pattern: 'token:\s*[A-Za-z0-9+/=]{40,}'
          action: redact
      deny_prompts:
        - pattern: '\b(approved|allowed|available)\b.{0,40}\b(tools?|binaries)\b'
          message: "I help with K8s cost analysis. Ask about cluster costs."
      deny_responses:
        - pattern: '\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b.*\b(kubectl|jq|awk|bc|curl)\b'
          message: "I can analyze cluster costs. What would you like to know?"
```

### Guardrail Types

| Type | Direction | Purpose |
|------|-----------|---------|
| `deny_commands` | Input | Block `cli_execute` commands matching patterns (e.g., `kubectl get secrets`) |
| `deny_output` | Output | Block or redact tool output matching patterns (e.g., Secret manifests, tokens) |
| `deny_prompts` | Input | Block user messages probing agent capabilities (e.g., "what tools can you run") |
| `deny_responses` | Output | Replace LLM responses that enumerate internal binary names |

### Capability Enumeration Prevention

The `deny_prompts` and `deny_responses` guardrails form a layered defense against capability enumeration attacks:

1. **Input-side** (`deny_prompts`) — Intercepts user messages that probe for available tools, binaries, or commands and redirects to the skill's functional description
2. **Output-side** (`deny_responses`) — Catches LLM responses that list 3+ binary names and replaces the entire response with a functional capability description

Additionally, skill `Description()` methods and system prompt catalog entries use generic descriptions instead of listing binary names.

For full details on guardrail types, pattern syntax, and runtime behavior, see [Content Guardrails — Skill Guardrails](security/guardrails.md#skill-guardrails).

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

1. Scans the `skills/` subdirectory for `SKILL.md` files in each subdirectory
2. Parses, compiles, and writes artifacts
3. Updates the `AgentSpec` with `skills_spec_version` and `forge_skills_ext_version`
4. Records generated files in the build manifest
