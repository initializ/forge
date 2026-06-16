package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// TestToolExecAudit_CarriesSequenceFromContext is a regression test for
// the bug where the BeforeToolExec and AfterToolExec audit hooks emitted
// via plain AuditLogger.Emit, skipping the per-invocation sequence
// counter installed on the request context.
//
// The user-visible symptom (reported on v0.15.0): tool_exec rows in the
// audit stream had no "seq" field even though they fired inside an
// invocation scope, breaking the gap-detection guarantee FWS-8 promises
// (every event from a given correlation_id should expose a monotonically
// increasing sequence number).
//
// This test invokes the same hook registration the runner uses, fires
// the hooks with a context carrying a SequenceCounter, and asserts the
// emitted tool_exec events carry the expected seq values.
func TestToolExecAudit_CarriesSequenceFromContext(t *testing.T) {
	var buf bytes.Buffer
	al := coreruntime.NewAuditLogger(&buf)

	r := &Runner{cfg: RunnerConfig{}}
	hooks := coreruntime.NewHookRegistry()
	r.registerAuditHooks(hooks, al)

	// Set up the per-invocation context the runner installs on every
	// A2A request entry.
	ctx := coreruntime.WithCorrelationID(context.Background(), "corr-abc")
	ctx = coreruntime.WithTaskID(ctx, "task-xyz")
	ctx = coreruntime.WithSequenceCounter(ctx, new(coreruntime.SequenceCounter))

	// Fire BeforeToolExec then AfterToolExec — exactly what the loop
	// does for a single tool call.
	if err := hooks.Fire(ctx, coreruntime.BeforeToolExec, &coreruntime.HookContext{
		ToolName:      "test_tool",
		ToolInput:     `{"arg":"value"}`,
		CorrelationID: "corr-abc",
		TaskID:        "task-xyz",
	}); err != nil {
		t.Fatalf("BeforeToolExec: %v", err)
	}
	if err := hooks.Fire(ctx, coreruntime.AfterToolExec, &coreruntime.HookContext{
		ToolName:         "test_tool",
		ToolInput:        `{"arg":"value"}`,
		ToolOutput:       "result",
		ToolExecDuration: 250 * time.Millisecond,
		CorrelationID:    "corr-abc",
		TaskID:           "task-xyz",
	}); err != nil {
		t.Fatalf("AfterToolExec: %v", err)
	}

	// Parse the two tool_exec events out of the NDJSON buffer.
	var events []coreruntime.AuditEvent
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev coreruntime.AuditEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if got := len(events); got != 2 {
		t.Fatalf("got %d events, want 2", got)
	}

	// Both events MUST carry sequence numbers — this is the regression
	// pin. Pre-fix the seq field was absent (omitempty zero).
	if events[0].Sequence != 1 {
		t.Errorf("BeforeToolExec event seq = %d, want 1", events[0].Sequence)
	}
	if events[1].Sequence != 2 {
		t.Errorf("AfterToolExec event seq = %d, want 2", events[1].Sequence)
	}

	// Sanity: correlation_id and task_id round-trip too.
	for i, ev := range events {
		if ev.CorrelationID != "corr-abc" {
			t.Errorf("event[%d] correlation_id = %q, want corr-abc", i, ev.CorrelationID)
		}
		if ev.TaskID != "task-xyz" {
			t.Errorf("event[%d] task_id = %q, want task-xyz", i, ev.TaskID)
		}
		if ev.Event != coreruntime.AuditToolExec {
			t.Errorf("event[%d] event = %q, want tool_exec", i, ev.Event)
		}
	}
}
