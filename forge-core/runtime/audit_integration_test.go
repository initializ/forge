package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditLoggerEventSequence(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)

	corrID := GenerateID()
	taskID := "task-integration-1"

	// Simulate a session lifecycle
	logger.Emit(AuditEvent{
		Event:         AuditSessionStart,
		CorrelationID: corrID,
		TaskID:        taskID,
	})

	logger.Emit(AuditEvent{
		Event:         AuditToolExec,
		CorrelationID: corrID,
		TaskID:        taskID,
		Fields:        map[string]any{"tool": "http_request", "phase": "start"},
	})

	logger.Emit(AuditEvent{
		Event:         AuditEgressAllowed,
		CorrelationID: corrID,
		TaskID:        taskID,
		Fields:        map[string]any{"domain": "api.openai.com"},
	})

	logger.Emit(AuditEvent{
		Event:         AuditToolExec,
		CorrelationID: corrID,
		TaskID:        taskID,
		Fields:        map[string]any{"tool": "http_request", "phase": "end"},
	})

	logger.Emit(AuditEvent{
		Event:         AuditLLMCall,
		CorrelationID: corrID,
		TaskID:        taskID,
		Fields:        map[string]any{"tokens": 150},
	})

	logger.Emit(AuditEvent{
		Event:         AuditSessionEnd,
		CorrelationID: corrID,
		TaskID:        taskID,
		Fields:        map[string]any{"state": "completed"},
	})

	// Parse all events
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 events, got %d", len(lines))
	}

	expectedEvents := []string{
		AuditSessionStart,
		AuditToolExec,
		AuditEgressAllowed,
		AuditToolExec,
		AuditLLMCall,
		AuditSessionEnd,
	}

	for i, line := range lines {
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", i, err)
		}
		if event.Event != expectedEvents[i] {
			t.Errorf("line %d: event = %q, want %q", i, event.Event, expectedEvents[i])
		}
		if event.CorrelationID != corrID {
			t.Errorf("line %d: correlation_id = %q, want %q", i, event.CorrelationID, corrID)
		}
		if event.TaskID != taskID {
			t.Errorf("line %d: task_id = %q, want %q", i, event.TaskID, taskID)
		}
		if event.Timestamp == "" {
			t.Errorf("line %d: timestamp should be auto-set", i)
		}
	}
}

func TestAuditContextIntegration(t *testing.T) {
	ctx := context.Background()

	corrID := GenerateID()
	taskID := "task-ctx-1"

	ctx = WithCorrelationID(ctx, corrID)
	ctx = WithTaskID(ctx, taskID)

	// Verify round-trip
	if got := CorrelationIDFromContext(ctx); got != corrID {
		t.Errorf("CorrelationIDFromContext = %q, want %q", got, corrID)
	}
	if got := TaskIDFromContext(ctx); got != taskID {
		t.Errorf("TaskIDFromContext = %q, want %q", got, taskID)
	}

	// Emit event using context values
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)
	logger.Emit(AuditEvent{
		Event:         AuditToolExec,
		CorrelationID: CorrelationIDFromContext(ctx),
		TaskID:        TaskIDFromContext(ctx),
		Fields:        map[string]any{"tool": "web_search"},
	})

	var event AuditEvent
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &event) //nolint:errcheck
	if event.CorrelationID != corrID {
		t.Errorf("emitted event correlation_id = %q, want %q", event.CorrelationID, corrID)
	}
}
