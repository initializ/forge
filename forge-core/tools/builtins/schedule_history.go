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

type scheduleHistoryTool struct {
	store scheduler.ScheduleStore
}

// NewScheduleHistoryTool creates a schedule_history tool for viewing execution history.
func NewScheduleHistoryTool(store scheduler.ScheduleStore) tools.Tool {
	return &scheduleHistoryTool{store: store}
}

type scheduleHistoryInput struct {
	ScheduleID string `json:"schedule_id"`
	Limit      int    `json:"limit"`
}

func (t *scheduleHistoryTool) Name() string             { return "schedule_history" }
func (t *scheduleHistoryTool) Category() tools.Category { return tools.CategoryBuiltin }
func (t *scheduleHistoryTool) Description() string {
	return "View execution history for scheduled tasks. Optionally filter by schedule ID."
}

func (t *scheduleHistoryTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"schedule_id": {"type": "string", "description": "Filter history by schedule ID"},
			"limit": {"type": "integer", "description": "Maximum entries to return (default: 20, max: 50)"}
		}
	}`)
}

func (t *scheduleHistoryTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input scheduleHistoryInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return "", fmt.Errorf("parsing input: %w", err)
		}
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	history, err := t.store.History(ctx, input.ScheduleID, limit)
	if err != nil {
		return "", fmt.Errorf("reading history: %w", err)
	}

	if len(history) == 0 {
		return "No execution history found.", nil
	}

	var b strings.Builder
	b.WriteString("| Timestamp | Schedule ID | Status | Duration | Correlation ID | Error |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")

	for _, h := range history {
		errStr := h.Error
		if errStr == "" {
			errStr = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
			h.Timestamp.Format(time.RFC3339),
			h.ScheduleID,
			h.Status,
			h.Duration,
			h.CorrelationID,
			errStr,
		)
	}

	return b.String(), nil
}
