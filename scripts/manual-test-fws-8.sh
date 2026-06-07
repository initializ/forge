#!/usr/bin/env bash
# manual-test-fws-8.sh — Validate audit hardening (issue #91 / FWS-8).
#
# Sandbox model: builds a fresh forge binary into a tempdir, starts it
# briefly with mock tools, and inspects the captured stderr NDJSON for
# the three hardening invariants:
#
#   1. Every event carries schema_version.
#   2. Events emitted on behalf of an A2A invocation carry a
#      monotonically increasing seq; startup events omit seq.
#   3. Default audit posture is metadata-only — no raw prompt /
#      completion / tool args / tool results in the event JSON.
#
# Scenario 4 fires an A2A request through the agent so we can see
# real sequence numbers in flight. The script needs no LLM API keys
# (mock-tools + a stub executor handle it).
#
# Run:    bash scripts/manual-test-fws-8.sh
# Clean:  rm -rf $TMPDIR/forge-fws-8.*   (sandbox printed at end)

set -euo pipefail

WORKTREE="$(cd "$(dirname "$0")/.." && pwd)"
SANDBOX="$(mktemp -d -t forge-fws-8.XXXXXX)"
FORGE_BIN="$SANDBOX/forge"
AGENT_DIR="$SANDBOX/agent"

trap 'echo; echo "Sandbox: $SANDBOX"' EXIT

step() { printf '\n\033[1;34m════════ %s ════════\033[0m\n' "$*"; }
sub()  { printf '  \033[1;33m›\033[0m %s\n' "$*"; }
ok()   { printf '    \033[1;32m✓\033[0m %s\n' "$*"; }
fail() { printf '    \033[1;31m✗ FAIL\033[0m %s\n' "$*"; }
dump() { sed 's/^/      /'; }

# ─── Build ───────────────────────────────────────────────────────────
step "Build forge with -a (force full rebuild)"
( cd "$WORKTREE/forge-cli/cmd/forge" && go build -a -o "$FORGE_BIN" . )

# ─── Minimal agent ───────────────────────────────────────────────────
mkdir -p "$AGENT_DIR"
cat > "$AGENT_DIR/forge.yaml" <<'EOF'
agent_id: fws8-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
egress:
  mode: deny-all
EOF

RUN_PORT=18091
run_for() {
  local seconds=$1 logfile=$2
  shift 2
  RUN_PORT=$((RUN_PORT + 1))
  ( cd "$AGENT_DIR" && exec "$FORGE_BIN" run --port "$RUN_PORT" --mock-tools "$@" ) \
    >"$logfile" 2>&1 &
  local pid=$!
  sleep "$seconds"
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  sleep 0.4
  echo "$RUN_PORT"
}

events() {
  grep -aE '^\{' "$1" 2>/dev/null \
    | jq -c 'select(.event != null)' 2>/dev/null \
    || true
}

# ─── 1. schema_version is stamped on every event ─────────────────────
step "1. schema_version stamped on every emitted event"
LOG="$SANDBOX/1.log"
run_for 3 "$LOG" >/dev/null
total=$(events "$LOG" | wc -l | tr -d ' ')
without_version=$(events "$LOG" | jq -c 'select(.schema_version == null or .schema_version == "")' | wc -l | tr -d ' ')
sub "events captured: $total ; events missing schema_version: $without_version"
if [ "$total" -ge 1 ] && [ "$without_version" -eq 0 ]; then
  ok "schema_version present on every event"
  events "$LOG" | jq -r '.schema_version' | sort -u | head -3 | dump
else
  fail "missing schema_version on $without_version of $total events"
fi

# ─── 2. startup events omit seq (no invocation scope) ────────────────
step "2. Startup events (no invocation scope) omit seq"
LOG="$SANDBOX/2.log"
run_for 3 "$LOG" >/dev/null
total=$(events "$LOG" | wc -l | tr -d ' ')
with_seq=$(events "$LOG" | jq -c 'select(.seq != null)' | wc -l | tr -d ' ')
sub "events captured: $total ; events that incorrectly carry seq: $with_seq"
if [ "$total" -ge 1 ] && [ "$with_seq" -eq 0 ]; then
  ok "no startup-scope events carry seq"
else
  fail "$with_seq startup events incorrectly carry seq"
  events "$LOG" | jq -c 'select(.seq != null)' | head -3 | dump
fi

# ─── 3. Default posture: no payload bytes in any event ───────────────
step "3. Default posture leaks NO prompt/completion/tool-args bytes"
LOG="$SANDBOX/3.log"
run_for 3 "$LOG" >/dev/null
# Forbidden field keys that must never appear in default-mode audit.
forbidden=(prompt_messages completion_text args result)
miss_count=0
for key in "${forbidden[@]}"; do
  hits=$(events "$LOG" | jq -c "select(.fields.\"$key\" != null)" | wc -l | tr -d ' ')
  if [ "$hits" -gt 0 ]; then
    fail "field '$key' present in $hits events (default posture should never emit it)"
    miss_count=$((miss_count + 1))
  fi
done
if [ "$miss_count" -eq 0 ]; then
  ok "no payload-capture fields in any default-mode event"
fi

# ─── 4. In-flight invocation produces gap-free seq starting at 1 ─────
step "4. A2A invocation produces gap-free seq starting at 1"
LOG="$SANDBOX/4.log"
PORT=$(run_for 0.001 "/dev/null")
# Start the agent and let it bind; then fire a tasks/send request.
LOG="$SANDBOX/4.log"
( cd "$AGENT_DIR" && exec "$FORGE_BIN" run --port "$PORT" --mock-tools ) \
  >"$LOG" 2>&1 &
PID=$!
# Wait for the server to bind.
for _ in {1..30}; do
  if curl -s -m 0.2 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
TOKEN=$(cat "$AGENT_DIR/.forge/runtime.token" 2>/dev/null || echo "")
# Send one tasks/send. We don't care if the LLM call fails (no API
# key) — sequence stamping happens at every emit, including the
# error-path ones.
curl -s -m 5 -X POST "http://127.0.0.1:$PORT/" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"1","method":"tasks/send","params":{"id":"task-fws8","message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}' \
  >/dev/null 2>&1 || true
sleep 0.5
kill -TERM "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true

# Pull all seq values for our task and verify monotonicity from 1.
seqs=$(events "$LOG" | jq -r 'select(.task_id == "task-fws8") | .seq' 2>/dev/null | tr -d 'null' | grep -v '^$' || true)
n_seq=$(echo "$seqs" | grep -c '^[0-9]' || true)
sub "events emitted under task-fws8: $n_seq (with seq stamped)"
if [ "$n_seq" -lt 1 ]; then
  fail "no per-invocation events captured for task-fws8"
else
  # Check seq is monotonic 1..N with no gaps.
  expected=1
  fail_seq=0
  for v in $seqs; do
    if [ "$v" != "$expected" ]; then
      fail "seq gap or non-monotonic: got $v, expected $expected"
      fail_seq=1
      break
    fi
    expected=$((expected + 1))
  done
  if [ "$fail_seq" -eq 0 ]; then
    ok "seq is monotonic 1..$((expected - 1)) with no gaps"
  fi
fi

# ─── Done ────────────────────────────────────────────────────────────
step "Logs preserved at $SANDBOX"
ls "$SANDBOX"/*.log 2>/dev/null | dump || true
