#!/usr/bin/env bash
# tavily-research.sh — Deep multi-source research using the Tavily Research API.
# Usage: ./tavily-research.sh '{"input": "research topic", "model": "auto"}'
#
# Requires: curl, jq, TAVILY_API_KEY environment variable.
set -euo pipefail

# --- Validate environment ---
if [ -z "${TAVILY_API_KEY:-}" ]; then
  echo '{"error": "TAVILY_API_KEY environment variable is not set"}' >&2
  exit 1
fi

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: tavily-research.sh {\"input\": \"...\"}"}' >&2
  exit 1
fi

# Validate JSON
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Check required fields ---
RESEARCH_INPUT=$(echo "$INPUT" | jq -r '.input // empty')
if [ -z "$RESEARCH_INPUT" ]; then
  echo '{"error": "input field is required"}' >&2
  exit 1
fi

# --- Submit research request ---
RESPONSE=$(curl -s -w "\n%{http_code}" \
  --max-time 30 \
  -X POST "https://api.tavily.com/research" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TAVILY_API_KEY}" \
  -d "$INPUT")

# Split response body and status code
HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -ne 200 ]; then
  echo "{\"error\": \"Tavily Research API returned status $HTTP_CODE\", \"details\": $BODY}" >&2
  exit 1
fi

# Return the response as-is. If status is "pending", the caller should
# use tavily_research_poll with the returned request_id to retrieve results.
echo "$BODY" | jq .
