#!/usr/bin/env bash
# code-review-diff.sh — AI-powered code review for diffs (GitHub PR or local git).
# Usage: ./code-review-diff.sh '{"pr_url": "...", "base_ref": "main", "focus": "all"}'
#
# Requires: curl, jq, git
# Env: ANTHROPIC_API_KEY or OPENAI_API_KEY (at least one)
# Optional: REVIEW_MODEL, REVIEW_MAX_DIFF_BYTES, GH_TOKEN, FORGE_REVIEW_STANDARDS_DIR
set -euo pipefail

# --- Read and validate input first (agent can fix these) ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: code-review-diff.sh {\"pr_url\": \"...\"} or {\"base_ref\": \"main\", \"repo_path\": \"/path/to/repo\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Parse fields ---
PR_URL=$(echo "$INPUT" | jq -r '.pr_url // empty')
BASE_REF=$(echo "$INPUT" | jq -r '.base_ref // empty')
REPO_PATH=$(echo "$INPUT" | jq -r '.repo_path // empty')
FOCUS=$(echo "$INPUT" | jq -r '.focus // "all"')
EXTRA_CONTEXT=$(echo "$INPUT" | jq -r '.extra_context // empty')

if [ -z "$PR_URL" ] && [ -z "$BASE_REF" ]; then
  echo '{"error": "one of pr_url or base_ref is required. Use pr_url for GitHub PRs or base_ref + repo_path for local diffs"}' >&2
  exit 1
fi

# --- Validate environment (requires deployment config) ---
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
  echo '{"error": "Either ANTHROPIC_API_KEY or OPENAI_API_KEY must be set"}' >&2
  exit 1
fi

# --- Change to repo directory for local operations ---
if [ -n "$REPO_PATH" ]; then
  # Expand ~ to actual home directory (shell doesn't expand ~ inside variables)
  REPO_PATH="${REPO_PATH/#\~/$HOME}"

  if [ ! -d "$REPO_PATH" ]; then
    echo "{\"error\": \"repo_path directory does not exist: $REPO_PATH\"}" >&2
    exit 1
  fi
  cd "$REPO_PATH"
elif [ -n "$BASE_REF" ]; then
  echo '{"error": "repo_path is required for local diff review (scripts run in the agent directory, not the user project)"}' >&2
  exit 1
fi

# --- Configuration ---
MAX_DIFF_BYTES="${REVIEW_MAX_DIFF_BYTES:-100000}"

# --- Obtain diff ---
DIFF_CONTENT=""

if [ -n "$PR_URL" ]; then
  # Extract owner/repo and PR number from various GitHub URL formats
  # Supports: https://github.com/owner/repo/pull/123
  #           https://github.com/owner/repo/pull/123/files
  #           https://github.com/owner/repo/pull/123/commits
  PR_PATH=$(echo "$PR_URL" | sed -E 's|^https?://github\.com/||' | sed -E 's#/(files|commits|checks)$##')
  OWNER_REPO=$(echo "$PR_PATH" | sed -E 's|/pull/[0-9]+.*$||')
  PR_NUMBER=$(echo "$PR_PATH" | sed -E 's|.*/pull/([0-9]+).*$|\1|')

  if [ -z "$OWNER_REPO" ] || [ -z "$PR_NUMBER" ]; then
    echo '{"error": "could not parse GitHub PR URL. Expected format: https://github.com/owner/repo/pull/123"}' >&2
    exit 1
  fi

  # Use gh CLI if available and GH_TOKEN is set, otherwise use API directly
  if command -v gh >/dev/null 2>&1 && [ -n "${GH_TOKEN:-}" ]; then
    DIFF_CONTENT=$(gh pr diff "$PR_NUMBER" --repo "$OWNER_REPO" 2>/dev/null) || true
  fi

  # Fallback to curl if gh didn't work
  if [ -z "$DIFF_CONTENT" ]; then
    DIFF_HEADERS=(-H "Accept: application/vnd.github.v3.diff")
    if [ -n "${GH_TOKEN:-}" ]; then
      DIFF_HEADERS+=(-H "Authorization: Bearer ${GH_TOKEN}")
    fi
    DIFF_CONTENT=$(curl -s --max-time 30 \
      "${DIFF_HEADERS[@]}" \
      "https://api.github.com/repos/${OWNER_REPO}/pulls/${PR_NUMBER}" 2>/dev/null) || true
  fi

  if [ -z "$DIFF_CONTENT" ]; then
    echo '{"error": "failed to fetch PR diff. Check GH_TOKEN and PR URL."}' >&2
    exit 1
  fi
