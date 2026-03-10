#!/usr/bin/env bash
# code-agent-run.sh — Install dependencies and start the dev server.
# Detects project type (Node, Python, Go, Spring Boot, static HTML) automatically.
# Usage: ./code-agent-run.sh '{"project_dir": "my-app"}'
#
# Requires: jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: code-agent-run.sh {\"project_dir\": \"...\"}"}' >&2
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

# --- Resolve path ---
# Relative paths resolve within workspace/ subdirectory (where code-agent file tools operate).
# Strip workspace/ prefix if present (avoids double-prefix when LLM passes "workspace/foo")
PROJECT_DIR="${PROJECT_DIR#workspace/}"
if [ "${PROJECT_DIR:0:1}" != "/" ]; then
  PROJECT_DIR="$(pwd)/workspace/$PROJECT_DIR"
fi

if [ ! -d "$PROJECT_DIR" ]; then
  echo "{\"error\": \"project directory not found: $PROJECT_DIR\"}" >&2
  exit 1
fi

cd "$PROJECT_DIR"

# --- Helper: open URL in browser ---
open_browser() {
  local url="$1"
  case "$(uname -s)" in
    Darwin) open "$url" 2>/dev/null || true ;;
    Linux)  xdg-open "$url" 2>/dev/null || true ;;
  esac
}

# --- Detect project type and run ---

# =====================
# Node.js (package.json)
# =====================
if [ -f "package.json" ]; then
  # Install dependencies if needed
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

  # Determine start command
  DEV_CMD="npm run dev"
  if node -e "const p=require('./package.json'); process.exit(p.scripts && p.scripts.dev ? 0 : 1)" 2>/dev/null; then
    DEV_CMD="npm run dev"
  elif node -e "const p=require('./package.json'); process.exit(p.scripts && p.scripts.start ? 0 : 1)" 2>/dev/null; then
    DEV_CMD="npm start"
  else
    DEV_CMD="npx vite --open"
  fi

  # Start dev server in background
  DEV_LOG="$PROJECT_DIR/.forge-dev.log"
  nohup $DEV_CMD > "$DEV_LOG" 2>&1 &
  DEV_PID=$!

  # Wait for server to start
  SERVER_URL="http://localhost:3000"
  SERVER_READY=false
  for i in 1 2 3 4 5 6 7 8; do
    sleep 1
    if ! kill -0 "$DEV_PID" 2>/dev/null; then
      DEV_ERR=$(tail -10 "$DEV_LOG" 2>/dev/null || echo "process exited")
      jq -n --arg err "dev server failed to start" --arg details "$DEV_ERR" \
        '{error: $err, details: $details}' >&2
      exit 1
    fi
    # Try to extract URL from output (works for Vite, Next.js, CRA, etc.)
    ACTUAL_URL=$(grep -oE 'https?://localhost:[0-9]+' "$DEV_LOG" 2>/dev/null | head -1 || true)
    if [ -n "$ACTUAL_URL" ]; then
      SERVER_URL="$ACTUAL_URL"
      SERVER_READY=true
      break
    fi
  done

  # Open browser if server detected a URL, otherwise try to open the default
  open_browser "$SERVER_URL"

  jq -n \
    --arg status "running" \
    --arg url "$SERVER_URL" \
    --arg pid "$DEV_PID" \
    --arg project_dir "$PROJECT_DIR" \
    --arg install "$INSTALL_STATUS" \
    --arg type "node" \
    --arg cmd "$DEV_CMD" \
    '{status: $status, url: $url, pid: ($pid | tonumber), project_dir: $project_dir, install: $install, type: $type, command: $cmd}'
  exit 0
fi

