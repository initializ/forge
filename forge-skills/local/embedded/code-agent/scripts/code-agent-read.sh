#!/usr/bin/env bash
# code-agent-read.sh — Read a file or list a project directory.
# Usage: ./code-agent-read.sh '{"project_dir": "my-app", "file_path": "src/App.jsx"}'
#        ./code-agent-read.sh '{"project_dir": "my-app", "file_path": "."}'
#
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: code-agent-read.sh {\"project_dir\": \"...\", \"file_path\": \"...\"}"}' >&2
  exit 1
fi

if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Extract fields ---
PROJECT_DIR=$(printf '%s' "$INPUT" | jq -r '.project_dir // empty')
FILE_PATH=$(printf '%s' "$INPUT" | jq -r '.file_path // empty')
OFFSET=$(printf '%s' "$INPUT" | jq -r '.offset // 1')
LIMIT=$(printf '%s' "$INPUT" | jq -r '.limit // 300')

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

# --- Resolve project_dir (relative paths resolve within workspace/) ---
# Strip workspace/ prefix if present (avoids double-prefix when LLM passes "workspace/foo")
PROJECT_DIR="${PROJECT_DIR#workspace/}"
if [ "${PROJECT_DIR:0:1}" != "/" ]; then
  PROJECT_DIR="$(pwd)/workspace/$PROJECT_DIR"
fi

if [ ! -d "$PROJECT_DIR" ]; then
  echo "{\"error\": \"project directory not found: $PROJECT_DIR\"}" >&2
  exit 1
fi

RESOLVED_PROJECT=$(cd "$PROJECT_DIR" && pwd)

# --- Directory listing ---
if [ "$FILE_PATH" = "." ]; then
  FILES=$(cd "$RESOLVED_PROJECT" && find . -type f \
    ! -path './node_modules/*' \
    ! -path './.git/*' \
    ! -path './dist/*' \
    ! -path './__pycache__/*' \
    ! -path './venv/*' \
    ! -path './.venv/*' \
    ! -path './vendor/*' \
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

SIZE=$(wc -c < "$FULL_PATH" | tr -d ' ')
TOTAL_LINES=$(wc -l < "$FULL_PATH" | tr -d ' ')
# Ensure TOTAL_LINES is at least 1 for non-empty files
if [ "$TOTAL_LINES" -eq 0 ] && [ "$SIZE" -gt 0 ]; then
  TOTAL_LINES=1
fi

END=$((OFFSET + LIMIT - 1))
if [ "$END" -gt "$TOTAL_LINES" ]; then
  END=$TOTAL_LINES
fi

CONTENT=$(sed -n "${OFFSET},${END}p" "$FULL_PATH")
TRUNCATED="false"
if [ "$END" -lt "$TOTAL_LINES" ]; then
  TRUNCATED="true"
  CONTENT="${CONTENT}

[FILE TRUNCATED: showing lines ${OFFSET}-${END} of ${TOTAL_LINES}. Use offset/limit to read other sections.]"
fi

MODIFIED=$(stat -f '%Sm' -t '%Y-%m-%dT%H:%M:%SZ' "$FULL_PATH" 2>/dev/null || stat --format='%y' "$FULL_PATH" 2>/dev/null || echo "unknown")

jq -n \
  --arg path "$FILE_PATH" \
  --arg content "$CONTENT" \
  --argjson size "$SIZE" \
  --argjson total_lines "$TOTAL_LINES" \
  --argjson offset "$OFFSET" \
  --argjson limit "$LIMIT" \
  --argjson truncated "$TRUNCATED" \
  --arg modified "$MODIFIED" \
  '{path: $path, content: $content, size: $size, total_lines: $total_lines, offset: $offset, limit: $limit, truncated: $truncated, modified: $modified}'
