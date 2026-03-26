#!/usr/bin/env bash
# github-get-user.sh — Get a GitHub user profile.
# Usage: ./github-get-user.sh '{"username": "octocat"}'
#
# Requires: gh, jq
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: github-get-user.sh {\"username\": \"...\"}"}' >&2
  exit 1
fi
if ! printf '%s' "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

USERNAME=$(printf '%s' "$INPUT" | jq -r '.username // empty')

if [ -z "$USERNAME" ]; then
  echo '{"error": "username is required"}' >&2
  exit 1
fi

# --- Fetch user profile ---
RESPONSE=$(gh api "users/${USERNAME}" 2>&1) || {
  echo "{\"error\": \"GitHub API call failed: $RESPONSE\"}" >&2
  exit 1
}

# --- Parse and output ---
printf '%s' "$RESPONSE" | jq '{
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
}'
