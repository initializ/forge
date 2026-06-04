package runtime

import (
	"context"
	"net/http"
	"testing"
)

// Regression tests for issue #86 / FWS-2 — Workflow correlation ID
// threading. The four orchestrator-supplied identifiers must flow
// through HTTP headers → context.Context → AuditEvent without loss,
// and audit events must omit the workflow fields entirely when no
// headers were sent (backward compat with pre-FWS-2 consumers).

func TestWorkflowContext_IsZero(t *testing.T) {
	if !(WorkflowContext{}).IsZero() {
		t.Errorf("zero-value WorkflowContext should be IsZero=true")
	}
	if (WorkflowContext{WorkflowID: "wf-1"}).IsZero() {
		t.Errorf("WorkflowContext with any non-empty field must NOT be IsZero")
	}
}

func TestWorkflowContextFromHTTPHeaders_ExtractsAllFour(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderWorkflowID, "wf-42")
	h.Set(HeaderWorkflowStageID, "stage-rollout")
	h.Set(HeaderWorkflowStepID, "step-canary")
	h.Set(HeaderInvocationCaller, "orchestrator")

	wc := WorkflowContextFromHTTPHeaders(h)
	if wc.WorkflowID != "wf-42" || wc.StageID != "stage-rollout" || wc.StepID != "step-canary" || wc.InvocationCaller != "orchestrator" {
		t.Errorf("WorkflowContext = %+v, want all four fields populated", wc)
	}
}

func TestWorkflowContextFromHTTPHeaders_MissingHeadersYieldZero(t *testing.T) {
	wc := WorkflowContextFromHTTPHeaders(http.Header{})
	if !wc.IsZero() {
		t.Errorf("missing headers should produce IsZero WorkflowContext, got %+v", wc)
	}
}

func TestWorkflowContextFromHTTPHeaders_PartialHeadersPropagateNonEmpty(t *testing.T) {
	// Operator might supply only some headers (e.g. workflow without
	// stage during pre-stage init). Partial extraction must work.
	h := http.Header{}
	h.Set(HeaderWorkflowID, "wf-only")

	wc := WorkflowContextFromHTTPHeaders(h)
	if wc.IsZero() {
		t.Errorf("partial headers should produce non-IsZero WorkflowContext")
	}
	if wc.WorkflowID != "wf-only" {
		t.Errorf("WorkflowID = %q, want wf-only", wc.WorkflowID)
	}
	if wc.StageID != "" || wc.StepID != "" || wc.InvocationCaller != "" {
		t.Errorf("missing fields should remain empty, got %+v", wc)
	}
}

func TestWorkflowContext_ContextRoundTrip(t *testing.T) {
	wc := WorkflowContext{
		WorkflowID:       "wf-1",
		StageID:          "stage-1",
		StepID:           "step-1",
		InvocationCaller: "caller-1",
	}
	ctx := WithWorkflowContext(context.Background(), wc)

	got := WorkflowContextFromContext(ctx)
	if got != wc {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, wc)
	}
}

func TestWorkflowContextFromContext_MissingReturnsZero(t *testing.T) {
	got := WorkflowContextFromContext(context.Background())
	if !got.IsZero() {
		t.Errorf("missing context value should return IsZero, got %+v", got)
	}
}

func TestApplyToHTTPHeaders_WritesNonEmptyFields(t *testing.T) {
	wc := WorkflowContext{
		WorkflowID:       "wf-9",
		StageID:          "stage-9",
		StepID:           "step-9",
		InvocationCaller: "agent-upstream",
	}
	h := http.Header{}
	wc.ApplyToHTTPHeaders(h)

	if h.Get(HeaderWorkflowID) != "wf-9" {
		t.Errorf("WorkflowID header = %q, want wf-9", h.Get(HeaderWorkflowID))
	}
	if h.Get(HeaderWorkflowStageID) != "stage-9" {
		t.Errorf("StageID header missing")
	}
	if h.Get(HeaderWorkflowStepID) != "step-9" {
		t.Errorf("StepID header missing")
	}
	if h.Get(HeaderInvocationCaller) != "agent-upstream" {
		t.Errorf("InvocationCaller header missing")
	}
}

func TestApplyToHTTPHeaders_OmitsEmptyFields(t *testing.T) {
	// Workflow without stage — outbound headers should mirror what's
	// set, not stamp empty values that downstream agents might
	// misinterpret as "explicitly empty stage."
	wc := WorkflowContext{WorkflowID: "wf-only"}
	h := http.Header{}
	wc.ApplyToHTTPHeaders(h)

	if h.Get(HeaderWorkflowID) != "wf-only" {
		t.Errorf("WorkflowID should be set")
	}
	// Use Values() (not direct map access) — http.Header canonicalizes
	// keys, so h[HeaderWorkflowStageID] would always be empty even
	// when the canonical-cased key is present.
	if len(h.Values(HeaderWorkflowStageID)) > 0 {
		t.Errorf("StageID should NOT be set when empty, got %v", h)
	}
}

func TestRoundTripHTTPHeaders(t *testing.T) {
	// Headers → ctx → headers round-trip preserves all fields. This
	// is the contract initializ's orchestrator + Forge runner depend on.
	in := http.Header{}
	in.Set(HeaderWorkflowID, "wf")
	in.Set(HeaderWorkflowStageID, "stg")
	in.Set(HeaderWorkflowStepID, "stp")
	in.Set(HeaderInvocationCaller, "ic")

	ctx := WithWorkflowContext(context.Background(), WorkflowContextFromHTTPHeaders(in))

	out := http.Header{}
	WorkflowContextFromContext(ctx).ApplyToHTTPHeaders(out)

	for _, k := range []string{
		HeaderWorkflowID,
		HeaderWorkflowStageID,
		HeaderWorkflowStepID,
		HeaderInvocationCaller,
	} {
		if out.Get(k) != in.Get(k) {
			t.Errorf("header %q round-trip mismatch: in=%q out=%q", k, in.Get(k), out.Get(k))
		}
	}
}
