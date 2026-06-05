package runtime

import (
	"context"
	"errors"
	"sync"
)

// CancellationReason classifies why an in-flight A2A invocation was
// cancelled. Sourced from the tasks/cancel JSON-RPC params and carried
// onto the invocation_cancelled audit event so a downstream consumer
// (cost aggregator, workflow UI, SIEM) can distinguish "the operator
// hit stop" from "the orchestrator hit a cost ceiling." See issue
// #88 / FWS-4.
//
// New reasons are additive — audit consumers that don't recognize a
// reason string should pass it through, not reject the event.
type CancellationReason string

const (
	// CancelReasonWorkflowFailure is set by the orchestrator when a
	// sibling step in a parallel stage failed under fail_workflow
	// semantics and this in-flight agent should abandon its work.
	CancelReasonWorkflowFailure CancellationReason = "workflow_failure"

	// CancelReasonCostLimitExceeded is set by the orchestrator when
	// the workflow's cumulative cost ceiling (from the FWS-3 token
	// totals in A2A response headers) was hit and the platform is
	// cutting off further LLM spend.
	CancelReasonCostLimitExceeded CancellationReason = "cost_limit_exceeded"

	// CancelReasonTimeout is set by the orchestrator (or by Forge's
	// own task deadline) when the wall-clock budget for the
	// invocation has been exhausted.
	CancelReasonTimeout CancellationReason = "timeout"

	// CancelReasonExternalSignal is the default — operator-initiated
	// cancel, debugging stop, anything else not covered by the more
	// specific reasons.
	CancelReasonExternalSignal CancellationReason = "external_signal"
)

// IsValid reports whether r is one of the documented reason values.
// Used at the tasks/cancel boundary to validate operator input; the
// runtime itself happily forwards whatever string was supplied — the
// validation is a UX nicety, not a security boundary.
func (r CancellationReason) IsValid() bool {
	switch r {
	case CancelReasonWorkflowFailure,
		CancelReasonCostLimitExceeded,
		CancelReasonTimeout,
		CancelReasonExternalSignal:
		return true
	}
	return false
}

// cancelledByOrchestrator is the sentinel error type the tasks/cancel
// handler hands to context.CancelCauseFunc. Carries the reason so the
// executeTask goroutine — which sees ctx.Done() in a different
// goroutine — can read it back via context.Cause without sharing
// mutable state with the handler.
//
// This is the idiomatic Go 1.20+ "cancellation with reason" pattern.
// Plain context.CancelFunc would force us to side-channel the reason
// via a registry-side mutex, which adds contention on the cancel hot
// path for no benefit.
type cancelledByOrchestrator struct {
	Reason CancellationReason
}

func (e *cancelledByOrchestrator) Error() string {
	return "invocation cancelled: " + string(e.Reason)
}

// CancellationReasonFromCause unwraps the reason stamped on ctx by
// the tasks/cancel path. Call this after observing ctx.Err() in the
// executeTask goroutine. Returns CancelReasonExternalSignal when ctx
// was cancelled without a typed reason (e.g. parent ctx deadline,
// graceful shutdown signal) — those are still cancellations but the
// emitting handler didn't classify them.
func CancellationReasonFromCause(ctx context.Context) CancellationReason {
	if ctx.Err() == nil {
		return ""
	}
	cause := context.Cause(ctx)
	var c *cancelledByOrchestrator
	if errors.As(cause, &c) {
		return c.Reason
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return CancelReasonTimeout
	}
	return CancelReasonExternalSignal
}

// CancellationRegistry tracks in-flight A2A invocations so the
// tasks/cancel handler can signal them. One registry per Runner; one
// entry per active invocation, keyed by task ID.
//
// The registry is the bridge between the JSON-RPC handler (which sees
// the cancel request) and the long-running executeTask goroutine
// (which holds the context.CancelCauseFunc). The handler looks up the
// task ID, invokes the stored cancel function with a typed reason,
// and the goroutine's ctx propagates the cancellation through the LLM
// client, tool execution, and audit emission.
type CancellationRegistry struct {
	mu      sync.Mutex
	entries map[string]*registryEntry
}

// registryEntry wraps the CancelCauseFunc so the release closure
// can identify "its" entry by pointer equality. Without this layer
// Go's func values aren't comparable across closures, so a newer
// Register call could leak the older release into the wrong slot.
type registryEntry struct {
	cancel context.CancelCauseFunc
}

// NewCancellationRegistry returns a fresh empty registry.
func NewCancellationRegistry() *CancellationRegistry {
	return &CancellationRegistry{entries: make(map[string]*registryEntry)}
}

// Register associates a CancelCauseFunc with a task ID. Returns a
// release closure the caller must defer — release pops the entry
// from the registry so it doesn't leak after the invocation finishes
// (success, failure, or cancellation).
//
// If a registration already exists for taskID (concurrent retries on
// the same ID, or buggy callers), Register overwrites it. The
// returned release uses pointer identity to pop only its own entry,
// so a stale release from the previous owner is a no-op.
func (r *CancellationRegistry) Register(taskID string, cancel context.CancelCauseFunc) (release func()) {
	entry := &registryEntry{cancel: cancel}
	r.mu.Lock()
	r.entries[taskID] = entry
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if cur := r.entries[taskID]; cur == entry {
			delete(r.entries, taskID)
		}
	}
}

// Cancel signals the in-flight invocation for taskID with a typed
// reason. Returns true when an entry was found and its cancel function
// invoked, false when no invocation is registered (already completed,
// never started, or already cancelled and unregistered). The handler
// maps false → a no-op response so cancel-after-complete is idempotent
// rather than an error.
//
// Reason validation is the caller's job; Cancel forwards whatever it
// gets so internal cancellations (graceful shutdown, parent deadline
// translation) can supply their own reason without going through the
// JSON-RPC validator.
func (r *CancellationRegistry) Cancel(taskID string, reason CancellationReason) bool {
	r.mu.Lock()
	entry, ok := r.entries[taskID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	entry.cancel(&cancelledByOrchestrator{Reason: reason})
	return true
}

// Len returns the number of in-flight registrations. Exposed for
// tests and operational observability — there is no per-task lookup
// API by design (the handler only needs Cancel; the executeTask
// goroutine reads its own reason via context.Cause on ctx).
func (r *CancellationRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
