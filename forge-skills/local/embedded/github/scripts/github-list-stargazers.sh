#!/usr/bin/env bash
# github-list-stargazers.sh — List stargazers for a GitHub repository.
# Usage: ./github-list-stargazers.sh '{"repo": "owner/repo", "page": 1, "per_page": 30}'
#
# Requires: gh, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-list-stargazers.sh {\"repo\": \"...\", \"page\": 1, \"per_page\": 30}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

REPO=$(printf '%s' "$INPUT" | jq -r '.repo // empty')
PAGE=$(printf '%s' "$INPUT" | jq -r '.page // empty')
PER_PAGE=$(printf '%s' "$INPUT" | jq -r '.per_page // empty')

if [ -z "$REPO" ]; then
  echo '{"error": "repo is required"}' >&2
  exit 1
fi

# --- Normalize repo format ---
if [[ "$REPO" == git@github.com:* ]]; then
  REPO="${REPO#git@github.com:}"
  REPO="${REPO%.git}"
fi
if [[ "$REPO" == https://github.com/* ]]; then
  REPO="${REPO#https://github.com/}"
  REPO="${REPO%.git}"
fi

# --- Defaults and clamping ---
PAGE="${PAGE:-1}"
PER_PAGE="${PER_PAGE:-30}"

if [ "$PAGE" -lt 1 ] 2>/dev/null; then PAGE=1; fi
if [ "$PER_PAGE" -lt 1 ] 2>/dev/null; then PER_PAGE=1; fi
if [ "$PER_PAGE" -gt 100 ] 2>/dev/null; then PER_PAGE=100; fi

OWNER="${REPO%%/*}"
REPO_NAME="${REPO##*/}"

# --- Fetch stargazers ---
RESPONSE=$(gh api "repos/${OWNER}/${REPO_NAME}/stargazers" \
  --method GET \
  -f page="$PAGE" \
  -f per_page="$PER_PAGE" 2>&1) || {
  echo "{\"error\": \"GitHub API call failed: $RESPONSE\"}" >&2
  exit 1
}

# --- Parse and output ---
COUNT=$(printf '%s' "$RESPONSE" | jq 'length')
HAS_NEXT="false"
if [ "$COUNT" -eq "$PER_PAGE" ]; then
  HAS_NEXT="true"
fi

printf '%s' "$RESPONSE" | jq --arg repo "${OWNER}/${REPO_NAME}" \
  --argjson page "$PAGE" \
  --argjson per_page "$PER_PAGE" \
  --argjson count "$COUNT" \
  --argjson has_next "$HAS_NEXT" \
  '{
    repo: $repo,
    stargazers: [.[] | {
      login: .login,
      url: .html_url
    }],
    pagination: {
      page: $page,
      per_page: $per_page,
      count: $count,
      has_next_page: $has_next
    }
  }'