else
  # Local git diff (repo_path validated and cd'd above)
  if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo '{"error": "not inside a git repository. Check repo_path or provide pr_url for remote review."}' >&2
    exit 1
  fi

  # Write diff to temp file to avoid storing multi-MB diffs in bash variables.
  # Large diffs (e.g. 3MB+) cause SIGPIPE and memory issues in shell pipelines.
  DIFF_TMPFILE=$(mktemp)

  # Use merge-base to find the fork point when diffing against a branch name,
  # so we only see changes on the current branch — not changes merged to the
  # base branch after this branch was created.
  # NOTE: Omit "HEAD" as the second argument so the diff includes uncommitted
  # working tree changes, not just committed ones.
  MERGE_BASE=$(git merge-base "$BASE_REF" HEAD 2>/dev/null) || true

  DIFF_REF="${MERGE_BASE:-$BASE_REF}"

  # Capture tracked changes (committed + uncommitted modifications)
  git diff "$DIFF_REF" > "$DIFF_TMPFILE" 2>/dev/null || true

  # Also include UNTRACKED files — git diff ignores them entirely, but they are
  # often the user's primary new work (new files not yet git-added).
  UNTRACKED_FILES=$(git ls-files --others --exclude-standard 2>/dev/null) || true
  if [ -n "$UNTRACKED_FILES" ]; then
    while IFS= read -r ufile; do
      if [ -f "$ufile" ]; then
        # Generate a diff-like entry for the untracked file
        git diff --no-index /dev/null "$ufile" >> "$DIFF_TMPFILE" 2>/dev/null || true
      fi
    done <<< "$UNTRACKED_FILES"
  fi

  if [ ! -s "$DIFF_TMPFILE" ]; then
    echo '{"error": "no diff found for base_ref: '"$BASE_REF"'. No tracked changes or untracked files detected."}' >&2
    exit 1
  fi
fi

# --- Handle large diffs ---
# When the diff exceeds MAX_DIFF_BYTES, do NOT silently truncate (the first N bytes
# are just alphabetically-early files, missing the user's actual changes). Instead,
# return the full file list (--stat) so the agent can ask the user to narrow scope.
DIFF_SIZE=0
if [ -n "${DIFF_TMPFILE:-}" ]; then
  DIFF_SIZE=$(wc -c < "$DIFF_TMPFILE" | tr -d ' ')
else
  DIFF_SIZE=${#DIFF_CONTENT}
fi

if [ "$DIFF_SIZE" -gt "$MAX_DIFF_BYTES" ]; then
  # Generate diff stat for the agent to present to the user
  if [ -n "${DIFF_TMPFILE:-}" ]; then
    # Local diff: stat from git (tracked) + untracked file list
    DIFF_STAT=$(git diff "${DIFF_REF:-$BASE_REF}" --stat 2>/dev/null || true)
    UNTRACKED_LIST=$(git ls-files --others --exclude-standard 2>/dev/null || true)
    if [ -n "$UNTRACKED_LIST" ]; then
      DIFF_STAT="${DIFF_STAT}
Untracked (new) files:
${UNTRACKED_LIST}"
    fi
    TRACKED_COUNT=$(git diff "${DIFF_REF:-$BASE_REF}" --name-only 2>/dev/null | wc -l | tr -d ' ')
    UNTRACKED_COUNT=$(echo "$UNTRACKED_LIST" | grep -c . 2>/dev/null || echo 0)
    FILE_COUNT=$((TRACKED_COUNT + UNTRACKED_COUNT))
  else
    # PR diff: extract file list from the diff content
    DIFF_STAT=$(echo "$DIFF_CONTENT" | grep '^diff --git' | sed 's|diff --git a/||; s| b/.*||' || true)
    FILE_COUNT=$(echo "$DIFF_STAT" | wc -l | tr -d ' ')
  fi

  jq -n \
    --arg diff_size "$DIFF_SIZE" \
    --arg max_size "$MAX_DIFF_BYTES" \
    --arg file_count "$FILE_COUNT" \
    --arg diff_stat "$DIFF_STAT" \
    --arg base_ref "$BASE_REF" \
    '{
      "error": "diff_too_large",
      "message": "The diff is too large to review in one pass. Ask the user to narrow the scope.",
      "diff_bytes": ($diff_size | tonumber),
      "max_bytes": ($max_size | tonumber),
      "files_changed": ($file_count | tonumber),
      "base_ref": $base_ref,
      "diff_stat": $diff_stat,
      "suggestions": [
        "Review only uncommitted changes: use base_ref=HEAD",
        "Review the last N commits: use base_ref=HEAD~N",
        "Review specific files: ask the user which files to focus on",
        "Increase the limit: set REVIEW_MAX_DIFF_BYTES env var"
      ]
    }' >&2
  exit 1
