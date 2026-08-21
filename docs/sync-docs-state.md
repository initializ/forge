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
| **Anchor commit** | `TBD` (set when this sweep's PR merges) |
| **Anchor tag/context** | full sweep on top of v0.18.1 |
| **Last full sweep** | 2026-08-21 |

## How to run the next sweep

1. `git diff <anchor>..main --name-only` — the code that changed since the anchor.
2. Map each changed path to its docs via the table in the `/sync-docs` skill.
3. For each mapped doc, verify it reflects the change; edit where stale/missing.
4. Broken-link check (`grep` loop in the skill).
5. Update **Anchor commit** above to the new `main` HEAD and commit.

Between full sweeps, per-PR `/sync-docs` still updates docs inline with each
feature; this anchor is the backstop that catches anything a per-PR run missed.

## Baseline sweep (v0.17.1 → v0.18.1)

A full sweep was performed covering everything merged between `v0.17.1` (the
previous CHANGELOG rollover) and `v0.18.1`. Gaps found and filled are listed in
the PR that introduces this file.
