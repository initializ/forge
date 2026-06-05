package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Regression tests for issue #88 / FWS-4 — invocation_cancelled audit
// event shape. Carries the classified reason, wall-clock duration up
// to cancellation, and any partial usage that landed before the
// cancel signal. Schema additive over the FWS-3 invocation_complete
// shape.

func TestEmitInvocationCancelled_CarriesReasonAndDuration(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitInvocationCancelled(context.Background(),
		CancelReasonCostLimitExceeded,
		350*time.Millisecond,
		map[string]any{"state": "canceled"},
	)

	var evt AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if evt.Event != AuditInvocationCancelled {
		t.Errorf("Event = %q, want %q", evt.Event, AuditInvocationCancelled)
	}
	if evt.DurationMs == nil || *evt.DurationMs != 350 {
		t.Errorf("DurationMs = %v, want 350", evt.DurationMs)
	}
	if evt.Fields["reason"] != string(CancelReasonCostLimitExceeded) {
		t.Errorf("fields.reason = %v, want %q", evt.Fields["reason"], CancelReasonCostLimitExceeded)
	}
	if evt.Fields["state"] != "canceled" {
		t.Errorf("fields.state should be preserved, got %v", evt.Fields["state"])
	}
}

func TestEmitInvocationCancelled_PartialUsagePassedThroughFields(t *testing.T) {
	// When the cancel arrives after some LLM calls completed, the
	// runner reads the LLMUsageAccumulator snapshot and adds the
	// totals to fields. The audit emitter must pass them through
	// unmodified so a downstream billing consumer sees what was
	// consumed before cancellation.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitInvocationCancelled(context.Background(),
		CancelReasonWorkflowFailure,
		200*time.Millisecond,
		map[string]any{
			"state":               "canceled",
			"input_tokens_total":  500,
			"output_tokens_total": 120,
			"llm_call_count":      2,
			"model":               "claude-sonnet-4-6",
			"provider":            "anthropic",
		},
	)

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if v, _ := evt.Fields["input_tokens_total"].(float64); v != 500 {
		t.Errorf("input_tokens_total = %v, want 500", evt.Fields["input_tokens_total"])
	}
	if v, _ := evt.Fields["output_tokens_total"].(float64); v != 120 {
		t.Errorf("output_tokens_total = %v, want 120", evt.Fields["output_tokens_total"])
	}
	if v, _ := evt.Fields["llm_call_count"].(float64); v != 2 {
		t.Errorf("llm_call_count = %v, want 2", evt.Fields["llm_call_count"])
	}
}

func TestEmitInvocationCancelled_NilFieldsStillEmitsReason(t *testing.T) {
	// Cancellation before any LLM call has the runner pass nil fields.
	// The reason key must still land — without it, audit consumers
	// can't classify the event.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitInvocationCancelled(context.Background(),
		CancelReasonExternalSignal,
		2*time.Millisecond,
		nil,
	)

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.Fields["reason"] != string(CancelReasonExternalSignal) {
		t.Errorf("nil-fields path must still set reason, got %+v", evt.Fields)
	}
}

func TestEmitInvocationCancelled_RoutesThroughEmitFromContext(t *testing.T) {
	// EmitInvocationCancelled goes through EmitFromContext so workflow
	// correlation auto-tags (issue #86 / FWS-2). Set a workflow ctx
	// and confirm the workflow fields land on the cancelled event.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithCorrelationID(context.Background(), "corr-c")
	ctx = WithTaskID(ctx, "task-c")
	ctx = WithWorkflowContext(ctx, WorkflowContext{
		WorkflowID: "wf-1", StageID: "stg", StepID: "stp",
		InvocationCaller: "orchestrator",
	})

	audit.EmitInvocationCancelled(ctx, CancelReasonTimeout, 0, nil)

	var evt AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &evt)
	if evt.CorrelationID != "corr-c" || evt.TaskID != "task-c" {
		t.Errorf("ctx correlation not threaded, got %+v", evt)
	}
	if evt.WorkflowID != "wf-1" || evt.StageID != "stg" || evt.StepID != "stp" || evt.InvocationCaller != "orchestrator" {
		t.Errorf("workflow correlation not auto-tagged on cancelled event, got %+v", evt)
	}
}

func TestEmitInvocationCancelled_PassesThroughUnknownReason(t *testing.T) {
	// The audit emitter forwards whatever string was supplied — value
	// validation happens at the JSON-RPC boundary, not in the
	// audit layer. Unknown reasons must NOT be silently rewritten;
	// downstream consumers see the original.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)
	audit.EmitInvocationCancelled(context.Background(),
		CancellationReason("future_reason_v2"),
		10*time.Millisecond,
		nil,
	)
	if !strings.Contains(buf.String(), `"reason":"future_reason_v2"`) {
		t.Errorf("unknown reason should pass through verbatim, got: %s", buf.String())
	}
}