fi

# Read diff content into variable (only when within size limit)
TRUNCATED="false"
if [ -n "${DIFF_TMPFILE:-}" ]; then
  DIFF_CONTENT=$(cat "$DIFF_TMPFILE")
fi

# --- Load custom standards if available ---
STANDARDS_CONTEXT=""
STANDARDS_DIR="${FORGE_REVIEW_STANDARDS_DIR:-}"
if [ -z "$STANDARDS_DIR" ] && [ -d ".forge-review/standards" ]; then
  STANDARDS_DIR=".forge-review/standards"
fi

if [ -n "$STANDARDS_DIR" ] && [ -d "$STANDARDS_DIR" ]; then
  for std_file in "$STANDARDS_DIR"/*.md; do
    if [ -f "$std_file" ]; then
      STANDARDS_CONTEXT="${STANDARDS_CONTEXT}
--- $(basename "$std_file") ---
$(cat "$std_file")
"
    fi
  done
fi

# --- Build review prompt ---
SYSTEM_PROMPT="You are an expert code reviewer. Analyze the provided diff and produce a structured JSON review.

Focus area: ${FOCUS}

Your review must be returned as a single JSON object with this exact schema:
{
  \"summary\": \"Brief overall assessment of the changes\",
  \"risk_level\": \"low|medium|high|critical\",
  \"findings\": [
    {
      \"file\": \"path/to/file\",
      \"line\": <line_number_or_null>,
      \"severity\": \"error|warning|info|nitpick\",
      \"category\": \"bug|security|style|performance|maintainability\",
      \"title\": \"Short title\",
      \"description\": \"Detailed explanation\",
      \"suggestion\": \"Suggested fix or improvement\"
    }
  ],
  \"stats\": {
    \"files_reviewed\": <count>,
    \"total_findings\": <count>,
    \"by_severity\": {\"error\": 0, \"warning\": 0, \"info\": 0, \"nitpick\": 0}
  }
}

Rules:
- Return ONLY valid JSON, no markdown fences, no extra text
- Be specific: include file paths and line numbers where possible
- Prioritize actionable findings over style nitpicks
- For security focus, emphasize OWASP top 10, injection, auth issues
- For bug focus, emphasize logic errors, null/nil dereferences, race conditions
- If the diff is truncated, note this in the summary"

if [ -n "$STANDARDS_CONTEXT" ]; then
  SYSTEM_PROMPT="${SYSTEM_PROMPT}

Apply these organization coding standards in your review:
${STANDARDS_CONTEXT}"
fi

USER_PROMPT="Review this diff"
if [ -n "$EXTRA_CONTEXT" ]; then
  USER_PROMPT="${USER_PROMPT}. Additional context: ${EXTRA_CONTEXT}"
fi
USER_PROMPT="${USER_PROMPT}:

${DIFF_CONTENT}"
if [ "$TRUNCATED" = "true" ]; then
  USER_PROMPT="${USER_PROMPT}

[NOTE: Diff was truncated at ${MAX_DIFF_BYTES} bytes. Some files may be missing from the review.]"
fi

# --- Route to LLM API ---
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  # Anthropic Claude API
  MODEL="${REVIEW_MODEL:-claude-sonnet-4-20250514}"

  # Use jq --rawfile for safe JSON encoding of prompts
  TEMP_SYSTEM=$(mktemp)
  TEMP_USER=$(mktemp)
  trap 'rm -f "$TEMP_SYSTEM" "$TEMP_USER" "${DIFF_TMPFILE:-}"' EXIT
  printf '%s' "$SYSTEM_PROMPT" > "$TEMP_SYSTEM"
  printf '%s' "$USER_PROMPT" > "$TEMP_USER"

  API_PAYLOAD=$(jq -n \
    --arg model "$MODEL" \
    --rawfile system "$TEMP_SYSTEM" \
    --rawfile user "$TEMP_USER" \
    '{
      model: $model,
      max_tokens: 4096,
      system: $system,
      messages: [
        {role: "user", content: $user}
      ]
    }')

  RESPONSE=$(curl -s --max-time 90 \
    -w "\n%{http_code}" \
    -X POST "https://api.anthropic.com/v1/messages" \
    -H "Content-Type: application/json" \
    -H "x-api-key: ${ANTHROPIC_API_KEY}" \
    -H "anthropic-version: 2023-06-01" \
    -d "$API_PAYLOAD")

  HTTP_CODE=$(echo "$RESPONSE" | tail -1)
  BODY=$(echo "$RESPONSE" | sed '$d')

  if [ "$HTTP_CODE" -ne 200 ]; then
    echo "{\"error\": \"Anthropic API returned status $HTTP_CODE\", \"details\": $(echo "$BODY" | jq -c '.' 2>/dev/null || echo '""')}" >&2
    exit 1
  fi

  # Extract text content from Anthropic response
  REVIEW_TEXT=$(echo "$BODY" | jq -r '.content[0].text // empty')

elif [ -n "${OPENAI_API_KEY:-}" ]; then
  # OpenAI API — supports both Chat Completions and Responses API (OAuth)
  MODEL="${REVIEW_MODEL:-gpt-5.4}"
  OPENAI_BASE="${OPENAI_BASE_URL:-https://api.openai.com/v1}"

  TEMP_SYSTEM=$(mktemp)
  TEMP_USER=$(mktemp)
  trap 'rm -f "$TEMP_SYSTEM" "$TEMP_USER" "${DIFF_TMPFILE:-}"' EXIT
  printf '%s' "$SYSTEM_PROMPT" > "$TEMP_SYSTEM"
  printf '%s' "$USER_PROMPT" > "$TEMP_USER"

  if [ -n "${OPENAI_BASE_URL:-}" ]; then
    # Responses API (OAuth/Codex flow) — requires streaming
    API_PAYLOAD=$(jq -n \
      --arg model "$MODEL" \
      --rawfile instructions "$TEMP_SYSTEM" \
      --rawfile user "$TEMP_USER" \
      '{
        model: $model,
        instructions: $instructions,
        input: [
          {role: "user", content: $user}
        ],
        store: false,
        stream: true
      }')

    STREAM_TMPFILE=$(mktemp)
    HTTP_CODE=$(curl -s --max-time 180 \
      -w "%{http_code}" \
      -o "$STREAM_TMPFILE" \
      -X POST "${OPENAI_BASE}/responses" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${OPENAI_API_KEY}" \
      -d "$API_PAYLOAD")

    if [ "$HTTP_CODE" -ne 200 ]; then
      BODY=$(cat "$STREAM_TMPFILE")
      rm -f "$STREAM_TMPFILE"
      echo "{\"error\": \"OpenAI Responses API returned status $HTTP_CODE\", \"details\": $(echo "$BODY" | jq -c '.' 2>/dev/null || echo '""')}" >&2
      exit 1
    fi

    # Parse SSE stream: collect text deltas from response.output_text.delta events.
    # Use jq -j (join) to concatenate without adding extra newlines between deltas.
    REVIEW_TEXT=$(grep '^data: ' "$STREAM_TMPFILE" \
      | sed 's/^data: //' \
      | grep -v '^\[DONE\]$' \
      | jq -j 'select(.type == "response.output_text.delta") | .delta // empty' 2>/dev/null)
    rm -f "$STREAM_TMPFILE"
  else
    # Standard Chat Completions API
    API_PAYLOAD=$(jq -n \
      --arg model "$MODEL" \
      --rawfile system "$TEMP_SYSTEM" \
      --rawfile user "$TEMP_USER" \
      '{
        model: $model,
        max_tokens: 4096,
        messages: [
          {role: "system", content: $system},
          {role: "user", content: $user}
        ]
      }')

    RESPONSE=$(curl -s --max-time 90 \
      -w "\n%{http_code}" \
      -X POST "${OPENAI_BASE}/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${OPENAI_API_KEY}" \
      -d "$API_PAYLOAD")

    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')

    if [ "$HTTP_CODE" -ne 200 ]; then
      echo "{\"error\": \"OpenAI API returned status $HTTP_CODE\", \"details\": $(echo "$BODY" | jq -c '.' 2>/dev/null || echo '""')}" >&2
      exit 1
    fi

    # Extract text from Chat Completions response
    REVIEW_TEXT=$(echo "$BODY" | jq -r '.choices[0].message.content // empty')
  fi
fi

if [ -z "${REVIEW_TEXT:-}" ]; then
  echo '{"error": "LLM returned empty response"}' >&2
  exit 1
fi

# --- Parse and validate JSON output ---
# Strip markdown fences if the LLM wrapped the response
REVIEW_TEXT=$(echo "$REVIEW_TEXT" | sed -E 's/^```(json)?//; s/```$//' | sed '/^$/d')

REVIEW_JSON=""
if echo "$REVIEW_TEXT" | jq empty 2>/dev/null; then
  REVIEW_JSON=$(echo "$REVIEW_TEXT" | jq .)
else
  # If LLM didn't return valid JSON, wrap it
  REVIEW_JSON=$(jq -n --arg text "$REVIEW_TEXT" '{
    "summary": $text,
    "risk_level": "unknown",
    "findings": [],
    "stats": {"files_reviewed": 0, "total_findings": 0, "by_severity": {}},
    "parse_warning": "LLM response was not valid JSON; raw text returned as summary"
  }')
fi

# --- Render markdown summary for human consumption ---
render_markdown() {
  local json="$1"
  local summary risk total errors warnings infos nitpicks

  summary=$(echo "$json" | jq -r '.summary // "No summary"')
  risk=$(echo "$json" | jq -r '.risk_level // "unknown"')
  total=$(echo "$json" | jq -r '.stats.total_findings // 0')
  errors=$(echo "$json" | jq -r '.stats.by_severity.error // 0')
  warnings=$(echo "$json" | jq -r '.stats.by_severity.warning // 0')
  infos=$(echo "$json" | jq -r '.stats.by_severity.info // 0')
  nitpicks=$(echo "$json" | jq -r '.stats.by_severity.nitpick // 0')

  echo "## Code Review Summary"
  echo ""
  echo "**Risk Level:** ${risk}"
  echo ""
  echo "${summary}"
  echo ""
  echo "**Findings:** ${total} total — ${errors} errors, ${warnings} warnings, ${infos} info, ${nitpicks} nitpicks"
  echo ""

  # Render each finding
  local count
  count=$(echo "$json" | jq '.findings | length')
  if [ "$count" -gt 0 ]; then
    echo "### Findings"
    echo ""
    echo "$json" | jq -r '.findings[] | "#### [\(.severity | ascii_upcase)] \(.title)\n**File:** `\(.file)`\(.line | if . then " (line \(.))" else "" end) | **Category:** \(.category)\n\n\(.description)\n\n**Suggestion:** \(.suggestion)\n\n---\n"'
  fi
}

MARKDOWN=$(render_markdown "$REVIEW_JSON")

# Output markdown (primary) followed by raw JSON for structured access
echo "$MARKDOWN"
echo ""
echo '<details><summary>Raw JSON</summary>'
echo ""
echo '```json'
echo "$REVIEW_JSON"
echo '```'
echo '</details>'
