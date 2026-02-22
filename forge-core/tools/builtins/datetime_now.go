package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/initializ/forge/forge-core/tools"
)

type datetimeNowTool struct{}

type datetimeNowInput struct {
	Format   string `json:"format,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

func (t *datetimeNowTool) Name() string { return "datetime_now" }
func (t *datetimeNowTool) Description() string {
	return "Get current date and time in specified format and timezone"
}
func (t *datetimeNowTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *datetimeNowTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"format": {"type": "string", "description": "Time format (rfc3339, unix, date, time, datetime). Default: rfc3339"},
			"timezone": {"type": "string", "description": "Timezone name (e.g. America/New_York, UTC). Default: UTC"}
		}
	}`)
}

func (t *datetimeNowTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input datetimeNowInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	loc := time.UTC
	if input.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(input.Timezone)
		if err != nil {
			return "", fmt.Errorf("invalid timezone %q: %w", input.Timezone, err)
		}
	}

	now := time.Now().In(loc)

	switch input.Format {
	case "unix":
		return fmt.Sprintf("%d", now.Unix()), nil
	case "date":
		return now.Format("2006-01-02"), nil
	case "time":
		return now.Format("15:04:05"), nil
	case "datetime":
		return now.Format("2006-01-02 15:04:05"), nil
	default: // "rfc3339" or empty
		return now.Format(time.RFC3339), nil
	}
}
