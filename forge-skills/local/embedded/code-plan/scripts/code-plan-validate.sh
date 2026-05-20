#!/usr/bin/env bash
# code-plan-validate.sh — Filesystem-only audit of a plan against repo state.
# Usage: ./code-plan-validate.sh '{"repo_path":"~/work/app","plan":{...}}'
#
# No LLM call. Pure filesystem checks: do files_to_modify exist? Do
# files_to_create collide with existing files?
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

plan_read_input "$@"

REPO_PATH="$(echo "$INPUT_JSON" | jq -r '.repo_path // empty')"
PLAN="$(echo "$INPUT_JSON" | jq -c '.plan // empty')"

[ -n "$REPO_PATH" ] || plan_die "repo_path is required"
[ -n "$PLAN" ] || plan_die "plan is required"
REPO_PATH="$(plan_resolve_repo_path "$REPO_PATH")"

# Build files_to_modify_exist: for each entry, did the file survive?
MODIFY_PATHS="$(echo "$PLAN" | jq -r '(.files_to_modify // [])[].path // empty')"
CREATE_PATHS="$(echo "$PLAN" | jq -r '(.files_to_create // [])[].path // empty')"

MODIFY_RESULTS='[]'
WARNINGS='[]'
idx=0
while IFS= read -r rel; do
  [ -z "$rel" ] && continue
  abs="$REPO_PATH/$rel"
  if [ -f "$abs" ]; then
    exists=true
  else
    exists=false
    WARNINGS="$(echo "$WARNINGS" | jq --arg msg "files_to_modify[$idx] ($rel) does not exist in the repo; the plan may be stale." '. + [$msg]')"
  fi
  MODIFY_RESULTS="$(echo "$MODIFY_RESULTS" | jq --arg p "$rel" --argjson e "$exists" '. + [{path:$p, exists:$e}]')"
  idx=$((idx + 1))
done <<< "$MODIFY_PATHS"

CREATE_RESULTS='[]'
idx=0
while IFS= read -r rel; do
  [ -z "$rel" ] && continue
  abs="$REPO_PATH/$rel"
  if [ -f "$abs" ]; then
    exists=true
    WARNINGS="$(echo "$WARNINGS" | jq --arg msg "files_to_create[$idx] ($rel) already exists in the repo; the plan would clobber it." '. + [$msg]')"
  else
    exists=false
  fi
  CREATE_RESULTS="$(echo "$CREATE_RESULTS" | jq --arg p "$rel" --argjson e "$exists" '. + [{path:$p, exists:$e}]')"
  idx=$((idx + 1))
done <<< "$CREATE_PATHS"

jq -n \
  --argjson modify "$MODIFY_RESULTS" \
  --argjson create "$CREATE_RESULTS" \
  --argjson warnings "$WARNINGS" \
  '{status:"ok", files_to_modify_exist:$modify, files_to_create_collisions:$create, warnings:$warnings}'
