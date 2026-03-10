#!/usr/bin/env bash
# github-checkout.sh — Switch or create a branch in a project.
# Usage: ./github-checkout.sh '{"project_dir": "my-app", "branch": "feat/new-feature", "create": true}'
#
# Requires: git, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-checkout.sh {\"project_dir\": \"...\", \"branch\": \"...\", \"create\": true|false}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

PROJECT_DIR=$(printf '%s' "$INPUT" | jq -r '.project_dir // empty')
BRANCH=$(printf '%s' "$INPUT" | jq -r '.branch // empty')
CREATE=$(printf '%s' "$INPUT" | jq -r '.create // false')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi
if [ -z "$BRANCH" ]; then
  echo '{"error": "branch is required"}' >&2
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
case "$BRANCH" in
  main|master)
    echo '{"error": "refusing to switch to protected branch: '"$BRANCH"'. Stay on a feature branch."}' >&2
    exit 1
    ;;
esac

# --- Checkout ---
if [ "$CREATE" = "true" ]; then
  git checkout -b "$BRANCH" --quiet
else
  git checkout "$BRANCH" --quiet
fi

jq -n \
  --arg status "switched" \
  --arg branch "$BRANCH" \
  '{status: $status, branch: $branch}'
