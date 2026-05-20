#!/usr/bin/env bash
# linear-get-workflow-states.sh — List a team's workflow states (Todo, In Progress, ...).
# shellcheck disable=SC2016  # GraphQL query strings use $var literally
# Must be called before linear_update_issue_state to discover the team's state IDs.
# Usage: ./linear-get-workflow-states.sh '{"team_id":"abc-..."}'
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
source "$SCRIPT_DIR/common.sh"

linear_read_input "$@"

TEAM_ID="$(echo "$INPUT_JSON" | jq -r '.team_id // empty')"
[ -n "$TEAM_ID" ] || linear_die "team_id field is required"

QUERY='query($teamId: String!) {
  team(id: $teamId) {
    states { nodes { id name type position } }
  }
}'

VARS="$(jq -n --arg id "$TEAM_ID" '{teamId: $id}')"
DATA="$(linear_graphql "$QUERY" "$VARS")"

TEAM="$(echo "$DATA" | jq '.team')"
if [ "$TEAM" = "null" ]; then
  linear_die "team not found: $TEAM_ID"
fi

echo "$DATA" | jq --arg tid "$TEAM_ID" '{
  team_id: $tid,
  states: [.team.states.nodes[] | {id, name, type, position}] | sort_by(.position)
}'
