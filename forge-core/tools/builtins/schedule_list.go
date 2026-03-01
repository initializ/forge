package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/scheduler"
	"github.com/initializ/forge/forge-core/tools"
)

type scheduleListTool struct {
	store scheduler.ScheduleStore
}

// NewScheduleListTool creates a schedule_list tool for listing schedules.
func NewScheduleListTool(store scheduler.ScheduleStore) tools.Tool {
	return &scheduleListTool{store: store}
}

type scheduleListInput struct {
	EnabledOnly *bool `json:"enabled_only"`
}

func (t *scheduleListTool) Name() string             { return "schedule_list" }
func (t *scheduleListTool) Category() tools.Category { return tools.CategoryBuiltin }
func (t *scheduleListTool) Description() string {
	return "List all scheduled tasks with their cron expressions, status, and next fire time."
}

func (t *scheduleListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"enabled_only": {"type": "boolean", "description": "If true, only show enabled schedules"}
		}
	}`)
}

func (t *scheduleListTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input scheduleListInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return "", fmt.Errorf("parsing input: %w", err)
		}
	}

	schedules, err := t.store.List(ctx)
	if err != nil {
		return "", fmt.Errorf("listing schedules: %w", err)
	}

	if len(schedules) == 0 {
		return "No schedules configured.", nil
	}

	now := time.Now().UTC()
	var b strings.Builder
	b.WriteString("| ID | Cron | Source | Enabled | Next Fire | Task |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	for _, sched := range schedules {
		if input.EnabledOnly != nil && *input.EnabledOnly && !sched.Enabled {
			continue
		}

		nextFire := "N/A"
		if sched.Enabled {
			parsed, parseErr := scheduler.Parse(sched.Cron)
			if parseErr == nil {
				ref := sched.LastRun
				if ref.IsZero() {
					ref = now
				}
				next := parsed.Next(ref)
				if !next.IsZero() {
					nextFire = next.Format(time.RFC3339)
				}
			}
		}

		// Truncate task description for table display.
		task := sched.Task
		if len(task) > 60 {
			task = task[:57] + "..."
		}

		fmt.Fprintf(&b, "| %s | %s | %s | %t | %s | %s |\n",
			sched.ID, sched.Cron, sched.Source, sched.Enabled, nextFire, task)
	}

	return b.String(), nil
}
