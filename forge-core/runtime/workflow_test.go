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
	h.Set(HeaderWorkflowExecutionID, "wfrun-42-abc")
	h.Set(HeaderWorkflowStageID, "stage-rollout")
	h.Set(HeaderWorkflowStepID, "step-canary")
	h.Set(HeaderInvocationCaller, "orchestrator")

	wc := WorkflowContextFromHTTPHeaders(h)
	if wc.WorkflowID != "wf-42" || wc.WorkflowExecutionID != "wfrun-42-abc" ||
		wc.StageID != "stage-rollout" || wc.StepID != "step-canary" ||
		wc.InvocationCaller != "orchestrator" {
		t.Errorf("WorkflowContext = %+v, want all five fields populated", wc)
	}
}

// TestWorkflowContextFromHTTPHeaders_DefinitionAndExecutionAreIndependent
// is the FORGE-2 / issue #185 split invariant: the two ids carry
// distinct semantics (definition stable across runs; execution unique
// per run) and a request can populate either or both. An operator
// query "show me all events for this run" joins on
// WorkflowExecutionID; "top failing workflows" joins on WorkflowID.
// Both must propagate independently — populating one MUST NOT
// auto-derive the other.
func TestWorkflowContextFromHTTPHeaders_DefinitionAndExecutionAreIndependent(t *testing.T) {
	// Definition only — common when a registry surface lists workflow
	// metadata without a specific run.
	h := http.Header{}
	h.Set(HeaderWorkflowID, "def-only")
	wc := WorkflowContextFromHTTPHeaders(h)
	if wc.WorkflowID != "def-only" {
		t.Errorf("WorkflowID = %q, want def-only", wc.WorkflowID)
	}
	if wc.WorkflowExecutionID != "" {
		t.Errorf("WorkflowExecutionID auto-populated from WorkflowID; got %q", wc.WorkflowExecutionID)
	}

	// Execution only — unusual but valid (a dispatcher with an
	// opaque per-run id and no definition handle).
	h = http.Header{}
	h.Set(HeaderWorkflowExecutionID, "exec-only-99")
	wc = WorkflowContextFromHTTPHeaders(h)
	if wc.WorkflowExecutionID != "exec-only-99" {
		t.Errorf("WorkflowExecutionID = %q, want exec-only-99", wc.WorkflowExecutionID)
	}
	if wc.WorkflowID != "" {
		t.Errorf("WorkflowID auto-populated from WorkflowExecutionID; got %q", wc.WorkflowID)
	}
	// Execution-only is still a non-zero context — IsZero must reflect that.
	if wc.IsZero() {
		t.Errorf("execution-only ctx reports IsZero=true; got %+v", wc)
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

// TestApplyToHTTPHeaders_PopulatesExecutionID pins the FORGE-2 /
// issue #185 plumbing: when a workflow context carries an execution
// id, ApplyToHTTPHeaders writes the X-Workflow-Execution-Id header on
// outbound A2A calls so downstream agents see the same per-run id and
// their audit events join on it.
func TestApplyToHTTPHeaders_PopulatesExecutionID(t *testing.T) {
	wc := WorkflowContext{WorkflowID: "wf-9", WorkflowExecutionID: "wfrun-9-zzz"}
	h := http.Header{}
	wc.ApplyToHTTPHeaders(h)

	if h.Get(HeaderWorkflowExecutionID) != "wfrun-9-zzz" {
		t.Errorf("WorkflowExecutionID header = %q, want wfrun-9-zzz",
			h.Get(HeaderWorkflowExecutionID))
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
	in.Set(HeaderWorkflowExecutionID, "wfrun")
	in.Set(HeaderWorkflowStageID, "stg")
	in.Set(HeaderWorkflowStepID, "stp")
	in.Set(HeaderInvocationCaller, "ic")

	ctx := WithWorkflowContext(context.Background(), WorkflowContextFromHTTPHeaders(in))

	out := http.Header{}
	WorkflowContextFromContext(ctx).ApplyToHTTPHeaders(out)

	for _, k := range []string{
		HeaderWorkflowID,
		HeaderWorkflowExecutionID,
		HeaderWorkflowStageID,
		HeaderWorkflowStepID,
		HeaderInvocationCaller,
	} {
		if out.Get(k) != in.Get(k) {
			t.Errorf("header %q round-trip mismatch: in=%q out=%q", k, in.Get(k), out.Get(k))
		}
	}
}
