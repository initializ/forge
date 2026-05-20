#!/usr/bin/env bash
# linear-add-comment.sh — Post a markdown comment to a Linear issue.
# shellcheck disable=SC2016  # GraphQL query strings use $var literally
# Body is capped at 10 000 chars client-side; longer bodies are truncated.
# Usage: ./linear-add-comment.sh '{"identifier":"ENG-123","body":"Working on this."}'
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

linear_read_input "$@"

IDENT="$(echo "$INPUT_JSON" | jq -r '.identifier // empty')"
BODY="$(echo "$INPUT_JSON" | jq -r '.body // empty')"
[ -n "$IDENT" ] || linear_die "identifier field is required (e.g. \"ENG-123\")"
[ -n "$BODY" ] || linear_die "body field is required"

# Cap body length at 10 000 characters.
BODY="$(printf '%s' "$BODY" | head -c 10000)"

# commentCreate expects the issue UUID, not the human identifier. Resolve it.
RESOLVE_QUERY='query($id: String!) { issue(id: $id) { id } }'
RESOLVE_VARS="$(jq -n --arg id "$IDENT" '{id: $id}')"
RESOLVE_DATA="$(linear_graphql "$RESOLVE_QUERY" "$RESOLVE_VARS")"
ISSUE_UUID="$(echo "$RESOLVE_DATA" | jq -r '.issue.id // empty')"
[ -n "$ISSUE_UUID" ] || linear_die "issue not found: $IDENT"

MUTATION='mutation($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
    comment { id url createdAt }
  }
}'

VARS="$(jq -n --arg id "$ISSUE_UUID" --arg b "$BODY" '{issueId: $id, body: $b}')"
DATA="$(linear_graphql "$MUTATION" "$VARS")"

SUCCESS="$(echo "$DATA" | jq -r '.commentCreate.success // false')"
if [ "$SUCCESS" != "true" ]; then
  linear_die "commentCreate returned success=false for $IDENT"
fi

echo "$DATA" | jq '{
  success: .commentCreate.success,
  comment: {
    id: .commentCreate.comment.id,
    url: .commentCreate.comment.url,
    created_at: .commentCreate.comment.createdAt
  }
}'
