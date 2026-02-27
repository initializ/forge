package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Audit event type constants.
const (
	AuditSessionStart  = "session_start"
	AuditSessionEnd    = "session_end"
	AuditToolExec      = "tool_exec"
	AuditEgressAllowed = "egress_allowed"
	AuditEgressBlocked = "egress_blocked"
	AuditLLMCall       = "llm_call"
	AuditGuardrail     = "guardrail_check"
)

// AuditEvent is a single structured audit record emitted as NDJSON.
type AuditEvent struct {
	Timestamp     string         `json:"ts"`
	Event         string         `json:"event"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	TaskID        string         `json:"task_id,omitempty"`
	Fields        map[string]any `json:"fields,omitempty"`
}

// AuditLogger writes structured NDJSON audit events to an io.Writer.
type AuditLogger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewAuditLogger creates a new AuditLogger writing to w.
func NewAuditLogger(w io.Writer) *AuditLogger {
	return &AuditLogger{w: w}
}

// Emit writes an audit event as a single NDJSON line. If Timestamp is empty
// it is set to the current time in RFC3339 format.
func (a *AuditLogger) Emit(event AuditEvent) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')

	a.mu.Lock()
	a.w.Write(data) //nolint:errcheck
	a.mu.Unlock()
}

// Context key types for correlation and task IDs.
type correlationIDKey struct{}
type taskIDKey struct{}

// WithCorrelationID stores a correlation ID in the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationIDFromContext retrieves the correlation ID from the context.
// Returns "" if not set.
func CorrelationIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithTaskID stores a task ID in the context.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey{}, id)
}

// TaskIDFromContext retrieves the task ID from the context.
// Returns "" if not set.
func TaskIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(taskIDKey{}).(string); ok {
		return id
	}
	return ""
}

// GenerateID produces a 16-character hex random ID using crypto/rand.
func GenerateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback: return a fixed string (should never happen in practice)
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}
