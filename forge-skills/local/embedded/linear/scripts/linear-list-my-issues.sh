#!/usr/bin/env bash
# linear-list-my-issues.sh — List issues assigned to the API key's owner.
# shellcheck disable=SC2016  # GraphQL query strings use $var literally
# Usage: ./linear-list-my-issues.sh '{"state":"started,unstarted","limit":20}'
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

linear_read_input "$@"

STATE_CSV="$(echo "$INPUT_JSON" | jq -r '.state // "started,unstarted"')"
LIMIT="$(echo "$INPUT_JSON" | jq -r '.limit // 20')"

if [ "$LIMIT" -gt 100 ] 2>/dev/null; then
  LIMIT=100
elif ! [ "$LIMIT" -gt 0 ] 2>/dev/null; then
  LIMIT=20
fi

# Convert CSV to JSON array.
STATE_ARRAY="$(echo "$STATE_CSV" | jq -R 'split(",") | map(gsub("^\\s+|\\s+$"; ""))')"

# Resolve viewer.id first.
VIEWER_DATA="$(linear_graphql 'query { viewer { id } }' '{}')"
VIEWER_ID="$(echo "$VIEWER_DATA" | jq -r '.viewer.id // empty')"
[ -n "$VIEWER_ID" ] || linear_die "could not resolve viewer id — is LINEAR_API_KEY valid?"

FILTER="$(jq -n --arg vid "$VIEWER_ID" --argjson states "$STATE_ARRAY" \
  '{assignee: {id: {eq: $vid}}, state: {type: {in: $states}}}')"

GQL='query($filter: IssueFilter, $first: Int!) {
  issues(filter: $filter, first: $first, orderBy: updatedAt) {
    nodes {
      identifier
      title
      url
      state { name type }
      assignee { email }
    }
  }
}'

VARS="$(jq -n --argjson f "$FILTER" --argjson n "$LIMIT" '{filter: $f, first: $n}')"
DATA="$(linear_graphql "$GQL" "$VARS")"

echo "$DATA" | jq '{
  count: (.issues.nodes | length),
  issues: [.issues.nodes[] | {
    identifier,
    title,
    state: (.state.name // null),
    assignee_email: (.assignee.email // null),
    url
  }]
}'
