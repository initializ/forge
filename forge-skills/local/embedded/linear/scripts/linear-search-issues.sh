#!/usr/bin/env bash
# linear-search-issues.sh — Filter Linear issues by team/state/assignee/label/query.
# shellcheck disable=SC2016  # GraphQL query strings use $var literally
# Usage: ./linear-search-issues.sh '{"team_id":"...","state":"started","limit":10}'
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

linear_read_input "$@"

TEAM_ID="$(echo "$INPUT_JSON" | jq -r '.team_id // empty')"
STATE="$(echo "$INPUT_JSON" | jq -r '.state // empty')"
ASSIGNEE_EMAIL="$(echo "$INPUT_JSON" | jq -r '.assignee_email // empty')"
LABEL="$(echo "$INPUT_JSON" | jq -r '.label // empty')"
QUERY_TEXT="$(echo "$INPUT_JSON" | jq -r '.query // empty')"
LIMIT="$(echo "$INPUT_JSON" | jq -r '.limit // 20')"

# Fall back to default team if not provided.
if [ -z "$TEAM_ID" ] && [ -n "${LINEAR_DEFAULT_TEAM_ID:-}" ]; then
  TEAM_ID="$LINEAR_DEFAULT_TEAM_ID"
fi

# Cap limit at 100.
if [ "$LIMIT" -gt 100 ] 2>/dev/null; then
  LIMIT=100
elif ! [ "$LIMIT" -gt 0 ] 2>/dev/null; then
  LIMIT=20
fi

# Build the filter object incrementally.
# Linear's TeamFilter accepts either an `id` (UUID) or a `key` (short team
# prefix like "ENG"). Auto-detect which form was supplied so the same
# parameter works whether the caller passes a UUID or a team key.
FILTER='{}'
if [ -n "$TEAM_ID" ]; then
  if [[ "$TEAM_ID" =~ ^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$ ]]; then
    FILTER="$(echo "$FILTER" | jq --arg id "$TEAM_ID" '. + {team: {id: {eq: $id}}}')"
  else
    FILTER="$(echo "$FILTER" | jq --arg key "$TEAM_ID" '. + {team: {key: {eq: $key}}}')"
  fi
fi
if [ -n "$STATE" ]; then
  FILTER="$(echo "$FILTER" | jq --arg t "$STATE" '. + {state: {type: {eq: $t}}}')"
fi
if [ -n "$ASSIGNEE_EMAIL" ]; then
  FILTER="$(echo "$FILTER" | jq --arg e "$ASSIGNEE_EMAIL" '. + {assignee: {email: {eq: $e}}}')"
fi
if [ -n "$LABEL" ]; then
  FILTER="$(echo "$FILTER" | jq --arg l "$LABEL" '. + {labels: {name: {eq: $l}}}')"
fi
if [ -n "$QUERY_TEXT" ]; then
  FILTER="$(echo "$FILTER" | jq --arg q "$QUERY_TEXT" '. + {searchableContent: {contains: $q}}')"
fi

# If the filter object has zero fields, omit it from variables so Linear
# treats the argument as "unset" rather than "an empty filter input".
# `orderBy` is dropped — Linear's default ordering is fine and the bare
# enum form was the leading suspect for the schema-validation rejection.
HAS_FILTER="$(echo "$FILTER" | jq 'length > 0')"
if [ "$HAS_FILTER" = "true" ]; then
  GQL='query($filter: IssueFilter, $first: Int!) {
    issues(filter: $filter, first: $first) {
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
else
  GQL='query($first: Int!) {
    issues(first: $first) {
      nodes {
        identifier
        title
        url
        state { name type }
        assignee { email }
      }
    }
  }'
  VARS="$(jq -n --argjson n "$LIMIT" '{first: $n}')"
fi

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
