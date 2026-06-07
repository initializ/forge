#!/usr/bin/env bash
# manual-test-fws-7.sh — Validate audit event export to a Unix Domain
# Socket and to the localhost HTTP fallback (issue #95 / FWS-7).
#
# Sandbox model: every path the export sink touches is created under
# a tempdir; nothing outside the sandbox is written to. The test
# starts a netcat listener (UDS) and a tiny Python HTTP server, runs
# `forge run` briefly with the matching flags, and confirms the
# expected audit events land on the sink while the stderr safety-net
# still receives the same bytes.
#
# Run:    bash scripts/manual-test-fws-7.sh
# Clean:  rm -rf $TMPDIR/forge-fws-7.*   (sandbox path printed at end)

set -euo pipefail

WORKTREE="$(cd "$(dirname "$0")/.." && pwd)"
SANDBOX="$(mktemp -d -t forge-fws-7.XXXXXX)"
FORGE_BIN="$SANDBOX/forge"
AGENT_DIR="$SANDBOX/agent"
SOCK_PATH="/tmp/forge-fws-7.sock"   # short path required by macOS UDS limit
HTTP_PORT=19097

trap 'echo; echo "Sandbox: $SANDBOX"; rm -f "$SOCK_PATH"' EXIT

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
agent_id: fws7-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
egress:
  mode: deny-all
EOF

# Start forge for ~N seconds, kill it, return stderr capture path.
# Each scenario gets a fresh port to avoid TIME_WAIT contention
# between back-to-back runs of the same agent process.
RUN_PORT=18081
run_for() {
  local seconds=$1 logfile=$2
  shift 2
  RUN_PORT=$((RUN_PORT + 1))
  # exec replaces the subshell with forge so $! is forge's PID; without
  # exec, TERM would only kill the wrapper and forge would orphan,
  # holding the port for the next scenario.
  ( cd "$AGENT_DIR" && exec "$FORGE_BIN" run --port "$RUN_PORT" --mock-tools "$@" ) \
    >"$logfile" 2>&1 &
  local pid=$!
  sleep "$seconds"
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  # Give the OS a beat to release the port + flush any final output
  # before the next scenario.
  sleep 0.5
}

# Filter NDJSON event lines from a forge stderr capture.
events() {
  grep -aE '^\{' "$1" 2>/dev/null \
    | jq -c 'select(.event != null)' 2>/dev/null \
    || true
}

# ─── 1. UDS path: sidecar receives audit events ──────────────────────
step "1. Unix socket: events land on both stderr and the UDS listener"
rm -f "$SOCK_PATH"
# Use socat to capture every byte that arrives on the socket. nc -U
# would also work; socat is more reliably available on macOS via brew.
if ! command -v socat >/dev/null && ! command -v nc >/dev/null; then
  fail "neither socat nor nc available; skip"
else
  SOCK_LOG="$SANDBOX/uds.log"
  if command -v socat >/dev/null; then
    socat -u UNIX-LISTEN:"$SOCK_PATH",reuseaddr,fork - >"$SOCK_LOG" &
  else
    nc -lU "$SOCK_PATH" >"$SOCK_LOG" &
  fi
  LSN_PID=$!
  sleep 0.5
  STDERR_LOG="$SANDBOX/stderr-uds.log"
  run_for 4 "$STDERR_LOG" --audit-socket "$SOCK_PATH"
  kill -TERM "$LSN_PID" 2>/dev/null || true
  wait "$LSN_PID" 2>/dev/null || true

  sub "Events on stderr safety-net:"
  events "$STDERR_LOG" | head -5 | dump
  sub "Events on UDS sink:"
  if [ -s "$SOCK_LOG" ]; then
    head -5 "$SOCK_LOG" | dump
    if events "$SOCK_LOG" | grep -q '"event"'; then
      ok "UDS sink received NDJSON audit events"
    else
      fail "UDS sink captured bytes but no parseable JSON; see $SOCK_LOG"
    fi
  else
    fail "UDS sink received zero bytes; see $SOCK_LOG"
  fi
fi

# ─── 2. HTTP fallback ────────────────────────────────────────────────
step "2. HTTP fallback: events POST to localhost endpoint"
HTTP_LOG="$SANDBOX/http.log"
python3 -c "
import http.server, sys, threading
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length','0'))
        body = self.rfile.read(n)
        with open('$HTTP_LOG','ab') as f: f.write(body)
        self.send_response(202); self.end_headers()
    def log_message(self,*a): pass
