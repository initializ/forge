---
name: weather
icon: 🌤️
category: utilities
tags:
  - weather
  - forecast
  - api
description: Get current weather and forecasts
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
      env:
        required: []
        one_of: []
        optional: []
    egress_domains:
      - wttr.in
---
## Tool: weather_current

Get current weather for a location.

**Input:** location (string) - City name or coordinates
**Output:** Current temperature, conditions, humidity, and wind speed

```bash
curl -s "https://wttr.in/${location}?format=j1"
```
Relevant fields in the response: `current_condition[0].temp_C`, `current_condition[0].weatherDesc[0].value`, `current_condition[0].humidity`, `current_condition[0].windspeedKmph`.

## Tool: weather_forecast

Get weather forecast for a location.

**Input:** location (string), days (integer: 1-3)
**Output:** Daily forecast with high/low temperatures and conditions

```bash
curl -s "https://wttr.in/${location}?format=j1"
```
The `weather` array in the response contains up to 3 days of forecast (today + 2 days ahead). Each entry has `date`, `maxtempC`, `mintempC`, and `hourly[].weatherDesc[0].value`. wttr.in's free JSON endpoint caps at 3 days — for a longer forecast window, `open-meteo.com` would be needed instead.
