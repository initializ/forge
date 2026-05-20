---
name: code-plan
icon: 🗺️
category: developer
tags:
  - planning
  - code-generation
  - ticket-to-pr
  - llm-powered
description: Turn a task description and repository into a structured implementation plan (files to create, files to modify, tests to add, risks).
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
        - git
      env:
        required: []
        one_of:
          - ANTHROPIC_API_KEY
          - OPENAI_API_KEY
        optional:
          - PLAN_MODEL
          - PLAN_MAX_REPO_SIGNAL_BYTES
    egress_domains:
      - api.anthropic.com
      - api.openai.com
    timeout_hint: 180
    guardrails:
      deny_output:
        - pattern: 'sk-[A-Za-z0-9]{20,}'
          action: redact
        - pattern: 'sk-ant-[A-Za-z0-9\-]+'
          action: redact
---

# Code Plan Skill

Generate a structured plan for implementing a task in a specific repository. The plan is the bridge between "I have a ticket" and "let me start writing code" — it lists files to create, files to modify, tests to add, and risks before any code is written.

This skill produces JSON. It does **not** write code; that is `code_agent_*`'s job.

## When to use this skill

- The user provides a Linear ticket, GitHub issue, or free-form task description and asks you to implement it.
- **Before** any `code_agent_write`, `code_agent_edit`, or `github_commit` tool call.
- When the user explicitly asks for a plan.

## When NOT to use this skill

- Tiny single-file edits (1–3 lines). Just edit directly.
- Pure exploration tasks. Use `grep_search` / `directory_tree` instead.
- Pure scaffolding tasks (e.g. "create a new React app"). Use `code_agent_scaffold` instead.

## Plan-then-execute discipline

Once a plan is generated, present its `summary` and `files_to_modify` / `files_to_create` lists to the user before writing code. Do NOT proceed to implementation if the plan returns `complexity: "high"` or non-empty `risks` without acknowledging them to the user. The plan is a contract: subsequent code-writing tool calls should match files listed in the plan. If the plan turns out to be wrong, regenerate it rather than silently drifting from it.

## Workflow integration

Canonical sequence for a ticket-to-PR flow:

```
linear_get_issue
  → code_plan_create                  [present summary + risks to user]
  → code_agent_write / code_agent_edit
  → code_plan_validate                [optional sanity check before commit]
  → github_commit
  → github_create_pr
```

## Repo signal extraction (informational)

`code_plan_create` automatically samples the repository so the LLM has enough context to plan without you pre-reading files. It collects:

1. The top entries of `git ls-files` (the repo tree).
2. The contents of detected manifest files: `package.json`, `go.mod`, `pyproject.toml`, `Cargo.toml`, `pom.xml`, `build.gradle*`.
3. The first ~4 KB of `README.md` if present.

The total signal is size-bounded (default 256 KB, override via `PLAN_MAX_REPO_SIGNAL_BYTES`). If the repo is too large to fit, the tool returns `status: "repo_too_large"` rather than truncating silently — pass `context_files` to scope the plan to the relevant files, or call from a subdirectory.

Just pass `repo_path`. Do not pre-read the repo with `grep_search` before calling this tool.

## Tool: code_plan_create

Generate a structured implementation plan for a task in a repository. One LLM call, one plan.

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| repo_path | string | yes | Absolute or `~`-prefixed path to the repo. Required because scripts run in the agent's working directory, not the user's project. |
| task | string | yes | Task description — Linear ticket body verbatim, or a free-form ask. Markdown ok. |
| context_files | string[] | no | Repo-relative paths to include in full as additional context (e.g. `["src/auth/login.go"]`). Capped at 5 files, 50 KB total. |
| target_branch | string | no | Branch name being worked on (informational, helps the LLM pick consistent naming). |
| ticket_id | string | no | Linear identifier or GitHub issue number. Echoed back in the output for traceability. |

**Output — success:**

```json
{
  "status": "ok",
  "ticket_id": "ENG-123",
  "stack_detected": "go",
  "summary": "One-paragraph plain-English description of the planned change.",
  "approach": "Higher-level reasoning: why this approach, what alternatives were considered.",
  "files_to_create": [
    { "path": "src/billing/invoice.go", "purpose": "New domain type and constructor for Invoice." }
  ],
  "files_to_modify": [
    { "path": "src/api/routes.go", "change": "Register /invoices route group, wire to handler." }
  ],
  "tests_to_add": [
    { "path": "src/billing/invoice_test.go", "covers": "Construction, validation, JSON round-trip." }
  ],
  "risks": [
    { "severity": "medium", "risk": "Existing customers will see a 422 on legacy payloads.", "mitigation": "Add a feature flag and default to the new behavior only for accounts created after launch date." }
  ],
  "complexity": "medium",
  "estimated_file_count": 3,
  "open_questions": [
    "Should invoice numbers be globally unique or per-customer?"
  ]
}
```

`complexity` is one of `low`, `medium`, `high`. `risks[].severity` is one of `low`, `medium`, `high`.

**Output — repo too large:**

```json
{
  "status": "repo_too_large",
  "tree_bytes": 524288,
  "limit_bytes": 262144,
  "suggestion": "Pass context_files explicitly to scope the plan, or run from a subdirectory."
}
```

**Output — no recognizable stack:**

If no manifest (`package.json`, `go.mod`, etc.) is found, the plan is still attempted but `stack_detected: "unknown"` is set and an explicit risk is added noting that the stack could not be inferred.

## Tool: code_plan_validate

Filesystem-only audit of a previously-generated plan against the current repo state. **No LLM call.** Cheap and idempotent — call it any time a plan was generated more than a few minutes ago, after pulling from remote, or before committing.

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| repo_path | string | yes | Path to the repo. |
| plan | object | yes | A previously-generated plan object (or its `files_to_create` / `files_to_modify` subset). |

**Output:**

```json
{
  "status": "ok",
  "files_to_modify_exist": [
    { "path": "src/api/routes.go", "exists": true }
  ],
  "files_to_create_collisions": [
    { "path": "src/billing/invoice.go", "exists": false }
  ],
  "warnings": [
    "files_to_modify[2] (src/legacy/old.go) does not exist in the repo; the plan may be stale."
  ]
}
```

A `files_to_modify` entry whose path does not exist becomes a warning. A `files_to_create` entry whose path already exists becomes a warning too — the plan would clobber existing code. Empty `warnings` means the plan still matches the repo.

## Safety constraints

- This skill never writes to the filesystem (`writes_files: false`). It returns JSON; downstream skills consume it.
- Repo content is sent to the configured LLM provider; egress is restricted to `api.anthropic.com` and `api.openai.com`.
- API keys are read from env vars, never logged. `guardrails.deny_output` redacts any key that leaks through an error payload.
- Repo signal is **size-bounded**. Over budget → `repo_too_large`, never silent truncation.
- One LLM call per `code_plan_create` invocation (plus one retry if the response fails schema validation).
