#!/usr/bin/env bash
# github-push.sh — Push a feature branch to the remote.
# Usage: ./github-push.sh '{"project_dir": "my-app", "branch": "feat/my-change"}'
#
# Requires: git, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-push.sh {\"project_dir\": \"...\", \"branch\": \"...\"}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

PROJECT_DIR=$(printf '%s' "$INPUT" | jq -r '.project_dir // empty')
BRANCH=$(printf '%s' "$INPUT" | jq -r '.branch // empty')

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

# Default to current branch
if [ -z "$BRANCH" ]; then
  BRANCH=$(git branch --show-current)
fi

# --- Protected branch guard ---
case "$BRANCH" in
  main|master)
    echo '{"error": "refusing to push to protected branch: '"$BRANCH"'. Use a feature branch instead."}' >&2
    exit 1
    ;;
esac

# --- Configure git credential helper for GH_TOKEN ---
if [ -n "${GH_TOKEN:-}" ]; then
  git -c credential.helper="!f() { echo \"protocol=https\"; echo \"host=github.com\"; echo \"username=x-access-token\"; echo \"password=$GH_TOKEN\"; }; f" \
    push -u origin "$BRANCH" --quiet 2>&1
else
  git push -u origin "$BRANCH" --quiet 2>&1
fi

SHA=$(git rev-parse --short HEAD)
REMOTE=$(git remote get-url origin 2>/dev/null || echo "origin")

jq -n \
  --arg status "pushed" \
  --arg branch "$BRANCH" \
  --arg sha "$SHA" \
  --arg remote "$REMOTE" \
  '{status: $status, branch: $branch, sha: $sha, remote: $remote}'
