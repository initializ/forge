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

# Weather Skill

Get current weather and short-range forecasts for any location using
[wttr.in](https://wttr.in). **No API key required** — wttr.in is a free,
keyless service, so this skill works out of the box.

## Quick Start

```bash
./scripts/weather-current.sh '{"location": "Tokyo"}'
./scripts/weather-forecast.sh '{"location": "Tokyo", "days": 3}'
```

The `location` accepts a city name (`Tokyo`, `New York`), an airport code
(`SFO`), or coordinates (`35.68,139.76`).

## Tool: weather_current

Get current weather for a location.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| location | string | yes | City name, airport code, or `lat,lon` coordinates |

**Output:** JSON object with `location`, `temperature_c`, `temperature_f`,
`feels_like_c`, `conditions`, `humidity`, `wind_kmph`, `wind_dir`, and
`observation_time_utc`.

```bash
./scripts/weather-current.sh '{"location": "Tokyo"}'
```

## Tool: weather_forecast

Get a daily weather forecast for a location.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| location | string | yes | City name, airport code, or `lat,lon` coordinates |
| days | integer | no | Number of days, 1-3. Default: 3 (wttr.in caps the free forecast at 3 days) |

**Output:** JSON object with `location` and a `forecast` array of
`{date, high_c, low_c, high_f, low_f, conditions, chance_of_rain_pct}`.

```bash
./scripts/weather-forecast.sh '{"location": "Tokyo", "days": 3}'
```
