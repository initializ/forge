#!/usr/bin/env bash
set -euo pipefail

input="$1"

location=$(jq -r '.location // empty' <<< "$input" 2>/dev/null) || location=""

if [[ -z "$location" ]]; then
  jq -n '{error: "location is required"}'
  exit 0
fi

encoded_location=$(jq -rn --arg s "$location" '$s|@uri')

response=$(curl -s -w "\n%{http_code}" "https://wttr.in/${encoded_location}?format=j1")
http_code=$(tail -n1 <<< "$response")
body=$(sed '$d' <<< "$response")

if [[ "$http_code" != "200" ]]; then
  jq -n --arg code "$http_code" '{error: "wttr.in request failed with status \($code)"}'
  exit 0
fi

echo "$body" | jq '{
  temperature_c: .current_condition[0].temp_C,
  condition: .current_condition[0].weatherDesc[0].value,
  humidity: .current_condition[0].humidity,
  wind_speed_kmph: .current_condition[0].windspeedKmph
}' 2>/dev/null || jq -n '{error: "failed to parse weather data"}'
