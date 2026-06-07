#!/usr/bin/env bash
# manual-test-fws-10.sh — Validate rate-limit configurability + cancel
# exemption (issue #110 / FWS-10).
#
# Three scenarios:
#   1. Default-config write burst — 20 consecutive POSTs from one IP
#      all pass. Pre-FWS-10 (burst=3) this would have hit 429 at #4.
#   2. Cancel exemption — drain the write bucket via tight config,
#      then fire 10 tasks/cancel requests. All 10 pass. Pre-FWS-10
#      (or with cancel_exempt=false) cancel #2 would have hit 429.
#   3. yaml override — server.rate_limit in forge.yaml raises burst
#      to 50; the agent honors it on next start.
#
# Sandboxed: every binary, agent dir, and log lives under a tempdir.
#
# Run:    bash scripts/manual-test-fws-10.sh
# Clean:  rm -rf $TMPDIR/forge-fws-10.*   (sandbox printed at end)

set -euo pipefail

WORKTREE="$(cd "$(dirname "$0")/.." && pwd)"
SANDBOX="$(mktemp -d -t forge-fws-10.XXXXXX)"
FORGE_BIN="$SANDBOX/forge"
AGENT_DIR="$SANDBOX/agent"
PORT=18101

trap 'echo; echo "Sandbox: $SANDBOX"; pkill -9 -f "$FORGE_BIN" 2>/dev/null || true' EXIT

step() { printf '\n\033[1;34m════════ %s ════════\033[0m\n' "$*"; }
sub()  { printf '  \033[1;33m›\033[0m %s\n' "$*"; }
ok()   { printf '    \033[1;32m✓\033[0m %s\n' "$*"; }
fail() { printf '    \033[1;31m✗ FAIL\033[0m %s\n' "$*"; }
dump() { sed 's/^/      /'; }

# ─── Build ───────────────────────────────────────────────────────────
step "Build forge with -a (force full rebuild)"
( cd "$WORKTREE/forge-cli/cmd/forge" && go build -a -o "$FORGE_BIN" . )

# Helper: start forge in the background on a fresh port, wait until
# /healthz responds, leave it running. Caller kills via pkill or by
# returning the PID via stdout.
# start_agent writes the spawned PID into the global AGENT_PID and
# returns 0 on /healthz success, 1 otherwise. Using a global rather
# than stdout dodges the `set -e + command substitution` gotcha that
# was aborting the script on the first scenario.
AGENT_PID=""
start_agent() {
  local logfile=$1
  shift  # any extra args go straight to forge run
  PORT=$((PORT + 1))
  ( cd "$AGENT_DIR" && exec "$FORGE_BIN" run --port "$PORT" --mock-tools "$@" ) \
    >"$logfile" 2>&1 &
  AGENT_PID=$!
  # Wait up to 10s for /healthz to respond. curl -m 1 leaves plenty of
  # room for the first request after a fresh listen-socket — the
  # tighter 200ms cap was a race on slow CI machines.
  for _ in $(seq 1 20); do
    if curl -s -m 1 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  kill -9 "$AGENT_PID" 2>/dev/null || true
  AGENT_PID=""
  return 1
}

stop_agent() {
  if [ -n "$AGENT_PID" ]; then
    kill -TERM "$AGENT_PID" 2>/dev/null || true
    wait "$AGENT_PID" 2>/dev/null || true
    AGENT_PID=""
  fi
  # Brief pause so the OS can release the port before the next
  # scenario rebinds. macOS in particular keeps the port in TIME_WAIT
  # for a moment after close.
  sleep 0.6
}

token_for() {
  cat "$AGENT_DIR/.forge/runtime.token" 2>/dev/null || echo ""
}

post_send() {
  curl -s -o /dev/null -w "%{http_code}" -m 3 -X POST "http://127.0.0.1:$PORT/" \
    -H "Authorization: Bearer $(token_for)" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":"1","method":"tasks/send","params":{"id":"t1","message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}'
}

post_cancel() {
  curl -s -o /dev/null -w "%{http_code}" -m 3 -X POST "http://127.0.0.1:$PORT/" \
    -H "Authorization: Bearer $(token_for)" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":"2","method":"tasks/cancel","params":{"id":"t1"}}'
}

# ─── Minimal agent ───────────────────────────────────────────────────
mkdir -p "$AGENT_DIR"
cat > "$AGENT_DIR/forge.yaml" <<'EOF'
agent_id: fws10-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
egress:
  mode: deny-all
EOF

# ─── 1. Default burst absorbs 20 writes ──────────────────────────────
step "1. Default config: 20 consecutive POSTs all pass (write_burst=20)"
start_agent "$SANDBOX/1.log" || { fail "agent failed to start"; exit 1; }
pass=0; throttled=0
for i in $(seq 1 20); do
  code=$(post_send)
  if [ "$code" = "200" ] || [ "$code" = "202" ]; then
    pass=$((pass + 1))
  elif [ "$code" = "429" ]; then
    throttled=$((throttled + 1))
  fi
done
stop_agent
sub "passed: $pass / 20 ; throttled: $throttled"
if [ "$pass" -ge 20 ] && [ "$throttled" -eq 0 ]; then
  ok "burst-20 default absorbs orchestrator dispatch without throttling"
else
  fail "expected 20 passes / 0 throttles, got $pass / $throttled"
fi

# ─── 2. Cancel exemption: drain bucket, then cancel-spam ────────────
step "2. Tight write bucket + cancel_exempt=true: 10 cancels pass after drain"
start_agent "$SANDBOX/2.log" \
        --rate-limit-write-rps 0.01 --rate-limit-write-burst 1 \
        || { fail "agent failed to start"; exit 1; }
# Drain the write bucket with one send.
drain_code=$(post_send)
sub "drain send: $drain_code"
# Confirm the bucket is now drained — second send should be 429.
second_code=$(post_send)
sub "second send (expect 429): $second_code"
# Now fire 10 cancels — all should pass.
cancel_pass=0; cancel_429=0
for i in $(seq 1 10); do
  code=$(post_cancel)
  if [ "$code" = "200" ] || [ "$code" = "202" ]; then cancel_pass=$((cancel_pass + 1));
  elif [ "$code" = "429" ]; then cancel_429=$((cancel_429 + 1)); fi
done
stop_agent
sub "cancel pass: $cancel_pass / 10 ; cancel 429: $cancel_429"
if [ "$cancel_pass" -ge 10 ] && [ "$cancel_429" -eq 0 ]; then
  ok "cancel sails through even with the write bucket fully drained"
else
  fail "expected 10 cancel passes / 0 throttles, got $cancel_pass / $cancel_429"
fi

# ─── 3. yaml override — server.rate_limit.write_burst = 50 ───────────
step "3. forge.yaml server.rate_limit.write_burst = 50 → 50 sends pass"
cat > "$AGENT_DIR/forge.yaml" <<'EOF'
agent_id: fws10-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
egress:
  mode: deny-all
server:
  rate_limit:
    write_burst: 50
EOF
start_agent "$SANDBOX/3.log" || { fail "agent failed to start"; exit 1; }
pass=0
set +e
for i in $(seq 1 50); do
  code=$(post_send)
  if [ "$code" = "200" ] || [ "$code" = "202" ]; then pass=$((pass + 1)); fi
done
set -e
stop_agent
sub "passed: $pass / 50"
if [ "$pass" -ge 50 ]; then
  ok "yaml-configured write_burst=50 honored on startup"
else
  fail "expected 50 passes, got $pass"
fi

step "Logs preserved at $SANDBOX"
ls "$SANDBOX"/*.log 2>/dev/null | dump || true
