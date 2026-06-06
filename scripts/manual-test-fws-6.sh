#!/usr/bin/env bash
# manual-test-fws-6.sh — Validate three-layer channel policy (issue #90 / FWS-6).
#
# Exercises every redesign-specific path end-to-end against a freshly
# built forge binary inside a fully-sandboxed environment:
#
#   - System layer  → $FORGE_SYSTEM_POLICY (redirected to a temp file —
#                     does NOT touch /etc/forge/policy.yaml)
#   - User layer    → $HOME/.forge/policy.yaml (HOME redirected to a
#                     temp dir — does NOT touch your real ~/.forge/)
#   - Workspace     → $FORGE_PLATFORM_POLICY (also temp file)
#
# Each scenario prints what was set up, what was expected, and what the
# audit pipeline actually emitted so you can eyeball pass/fail without
# trusting a self-graded "PASS" badge.
#
# Run:    bash scripts/manual-test-fws-6.sh
# Clean:  rm -rf $TMPDIR/forge-fws-6.*   (sandbox dir is printed at the end)

set -euo pipefail

# ─── Setup ───────────────────────────────────────────────────────────
WORKTREE="$(cd "$(dirname "$0")/.." && pwd)"
SANDBOX="$(mktemp -d -t forge-fws-6.XXXXXX)"
FORGE_BIN="$SANDBOX/forge"
AGENT_DIR="$SANDBOX/agent"

# Isolation: every policy path the runtime checks now resolves into
# the sandbox. Your real ~/.forge/ and /etc/forge/ are not touched.
export FORGE_SYSTEM_POLICY="$SANDBOX/system-policy.yaml"
export HOME="$SANDBOX/home"
mkdir -p "$HOME/.forge" "$AGENT_DIR"

trap 'echo; echo "Sandbox: $SANDBOX (leave it for inspection or rm -rf)"; ' EXIT

step() { printf '\n\033[1;34m════════ %s ════════\033[0m\n' "$*"; }
sub()  { printf '  \033[1;33m›\033[0m %s\n' "$*"; }
ok()   { printf '    \033[1;32m✓\033[0m %s\n' "$*"; }
fail() { printf '    \033[1;31m✗ FAIL\033[0m %s\n' "$*"; }
dump() { sed 's/^/      /'; }

# ─── Build ───────────────────────────────────────────────────────────
step "Build forge with -a (force full rebuild to defeat worktree cache)"
( cd "$WORKTREE/forge-cli/cmd/forge" && go build -a -o "$FORGE_BIN" . )
"$FORGE_BIN" --version 2>&1 | head -1 || true

# ─── Minimal test agent ──────────────────────────────────────────────
cat > "$AGENT_DIR/forge.yaml" <<'EOF'
agent_id: fws6-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
channels:
  - slack
  - telegram
egress:
  mode: deny-all
EOF

