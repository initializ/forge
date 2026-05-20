#!/usr/bin/env bash
# shellcheck disable=SC2016  # sed/regex strings use $ as anchor, not shell var
# Shared helpers for code-plan skill scripts.
# Do not run this file directly — it is sourced by code-plan-*.sh.

# Default budget for the repo signal sent to the LLM. 256 KB keeps the prompt
# well under provider request limits even with the task description and full
# system prompt appended.
PLAN_DEFAULT_REPO_SIGNAL_BYTES=262144

# Read JSON arg from $1 or stdin, validate, set INPUT_JSON.
plan_read_input() {
  if [ -n "${1:-}" ]; then
    INPUT_JSON="$1"
  else
    INPUT_JSON="$(cat)"
  fi
  if ! echo "$INPUT_JSON" | jq -e . >/dev/null 2>&1; then
    plan_die "invalid JSON input"
  fi
}

# Emit error to stderr as JSON, exit 1.
plan_die() {
  jq -n --arg msg "$1" '{"error": $msg}' >&2
  exit 1
}

# Require an env var to be set, else die.
plan_require_env() {
  local var="$1"
  if [ -z "${!var:-}" ]; then
    plan_die "missing required environment variable: $var"
  fi
}

# Expand ~ to $HOME, verify the path exists and is a directory, echo the
# resolved absolute path. Dies on missing path so callers don't have to
# re-check.
plan_resolve_repo_path() {
  local p="$1"
  p="${p/#\~/$HOME}"
  if [ ! -d "$p" ]; then
    plan_die "repo_path does not exist or is not a directory: $p"
  fi
  echo "$p"
}

# Inspect repo_path and return a short stack identifier:
#   node | go | python | rust | java | unknown
# Looks at manifest presence, not file contents. First match wins.
plan_detect_stack() {
  local repo="$1"
  if [ -f "$repo/go.mod" ]; then
    echo "go"
  elif [ -f "$repo/package.json" ]; then
    echo "node"
  elif [ -f "$repo/pyproject.toml" ] || [ -f "$repo/setup.py" ] || [ -f "$repo/requirements.txt" ]; then
    echo "python"
  elif [ -f "$repo/Cargo.toml" ]; then
    echo "rust"
  elif [ -f "$repo/pom.xml" ] || ls "$repo"/build.gradle* >/dev/null 2>&1; then
    echo "java"
  else
    echo "unknown"
  fi
}

# Extract a bounded "repo signal" for the LLM: tree listing + manifest
# contents + README excerpt. Echoes the signal on stdout; echoes empty
# string when the total exceeds the byte budget (caller emits
# repo_too_large).
#
# Arguments:
#   $1 = repo_path (already resolved)
#   $2 = optional byte budget; defaults to $PLAN_MAX_REPO_SIGNAL_BYTES env
#        or PLAN_DEFAULT_REPO_SIGNAL_BYTES.
plan_extract_repo_signal() {
  local repo="$1"
  local budget="${2:-${PLAN_MAX_REPO_SIGNAL_BYTES:-$PLAN_DEFAULT_REPO_SIGNAL_BYTES}}"
  local out=""

  # Tree: top 200 entries from git ls-files; fall back to find if not a git repo.
  local tree
  if (cd "$repo" && git rev-parse --is-inside-work-tree >/dev/null 2>&1); then
    tree="$(cd "$repo" && git ls-files 2>/dev/null | head -200)"
  else
    tree="$(cd "$repo" && find . -maxdepth 4 -type f -not -path '*/.*' 2>/dev/null | sed 's|^\./||' | head -200)"
  fi
  out+=$'## tree\n'"$tree"$'\n\n'

  # Manifests — concatenate each that exists.
  out+=$'## manifest\n'
  local manifest_count=0
  for m in go.mod package.json pyproject.toml setup.py requirements.txt Cargo.toml pom.xml build.gradle build.gradle.kts; do
    if [ -f "$repo/$m" ]; then
      out+="--- $m ---"$'\n'
      out+="$(cat "$repo/$m")"$'\n\n'
      manifest_count=$((manifest_count + 1))
    fi
  done
  if [ "$manifest_count" -eq 0 ]; then
    out+=$'(no recognised manifest)\n\n'
  fi

  # README excerpt — first 4 KB.
  if [ -f "$repo/README.md" ]; then
    out+=$'## readme\n'
    out+="$(head -c 4096 "$repo/README.md")"$'\n'
  fi

  # Size-budget check.
  local size=${#out}
  if [ "$size" -gt "$budget" ]; then
    echo ""
    return 0
  fi
  printf '%s' "$out"
}

# Call the configured LLM with a system + user prompt. Strips markdown fences
# from the response. Echoes the raw text content on stdout. Dies on HTTP error
# or empty response.
#
# Provider selection: ANTHROPIC_API_KEY wins if set, else OPENAI_API_KEY.
# Model override via PLAN_MODEL env var.
plan_call_llm() {
  local system_prompt="$1"
  local user_prompt="$2"

  local sys_tmp user_tmp resp http_code body text
  sys_tmp="$(mktemp)"
  user_tmp="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$sys_tmp' '$user_tmp'" RETURN
  printf '%s' "$system_prompt" > "$sys_tmp"
  printf '%s' "$user_prompt" > "$user_tmp"

  if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    local model="${PLAN_MODEL:-claude-sonnet-4-5}"
    local payload
    payload="$(jq -n \
      --arg model "$model" \
      --rawfile system "$sys_tmp" \
      --rawfile user "$user_tmp" \
      '{model: $model, max_tokens: 4096, system: $system, messages: [{role: "user", content: $user}]}')"
    resp="$(curl -sS --max-time 150 \
      -w "\n%{http_code}" \
      -X POST "https://api.anthropic.com/v1/messages" \
      -H "Content-Type: application/json" \
      -H "x-api-key: ${ANTHROPIC_API_KEY}" \
      -H "anthropic-version: 2023-06-01" \
      -d "$payload")" || plan_die "network error calling Anthropic API"
    http_code="$(echo "$resp" | tail -1)"
    body="$(echo "$resp" | sed '$d')"
    if [ "$http_code" -ne 200 ]; then
      plan_die "Anthropic API returned status $http_code"
    fi
    text="$(echo "$body" | jq -r '.content[0].text // empty')"
  elif [ -n "${OPENAI_API_KEY:-}" ]; then
    local model="${PLAN_MODEL:-gpt-4.1}"
    local base="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
    local payload
    payload="$(jq -n \
      --arg model "$model" \
      --rawfile system "$sys_tmp" \
      --rawfile user "$user_tmp" \
      '{model: $model, max_tokens: 4096, messages: [{role: "system", content: $system}, {role: "user", content: $user}]}')"
    resp="$(curl -sS --max-time 150 \
      -w "\n%{http_code}" \
      -X POST "${base}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${OPENAI_API_KEY}" \
      -d "$payload")" || plan_die "network error calling OpenAI API"
    http_code="$(echo "$resp" | tail -1)"
    body="$(echo "$resp" | sed '$d')"
    if [ "$http_code" -ne 200 ]; then
      plan_die "OpenAI API returned status $http_code"
    fi
    text="$(echo "$body" | jq -r '.choices[0].message.content // empty')"
  else
    plan_die "either ANTHROPIC_API_KEY or OPENAI_API_KEY must be set"
  fi

  if [ -z "$text" ]; then
    plan_die "LLM returned empty response"
  fi

  # Strip markdown fences if present (```json ... ```).
  text="$(echo "$text" | sed -E 's/^```([a-zA-Z]+)?$//; s/```$//' | sed '/^$/d')"
  printf '%s' "$text"
}
