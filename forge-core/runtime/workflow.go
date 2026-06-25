package runtime

import (
	"context"
	"net/http"
)

// Workflow correlation header names (issue #86 / FWS-2). Sent by any
// A2A-compatible orchestrator on every request that's part of a
// workflow execution. Header names are deliberately vendor-neutral so
// any orchestrator (initializ Command, custom registries, third-party
// platforms) can drive Forge's correlation surface without adopting a
// vendor prefix. Forge agents extract them at the request boundary,
// stash them in context.Context, and tag every audit event with the
// matching workflow / execution / stage / step identifiers so audit
// consumers can correlate events across multiple agents participating
// in the same workflow.
//
// FORGE-2 / issue #185 split the previously-overloaded X-Workflow-ID
// header into two distinct identifiers so operators can answer both
// "show me this specific run" (per-execution) and "show me every run
// of this workflow" / "top failing workflows" (per-definition) queries
// without a join on opaque ids:
//
//   - HeaderWorkflowID:           workflow DEFINITION id, stable across all
//     runs of the same workflow.
//   - HeaderWorkflowExecutionID:  PER-RUN instance id, unique per
//     workflow execution.
//
// Industry precedent for the split: GitHub Actions (workflow +
// workflow_run_id), Tekton (Pipeline + PipelineRun), Argo (Workflow +
// WorkflowRun).
//
// Absence of these headers is the normal case for direct A2A
// invocations (e.g. local development, peer agents not orchestrated).
// When absent, audit events emit without the workflow fields — full
// backward compatibility with pre-FWS-2 audit consumers.
const (
	HeaderWorkflowID          = "X-Workflow-ID"
	HeaderWorkflowExecutionID = "X-Workflow-Execution-ID"
	HeaderWorkflowStageID     = "X-Workflow-Stage-ID"
	HeaderWorkflowStepID      = "X-Workflow-Step-ID"
	HeaderInvocationCaller    = "X-Invocation-Caller"
)

// WorkflowContext carries the orchestration identifiers a Forge agent
// extracts from inbound A2A request headers. Zero value is meaningful
// — it represents "no workflow context" (direct A2A invocation).
type WorkflowContext struct {
	// WorkflowID identifies the workflow DEFINITION — stable across
	// all runs of the same workflow. Sourced from X-Workflow-ID.
	// Audit consumers join on this for definition-level rollups:
	// "top failing workflows," "latency by workflow definition."
	WorkflowID string

	// WorkflowExecutionID identifies the PER-RUN instance — unique
	// per workflow execution. Sourced from X-Workflow-Execution-ID.
	// Audit consumers join on this for per-run timelines: "show me
	// every event in this specific run, across every agent the
	// orchestrator dispatched to." Added in FORGE-2 / issue #185.
	WorkflowExecutionID string

	// StageID identifies a stage within the workflow (a group of
	// steps that may run in parallel).
	StageID string

	// StepID identifies the specific step within the stage that
	// invoked this agent.
	StepID string

	// InvocationCaller identifies the upstream caller — typically the
	// orchestrator's identity, but for agent-to-agent calls within a
	// workflow it carries the upstream agent's identifier.
	InvocationCaller string
}

// IsZero reports whether the WorkflowContext carries no orchestration
// identifiers. Used by audit and helpers to decide whether to stamp
// workflow fields (when zero, fields are omitted entirely so the
// emitted JSON matches the pre-FWS-2 shape).
func (w WorkflowContext) IsZero() bool {
	return w.WorkflowID == "" &&
		w.WorkflowExecutionID == "" &&
		w.StageID == "" &&
		w.StepID == "" &&
		w.InvocationCaller == ""
}

// WorkflowContextFromHTTPHeaders extracts the orchestration identifiers
// from an inbound HTTP request's headers. Missing headers map to empty
// fields; the returned WorkflowContext is `IsZero` when none are set.
func WorkflowContextFromHTTPHeaders(h http.Header) WorkflowContext {
	return WorkflowContext{
		WorkflowID:          h.Get(HeaderWorkflowID),
		WorkflowExecutionID: h.Get(HeaderWorkflowExecutionID),
		StageID:             h.Get(HeaderWorkflowStageID),
		StepID:              h.Get(HeaderWorkflowStepID),
		InvocationCaller:    h.Get(HeaderInvocationCaller),
	}
}

// ApplyToHTTPHeaders writes any non-empty WorkflowContext fields onto
// outbound request headers. Used by tools that explicitly propagate
// workflow context to downstream A2A calls (the issue's "agent
// invoking another agent during workflow execution" path).
//
// Auto-propagation is deliberately not built into the egress proxy —
// the X-Workflow-* headers identify the workflow and would leak if
// the agent calls a non-workflow third-party API. Tools propagate
// explicitly when they know the target is a workflow peer.
func (w WorkflowContext) ApplyToHTTPHeaders(h http.Header) {
	if w.WorkflowID != "" {
		h.Set(HeaderWorkflowID, w.WorkflowID)
	}
	if w.WorkflowExecutionID != "" {
		h.Set(HeaderWorkflowExecutionID, w.WorkflowExecutionID)
	}
	if w.StageID != "" {
		h.Set(HeaderWorkflowStageID, w.StageID)
	}
	if w.StepID != "" {
		h.Set(HeaderWorkflowStepID, w.StepID)
	}
	if w.InvocationCaller != "" {
		h.Set(HeaderInvocationCaller, w.InvocationCaller)
	}
}

// Context key for the WorkflowContext. Kept unexported — callers go
// through WithWorkflowContext / WorkflowContextFromContext so the
// key type can never collide with another package's context key.
type workflowContextKey struct{}

// WithWorkflowContext stores a WorkflowContext in the request context.
// Mirrors the WithCorrelationID / WithTaskID pattern already used by
// the audit layer.
func WithWorkflowContext(ctx context.Context, w WorkflowContext) context.Context {
	return context.WithValue(ctx, workflowContextKey{}, w)
}

// WorkflowContextFromContext retrieves the WorkflowContext from the
// context. Returns the zero value (IsZero == true) when none was set.
func WorkflowContextFromContext(ctx context.Context) WorkflowContext {
	if w, ok := ctx.Value(workflowContextKey{}).(WorkflowContext); ok {
		return w
	}
	return WorkflowContext{}
}
