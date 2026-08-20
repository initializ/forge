---
title: "Skills CLI"
description: "CLI commands for managing, validating, and auditing skills."
order: 3
---

## CLI Workflow

```bash
# Initialize a project with skills support
forge init my-agent --from-skills

# Build compiles skills automatically
forge build
```

## Importing a skill folder

Convert an existing skill folder — a `SKILL.md` plus its scripts and reference
files — into a vendored skill. Two entry points share the same importer:

```bash
# Scaffold a NEW agent from a skill folder
forge init my-agent --from-skill-dir ./path/to/skill-folder

# Import into an EXISTING agent project (run from a dir containing forge.yaml)
cd my-agent
forge skills import ./path/to/skill-folder
```

`forge init --from-skill-dir` scaffolds the agent first (the normal init flow —
model provider, channels, auth, etc. still apply), then vendors the folder and
merges its egress into the freshly generated `forge.yaml`. `forge skills import`
does the same vendoring into an already-scaffolded project.

Accepted input layout (flat folders work too — files are classified by role):

```
skill-folder/
  SKILL.md                 # required
  scripts/                 # *.sh / *.py / *.js
    fetch-data.py
  reference/               # arbitrary files the skill reads at runtime
    schema.json
  requirements.txt         # optional Python deps
```

What it does:

- **Vendors** `SKILL.md` into `skills/<name>/SKILL.md`, executable scripts
  (`.sh`/`.py`/`.js`) under `skills/<name>/scripts/`, and every other file as a
  reference preserving its relative path. All writes are confined to the skill
  directory; `.git`/`.venv`/`__pycache__`/`node_modules` and similar build cruft
  are skipped.
- **Resolves the skill name** from the `SKILL.md` frontmatter `name` (falling
  back to the sanitized folder name; override with `--name`).
- **Merges** `metadata.forge.egress_domains` into `forge.yaml`
  `egress.allowed_domains`.
- **Reports** the `requires.env` variables you still need to supply (set them in
  `.env` or via `forge secret set <KEY>`).
- **Infers a `metadata.forge` block** when the imported `SKILL.md` has none —
  many real skills are plain (`name` + `description` only). It derives
  `requires.bins` from the script interpreters (`.py` → `python3`, `.js` →
  `node`), and reports **candidate** `egress_domains` (http(s) hosts found in
  scripts) and `requires.env` (env-var reads — Python `os.environ`/`os.getenv`,
  JS `process.env`, and shell `$VAR`/`${VAR}` minus locally-assigned and common
  shell variables) for you to review. By default it
  **prints** a paste-ready suggested block; the interpreter part is high-
  confidence, the egress/env parts are candidates and are never auto-declared.

Flags:

| Flag | Description |
|------|-------------|
| `--name <name>` | Skill name override (default: frontmatter `name` or folder basename). Must be kebab-case. |
| `--overwrite` | Replace an existing `skills/<name>/` directory (clears stale scripts). |
| `--write-forge-meta` | Inject the inferred `requires.bins` into the vendored `SKILL.md` (only when it has no `metadata.forge`/`metadata:` block). Egress/env stay printed as review candidates — never auto-declared, so egress is not silently widened. |

