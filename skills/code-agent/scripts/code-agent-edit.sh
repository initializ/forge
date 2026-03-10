#!/usr/bin/env bash
# code-agent-edit.sh — Surgical string replacement in a project file.
# Usage: ./code-agent-edit.sh '{"project_dir": "my-app", "file_path": "src/App.jsx", "old_text": "Count: 0", "new_text": "Clicks: 0"}'
#
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: code-agent-edit.sh {\"project_dir\": \"...\", \"file_path\": \"...\", \"old_text\": \"...\", \"new_text\": \"...\"}"}' >&2
  exit 1
fi
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

PROJECT_DIR=$(echo "$INPUT" | jq -r '.project_dir // empty')
FILE_PATH=$(echo "$INPUT" | jq -r '.file_path // empty')
OLD_TEXT=$(echo "$INPUT" | jq -r '.old_text // empty')
NEW_TEXT=$(echo "$INPUT" | jq -r '.new_text // empty')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi
if [ -z "$FILE_PATH" ]; then
  echo '{"error": "file_path is required"}' >&2
  exit 1
fi
if [ -z "$OLD_TEXT" ]; then
  echo '{"error": "old_text is required"}' >&2
  exit 1
fi

# --- Path traversal prevention ---
case "$FILE_PATH" in
  /*|*..*)
    echo '{"error": "file_path must be relative and must not contain .."}' >&2
    exit 1
    ;;
esac

# --- Resolve project_dir ---
if [ "${PROJECT_DIR:0:1}" != "/" ]; then
  PROJECT_DIR="$(pwd)/workspace/$PROJECT_DIR"
fi

if [ ! -d "$PROJECT_DIR" ]; then
  echo "{\"error\": \"project directory not found: $PROJECT_DIR\"}" >&2
  exit 1
fi

RESOLVED_PROJECT=$(cd "$PROJECT_DIR" && pwd)
FULL_PATH="$RESOLVED_PROJECT/$FILE_PATH"

# Verify path stays within project
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

# --- Read original file ---
ORIGINAL=$(cat "$FULL_PATH")

# --- Count occurrences of old_text ---
# Use awk for reliable substring counting (handles special chars better than grep)
COUNT=$(awk -v pat="$OLD_TEXT" 'BEGIN{c=0} {while(i=index($0,pat)){c++;$0=substr($0,i+length(pat))}} END{print c}' "$FULL_PATH")

if [ "$COUNT" -eq 0 ]; then
  echo '{"error": "old_text not found in file"}' >&2
  exit 1
fi
if [ "$COUNT" -gt 1 ]; then
  jq -n --arg count "$COUNT" \
    '{error: "old_text found multiple times — be more specific to match exactly once", occurrences: ($count | tonumber)}' >&2
  exit 1
fi

# --- Perform replacement ---
# Use python for reliable multi-line string replacement (avoids sed escaping issues)
python3 -c "
import sys, json
inp = json.loads(sys.argv[1])
with open(sys.argv[2], 'r') as f:
    content = f.read()
content = content.replace(inp['old_text'], inp['new_text'], 1)
with open(sys.argv[2], 'w') as f:
    f.write(content)
" "$INPUT" "$FULL_PATH"

# --- Generate diff ---
DIFF=$(diff -u <(echo "$ORIGINAL") <(cat "$FULL_PATH") 2>/dev/null || true)
MODIFIED_SIZE=$(wc -c < "$FULL_PATH" | tr -d ' ')

jq -n \
  --arg path "$FILE_PATH" \
  --arg action "edited" \
  --arg size "$MODIFIED_SIZE" \
  --arg diff "$DIFF" \
  '{path: $path, action: $action, size: ($size | tonumber), diff: $diff}'
