#!/usr/bin/env bash
# github-branch-name-from-ticket.sh — Generate a conventional branch name
# from a ticket identifier and title.
# Usage: ./github-branch-name-from-ticket.sh '{"ticket_id":"ENG-123","title":"Add invoice creation"}'
#
# No network call. Pure string transformation. Output:
#   {"branch": "<prefix>/<lower-id>-<slug>"}
set -euo pipefail

INPUT_JSON="${1:-$(cat)}"
if ! printf '%s' "$INPUT_JSON" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

TICKET_ID="$(printf '%s' "$INPUT_JSON" | jq -r '.ticket_id // empty')"
TITLE="$(printf '%s' "$INPUT_JSON" | jq -r '.title // empty')"
PREFIX="$(printf '%s' "$INPUT_JSON" | jq -r '.prefix // "feat"')"

if [ -z "$TICKET_ID" ]; then
  echo '{"error": "ticket_id is required"}' >&2
  exit 1
fi
if [ -z "$TITLE" ]; then
  echo '{"error": "title is required"}' >&2
  exit 1
fi

# Validate prefix against allow-list; fall back to feat for any other value.
case "$PREFIX" in
  feat|fix|chore|docs|refactor) ;;
  *) PREFIX="feat" ;;
esac

LOWER_ID="$(printf '%s' "$TICKET_ID" | tr '[:upper:]' '[:lower:]')"

# Slugify: lowercase, non-alnum → -, collapse repeats, trim leading/trailing -.
SLUG="$(printf '%s' "$TITLE" \
  | tr '[:upper:]' '[:lower:]' \
  | sed -E 's/[^a-z0-9]+/-/g' \
  | sed -E 's/^-+//' \
  | sed -E 's/-+$//')"

# Truncate slug to 60 chars; if a hyphen exists in the truncated string,
# cut back to it so we don't leave a half-word at the end.
if [ "${#SLUG}" -gt 60 ]; then
  SLUG="${SLUG:0:60}"
  case "$SLUG" in
    *-*) SLUG="${SLUG%-*}" ;;
  esac
fi

BRANCH="$PREFIX/$LOWER_ID-$SLUG"

jq -n --arg branch "$BRANCH" '{branch: $branch}'
