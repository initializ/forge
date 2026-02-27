package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestAuditLoggerEmit(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)

	logger.Emit(AuditEvent{
		Event:         AuditToolExec,
		CorrelationID: "abc123",
		TaskID:        "task-1",
		Fields:        map[string]any{"tool": "http_request"},
	})

	line := strings.TrimSpace(buf.String())
	var event AuditEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("invalid JSON: %v\nline: %s", err, line)
	}

	if event.Event != AuditToolExec {
		t.Errorf("event = %q, want %q", event.Event, AuditToolExec)
	}
	if event.CorrelationID != "abc123" {
		t.Errorf("correlation_id = %q, want %q", event.CorrelationID, "abc123")
	}
	if event.TaskID != "task-1" {
		t.Errorf("task_id = %q, want %q", event.TaskID, "task-1")
	}
	if event.Timestamp == "" {
		t.Error("timestamp should be auto-set")
	}
	if event.Fields["tool"] != "http_request" {
		t.Errorf("fields.tool = %v, want %q", event.Fields["tool"], "http_request")
	}
}

func TestAuditLoggerTimestampPreserved(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)

	logger.Emit(AuditEvent{
		Timestamp: "2024-01-01T00:00:00Z",
		Event:     AuditSessionStart,
	})

	var event AuditEvent
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &event) //nolint:errcheck
	if event.Timestamp != "2024-01-01T00:00:00Z" {
		t.Errorf("timestamp should be preserved, got %q", event.Timestamp)
	}
}

func TestAuditLoggerConcurrent(t *testing.T) {
	var buf bytes.Buffer
	logger := NewAuditLogger(&buf)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			logger.Emit(AuditEvent{
				Event:  AuditToolExec,
				Fields: map[string]any{"n": n},
			})
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 100 {
		t.Fatalf("expected 100 lines, got %d", len(lines))
	}

	for i, line := range lines {
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
	}
}

func TestCorrelationIDContext(t *testing.T) {
	ctx := context.Background()

	// Missing returns empty string
	if id := CorrelationIDFromContext(ctx); id != "" {
		t.Errorf("expected empty, got %q", id)
	}

	// Round-trip
	ctx = WithCorrelationID(ctx, "corr-42")
	if id := CorrelationIDFromContext(ctx); id != "corr-42" {
		t.Errorf("expected %q, got %q", "corr-42", id)
	}
}

func TestTaskIDContext(t *testing.T) {
	ctx := context.Background()

	// Missing returns empty string
	if id := TaskIDFromContext(ctx); id != "" {
		t.Errorf("expected empty, got %q", id)
	}

	// Round-trip
	ctx = WithTaskID(ctx, "task-99")
	if id := TaskIDFromContext(ctx); id != "task-99" {
		t.Errorf("expected %q, got %q", "task-99", id)
	}
}

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()

	if len(id1) != 16 {
		t.Errorf("expected 16-char hex, got %d chars: %q", len(id1), id1)
	}
	if len(id2) != 16 {
		t.Errorf("expected 16-char hex, got %d chars: %q", len(id2), id2)
	}
	if id1 == id2 {
		t.Errorf("two GenerateID calls should produce different values: %q", id1)
	}
}
