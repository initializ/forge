#!/usr/bin/env bash
# code-plan-create.sh — Generate a structured implementation plan for a task.
# Usage: ./code-plan-create.sh '{"repo_path":"~/work/app","task":"...","ticket_id":"ENG-1"}'
#
# Requires: curl, jq, git
# Env: ANTHROPIC_API_KEY or OPENAI_API_KEY (at least one)
# Optional: PLAN_MODEL, PLAN_MAX_REPO_SIGNAL_BYTES
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

plan_read_input "$@"

REPO_PATH="$(echo "$INPUT_JSON" | jq -r '.repo_path // empty')"
TASK="$(echo "$INPUT_JSON" | jq -r '.task // empty')"
TICKET_ID="$(echo "$INPUT_JSON" | jq -r '.ticket_id // empty')"
TARGET_BRANCH="$(echo "$INPUT_JSON" | jq -r '.target_branch // empty')"
CONTEXT_FILES="$(echo "$INPUT_JSON" | jq -c '.context_files // []')"

[ -n "$REPO_PATH" ] || plan_die "repo_path is required"
[ -n "$TASK" ] || plan_die "task is required"

if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
  plan_die "either ANTHROPIC_API_KEY or OPENAI_API_KEY must be set"
fi

REPO_PATH="$(plan_resolve_repo_path "$REPO_PATH")"
STACK="$(plan_detect_stack "$REPO_PATH")"

BUDGET="${PLAN_MAX_REPO_SIGNAL_BYTES:-$PLAN_DEFAULT_REPO_SIGNAL_BYTES}"
SIGNAL="$(plan_extract_repo_signal "$REPO_PATH" "$BUDGET")"
if [ -z "$SIGNAL" ]; then
  # Probe the unbounded size so the caller knows how far over budget they are.
  RAW_SIZE="$(cd "$REPO_PATH" && { git ls-files 2>/dev/null || find . -maxdepth 4 -type f -not -path '*/.*'; } | wc -c)"
  jq -n --argjson tb "$RAW_SIZE" --argjson lb "$BUDGET" \
    '{status:"repo_too_large", tree_bytes:$tb, limit_bytes:$lb, suggestion:"Pass context_files explicitly to scope the plan, or run from a subdirectory."}'
  exit 0
fi

# Append context_files content (capped at 5 files, 50 KB total).
CONTEXT_SECTION=""
if [ "$(echo "$CONTEXT_FILES" | jq 'length')" -gt 0 ]; then
  CONTEXT_SECTION=$'\n## context_files\n'
  total_bytes=0
  count=0
  while IFS= read -r rel; do
    [ "$count" -ge 5 ] && break
    [ -z "$rel" ] && continue
    abs="$REPO_PATH/$rel"
    if [ -f "$abs" ]; then
      file_size=$(wc -c <"$abs" | tr -d ' ')
      remaining=$((51200 - total_bytes))
      [ "$remaining" -le 0 ] && break
      take=$file_size
      [ "$take" -gt "$remaining" ] && take=$remaining
      CONTEXT_SECTION+="--- $rel ---"$'\n'
      CONTEXT_SECTION+="$(head -c "$take" "$abs")"$'\n\n'
      total_bytes=$((total_bytes + take))
      count=$((count + 1))
    fi
  done < <(echo "$CONTEXT_FILES" | jq -r '.[]')
fi

# --- System prompt: schema + rules. The LLM must return only JSON. ---
SYSTEM_PROMPT='You are a senior engineer producing a structured implementation plan for a specific task in a specific repository.

Return a single JSON object matching this exact schema:

{
  "summary": "One-paragraph plain-English description of the planned change.",
  "approach": "Higher-level reasoning: why this approach, what alternatives were considered.",
  "files_to_create": [
    { "path": "relative/path", "purpose": "Why this file is needed." }
  ],
  "files_to_modify": [
    { "path": "relative/path", "change": "What changes here and why." }
  ],
  "tests_to_add": [
    { "path": "relative/path", "covers": "What this test exercises." }
  ],
  "risks": [
    { "severity": "low|medium|high", "risk": "What might go wrong.", "mitigation": "How to mitigate." }
  ],
  "complexity": "low|medium|high",
  "estimated_file_count": 0,
  "open_questions": [ "Unresolved question if the task is ambiguous." ]
}

Rules:
- Return ONLY a JSON object matching the schema. No markdown fences. No explanation outside the JSON.
- If the task is ambiguous, include the ambiguity in open_questions rather than guessing. Empty open_questions is acceptable if the task is fully specified.
- If the task description references files or symbols not present in the repo signal, do not invent them. Add a risk noting the absence.
- Prefer modifying existing files over creating new ones unless the task clearly introduces a new concept.
- Tests are not optional. Every plan must include tests_to_add unless the task is documentation-only.
- Use repo-relative paths only (e.g. "src/foo.go", never absolute or with ~).
- estimated_file_count must equal len(files_to_create) + len(files_to_modify) + len(tests_to_add).'

# Build the user prompt.
USER_PROMPT="## task"$'\n'"$TASK"$'\n\n'
if [ -n "$TICKET_ID" ]; then
  USER_PROMPT+="ticket_id: $TICKET_ID"$'\n'
fi
if [ -n "$TARGET_BRANCH" ]; then
  USER_PROMPT+="target_branch: $TARGET_BRANCH"$'\n'
fi
USER_PROMPT+=$'\n'"## stack_detected"$'\n'"$STACK"$'\n\n'
USER_PROMPT+="## repo_signal"$'\n'"$SIGNAL"
if [ -n "$CONTEXT_SECTION" ]; then
  USER_PROMPT+="$CONTEXT_SECTION"
fi

# --- One LLM call. Retry once if the response fails schema validation. ---

REQUIRED_KEYS='["summary","approach","files_to_create","files_to_modify","tests_to_add","risks","complexity","estimated_file_count","open_questions"]'

validate_plan() {
  local json="$1"
  if ! echo "$json" | jq -e . >/dev/null 2>&1; then
    return 1
  fi
  for key in summary approach files_to_create files_to_modify tests_to_add risks complexity estimated_file_count open_questions; do
    if ! echo "$json" | jq -e "has(\"$key\")" >/dev/null; then
      return 1
    fi
  done
  return 0
}

RAW="$(plan_call_llm "$SYSTEM_PROMPT" "$USER_PROMPT")"

if ! validate_plan "$RAW"; then
  # Retry once with an explicit schema-reminder follow-up.
  RETRY_USER="$USER_PROMPT"$'\n\nYour previous response did not match the schema. Required top-level keys: '"$REQUIRED_KEYS"$'. Return ONLY the JSON object, no markdown fences, no commentary.'
  RAW="$(plan_call_llm "$SYSTEM_PROMPT" "$RETRY_USER")"
fi

if ! validate_plan "$RAW"; then
  # Final emission: structured error including the raw text (capped) for debugging.
  jq -n --arg raw "$(echo "$RAW" | head -c 2000)" \
    '{status:"error", error:"llm output did not match plan schema", raw:$raw}'
  exit 0
fi

# Compose final output: prepend status + ticket_id + stack_detected.
echo "$RAW" | jq --arg tid "$TICKET_ID" --arg stack "$STACK" \
  '{status:"ok", ticket_id:$tid, stack_detected:$stack} + .'