# ─── Runtime probe helpers ───────────────────────────────────────────
# Starts forge run for ~3s, captures all stderr (banner + audit NDJSON),
# kills the process, and leaves the log at $1 for inspection.
run_capture() {
  local logfile=$1
  # --with is REQUIRED to exercise the channel filter + audit emit;
  # without it forge run just brings up the A2A server and the policy
  # filter never runs (`cfg.Channels` in forge.yaml is a declaration,
  # not an instruction to start adapters).
  ( cd "$AGENT_DIR" && "$FORGE_BIN" run --port 18080 --mock-tools \
      --with slack,telegram ) >"$logfile" 2>&1 &
  local pid=$!
  sleep 3
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

# Extract channel_denied_by_policy events. Robust against empty input
# (grep returning 1 on no-match would otherwise crash the pipeline
# under `set -o pipefail`).
channel_denies() {
  local out
  out=$(grep -aE '^\{' "$1" 2>/dev/null || true)
  [ -z "$out" ] && return 0
  echo "$out" | jq -c 'select(.event == "channel_denied_by_policy")' 2>/dev/null || true
}

# Extract a single field from a channel_denied event for a given
# channel name. Empty string when the event is absent.
field_for() {
  local events=$1 channel=$2 field=$3
  local line
  line=$(echo "$events" | grep "\"channel\":\"$channel\"" 2>/dev/null || true)
  [ -z "$line" ] && return 0
  echo "$line" | jq -r ".fields.$field" 2>/dev/null || true
}

# ─── Scenario 1: backward-compat (no layers, no denies) ──────────────
step "1. No policy layers → no channel_denied events (backward compat)"
rm -f "$FORGE_SYSTEM_POLICY" "$HOME/.forge/policy.yaml"
unset FORGE_PLATFORM_POLICY 2>/dev/null || true
run_capture "$SANDBOX/1.log"
events=$(channel_denies "$SANDBOX/1.log")
if [ -z "$events" ]; then
  ok "zero channel_denied events"
else
  fail "expected zero denies, got:"; echo "$events" | dump
fi

# ─── Scenario 2: user layer via CLI ─────────────────────────────────
step "2. forge channel disable telegram → writes user layer"
( cd "$AGENT_DIR" && "$FORGE_BIN" channel disable telegram 2>&1 | dump )
sub "User policy file ($HOME/.forge/policy.yaml):"
cat "$HOME/.forge/policy.yaml" | dump
run_capture "$SANDBOX/2.log"
events=$(channel_denies "$SANDBOX/2.log")
sub "channel_denied events emitted at startup:"
echo "$events" | dump
layer=$(field_for "$events" telegram layer)
[ "$layer" = "user" ] && ok "telegram denied, attributed to layer=user" \
                       || fail "telegram should be denied with layer=user, got '$layer'"

# ─── Scenario 3: system layer ────────────────────────────────────────
step "3. Add system layer (denied_channels: [slack]) → two denies, distinct layers"
cat > "$FORGE_SYSTEM_POLICY" <<'EOF'
denied_channels:
  - slack
EOF
run_capture "$SANDBOX/3.log"
events=$(channel_denies "$SANDBOX/3.log")
sub "channel_denied events:"
echo "$events" | dump
slack_layer=$(field_for "$events" slack layer)
tg_layer=$(field_for "$events" telegram layer)
[ "$slack_layer" = "system" ] && ok "slack → layer=system" || fail "slack should be layer=system, got '$slack_layer'"
[ "$tg_layer" = "user" ] && ok "telegram → layer=user" || fail "telegram should be layer=user, got '$tg_layer'"

# ─── Scenario 4: attribution precedence ──────────────────────────────
step "4. Both system AND user deny telegram → system wins attribution"
cat > "$FORGE_SYSTEM_POLICY" <<'EOF'
denied_channels:
  - telegram
  - slack
EOF
# user policy from scenario 2 already has telegram
sub "System layer:"; cat "$FORGE_SYSTEM_POLICY" | dump
sub "User layer:";   cat "$HOME/.forge/policy.yaml" | dump
run_capture "$SANDBOX/4.log"
events=$(channel_denies "$SANDBOX/4.log")
sub "all channel_denied events:"
echo "$events" | dump
attrib=$(field_for "$events" telegram layer)
src=$(field_for "$events" telegram source)
[ "$attrib" = "system" ] && ok "telegram attributed to layer=system" || fail "expected layer=system, got '$attrib'"
[ "$src" = "$FORGE_SYSTEM_POLICY" ] && ok "source=$FORGE_SYSTEM_POLICY" || fail "source mismatch: '$src'"

# ─── Scenario 5: enable removes user-layer entry (file cleaned up) ───
step "5. forge channel enable telegram → user policy file removed when empty"
( cd "$AGENT_DIR" && "$FORGE_BIN" channel enable telegram 2>&1 | dump )
if [ -e "$HOME/.forge/policy.yaml" ]; then
  fail "user policy file still present:"
  cat "$HOME/.forge/policy.yaml" | dump
else
  ok "user policy file removed (empty doc → no on-disk noise)"
fi

# ─── Scenario 6: --system flag writes to system path ─────────────────
step "6. forge channel disable msteams --system → writes system path"
rm -f "$FORGE_SYSTEM_POLICY"
( cd "$AGENT_DIR" && "$FORGE_BIN" channel disable msteams --system 2>&1 | dump )
sub "System policy file ($FORGE_SYSTEM_POLICY):"
cat "$FORGE_SYSTEM_POLICY" | dump
if grep -q 'msteams' "$FORGE_SYSTEM_POLICY"; then
  ok "--system wrote to FORGE_SYSTEM_POLICY path (non-root warning expected in output above)"
else
  fail "--system did not write expected entry"
fi

# ─── Scenario 7: forge channel serve refuses denied target ───────────
step "7. forge channel serve slack → refuses to start when slack is denied"
cat > "$FORGE_SYSTEM_POLICY" <<'EOF'
denied_channels:
  - slack
EOF
out=$( cd "$AGENT_DIR" && "$FORGE_BIN" channel serve slack 2>&1 || true )
echo "$out" | head -10 | dump
if echo "$out" | grep -qiE 'denied|policy'; then
  ok "channel serve refused with policy-related error"
else
  fail "expected a deny-related error; got:"; echo "$out" | dump
fi

# ─── Scenario 8: egress violation carries layer attribution ──────────
step "8. Egress deny via user policy → startup error names enforcing layer"
cat > "$AGENT_DIR/forge.yaml" <<'EOF'
agent_id: fws6-test
version: 0.0.1
framework: forge
model:
  provider: ollama
  name: llama3
channels: []
egress:
  mode: allowlist
  allowed_domains:
    - api.notion.com
EOF
rm -f "$FORGE_SYSTEM_POLICY"
cat > "$HOME/.forge/policy.yaml" <<'EOF'
denied_egress_domains:
  - api.notion.com
EOF
sub "User policy denies api.notion.com; forge.yaml declares it"
out=$( cd "$AGENT_DIR" && "$FORGE_BIN" run --port 18080 --mock-tools 2>&1 || true )
echo "$out" | grep -aE 'denied_egress|enforced by|policy' | head -10 | dump
if echo "$out" | grep -q 'enforced by user policy'; then
  ok "violation error names the enforcing layer (user) + path"
else
  fail "expected 'enforced by user policy' in error; check log $SANDBOX/8.log"
  echo "$out" > "$SANDBOX/8.log"
fi

# ─── Scenario 9: forge validate --platform-policy lints each layer ───
step "9. forge validate --platform-policy lints arbitrary layer files"
cat > "$SANDBOX/bogus.yaml" <<'EOF'
denied_channels:
  - telegram
unknown_field: this_should_fail   # strict decoding rejects typos
EOF
out=$( "$FORGE_BIN" validate --platform-policy="$SANDBOX/bogus.yaml" 2>&1 || true )
echo "$out" | head -5 | dump
if echo "$out" | grep -qi 'unknown'; then
  ok "strict decoding rejected the unknown_field typo"
else
  fail "expected an 'unknown field' error"
fi

# ─── Done ────────────────────────────────────────────────────────────
step "Logs preserved at $SANDBOX for post-mortem"
ls "$SANDBOX"/*.log 2>/dev/null | dump || true
