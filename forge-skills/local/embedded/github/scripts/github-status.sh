#!/usr/bin/env bash
# github-status.sh — Show git status for a project.
# Usage: ./github-status.sh '{"project_dir": "my-app"}'
#
# Requires: git, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-status.sh {\"project_dir\": \"...\"}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

PROJECT_DIR=$(printf '%s' "$INPUT" | jq -r '.project_dir // empty')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi

# --- Path traversal prevention ---
case "$PROJECT_DIR" in
  /*|*..*)
    echo '{"error": "project_dir must be relative and must not contain .."}' >&2
    exit 1
    ;;
esac

# --- Resolve workspace ---
# Strip workspace/ prefix if present (avoids double-prefix)
PROJECT_DIR="${PROJECT_DIR#workspace/}"
WORKSPACE="$(pwd)/workspace"
TARGET="$WORKSPACE/$PROJECT_DIR"

if [ ! -d "$TARGET/.git" ]; then
  echo "{\"error\": \"not a git repository: workspace/$PROJECT_DIR\"}" >&2
  exit 1
fi

cd "$TARGET"

# --- Gather status ---
BRANCH=$(git branch --show-current)

# Modified (unstaged)
MODIFIED=$(git diff --name-only 2>/dev/null | jq -R -s 'split("\n") | map(select(. != ""))')

# Staged
STAGED=$(git diff --cached --name-only 2>/dev/null | jq -R -s 'split("\n") | map(select(. != ""))')

# Untracked
UNTRACKED=$(git ls-files --others --exclude-standard 2>/dev/null | jq -R -s 'split("\n") | map(select(. != ""))')

# Ahead/behind upstream (if tracking branch exists)
AHEAD=0
BEHIND=0
if git rev-parse --abbrev-ref '@{upstream}' >/dev/null 2>&1; then
  AHEAD=$(git rev-list --count '@{upstream}..HEAD' 2>/dev/null || echo 0)
  BEHIND=$(git rev-list --count 'HEAD..@{upstream}' 2>/dev/null || echo 0)
fi

jq -n \
  --arg branch "$BRANCH" \
  --argjson modified "$MODIFIED" \
  --argjson staged "$STAGED" \
  --argjson untracked "$UNTRACKED" \
  --arg ahead "$AHEAD" \
  --arg behind "$BEHIND" \
  '{branch: $branch, modified: $modified, staged: $staged, untracked: $untracked, ahead: ($ahead | tonumber), behind: ($behind | tonumber)}'
