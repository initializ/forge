#!/usr/bin/env bash
# github-stargazer-profiles.sh — List stargazers and fetch their profiles (2-step compound tool).
# Usage: ./github-stargazer-profiles.sh '{"repo": "owner/repo", "page": 1, "per_page": 30}'
#
# Requires: gh, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-stargazer-profiles.sh {\"repo\": \"...\", \"page\": 1, \"per_page\": 30}"}' >&2
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

# --- Step 1: Fetch stargazers ---
STAR_RESPONSE=$(gh api "repos/${OWNER}/${REPO_NAME}/stargazers" \
  --method GET \
  -f page="$PAGE" \
  -f per_page="$PER_PAGE" 2>&1) || {
  echo "{\"error\": \"GitHub API call failed: $STAR_RESPONSE\"}" >&2
  exit 1
}

TOTAL_STARGAZERS=$(printf '%s' "$STAR_RESPONSE" | jq 'length')
HAS_NEXT="false"
if [ "$TOTAL_STARGAZERS" -eq "$PER_PAGE" ]; then
  HAS_NEXT="true"
fi

# Extract unique logins
LOGINS=$(printf '%s' "$STAR_RESPONSE" | jq -r '[.[] | .login] | unique | .[]')
UNIQUE_USERS=$(printf '%s' "$STAR_RESPONSE" | jq '[.[] | .login] | unique | length')

# --- Step 2: Fetch profile for each unique stargazer ---
PROFILES="[]"

while IFS= read -r LOGIN; do
  [ -z "$LOGIN" ] && continue

  PROFILE=$(gh api "users/${LOGIN}" 2>/dev/null) || {
    # Gracefully skip failed profile fetches
    PROFILES=$(printf '%s' "$PROFILES" | jq --arg login "$LOGIN" \
      '. + [{login: $login, error: "failed to fetch profile"}]')
    continue
  }

  PROFILES=$(printf '%s' "$PROFILE" | jq --argjson existing "$PROFILES" \
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
      url: .html_url
    }]')
done <<< "$LOGINS"

# --- Output ---
jq -n \
  --arg repo "${OWNER}/${REPO_NAME}" \
  --argjson profiles "$PROFILES" \
  --argjson total_stargazers "$TOTAL_STARGAZERS" \
  --argjson unique_users "$UNIQUE_USERS" \
  --argjson page "$PAGE" \
  --argjson per_page "$PER_PAGE" \
  --argjson count "$TOTAL_STARGAZERS" \
  --argjson has_next "$HAS_NEXT" \
  '{
    repo: $repo,
    profiles: $profiles,
    total_stargazers_scanned: $total_stargazers,
    unique_users: $unique_users,
    pagination: {
      page: $page,
      per_page: $per_page,
      count: $count,
      has_next_page: $has_next
    }
  }'
