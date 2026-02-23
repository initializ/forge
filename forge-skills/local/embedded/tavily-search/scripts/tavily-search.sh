#!/usr/bin/env bash
# tavily-search.sh — Search the web using the Tavily API.
# Usage: ./tavily-search.sh '{"query": "search terms", "max_results": 5}'
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
  echo '{"error": "usage: tavily-search.sh {\"query\": \"...\"}"}' >&2
  exit 1
fi

# Validate JSON
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Check required fields ---
QUERY=$(echo "$INPUT" | jq -r '.query // empty')
if [ -z "$QUERY" ]; then
  echo '{"error": "query field is required"}' >&2
  exit 1
fi

# --- Call Tavily API ---
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "https://api.tavily.com/search" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TAVILY_API_KEY}" \
  -d "$INPUT")

# Split response body and status code
HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -ne 200 ]; then
  echo "{\"error\": \"Tavily API returned status $HTTP_CODE\", \"details\": $BODY}" >&2
  exit 1
fi

# --- Pretty-print response ---
echo "$BODY" | jq .
