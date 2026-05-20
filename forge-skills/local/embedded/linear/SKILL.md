---
name: linear
icon: 📋
category: project-management
tags:
  - linear
  - issue-tracking
  - tickets
  - workflow
description: Read Linear issues, transition state, and post comments. The entry point for ticket-driven agent workflows.
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
      env:
        required:
          - LINEAR_API_KEY
        one_of: []
        optional:
          - LINEAR_DEFAULT_TEAM_ID
    egress_domains:
      - api.linear.app
    timeout_hint: 60
    guardrails:
      deny_output:
        - pattern: '"apiKey"\s*:\s*"[^"]+"'
          action: redact
        - pattern: 'Bearer\s+lin_api_[A-Za-z0-9]+'
          action: redact
---

# Linear Skill

You can read, search, and update Linear issues, list workflow states, and post comments. Use this skill as the entry point for ticket-driven workflows — turn `ENG-123` into a structured payload, transition state, comment on progress.

## When to use this skill

- The user references a Linear issue by identifier (`ENG-123`, `OPS-7`, etc.).
- The user asks for "my tickets" / "open issues" / "what's assigned to me".
- An automation needs to mark work started, post a PR link as a comment, or move a ticket through workflow states.

## Issue identifier format

Linear identifiers look like `<TEAM_KEY>-<NUMBER>` — `ENG-123`, `OPS-7`. They are **not** GraphQL UUIDs. The Linear API accepts both forms on `issue(id:)`, so always pass the human identifier the user gave you. Never invent or normalise identifiers — use them verbatim.

## State transition pattern (hard rule)

You MUST call `linear_get_workflow_states` first and use the resolved `id` when calling `linear_update_issue_state`. State IDs are per-team UUIDs; state names like `"Todo"` or `"In Progress"` are not portable across teams and cannot be passed to `linear_update_issue_state` directly.

Workflow:

1. `linear_get_issue` to find the issue's `team.id`.
2. `linear_get_workflow_states` with that `team_id` to enumerate the team's states.
3. Pick the state by matching its `name` (case-insensitive) or `type` (`unstarted`, `started`, `completed`, `canceled`, `triage`, `backlog`).
4. `linear_update_issue_state` with the issue identifier and the resolved `state_id`.

## Commenting etiquette

Post a comment when:

- Work has been picked up (one comment, e.g. `"Working on this."`).
- A pull request has been opened (one comment with the PR URL).
- Work is blocked and the user should know.
- Work is complete (one comment summarising what shipped).

Do not chatter. Post no more than **one comment per agent action**. Do not narrate intermediate tool calls, file edits, or reasoning into Linear comments — the channel-side conversation is the place for that. Comments are durable artifacts on the ticket; treat them like git commit messages, not Slack messages.

## Safety rules

- Never include secrets, tokens, or environment variable values in comment bodies.
- Never transition an issue to a "Done" or "Completed" state without explicit user confirmation.
- Never delete issues, comments, or projects through this skill — the tools don't support it. If the user asks, tell them this skill is read/comment/transition only.
- When the user asks for "my tickets", default to state filter `"Todo"` or `"In Progress"` (state types `unstarted`, `started`) unless they specify otherwise.

## Tool: linear_get_issue

Fetch a Linear issue by its human identifier.

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| identifier | string | yes | Issue identifier like `ENG-123` |

**Output:**

```json
{
  "identifier": "ENG-123",
  "title": "...",
  "description": "...",
  "state": { "id": "...", "name": "In Progress", "type": "started" },
  "assignee": { "email": "...", "name": "..." },
  "team": { "id": "...", "key": "ENG", "name": "Engineering" },
  "labels": ["bug", "p1"],
  "priority": 2,
  "url": "https://linear.app/..."
}
```

## Tool: linear_search_issues

Filter issues across one team — or across **all teams** the API key can see, if no team is supplied. **All parameters are optional.** Call this tool with `{}` when the user asks something broad like "list open issues" or "what's in the backlog" — do not ask for a team_id first; the result will include the team key in each issue's identifier (e.g. `ENG-12` vs `OPS-7`) and the user can drill down from there.

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| team_id | string | no | Linear team UUID **or** team key like `ENG`. Auto-detects which form. Defaults to `$LINEAR_DEFAULT_TEAM_ID` when set. |
| state | string | no | Workflow state type (`unstarted`, `started`, `completed`, `canceled`, `triage`, `backlog`). |
| assignee_email | string | no | Filter by assignee email. |
| label | string | no | Filter by label name. |
| query | string | no | Free-text search over title and description. |
| limit | integer | no | Max results (default 20, capped at 100). |

**Examples — call the tool directly, do not ask the user for an ID:**

| User says | Tool input |
| --- | --- |
| "list open issues" / "what's in the backlog" | `{}` |
| "show me INI tickets" | `{"team_id": "INI"}` (team key works) |
| "open bugs in ENG" | `{"team_id": "ENG", "state": "unstarted", "label": "bug"}` |
| "what's @alice working on?" | `{"assignee_email": "alice@example.com", "state": "started"}` |

**Output:**

```json
{
  "count": 3,
  "issues": [
    { "identifier": "ENG-1", "title": "...", "state": "Todo", "assignee_email": "...", "url": "..." }
  ]
}
```

## Tool: linear_list_my_issues

List issues assigned to the API key's owner (`viewer`).

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| state | string | no | Comma-separated state types. Default: `"started,unstarted"` ("In Progress" / "Todo"). |
| limit | integer | no | Max results (default 20, capped at 100). |

**Output:** same shape as `linear_search_issues`.

## Tool: linear_get_workflow_states

Enumerate a team's workflow states. **Required before `linear_update_issue_state`.**

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| team_id | string | yes | Linear team UUID (from `linear_get_issue`'s `team.id`). |

**Output:**

```json
{
  "team_id": "...",
  "states": [
    { "id": "...", "name": "Todo",        "type": "unstarted", "position": 0 },
    { "id": "...", "name": "In Progress", "type": "started",   "position": 1 },
    { "id": "...", "name": "Done",        "type": "completed", "position": 2 }
  ]
}
```

States are sorted by `position`.

## Tool: linear_update_issue_state

Transition an issue to a different workflow state. The `state_id` must come from `linear_get_workflow_states` — do not invent or guess it.

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| identifier | string | yes | Issue identifier like `ENG-123`. |
| state_id | string | yes | Workflow state UUID from `linear_get_workflow_states`. |

**Output:**

```json
{
  "success": true,
  "identifier": "ENG-123",
  "state": { "id": "...", "name": "In Progress", "type": "started" }
}
```

## Tool: linear_add_comment

Post a markdown comment to an issue.

**Input:**

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| identifier | string | yes | Issue identifier like `ENG-123`. |
| body | string | yes | Markdown comment body. Capped at 10 000 characters; longer bodies are truncated client-side. |

**Output:**

```json
{
  "success": true,
  "comment": { "id": "...", "url": "https://linear.app/...", "created_at": "2026-05-20T..." }
}
```
