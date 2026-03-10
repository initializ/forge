#!/usr/bin/env bash
# github-commit.sh — Stage and commit changes on a feature branch.
# Usage: ./github-commit.sh '{"project_dir": "my-app", "message": "fix: resolve login bug", "files": ["src/auth.go"]}'
#
# Requires: git, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-commit.sh {\"project_dir\": \"...\", \"message\": \"...\", \"files\": [...]}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

PROJECT_DIR=$(printf '%s' "$INPUT" | jq -r '.project_dir // empty')
MESSAGE=$(printf '%s' "$INPUT" | jq -r '.message // empty')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi
if [ -z "$MESSAGE" ]; then
  echo '{"error": "message is required"}' >&2
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

# --- Protected branch guard ---
BRANCH=$(git branch --show-current)
case "$BRANCH" in
  main|master)
    echo '{"error": "refusing to commit on protected branch: '"$BRANCH"'. Create or switch to a feature branch first."}' >&2
    exit 1
    ;;
esac

# --- Configure git user (repo-level, idempotent) ---
git config user.email "266392669+useforgeai@users.noreply.github.com"
git config user.name "Forge Agent"

# --- Stage files ---
# Normalize: accept files as a string (single file, space-separated, or newline-separated) or array.
# The LLM sometimes passes "files": "path" or "files": "a b c" instead of "files": ["a","b","c"].
NORMALIZED_INPUT=$(printf '%s' "$INPUT" | jq '
  if (.files | type) == "string" then
    .files = (.files | split("\n") | map(split(" ")) | flatten | map(select(length > 0)))
  else . end')
HAS_FILES=$(printf '%s' "$NORMALIZED_INPUT" | jq 'has("files") and (.files | length > 0)')
if [ "$HAS_FILES" = "true" ]; then
  # Stage specific files
  while IFS= read -r file; do
    # Validate each file path
    case "$file" in
      /*|*..*)
        echo "{\"error\": \"file path must be relative and must not contain ..: $file\"}" >&2
        exit 1
        ;;
    esac
    git add -- "$file"
  done < <(printf '%s' "$NORMALIZED_INPUT" | jq -r '.files[]')
else
  # Stage all changes
  git add -A
fi

# --- Check for staged changes ---
if git diff --cached --quiet; then
  echo '{"error": "no changes staged to commit"}' >&2
  exit 1
fi

# --- Append co-authored-by trailer ---
FULL_MESSAGE="$MESSAGE

Co-authored-by: Forge Agent <266392669+useforgeai@users.noreply.github.com>"

# --- Commit ---
git commit -m "$FULL_MESSAGE" --quiet

SHA=$(git rev-parse --short HEAD)
FILES_CHANGED=$(git diff-tree --no-commit-id --name-only -r HEAD | wc -l | tr -d ' ')

jq -n \
  --arg sha "$SHA" \
  --arg branch "$BRANCH" \
  --arg files_changed "$FILES_CHANGED" \
  '{sha: $sha, branch: $branch, files_changed: ($files_changed | tonumber)}'
