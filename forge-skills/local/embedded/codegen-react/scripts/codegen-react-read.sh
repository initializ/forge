#!/usr/bin/env bash
# codegen-react-read.sh — Read a file or list a React project directory.
# Usage: ./codegen-react-read.sh '{"project_dir": "/tmp/my-app", "file_path": "src/App.jsx"}'
#
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: codegen-react-read.sh {\"project_dir\": \"...\", \"file_path\": \"...\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Extract fields ---
PROJECT_DIR=$(echo "$INPUT" | jq -r '.project_dir // empty')
FILE_PATH=$(echo "$INPUT" | jq -r '.file_path // empty')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi
if [ -z "$FILE_PATH" ]; then
  echo '{"error": "file_path is required"}' >&2
  exit 1
fi

# --- Path traversal prevention ---
case "$FILE_PATH" in
  /*|*..*)
    echo '{"error": "file_path must be relative and must not contain .."}' >&2
    exit 1
    ;;
esac

if [ ! -d "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir does not exist"}' >&2
  exit 1
fi

# Resolve and verify the target stays within project_dir
RESOLVED_PROJECT=$(cd "$PROJECT_DIR" && pwd)

# --- Directory listing ---
if [ "$FILE_PATH" = "." ]; then
  FILES=$(cd "$RESOLVED_PROJECT" && find . -type f \
    ! -path './node_modules/*' \
    ! -path './.git/*' \
    ! -path './dist/*' \
    -maxdepth 5 | sed 's|^\./||' | sort)
  echo "$FILES" | jq -R -s '{
    path: ".",
    type: "directory",
    files: (split("\n") | map(select(length > 0)))
  }'
  exit 0
fi

# --- File read ---
FULL_PATH="$RESOLVED_PROJECT/$FILE_PATH"

# Verify resolved path is still under project dir
RESOLVED_FULL=$(cd "$(dirname "$FULL_PATH")" 2>/dev/null && pwd)/$(basename "$FULL_PATH") 2>/dev/null || true
case "$RESOLVED_FULL" in
  "$RESOLVED_PROJECT"/*)
    ;;
  *)
    echo '{"error": "file_path resolves outside project_dir"}' >&2
    exit 1
    ;;
esac

if [ ! -f "$FULL_PATH" ]; then
  echo '{"error": "file not found: '"$FILE_PATH"'"}' >&2
  exit 1
fi

CONTENT=$(cat "$FULL_PATH")
SIZE=$(wc -c < "$FULL_PATH" | tr -d ' ')
MODIFIED=$(stat -f '%Sm' -t '%Y-%m-%dT%H:%M:%SZ' "$FULL_PATH" 2>/dev/null || stat --format='%y' "$FULL_PATH" 2>/dev/null || echo "unknown")

jq -n \
  --arg path "$FILE_PATH" \
  --arg content "$CONTENT" \
  --arg size "$SIZE" \
  --arg modified "$MODIFIED" \
  '{path: $path, content: $content, size: ($size | tonumber), modified: $modified}'
