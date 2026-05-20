#!/usr/bin/env bash
# linear-update-issue-state.sh — Transition an issue to a different workflow state.
# shellcheck disable=SC2016  # GraphQL query strings use $var literally
# Resolve the state_id via linear-get-workflow-states.sh first.
# Usage: ./linear-update-issue-state.sh '{"identifier":"ENG-123","state_id":"uuid"}'
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

linear_read_input "$@"

IDENT="$(echo "$INPUT_JSON" | jq -r '.identifier // empty')"
STATE_ID="$(echo "$INPUT_JSON" | jq -r '.state_id // empty')"
[ -n "$IDENT" ] || linear_die "identifier field is required (e.g. \"ENG-123\")"
[ -n "$STATE_ID" ] || linear_die "state_id field is required — call linear_get_workflow_states first"

MUTATION='mutation($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) {
    success
    issue { identifier state { id name type } }
  }
}'

VARS="$(jq -n --arg id "$IDENT" --arg sid "$STATE_ID" '{id: $id, stateId: $sid}')"
DATA="$(linear_graphql "$MUTATION" "$VARS")"

SUCCESS="$(echo "$DATA" | jq -r '.issueUpdate.success // false')"
if [ "$SUCCESS" != "true" ]; then
  linear_die "issueUpdate returned success=false for $IDENT"
fi

echo "$DATA" | jq '{
  success: .issueUpdate.success,
  identifier: .issueUpdate.issue.identifier,
  state: .issueUpdate.issue.state
}'
