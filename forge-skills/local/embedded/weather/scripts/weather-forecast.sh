#!/usr/bin/env bash
set -euo pipefail

input="$1"

location=$(jq -r '.location // empty' <<< "$input" 2>/dev/null) || location=""
days=$(jq -r '.days // 3' <<< "$input" 2>/dev/null) || days=3

if [[ -z "$location" ]]; then
  jq -n '{error: "location is required"}'
  exit 0
fi

if ! [[ "$days" =~ ^[0-9]+$ ]]; then
  days=3
fi
if (( days < 1 )); then days=1; fi
if (( days > 3 )); then days=3; fi

encoded_location=$(jq -rn --arg s "$location" '$s|@uri')

response=$(curl -s -w "\n%{http_code}" "https://wttr.in/${encoded_location}?format=j1")
http_code=$(tail -n1 <<< "$response")
body=$(sed '$d' <<< "$response")

if [[ "$http_code" != "200" ]]; then
  jq -n --arg code "$http_code" '{error: "wttr.in request failed with status \($code)"}'
  exit 0
fi

echo "$body" | jq --argjson days "$days" '{
  forecast: [.weather[0:$days][] | {
    date: .date,
    max_temp_c: .maxtempC,
    min_temp_c: .mintempC,
    condition: .hourly[0].weatherDesc[0].value
  }]
}' 2>/dev/null || jq -n '{error: "failed to parse forecast data"}'
