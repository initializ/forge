#!/usr/bin/env bash
# code-review-file.sh — AI-powered deep review of a single file.
# Usage: ./code-review-file.sh '{"file_path": "src/main.go", "focus": "security"}'
#
# Requires: curl, jq
# Env: ANTHROPIC_API_KEY or OPENAI_API_KEY (at least one)
# Optional:
#   REVIEW_PROVIDER          — "anthropic" or "openai"; explicit override.
#                              Auto-detected when unset (see #133).
#   REVIEW_MODEL             — provider-specific model name.
#   GH_TOKEN                 — GitHub token.
#   FORGE_REVIEW_STANDARDS_DIR — custom standards dir.
#   OPENAI_BASE_URL          — base URL for OpenAI-compatible providers.
#                              Always uses /chat/completions; see
#                              OPENAI_USE_RESPONSES_API to opt into
#                              OpenAI's proprietary Responses API.
#   OPENAI_USE_RESPONSES_API — set to "1" for OpenAI Responses API.
set -euo pipefail

# --- Read and validate input first (agent can fix these) ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: code-review-file.sh {\"file_path\": \"path/to/file\", \"repo_path\": \"/path/to/repo\"}"}' >&2
  exit 1
fi

if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Parse fields ---
FILE_PATH=$(echo "$INPUT" | jq -r '.file_path // empty')
REPO_PATH=$(echo "$INPUT" | jq -r '.repo_path // empty')
PR_URL=$(echo "$INPUT" | jq -r '.pr_url // empty')
FOCUS=$(echo "$INPUT" | jq -r '.focus // "all"')
EXTRA_CONTEXT=$(echo "$INPUT" | jq -r '.extra_context // empty')

if [ -z "$FILE_PATH" ]; then
  echo '{"error": "file_path field is required"}' >&2
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
elif [ -z "$PR_URL" ]; then
  echo '{"error": "repo_path is required for local file review (scripts run in the agent directory, not the user project)"}' >&2
  exit 1
fi

# --- Obtain file content ---
FILE_CONTENT=""

if [ -n "$PR_URL" ]; then
  # Extract owner/repo and PR number from GitHub URL
  PR_PATH=$(echo "$PR_URL" | sed -E 's|^https?://github\.com/||' | sed -E 's#/(files|commits|checks)$##')
  OWNER_REPO=$(echo "$PR_PATH" | sed -E 's|/pull/[0-9]+.*$||')
  PR_NUMBER=$(echo "$PR_PATH" | sed -E 's|.*/pull/([0-9]+).*$|\1|')

  if [ -z "$OWNER_REPO" ] || [ -z "$PR_NUMBER" ]; then
    echo '{"error": "could not parse GitHub PR URL"}' >&2
    exit 1
  fi

  # Get the head branch SHA from the PR
  if command -v gh >/dev/null 2>&1 && [ -n "${GH_TOKEN:-}" ]; then
    HEAD_SHA=$(gh pr view "$PR_NUMBER" --repo "$OWNER_REPO" --json headRefOid --jq '.headRefOid' 2>/dev/null) || true
  fi

  if [ -z "${HEAD_SHA:-}" ] && [ -n "${GH_TOKEN:-}" ]; then
    HEAD_SHA=$(curl -s --max-time 15 \
      -H "Authorization: Bearer ${GH_TOKEN}" \
      -H "Accept: application/vnd.github.v3+json" \
      "https://api.github.com/repos/${OWNER_REPO}/pulls/${PR_NUMBER}" \
      | jq -r '.head.sha // empty' 2>/dev/null) || true
  fi

  if [ -z "${HEAD_SHA:-}" ]; then
    echo '{"error": "could not determine PR head SHA. Check GH_TOKEN."}' >&2
    exit 1
  fi

  # Fetch file content at PR head
  API_HEADERS=(-H "Accept: application/vnd.github.v3.raw")
  if [ -n "${GH_TOKEN:-}" ]; then
    API_HEADERS+=(-H "Authorization: Bearer ${GH_TOKEN}")
  fi

  FILE_CONTENT=$(curl -s --max-time 30 \
    "${API_HEADERS[@]}" \
    "https://api.github.com/repos/${OWNER_REPO}/contents/${FILE_PATH}?ref=${HEAD_SHA}" 2>/dev/null) || true

  if [ -z "$FILE_CONTENT" ]; then
    echo "{\"error\": \"could not fetch file '${FILE_PATH}' from PR head\"}" >&2
    exit 1
  fi
else
  # Local file
  if [ ! -f "$FILE_PATH" ]; then
    echo "{\"error\": \"file not found: ${FILE_PATH}\"}" >&2
    exit 1
  fi

  FILE_CONTENT=$(cat "$FILE_PATH")
fi

if [ -z "$FILE_CONTENT" ]; then
  echo '{"error": "file is empty or could not be read"}' >&2
  exit 1
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
SYSTEM_PROMPT="You are an expert code reviewer performing a deep review of a single file. Analyze the entire file for bugs, security issues, and improvements.

Focus area: ${FOCUS}
File: ${FILE_PATH}

Your review must be returned as a single JSON object with this exact schema:
{
  \"summary\": \"Brief overall assessment of the file\",
  \"risk_level\": \"low|medium|high|critical\",
  \"findings\": [
    {
      \"file\": \"${FILE_PATH}\",
      \"line\": <line_number_or_null>,
      \"severity\": \"error|warning|info|nitpick\",
      \"category\": \"bug|security|style|performance|maintainability\",
      \"title\": \"Short title\",
      \"description\": \"Detailed explanation\",
      \"suggestion\": \"Suggested fix or improvement\"
    }
  ],
  \"stats\": {
    \"files_reviewed\": 1,
    \"total_findings\": <count>,
    \"by_severity\": {\"error\": 0, \"warning\": 0, \"info\": 0, \"nitpick\": 0}
  }
}

