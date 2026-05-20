#!/usr/bin/env bash
# linear-get-issue.sh — Fetch one Linear issue by its human identifier.
# shellcheck disable=SC2016  # GraphQL query strings use $var literally
# Usage: ./linear-get-issue.sh '{"identifier": "ENG-123"}'
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

linear_read_input "$@"

IDENT="$(echo "$INPUT_JSON" | jq -r '.identifier // empty')"
[ -n "$IDENT" ] || linear_die "identifier field is required (e.g. \"ENG-123\")"

QUERY='query($id: String!) {
  issue(id: $id) {
    identifier
    title
    description
    state { id name type }
    assignee { email name }
    team { id key name }
    labels { nodes { name } }
    priority
    url
  }
}'

VARS="$(jq -n --arg id "$IDENT" '{id: $id}')"
DATA="$(linear_graphql "$QUERY" "$VARS")"

ISSUE="$(echo "$DATA" | jq '.issue')"
if [ "$ISSUE" = "null" ]; then
  linear_die "issue not found: $IDENT"
fi

echo "$ISSUE" | jq '{
  identifier,
  title,
  description,
  state,
  assignee,
  team,
  labels: [(.labels.nodes // [])[].name],
  priority,
  url
}'
