---
title: "Docs Sync State (rolling anchor)"
description: "Records the last commit up to which documentation was confirmed in sync, so /sync-docs can run incrementally."
---

This file is the **rolling anchor** for documentation sync. It records the last
commit at which a full docs sweep confirmed the `docs/` tree (and
`.claude/skills/forge.md`) reflect the code. The `/sync-docs` process reads it
so each run only has to cover changes **since** the anchor, not all of history.

## Current anchor

| Field | Value |
|-------|-------|
| **Anchor commit** | `2f0649e` (main HEAD audited by this sweep) |
| **Anchor tag/context** | v0.17.1 → v0.18.1 (120 commits / ~42 PRs) |
| **Last full sweep** | 2026-08-21 |

The next sweep only needs `git diff 2f0649e..main --name-only`.

## How to run the next sweep

1. `git diff <anchor>..main --name-only` — the code that changed since the anchor.
2. Map each changed path to its docs via the table in the `/sync-docs` skill.
3. For each mapped doc, verify it reflects the change; edit where stale/missing.
4. Broken-link check (`grep` loop in the skill).
5. Update **Anchor commit** above to the new `main` HEAD and commit.

Between full sweeps, per-PR `/sync-docs` still updates docs inline with each
feature; this anchor is the backstop that catches anything a per-PR run missed.

## Baseline sweep (v0.17.1 → v0.18.1)

A full sweep (5 parallel domain audits) covered everything merged between
`v0.17.1` (the previous CHANGELOG rollover) and `v0.18.1`. Gaps found and filled:

- **forge-yaml-schema.md** — added the entire `mcp:` block (auth types, grants, tools), the `apis:` block (per-op API tools), the `pdp:` block, and egress `allowed_tcp` / `allowed_private_cidrs`; added `defer` approver fields.
- **audit-logging.md** — added `llm_call_failed` + `mcp_auth_required/resolved/timeout` to the event catalog; documented always-on `fields.error` redaction, channel-sender attribution on `auth_verify`, and `llm_call.url` userinfo stripping.
- **defer-decisions.md** — corrected the stale "deferral abandoned / cap timeout ≤ 6m" note for the #402 task-detach (task now survives caller disconnect).
- **tools-and-builtins.md** — removed the deleted `openapi_call` adapter; documented per-op `apis.servers` → `<name>__<op>` tools (#400).
- **memory-system.md** — the `remote` session store attaches independent of `memory.persistence` (#372/#373).
- **platform-policy.md** — new managed **PDP** section (#399).
- **tenancy.md** — platform-callout `Org-Id`/`Workspace-Id` headers.
- **channels.md** — Slack raw-JSON suppression + `<<ctxzip:…>>` marker stripping (#384).
- **skill-md-format.md / writing-custom-skills.md** — fixed stale "runs via bash" → per-interpreter (#405 D2).
- **cli-reference.md** — added the `forge mcp` section and the startup version banner (#335/#336).
- **mcp/** — de-staled the "seven events" framing + added consent-gate events; tool-name regex (#370); delegated-token 5-min TTL cap (#380).
- **.claude/skills/forge.md** — de-staled R10 (delegated identity is implemented, not proposed; correct event names).
