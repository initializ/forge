#!/usr/bin/env bash
# codegen-react-run.sh — Install dependencies and start the Vite dev server.
# Usage: ./codegen-react-run.sh '{"project_dir": "/tmp/my-app"}'
#
# Requires: node, npx, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: codegen-react-run.sh {\"project_dir\": \"...\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Extract fields ---
PROJECT_DIR=$(echo "$INPUT" | jq -r '.project_dir // empty')

if [ -z "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir is required"}' >&2
  exit 1
fi

if [ ! -d "$PROJECT_DIR" ]; then
  echo '{"error": "project_dir does not exist"}' >&2
  exit 1
fi

if [ ! -f "$PROJECT_DIR/package.json" ]; then
  echo '{"error": "no package.json found in project_dir — run codegen_react_scaffold first"}' >&2
  exit 1
fi

cd "$PROJECT_DIR"

# --- Install dependencies if needed ---
INSTALL_STATUS="skipped"
if [ ! -d "node_modules" ]; then
  INSTALL_STATUS="installed"
  if ! npm install --loglevel=error > .forge-install.log 2>&1; then
    INSTALL_ERR=$(tail -5 .forge-install.log 2>/dev/null || echo "unknown error")
    jq -n --arg err "npm install failed" --arg details "$INSTALL_ERR" \
      '{error: $err, details: $details}' >&2
    exit 1
  fi
fi

# --- Start dev server in background ---
DEV_LOG="$PROJECT_DIR/.forge-dev.log"
nohup npm run dev > "$DEV_LOG" 2>&1 &
DEV_PID=$!

# Wait for the server to start (check for "Local:" in Vite output)
SERVER_URL="http://localhost:3000"
for i in 1 2 3 4 5; do
  sleep 1
  if ! kill -0 "$DEV_PID" 2>/dev/null; then
    DEV_ERR=$(tail -5 "$DEV_LOG" 2>/dev/null || echo "process exited")
    jq -n --arg err "dev server failed to start" --arg details "$DEV_ERR" \
      '{error: $err, details: $details}' >&2
    exit 1
  fi
  # Try to extract actual URL from Vite output
  ACTUAL_URL=$(grep -o 'http://localhost:[0-9]*' "$DEV_LOG" 2>/dev/null | head -1 || true)
  if [ -n "$ACTUAL_URL" ]; then
    SERVER_URL="$ACTUAL_URL"
    break
  fi
done

jq -n \
  --arg status "running" \
  --arg url "$SERVER_URL" \
  --arg pid "$DEV_PID" \
  --arg project_dir "$PROJECT_DIR" \
  --arg install "$INSTALL_STATUS" \
  '{status: $status, url: $url, pid: ($pid | tonumber), project_dir: $project_dir, install: $install}'
