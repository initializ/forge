#!/usr/bin/env bash
# tavily-research-poll.sh — Poll for Tavily Research results by request_id.
# Waits internally (sleep + retry) until the research completes or times out.
# Usage: ./tavily-research-poll.sh '{"request_id": "..."}'
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
  echo '{"error": "usage: tavily-research-poll.sh {\"request_id\": \"...\"}"}' >&2
  exit 1
fi

# Validate JSON
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Check required fields ---
REQUEST_ID=$(echo "$INPUT" | jq -r '.request_id // empty')
if [ -z "$REQUEST_ID" ]; then
  echo '{"error": "request_id field is required"}' >&2
  exit 1
fi

# --- Poll loop: wait for research to complete ---
POLL_INTERVAL=10
MAX_WAIT=280
ELAPSED=0

while true; do
  RESPONSE=$(curl -s -w "\n%{http_code}" \
    --max-time 15 \
    "https://api.tavily.com/research/${REQUEST_ID}" \
    -H "Authorization: Bearer ${TAVILY_API_KEY}")

  HTTP_CODE=$(echo "$RESPONSE" | tail -1)
  BODY=$(echo "$RESPONSE" | sed '$d')

  # On HTTP error, keep retrying (transient failures)
  if [ "$HTTP_CODE" -ne 200 ]; then
    ELAPSED=$((ELAPSED + POLL_INTERVAL))
    if [ "$ELAPSED" -ge "$MAX_WAIT" ]; then
      echo "{\"error\": \"research timed out after ${MAX_WAIT}s (last HTTP status: $HTTP_CODE)\", \"request_id\": \"${REQUEST_ID}\"}" >&2
      exit 1
    fi
    sleep "$POLL_INTERVAL"
    continue
  fi

  STATUS=$(echo "$BODY" | jq -r '.status // empty')

  case "$STATUS" in
    completed)
      # Format with summary first for memory compaction
      SUMMARY=$(echo "$BODY" | jq -r '.summary // empty')
      if [ -n "$SUMMARY" ]; then
        echo "$BODY" | jq '{status, summary, topic, report, sources, research_time}'
      else
        echo "$BODY" | jq .
      fi
      exit 0
      ;;
    failed|error)
      echo "$BODY" >&2
      exit 1
      ;;
    *)
      # Still pending or in_progress — wait and retry
      ELAPSED=$((ELAPSED + POLL_INTERVAL))
      if [ "$ELAPSED" -ge "$MAX_WAIT" ]; then
        echo "{\"error\": \"research timed out after ${MAX_WAIT}s\", \"request_id\": \"${REQUEST_ID}\", \"last_status\": \"${STATUS}\"}" >&2
        exit 1
      fi
      sleep "$POLL_INTERVAL"
      ;;
  esac
done