> **Python scripts** get first-class tool registration: a `## Tool: foo_bar`
> backed by `scripts/foo-bar.py` (or `.js`) is registered as a callable tool the
> model invokes by name, run under `python3` (or `node`) — same as `.sh`. A
> `.py`/`.js` script that doesn't map to a `## Tool:` heading stays reachable by
> path via `run_skill_script`. A skill folder may ship a `requirements.txt`:
> `forge build` discovers `skills/<name>/requirements.txt`, forces `python3` +
> `pip` into the image (you don't need to list them in `requires.bins`), and
> adds a `pip install -r` step for it in the generated Dockerfile. For Python
> scripts **without** a `requirements.txt`, list `python3` under
> `metadata.forge.requires.bins` so the interpreter is still provisioned —
> `forge skills import` warns if it's missing.

## Security Audit

`forge skills audit` scores each skill in the project across four categories — egress, binary, env, script — and runs a `SecurityPolicy` check for hard violations. By default it uses the analyzer's `DefaultPolicy`. A custom policy YAML can be supplied with `--policy`.

The same SecurityPolicy gates `forge build` via its `security-analysis` stage. By default the build uses `DefaultPolicy`; supply an override via either:

- `forge build --policy=path/to/policy.yaml` (CLI flag), or
- `security.policy_path` in `forge.yaml` (committed alongside the agent).

The CLI flag wins over the forge.yaml field. When a build fails the security policy check, the per-skill rule + message detail is printed to stderr (plus the path to the `compiled/security-audit.json` artifact for the full breakdown).

```bash
# Audit skills in the default flat layout (skills/SKILL.md).
forge skills audit

# Audit subdirectory-style skills (skills/<name>/SKILL.md).
forge skills audit --dir skills

# JSON output for tooling.
forge skills audit --dir skills --format json

# Load a custom policy that adjusts both scoring and policy checks.
forge skills audit --dir skills --policy policy.yaml

# Same policy file consumed by forge build:
forge build --policy policy.yaml
```

### Policy YAML

```yaml
# policy.yaml
script_policy: allow              # allow | warn (default) | deny
max_risk_score: 90                # PolicyViolation if exceeded
                                  # (DefaultPolicy is 90; lower it
                                  #  for a stricter posture)

# Scoring overrides — reduce points for items the operator has accepted.
# Every affected RiskFactor's description carries "(via policy)" or
# "(acknowledged by policy)" so the override stays auditable.
trusted_domains:
  - internal.example.com          # +2 instead of +10 (unknown)
acknowledged_bins:
  - python                        # +3 instead of +15 (high-risk)
acknowledged_env:
  - DB_PASSWORD                   # +5 instead of +10 (sensitive)
```

Scoring overrides only down-weight builtin classifications — they cannot escalate a standard binary, env var, or domain. A binary not in the builtin high-risk set stays at +3 even if listed in `acknowledged_bins`.

The builtin `trustedDomains` map covers the standard vendor surfaces (GitHub: `api.github.com`, `github.com`, `raw.githubusercontent.com`, `patch-diff.githubusercontent.com`, `gist.githubusercontent.com`, `objects.githubusercontent.com`; LLM providers: `api.openai.com`, `chatgpt.com`, `api.anthropic.com`, `api.together.ai`, `api.cohere.com`, `api.tavily.com`; channels: `api.slack.com`, `hooks.slack.com`, `api.telegram.org`; cloud APIs: `googleapis.com`). Per-agent acknowledgements (custom LLM gateways, internal services) go in `trusted_domains:` on a policy file.

The env-category score is capped at 25 points so multi-purpose skills declaring many config-knob env vars don't have their aggregate score dominated by a single axis. Per-item factors are still emitted in the audit report — only the points contribution is capped.

### Audit output

Each `RiskFactor` records the override in its description, so policy-driven downgrades are visible in both text and JSON output:

```
risky-skill                  Risk: medium (30/100)
  Factors:
    egress   +2   trusted domain (via policy): internal.example.com
    binary   +3   high-risk binary (acknowledged by policy): python
    env      +5   sensitive variable (acknowledged by policy): DB_PASSWORD
    script   +20  has executable script
```

Tooling can match on the substrings `(via policy)` or `(acknowledged by policy)` to flag policy-driven downgrades for review.

## Skill Builder (Web UI)

The [Web Dashboard](../reference/web-dashboard.md#skill-builder) includes an AI-powered Skill Builder that generates valid SKILL.md files and helper scripts through a conversational interface. It uses a [workspace-level LLM](../ui/skill-builder-llm.md) (independent of any specific agent's runtime LLM) and includes server-side validation before saving to the agent's `skills/` directory. On save, the builder automatically parses the skill's requirements and:

- **Merges egress domains** into `forge.yaml` `egress.allowed_domains` (deduplicated)
- **Writes user-provided env vars** to `.env` (skipping keys already present)
- **Reports missing env vars** so the user can provide values and re-save
