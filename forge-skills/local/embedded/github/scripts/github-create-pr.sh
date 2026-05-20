#!/usr/bin/env bash
# github-create-pr.sh — Create a pull request on GitHub.
# Usage: ./github-create-pr.sh '{"repo": "owner/repo", "title": "Fix bug", "body": "Description", "head": "feat/branch", "base": "main"}'
#
# Requires: gh, git, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-create-pr.sh {\"repo\": \"...\", \"title\": \"...\", \"body\": \"...\", \"head\": \"...\", \"base\": \"...\"}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

REPO=$(printf '%s' "$INPUT" | jq -r '.repo // empty')
TITLE=$(printf '%s' "$INPUT" | jq -r '.title // empty')
BODY=$(printf '%s' "$INPUT" | jq -r '.body // empty')
HEAD=$(printf '%s' "$INPUT" | jq -r '.head // empty')
BASE=$(printf '%s' "$INPUT" | jq -r '.base // empty')
TICKET_ID=$(printf '%s' "$INPUT" | jq -r '.ticket_id // empty')
TICKET_URL=$(printf '%s' "$INPUT" | jq -r '.ticket_url // empty')

if [ -z "$REPO" ]; then
  echo '{"error": "repo is required"}' >&2
  exit 1
fi
if [ -z "$TITLE" ]; then
  echo '{"error": "title is required"}' >&2
  exit 1
fi
if [ -z "$HEAD" ]; then
  echo '{"error": "head branch is required"}' >&2
  exit 1
fi

# --- Normalize repo format ---
# Convert SSH URL: git@github.com:owner/repo.git → owner/repo
if [[ "$REPO" == git@github.com:* ]]; then
  REPO="${REPO#git@github.com:}"
  REPO="${REPO%.git}"
fi
# Convert HTTPS URL: https://github.com/owner/repo.git → owner/repo
if [[ "$REPO" == https://github.com/* ]]; then
  REPO="${REPO#https://github.com/}"
  REPO="${REPO%.git}"
fi

# Default base branch to main
if [ -z "$BASE" ]; then
  BASE="main"
fi

# --- Apply ticket-driven conventions ---
# When ticket_id is provided, suffix the title with [<ticket_id>] if not
# already present, and append a "Tracks:" back-link footer to the body.
# This lets callers pass an unmodified title/body and have the skill
# enforce the convention uniformly.
if [ -n "$TICKET_ID" ]; then
  case "$TITLE" in
    *"[$TICKET_ID]"*) ;;
    *) TITLE="$TITLE [$TICKET_ID]" ;;
  esac
  if [ -n "$TICKET_URL" ]; then
    BODY="$BODY

---
Tracks: [$TICKET_ID]($TICKET_URL)"
  else
    BODY="$BODY

---
Tracks: $TICKET_ID"
  fi
fi

# --- Create PR via gh CLI ---
PR_URL=$(gh pr create \
  --repo "$REPO" \
  --title "$TITLE" \
  --body "$BODY" \
  --head "$HEAD" \
  --base "$BASE" 2>&1)

if [ $? -ne 0 ]; then
  echo "{\"error\": \"failed to create PR: $PR_URL\"}" >&2
  exit 1
fi

jq -n \
  --arg url "$PR_URL" \
  --arg repo "$REPO" \
  --arg head "$HEAD" \
  --arg base "$BASE" \
  '{status: "created", url: $url, repo: $repo, head: $head, base: $base}'
