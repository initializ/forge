#!/usr/bin/env bash
# github-clone.sh — Clone a GitHub repository and create a feature branch.
# Usage: ./github-clone.sh '{"repo": "owner/repo", "branch": "feat/my-change", "project_dir": "my-app"}'
#
# Requires: gh, git, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-clone.sh {\"repo\": \"owner/repo\", \"branch\": \"...\", \"project_dir\": \"...\"}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

REPO=$(printf '%s' "$INPUT" | jq -r '.repo // empty')
BRANCH=$(printf '%s' "$INPUT" | jq -r '.branch // empty')
PROJECT_DIR=$(printf '%s' "$INPUT" | jq -r '.project_dir // empty')

if [ -z "$REPO" ]; then
  echo '{"error": "repo is required (e.g. owner/repo, git@github.com:owner/repo.git, or https://github.com/owner/repo.git)"}' >&2
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

# Default project_dir to the repo name portion
if [ -z "$PROJECT_DIR" ]; then
  PROJECT_DIR=$(basename "$REPO")
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
mkdir -p "$WORKSPACE"
TARGET="$WORKSPACE/$PROJECT_DIR"

if [ -d "$TARGET" ]; then
  echo "{\"error\": \"directory already exists: workspace/$PROJECT_DIR\"}" >&2
  exit 1
fi

# --- Clone via gh (uses GH_TOKEN automatically) ---
if ! gh repo clone "$REPO" "$TARGET" -- --quiet 2>/dev/null; then
  echo "{\"error\": \"failed to clone $REPO\"}" >&2
  exit 1
fi

cd "$TARGET"

# --- Create feature branch ---
if [ -z "$BRANCH" ]; then
  BRANCH="forge/$(date +%Y%m%d)-$(openssl rand -hex 3)"
fi

# Refuse to stay on main/master
git checkout -b "$BRANCH" --quiet

# --- Configure git user at repo level ---
git config user.email "266392669+useforgeai@users.noreply.github.com"
git config user.name "Forge Agent"

jq -n \
  --arg status "cloned" \
  --arg repo "$REPO" \
  --arg branch "$BRANCH" \
  --arg project_dir "$PROJECT_DIR" \
  '{status: $status, repo: $repo, branch: $branch, project_dir: $project_dir}'
