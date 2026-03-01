package builtins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/scheduler"
	"github.com/initializ/forge/forge-core/tools"
)

type scheduleDeleteTool struct {
	store    scheduler.ScheduleStore
	reloader ScheduleReloader
}

// NewScheduleDeleteTool creates a schedule_delete tool for removing schedules.
func NewScheduleDeleteTool(store scheduler.ScheduleStore, reloader ScheduleReloader) tools.Tool {
	return &scheduleDeleteTool{store: store, reloader: reloader}
}

type scheduleDeleteInput struct {
	ID string `json:"id"`
}

func (t *scheduleDeleteTool) Name() string             { return "schedule_delete" }
func (t *scheduleDeleteTool) Category() tools.Category { return tools.CategoryBuiltin }
func (t *scheduleDeleteTool) Description() string {
	return "Delete a scheduled task by ID. Cannot delete schedules defined in forge.yaml."
}

func (t *scheduleDeleteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "The schedule ID to delete"}
		},
		"required": ["id"]
	}`)
}

func (t *scheduleDeleteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input scheduleDeleteInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	if input.ID == "" {
		return "", fmt.Errorf("id is required")
	}

	existing, err := t.store.Get(ctx, input.ID)
	if err != nil {
		return "", fmt.Errorf("looking up schedule: %w", err)
	}
	if existing == nil {
		return "", fmt.Errorf("schedule %q not found", input.ID)
	}
	if existing.Source == "yaml" {
		return "", fmt.Errorf("cannot delete schedule %q: it is defined in forge.yaml (source: yaml). Remove it from forge.yaml instead", input.ID)
	}

	if err := t.store.Delete(ctx, input.ID); err != nil {
		return "", fmt.Errorf("deleting schedule: %w", err)
	}

	t.reloader.Reload(ctx)

	return fmt.Sprintf("Deleted schedule %q.", input.ID), nil
}