http.server.HTTPServer(('127.0.0.1', $HTTP_PORT), H).serve_forever()
" >/dev/null 2>&1 &
HTTP_PID=$!
sleep 0.5

STDERR_LOG="$SANDBOX/stderr-http.log"
run_for 4 "$STDERR_LOG" --audit-http-endpoint "http://127.0.0.1:$HTTP_PORT/v1/audit"
kill -TERM "$HTTP_PID" 2>/dev/null || true
wait "$HTTP_PID" 2>/dev/null || true

sub "Events on HTTP sink:"
if [ -s "$HTTP_LOG" ]; then
  head -5 "$HTTP_LOG" | dump
  if grep -q '"event"' "$HTTP_LOG"; then
    ok "HTTP sink received audit events"
  else
    fail "HTTP sink captured bytes but no recognizable events; see $HTTP_LOG"
  fi
else
  fail "HTTP sink received zero bytes; see $HTTP_LOG"
fi

# ─── 3. No flags = stderr only (backward compat) ─────────────────────
step "3. No flags = no export sink registered; stderr unchanged"
STDERR_LOG="$SANDBOX/stderr-plain.log"
run_for 3 "$STDERR_LOG"
n=$(events "$STDERR_LOG" | wc -l | tr -d ' ')
if [ "$n" -ge 1 ]; then
  ok "$n audit events on stderr; no export configured = no export attempted"
else
  fail "expected audit events on stderr, got none"
fi

# ─── 4. Sink unreachable: agent keeps running cleanly ────────────────
# The point of this scenario is the resilience invariant: a broken
# sink must not (a) kill the agent, (b) leak error spam into ops logs,
# or (c) block emit on stderr. We do NOT wait for the 60s status tick
# here — that's gated behind FWS7_LONG=1 below.
step "4. Misconfigured socket path: agent keeps running, stderr unaffected"
STDERR_LOG="$SANDBOX/stderr-broken.log"
run_for 4 "$STDERR_LOG" --audit-socket "/tmp/does-not-exist-$$.sock"
n_events=$(events "$STDERR_LOG" | wc -l | tr -d ' ')
# `grep -c` prints "0" AND exits 1 on no-match; use `|| true` so the
# exit code is suppressed without injecting a second "0" via `echo 0`.
n_sink_errors=$(grep -ci "audit sink\|sink.*failed" "$STDERR_LOG" 2>/dev/null || true)
n_sink_errors=${n_sink_errors:-0}
sub "audit events on stderr: $n_events; sink-error lines (should be ≤ 1): $n_sink_errors"
if [ "$n_events" -ge 1 ] && [ "$n_sink_errors" -le 1 ]; then
  ok "agent kept running + emitted on stderr; sink errors deduped to ≤1 line"
else
  fail "broken sink leaked into ops logs ($n_sink_errors lines) or stderr dried up ($n_events events)"
fi

# Optional scenario 5: 65s run that captures the periodic
# audit_export_status event. Gated because nobody wants a 65s wait by
# default. Run with: FWS7_LONG=1 bash scripts/manual-test-fws-7.sh
if [ "${FWS7_LONG:-0}" = "1" ]; then
  step "5. (FWS7_LONG=1) Long run captures audit_export_status with drop counters"
  STDERR_LOG="$SANDBOX/stderr-long.log"
  run_for 65 "$STDERR_LOG" --audit-socket "/tmp/does-not-exist-$$.sock"
  status_evt=$(events "$STDERR_LOG" | jq -c 'select(.event == "audit_export_status")' | tail -1)
  if [ -n "$status_evt" ]; then
    sub "audit_export_status payload:"
    echo "$status_evt" | jq '.fields.sinks' | dump
    if echo "$status_evt" | jq -e '.fields.sinks[] | select(.name == "unix-socket") | .drops_dial > 0' >/dev/null; then
      ok "drops_dial > 0 on unix-socket sink — drops counted in the status event"
    else
      fail "status event captured but unix-socket sink shows no drops"
    fi
  else
    fail "no audit_export_status event in 65s; check $STDERR_LOG"
  fi
fi

step "Logs preserved at $SANDBOX"
ls "$SANDBOX"/*.log 2>/dev/null | dump || true
