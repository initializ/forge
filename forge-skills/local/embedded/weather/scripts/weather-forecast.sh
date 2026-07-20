#!/usr/bin/env bash
# weather-forecast.sh — Daily forecast for a location via wttr.in.
# Usage: ./weather-forecast.sh '{"location": "Tokyo", "days": 3}'
#
# Requires: curl, jq. No API key or environment variable needed — wttr.in
# is a free, keyless service. wttr.in caps the free forecast at 3 days.
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: weather-forecast.sh {\"location\": \"...\", \"days\": 3}"}' >&2
  exit 1
fi

# Validate JSON
if ! echo "$INPUT" | jq empty 2>/dev/null; then
  echo '{"error": "invalid JSON input"}' >&2
  exit 1
fi

# --- Check required fields ---
LOCATION=$(echo "$INPUT" | jq -r '.location // empty')
if [ -z "$LOCATION" ]; then
  echo '{"error": "location field is required"}' >&2
  exit 1
fi

# Clamp days to the 1-3 range wttr.in serves for free.
DAYS=$(echo "$INPUT" | jq -r '.days // 3')
case "$DAYS" in
  '' | *[!0-9]*) DAYS=3 ;;
esac
if [ "$DAYS" -lt 1 ]; then DAYS=1; fi
if [ "$DAYS" -gt 3 ]; then DAYS=3; fi

# URL-encode the location for the wttr.in path segment.
LOCATION_ENC=$(jq -rn --arg s "$LOCATION" '$s|@uri')

# --- Call wttr.in (j1 = JSON format) ---
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -H "User-Agent: curl" \
  "https://wttr.in/${LOCATION_ENC}?format=j1")

# Split response body and status code
HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -ne 200 ]; then
  echo "{\"error\": \"wttr.in returned status $HTTP_CODE for location '$LOCATION'\"}" >&2
  exit 1
fi

# --- Shape the forecast (hourly index 4 ~ midday) ---
echo "$BODY" | jq --argjson days "$DAYS" '{
  location: (.nearest_area[0] | "\(.areaName[0].value), \(.country[0].value)"),
  forecast: [ .weather[0:$days][] | {
    date: .date,
    high_c: (.maxtempC | tonumber),
    low_c: (.mintempC | tonumber),
    high_f: (.maxtempF | tonumber),
    low_f: (.mintempF | tonumber),
    conditions: .hourly[4].weatherDesc[0].value,
    chance_of_rain_pct: (.hourly[4].chanceofrain | tonumber)
  } ]
}'
