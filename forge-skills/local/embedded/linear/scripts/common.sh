#!/usr/bin/env bash
# Shared helpers for linear skill scripts.
# Do not run this file directly — it is sourced.

LINEAR_API_URL="https://api.linear.app/graphql"

# Read JSON arg from $1 or stdin, validate, set INPUT_JSON.
linear_read_input() {
  if [ -n "${1:-}" ]; then
    INPUT_JSON="$1"
  else
    INPUT_JSON="$(cat)"
  fi
  if ! echo "$INPUT_JSON" | jq -e . >/dev/null 2>&1; then
    linear_die "invalid JSON input"
  fi
}

# Emit error to stderr as JSON, exit 1.
linear_die() {
  jq -n --arg msg "$1" '{"error": $msg}' >&2
  exit 1
}

# Require an env var to be set, else die.
linear_require_env() {
  local var="$1"
  if [ -z "${!var:-}" ]; then
    linear_die "missing required environment variable: $var"
  fi
}

# POST a GraphQL query. Args: $1 = query, $2 = variables JSON.
# Echoes the data payload on success, dies on error.
linear_graphql() {
  local query="$1"
  local variables="${2:-{\}}"
  linear_require_env LINEAR_API_KEY
  local body
  body="$(jq -n --arg q "$query" --argjson v "$variables" '{query: $q, variables: $v}')"
  local resp
  resp="$(curl -sS -X POST \
    -H "Authorization: $LINEAR_API_KEY" \
    -H "Content-Type: application/json" \
    --max-time 30 \
    -d "$body" \
    "$LINEAR_API_URL")" || linear_die "network error calling Linear API"
  # Surface GraphQL errors clearly.
  if echo "$resp" | jq -e '.errors' >/dev/null 2>&1; then
    local err
    err="$(echo "$resp" | jq -r '.errors[0].message')"
    linear_die "linear api error: $err"
  fi
  echo "$resp" | jq '.data'
}