# =====================
# Python
# =====================
if [ -f "requirements.txt" ] || [ -f "pyproject.toml" ] || [ -f "setup.py" ]; then
  # Install dependencies
  INSTALL_STATUS="skipped"
  if [ -f "requirements.txt" ]; then
    INSTALL_STATUS="installed"
    if ! pip install -r requirements.txt > .forge-install.log 2>&1; then
      INSTALL_ERR=$(tail -5 .forge-install.log 2>/dev/null || echo "unknown error")
      jq -n --arg err "pip install failed" --arg details "$INSTALL_ERR" \
        '{error: $err, details: $details}' >&2
      exit 1
    fi
  fi

  # Detect entry point
  DEV_CMD=""
  PORT=8000
  if [ -f "manage.py" ]; then
    DEV_CMD="python manage.py runserver 0.0.0.0:$PORT"
  elif [ -f "app.py" ]; then
    DEV_CMD="python app.py"
    PORT=5000
  elif [ -f "main.py" ]; then
    DEV_CMD="python main.py"
  else
    # Fallback: try uvicorn or flask
    if grep -q "fastapi\|uvicorn" requirements.txt 2>/dev/null; then
      DEV_CMD="uvicorn main:app --reload --port $PORT"
    elif grep -q "flask" requirements.txt 2>/dev/null; then
      DEV_CMD="flask run --port $PORT"
    else
      DEV_CMD="python -m http.server $PORT"
    fi
  fi

  DEV_LOG="$PROJECT_DIR/.forge-dev.log"
  nohup $DEV_CMD > "$DEV_LOG" 2>&1 &
  DEV_PID=$!

  SERVER_URL="http://localhost:$PORT"
  sleep 2

  if ! kill -0 "$DEV_PID" 2>/dev/null; then
    DEV_ERR=$(tail -10 "$DEV_LOG" 2>/dev/null || echo "process exited")
    jq -n --arg err "server failed to start" --arg details "$DEV_ERR" \
      '{error: $err, details: $details}' >&2
    exit 1
  fi

  open_browser "$SERVER_URL"

  jq -n \
    --arg status "running" \
    --arg url "$SERVER_URL" \
    --arg pid "$DEV_PID" \
    --arg project_dir "$PROJECT_DIR" \
    --arg install "$INSTALL_STATUS" \
    --arg type "python" \
    --arg cmd "$DEV_CMD" \
    '{status: $status, url: $url, pid: ($pid | tonumber), project_dir: $project_dir, install: $install, type: $type, command: $cmd}'
  exit 0
fi

# =====================
# Go
# =====================
if [ -f "go.mod" ]; then
  # Download dependencies
  INSTALL_STATUS="skipped"
  if ! [ -d "vendor" ] && ! go env GOMODCACHE | xargs test -d 2>/dev/null; then
    INSTALL_STATUS="installed"
  fi
  if ! go mod download > .forge-install.log 2>&1; then
    INSTALL_ERR=$(tail -5 .forge-install.log 2>/dev/null || echo "unknown error")
    jq -n --arg err "go mod download failed" --arg details "$INSTALL_ERR" \
      '{error: $err, details: $details}' >&2
    exit 1
  fi

  DEV_LOG="$PROJECT_DIR/.forge-dev.log"
  nohup go run . > "$DEV_LOG" 2>&1 &
  DEV_PID=$!
  PORT=8080

  # Wait for server to start (Go compiles first, may take a few seconds)
  SERVER_READY=false
  for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 1
    if ! kill -0 "$DEV_PID" 2>/dev/null; then
      DEV_ERR=$(tail -10 "$DEV_LOG" 2>/dev/null || echo "process exited")
      jq -n --arg err "go run failed" --arg details "$DEV_ERR" \
        '{error: $err, details: $details}' >&2
      exit 1
    fi
    ACTUAL_URL=$(grep -oE 'https?://[^[:space:]]+' "$DEV_LOG" 2>/dev/null | head -1 || true)
    if [ -n "$ACTUAL_URL" ]; then
      SERVER_READY=true
      break
    fi
  done

  SERVER_URL="${ACTUAL_URL:-http://localhost:$PORT}"
  open_browser "$SERVER_URL"

  jq -n \
    --arg status "running" \
    --arg url "$SERVER_URL" \
    --arg pid "$DEV_PID" \
    --arg project_dir "$PROJECT_DIR" \
    --arg install "$INSTALL_STATUS" \
    --arg type "go" \
    --arg cmd "go run ." \
    '{status: $status, url: $url, pid: ($pid | tonumber), project_dir: $project_dir, install: $install, type: $type, command: $cmd}'
  exit 0
