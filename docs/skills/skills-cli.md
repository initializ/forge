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

## Skill Builder (Web UI)

The [Web Dashboard](dashboard.md#skill-builder) includes an AI-powered Skill Builder that generates valid SKILL.md files and helper scripts through a conversational interface. It uses the agent's own LLM provider and includes server-side validation before saving to the agent's `skills/` directory. On save, the builder automatically parses the skill's requirements and:

- **Merges egress domains** into `forge.yaml` `egress.allowed_domains` (deduplicated)
- **Writes user-provided env vars** to `.env` (skipping keys already present)
- **Reports missing env vars** so the user can provide values and re-save