Rules:
- Return ONLY valid JSON, no markdown fences, no extra text
- Review the ENTIRE file, not just a diff — consider overall structure, error handling, edge cases
- Be specific: include line numbers for each finding
- Prioritize actionable findings over style nitpicks
- For security focus, emphasize OWASP top 10, injection, auth, secrets exposure
- For bug focus, emphasize logic errors, null/nil dereferences, race conditions, resource leaks"

if [ -n "$STANDARDS_CONTEXT" ]; then
  SYSTEM_PROMPT="${SYSTEM_PROMPT}

Apply these organization coding standards in your review:
${STANDARDS_CONTEXT}"
fi

USER_PROMPT="Review this file (${FILE_PATH})"
if [ -n "$EXTRA_CONTEXT" ]; then
  USER_PROMPT="${USER_PROMPT}. Additional context: ${EXTRA_CONTEXT}"
fi
USER_PROMPT="${USER_PROMPT}:

${FILE_CONTENT}"

# --- Select provider (see code-review-diff.sh and #133 for rationale) ---
PROVIDER="${REVIEW_PROVIDER:-}"
if [ -z "$PROVIDER" ]; then
  if [ -n "${REVIEW_MODEL:-}" ]; then
    case "$REVIEW_MODEL" in
      claude-*|anthropic/*) PROVIDER="anthropic" ;;
      *)                    PROVIDER="openai"    ;;
    esac
  elif [ -n "${ANTHROPIC_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
    PROVIDER="anthropic"
  elif [ -n "${OPENAI_API_KEY:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    PROVIDER="openai"
  else
    PROVIDER="openai"
  fi
fi

case "$PROVIDER" in
  anthropic)
    if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
      echo '{"error": "REVIEW_PROVIDER=anthropic (or REVIEW_MODEL inferred Anthropic) but ANTHROPIC_API_KEY is not set"}' >&2
      exit 1
    fi
    ;;
  openai)
    if [ -z "${OPENAI_API_KEY:-}" ]; then
      echo '{"error": "REVIEW_PROVIDER=openai (or REVIEW_MODEL inferred OpenAI) but OPENAI_API_KEY is not set"}' >&2
      exit 1
    fi
    ;;
  *)
    echo "{\"error\": \"REVIEW_PROVIDER must be 'anthropic' or 'openai'; got: $PROVIDER\"}" >&2
    exit 1
    ;;
esac

# --- Route to LLM API ---
if [ "$PROVIDER" = "anthropic" ]; then
  MODEL="${REVIEW_MODEL:-claude-sonnet-4-20250514}"

  TEMP_SYSTEM=$(mktemp)
  TEMP_USER=$(mktemp)
  trap 'rm -f "$TEMP_SYSTEM" "$TEMP_USER"' EXIT
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

  REVIEW_TEXT=$(echo "$BODY" | jq -r '.content[0].text // empty')

elif [ "$PROVIDER" = "openai" ]; then
  # OpenAI / OpenAI-compatible API. See code-review-diff.sh for the
  # full rationale on the OPENAI_BASE_URL vs OPENAI_USE_RESPONSES_API
  # decoupling (closes #133 Bug 2).
  MODEL="${REVIEW_MODEL:-gpt-5.4}"
  OPENAI_BASE="${OPENAI_BASE_URL:-https://api.openai.com/v1}"

  TEMP_SYSTEM=$(mktemp)
  TEMP_USER=$(mktemp)
  trap 'rm -f "$TEMP_SYSTEM" "$TEMP_USER"' EXIT
  printf '%s' "$SYSTEM_PROMPT" > "$TEMP_SYSTEM"
  printf '%s' "$USER_PROMPT" > "$TEMP_USER"

  if [ "${OPENAI_USE_RESPONSES_API:-0}" = "1" ]; then
    # OpenAI proprietary Responses API (Codex/OAuth flow) — requires streaming
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
    # Standard Chat Completions API.
    #
    # max_completion_tokens (NOT max_tokens) — see #141 / mirror of
    # the equivalent block in code-review-diff.sh for the full
    # rationale. OpenAI deprecated max_tokens; reasoning models and
    # strict OpenAI-compatible providers (Together.ai's Kimi-K2.6
    # series, Moonshot, ...) reject the legacy field with HTTP 400.
    # max_completion_tokens is forward-compatible and backward-tolerant.
    API_PAYLOAD=$(jq -n \
      --arg model "$MODEL" \
      --rawfile system "$TEMP_SYSTEM" \
      --rawfile user "$TEMP_USER" \
      '{
        model: $model,
        max_completion_tokens: 4096,
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

    REVIEW_TEXT=$(echo "$BODY" | jq -r '.choices[0].message.content // empty')
  fi
fi

if [ -z "${REVIEW_TEXT:-}" ]; then
  echo '{"error": "LLM returned empty response"}' >&2
  exit 1
fi

# --- Parse and validate JSON output ---
REVIEW_TEXT=$(echo "$REVIEW_TEXT" | sed -E 's/^```(json)?//; s/```$//' | sed '/^$/d')

REVIEW_JSON=""
if echo "$REVIEW_TEXT" | jq empty 2>/dev/null; then
  REVIEW_JSON=$(echo "$REVIEW_TEXT" | jq .)
else
  REVIEW_JSON=$(jq -n --arg text "$REVIEW_TEXT" '{
    "summary": $text,
    "risk_level": "unknown",
    "findings": [],
    "stats": {"files_reviewed": 1, "total_findings": 0, "by_severity": {}},
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