fi

# =====================
# Spring Boot (pom.xml)
# =====================
if [ -f "pom.xml" ]; then
  # Determine Maven command
  MVN_CMD="mvn"
  if [ -f "mvnw" ]; then
    chmod +x mvnw
    MVN_CMD="./mvnw"
  fi

  # Install dependencies
  INSTALL_STATUS="installed"
  if ! $MVN_CMD dependency:resolve -q > .forge-install.log 2>&1; then
    INSTALL_ERR=$(tail -10 .forge-install.log 2>/dev/null || echo "unknown error")
    jq -n --arg err "maven dependency install failed" --arg details "$INSTALL_ERR" \
      '{error: $err, details: $details}' >&2
    exit 1
  fi

  DEV_LOG="$PROJECT_DIR/.forge-dev.log"
  DEV_CMD="$MVN_CMD spring-boot:run"
  nohup $DEV_CMD > "$DEV_LOG" 2>&1 &
  DEV_PID=$!
  PORT=8080

  # Spring Boot takes longer to start — wait up to 30 seconds
  SERVER_READY=false
  for i in $(seq 1 30); do
    sleep 1
    if ! kill -0 "$DEV_PID" 2>/dev/null; then
      DEV_ERR=$(tail -15 "$DEV_LOG" 2>/dev/null || echo "process exited")
      jq -n --arg err "spring-boot:run failed" --arg details "$DEV_ERR" \
        '{error: $err, details: $details}' >&2
      exit 1
    fi
    # Spring Boot logs: "Tomcat started on port 8080" or "Started Application in X seconds"
    if grep -qE 'Started \w+ in|Tomcat started on port' "$DEV_LOG" 2>/dev/null; then
      SERVER_READY=true
      break
    fi
  done

  ACTUAL_URL=$(grep -oE 'https?://[^[:space:]]+' "$DEV_LOG" 2>/dev/null | head -1 || true)
  SERVER_URL="${ACTUAL_URL:-http://localhost:$PORT}"
  open_browser "$SERVER_URL"

  jq -n \
    --arg status "running" \
    --arg url "$SERVER_URL" \
    --arg pid "$DEV_PID" \
    --arg project_dir "$PROJECT_DIR" \
    --arg install "$INSTALL_STATUS" \
    --arg type "spring-boot" \
    --arg cmd "$DEV_CMD" \
    '{status: $status, url: $url, pid: ($pid | tonumber), project_dir: $project_dir, install: $install, type: $type, command: $cmd}'
  exit 0
fi

# =====================
# Static HTML (fallback)
# =====================
if [ -f "index.html" ]; then
  PORT=8080
  DEV_LOG="$PROJECT_DIR/.forge-dev.log"
  nohup python3 -m http.server "$PORT" > "$DEV_LOG" 2>&1 &
  DEV_PID=$!
  sleep 1

  SERVER_URL="http://localhost:$PORT"
  open_browser "$SERVER_URL"

  jq -n \
    --arg status "running" \
    --arg url "$SERVER_URL" \
    --arg pid "$DEV_PID" \
    --arg project_dir "$PROJECT_DIR" \
    --arg install "n/a" \
    --arg type "static" \
    --arg cmd "python3 -m http.server $PORT" \
    '{status: $status, url: $url, pid: ($pid | tonumber), project_dir: $project_dir, install: $install, type: $type, command: $cmd}'
  exit 0
fi

# No known project type
echo '{"error": "could not detect project type — no package.json, requirements.txt, go.mod, pom.xml, or index.html found"}' >&2
exit 1
