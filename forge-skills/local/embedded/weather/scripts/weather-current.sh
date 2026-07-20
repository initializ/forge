#!/usr/bin/env bash
# weather-current.sh — Current weather for a location via wttr.in.
# Usage: ./weather-current.sh '{"location": "Tokyo"}'
#
# Requires: curl, jq. No API key or environment variable needed — wttr.in
# is a free, keyless service.
set -euo pipefail

# --- Read input ---
INPUT="${1:-}"
if [ -z "$INPUT" ]; then
  echo '{"error": "usage: weather-current.sh {\"location\": \"...\"}"}' >&2
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

# --- Shape the current conditions ---
echo "$BODY" | jq '{
  location: (.nearest_area[0] | "\(.areaName[0].value), \(.country[0].value)"),
  temperature_c: (.current_condition[0].temp_C | tonumber),
  temperature_f: (.current_condition[0].temp_F | tonumber),
  feels_like_c: (.current_condition[0].FeelsLikeC | tonumber),
  conditions: .current_condition[0].weatherDesc[0].value,
  humidity: (.current_condition[0].humidity | tonumber),
  wind_kmph: (.current_condition[0].windspeedKmph | tonumber),
  wind_dir: .current_condition[0].winddir16Point,
  observation_time_utc: .current_condition[0].observation_time
}'
