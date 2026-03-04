#!/usr/bin/env bash
# codegen-html-write.sh — Write or update a file in an HTML project.
# Usage: ./codegen-html-write.sh '{"project_dir": "/tmp/my-app", "file_path": "index.html", "content": "..."}'
#
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: codegen-html-write.sh {\"project_dir\": \"...\", \"file_path\": \"...\", \"content\": \"...\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Extract fields ---
PROJECT_DIR=$(echo "$INPUT" | jq -r '.project_dir // empty')
FILE_PATH=$(echo "$INPUT" | jq -r '.file_path // empty')
CONTENT=$(echo "$INPUT" | jq -r '.content // empty')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi
if [ -z "$FILE_PATH" ]; then
  echo '{"error": "file_path is required"}' >&2
  exit 1
fi
if [ -z "$CONTENT" ]; then
  echo '{"error": "content is required"}' >&2
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
FULL_PATH="$RESOLVED_PROJECT/$FILE_PATH"

# Create parent directory
PARENT_DIR=$(dirname "$FULL_PATH")
mkdir -p "$PARENT_DIR"

# Verify resolved path is still under project dir
RESOLVED_PARENT=$(cd "$PARENT_DIR" && pwd)
case "$RESOLVED_PARENT" in
  "$RESOLVED_PROJECT"|"$RESOLVED_PROJECT"/*)
    ;;
  *)
    echo '{"error": "file_path resolves outside project_dir"}' >&2
    exit 1
    ;;
esac

# --- Determine action ---
ACTION="created"
if [ -f "$FULL_PATH" ]; then
  ACTION="updated"
fi

# --- Write file ---
echo "$INPUT" | jq -r '.content' > "$FULL_PATH"

SIZE=$(wc -c < "$FULL_PATH" | tr -d ' ')

jq -n \
  --arg path "$FILE_PATH" \
  --arg action "$ACTION" \
  --arg size "$SIZE" \
  '{path: $path, action: $action, size: ($size | tonumber)}'
