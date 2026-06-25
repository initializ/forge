package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for issue #86 / FWS-2 — EmitFromContext auto-tags
// the four workflow correlation fields on every audit event when a
// WorkflowContext is in the request ctx. Absence is the backward-compat
// case: fields are omitted from the JSON entirely.

func TestEmitFromContext_TagsWorkflowFieldsWhenContextHasThem(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID:          "wf-100",
		WorkflowExecutionID: "wfrun-100-abc",
		StageID:             "stage-A",
		StepID:              "step-1",
		InvocationCaller:    "orchestrator",
	})

	audit.EmitFromContext(ctx, AuditEvent{Event: "tool_exec"})

	var got AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if got.WorkflowID != "wf-100" || got.WorkflowExecutionID != "wfrun-100-abc" ||
		got.StageID != "stage-A" || got.StepID != "step-1" ||
		got.InvocationCaller != "orchestrator" {
		t.Errorf("workflow fields not tagged: %+v", got)
	}
}

// TestEmitFromContext_TagsBothWorkflowDefinitionAndExecution is the
// snapshot golden the issue calls out: an llm_call audit event
// emitted under a workflow run carries BOTH workflow_id (definition)
// and workflow_execution_id (per-run) at the top level so a SIEM can
// build per-run timelines without joining on opaque ids. FORGE-2 /
// issue #185.
func TestEmitFromContext_TagsBothWorkflowDefinitionAndExecution(t *testing.T) {
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID:          "compliance-review",
		WorkflowExecutionID: "exec-2026-06-25-001",
	})

	audit.EmitFromContext(ctx, AuditEvent{Event: "llm_call"})

	js := buf.String()
	for _, want := range []string{
		`"event":"llm_call"`,
		`"workflow_id":"compliance-review"`,
		`"workflow_execution_id":"exec-2026-06-25-001"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("emitted JSON missing %s; got:\n%s", want, js)
		}
	}
}

func TestEmitFromContext_OmitsWorkflowFieldsWhenContextEmpty(t *testing.T) {
	// Backward compatibility: direct A2A invocation (no orchestrator
	// headers) must produce audit JSON without workflow_id /
	// stage_id / step_id / invocation_caller fields at all.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.EmitFromContext(context.Background(), AuditEvent{Event: "session_start"})

	got := buf.String()
	for _, forbidden := range []string{
		`"workflow_id"`, `"workflow_execution_id"`,
		`"stage_id"`, `"step_id"`, `"invocation_caller"`,
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("empty ctx should omit %s from JSON, got:\n%s", forbidden, got)
		}
	}
}

func TestEmitFromContext_ExplicitFieldsTakePrecedenceOverContext(t *testing.T) {
	// EmitFromContext is a fallback: any field already set on the
	// AuditEvent literal wins. Lets callers stamp authoritative
	// values when needed (e.g. cross-cutting events that span multiple
	// invocation ctxs).
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID: "from-ctx",
	})
	audit.EmitFromContext(ctx, AuditEvent{
		Event:      "tool_exec",
		WorkflowID: "explicit-wins",
	})

	var got AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.WorkflowID != "explicit-wins" {
		t.Errorf("explicit WorkflowID should win over ctx, got %q", got.WorkflowID)
	}
}

func TestEmitFromContext_AlsoTagsCorrelationAndTaskID(t *testing.T) {
	// EmitFromContext is the unified path for ctx-derived tagging —
	// it must cover CorrelationID + TaskID alongside the workflow
	// fields, mirroring the contract handlers expect.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithCorrelationID(context.Background(), "corr-X")
	ctx = WithTaskID(ctx, "task-Y")
	ctx = WithWorkflowContext(ctx, WorkflowContext{WorkflowID: "wf-Z"})

	audit.EmitFromContext(ctx, AuditEvent{Event: "session_start"})

	var got AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got)
	if got.CorrelationID != "corr-X" || got.TaskID != "task-Y" || got.WorkflowID != "wf-Z" {
		t.Errorf("expected all three ctx-derived fields tagged, got %+v", got)
	}
}

func TestEmitFromContext_PartialWorkflowContextOmitsAbsentFields(t *testing.T) {
	// Operator might forward only the workflow ID. The remaining
	// fields must stay absent from the JSON (omitempty).
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID: "wf-only",
	})
	audit.EmitFromContext(ctx, AuditEvent{Event: "tool_exec"})

	js := buf.String()
	if !strings.Contains(js, `"workflow_id":"wf-only"`) {
		t.Errorf("WorkflowID should be present in JSON, got:\n%s", js)
	}
	if strings.Contains(js, `"stage_id"`) || strings.Contains(js, `"step_id"`) || strings.Contains(js, `"invocation_caller"`) {
		t.Errorf("empty workflow sub-fields should be omitted, got:\n%s", js)
	}
}

func TestEmit_DirectPathStillWorksWithoutAnyCtx(t *testing.T) {
	// The classic Emit (no ctx) keeps working — pre-FWS-2 callers
	// continue to function identically.
	var buf bytes.Buffer
	audit := NewAuditLogger(&buf)

	audit.Emit(AuditEvent{
		Event:         "session_start",
		CorrelationID: "explicit",
		TaskID:        "explicit",
	})

	var got AuditEvent
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got)
	if got.CorrelationID != "explicit" || got.TaskID != "explicit" {
		t.Errorf("Emit should pass through explicit fields, got %+v", got)
	}
	// Workflow fields should be absent (Emit doesn't touch ctx).
	if got.WorkflowID != "" {
		t.Errorf("Emit must not pull from ctx; WorkflowID should be empty")
	}
}
