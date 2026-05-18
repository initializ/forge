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

## Security Audit

`forge skills audit` scores each skill in the project across four categories — egress, binary, env, script — and runs a `SecurityPolicy` check for hard violations. By default it uses the analyzer's `DefaultPolicy`. A custom policy YAML can be supplied with `--policy`.

```bash
# Audit skills in the default flat layout (skills/SKILL.md).
forge skills audit

# Audit subdirectory-style skills (skills/<name>/SKILL.md).
forge skills audit --dir skills

# JSON output for tooling.
forge skills audit --dir skills --format json

# Load a custom policy that adjusts both scoring and policy checks.
forge skills audit --dir skills --policy policy.yaml
```

### Policy YAML

```yaml
# policy.yaml
script_policy: allow              # allow | warn (default) | deny
max_risk_score: 75                # PolicyViolation if exceeded

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

The [Web Dashboard](../reference/web-dashboard.md#skill-builder) includes an AI-powered Skill Builder that generates valid SKILL.md files and helper scripts through a conversational interface. It uses the agent's own LLM provider and includes server-side validation before saving to the agent's `skills/` directory. On save, the builder automatically parses the skill's requirements and:

- **Merges egress domains** into `forge.yaml` `egress.allowed_domains` (deduplicated)
- **Writes user-provided env vars** to `.env` (skipping keys already present)
- **Reports missing env vars** so the user can provide values and re-save
