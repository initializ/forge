#!/usr/bin/env bash
# github-pr-author-profiles.sh — List PR authors and fetch their profiles (2-step compound tool).
# Usage: ./github-pr-author-profiles.sh '{"repo": "owner/repo", "state": "open", "page": 1, "per_page": 30}'
#
# Requires: gh, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-pr-author-profiles.sh {\"repo\": \"...\", \"state\": \"open\", \"page\": 1, \"per_page\": 30}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

REPO=$(printf '%s' "$INPUT" | jq -r '.repo // empty')
STATE=$(printf '%s' "$INPUT" | jq -r '.state // empty')
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
STATE="${STATE:-open}"
PAGE="${PAGE:-1}"
PER_PAGE="${PER_PAGE:-30}"

if [ "$PAGE" -lt 1 ] 2>/dev/null; then PAGE=1; fi
if [ "$PER_PAGE" -lt 1 ] 2>/dev/null; then PER_PAGE=1; fi
if [ "$PER_PAGE" -gt 100 ] 2>/dev/null; then PER_PAGE=100; fi

OWNER="${REPO%%/*}"
REPO_NAME="${REPO##*/}"

# --- Step 1: Fetch PRs and extract unique authors with PR counts ---
PR_RESPONSE=$(gh api "repos/${OWNER}/${REPO_NAME}/pulls" \
  --method GET \
  -f state="$STATE" \
  -f page="$PAGE" \
  -f per_page="$PER_PAGE" 2>&1) || {
  echo "{\"error\": \"GitHub API call failed: $PR_RESPONSE\"}" >&2
  exit 1
}

TOTAL_PRS=$(printf '%s' "$PR_RESPONSE" | jq 'length')
HAS_NEXT="false"
if [ "$TOTAL_PRS" -eq "$PER_PAGE" ]; then
  HAS_NEXT="true"
fi

# Extract unique authors with their PR counts
AUTHORS_WITH_COUNTS=$(printf '%s' "$PR_RESPONSE" | jq -r '[.[] | .user.login] | group_by(.) | map({login: .[0], pr_count: length}) | .[] | @json')
UNIQUE_AUTHORS=$(printf '%s' "$PR_RESPONSE" | jq '[.[] | .user.login] | unique | length')

# --- Step 2: Fetch profile for each unique author ---
PROFILES="[]"

while IFS= read -r author_json; do
  [ -z "$author_json" ] && continue
  LOGIN=$(printf '%s' "$author_json" | jq -r '.login')
  PR_COUNT=$(printf '%s' "$author_json" | jq -r '.pr_count')

  PROFILE=$(gh api "users/${LOGIN}" 2>/dev/null) || {
    # Gracefully skip failed profile fetches
    PROFILES=$(printf '%s' "$PROFILES" | jq --arg login "$LOGIN" --argjson pr_count "$PR_COUNT" \
      '. + [{login: $login, error: "failed to fetch profile", pr_count: $pr_count}]')
    continue
  }

  PROFILES=$(printf '%s' "$PROFILE" | jq --argjson existing "$PROFILES" --argjson pr_count "$PR_COUNT" \
    '$existing + [{
      login: .login,
      name: .name,
      email: .email,
      bio: .bio,
      company: .company,
      location: .location,
      blog: .blog,
      public_repos: .public_repos,
      followers: .followers,
      following: .following,
      created_at: .created_at,
      url: .html_url,
      pr_count: $pr_count
    }]')
done <<< "$AUTHORS_WITH_COUNTS"

# --- Output ---
jq -n \
  --arg repo "${OWNER}/${REPO_NAME}" \
  --arg state "$STATE" \
  --argjson profiles "$PROFILES" \
  --argjson total_prs "$TOTAL_PRS" \
  --argjson unique_authors "$UNIQUE_AUTHORS" \
  --argjson page "$PAGE" \
  --argjson per_page "$PER_PAGE" \
  --argjson count "$TOTAL_PRS" \
  --argjson has_next "$HAS_NEXT" \
  '{
    repo: $repo,
    state: $state,
    profiles: $profiles,
    total_prs_scanned: $total_prs,
    unique_authors: $unique_authors,
    pagination: {
      page: $page,
      per_page: $per_page,
      count: $count,
      has_next_page: $has_next
    }
  }'
