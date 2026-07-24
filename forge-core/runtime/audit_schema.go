package runtime

import (
	"context"
	"sync"
	"sync/atomic"
)

// AuditSchemaVersion is the current audit event contract version.
// Every emitted event carries this string in its `schema_version`
// field so consumers can detect schema upgrades. Backward-compatible
// additions (new optional fields) do NOT bump the version; removals
// or semantic changes do.
//
// Version policy:
//
//	1.0 — initial documented contract (issue #91 / FWS-8). Includes the
//	      pre-FWS-8 fields (ts, event, correlation_id, task_id, workflow_*,
//	      model, provider, input_tokens, output_tokens, duration_ms,
//	      request_id, fields) plus seq and schema_version.
//
// See docs/security/audit-logging.md for the full schema reference.
const AuditSchemaVersion = "1.0"

// SequenceCounter is the per-invocation atomic counter that drives
// AuditEvent.Sequence. One counter per A2A invocation; stuffed into
// the request context by the A2A handler at request entry, read by
// EmitFromContext (and any emit-from-context helper) to stamp the
// next sequence number.
//
// Type alias for *atomic.Int64 so callers can construct one with
// `new(atomic.Int64)` and so the package stays small.
type SequenceCounter = atomic.Int64

// sequenceCounterKey is the unexported context-key sentinel for the
// per-invocation counter. Unexported so the package surface stays
// narrow — callers thread the counter via WithSequenceCounter /
// SequenceCounterFromContext, never directly.
type sequenceCounterKey struct{}

// WithSequenceCounter stores a per-invocation sequence counter in the
// context. Called by the A2A request entry point exactly once per
// invocation; every audit emit downstream picks the counter up via
// SequenceCounterFromContext. Events emitted outside an invocation
// scope (startup banners, policy_loaded) inherit no counter and emit
// with Sequence == 0 (which JSON-omits via omitempty).
func WithSequenceCounter(ctx context.Context, c *SequenceCounter) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, sequenceCounterKey{}, c)
}

// SequenceCounterFromContext returns the per-invocation counter, or
// nil if none was set. The audit emit path uses nil-vs-non-nil to
// decide whether to stamp a Sequence on outbound events.
func SequenceCounterFromContext(ctx context.Context) *SequenceCounter {
	if c, ok := ctx.Value(sequenceCounterKey{}).(*SequenceCounter); ok {
		return c
	}
	return nil
}

// NextSequence increments the per-invocation counter and returns the
// new value. Atomic; safe to call from multiple goroutines without
// external synchronization. Returns 0 when no counter is in context
// (caller can JSON-omit the field).
func NextSequence(ctx context.Context) int64 {
	c := SequenceCounterFromContext(ctx)
	if c == nil {
		return 0
	}
	return c.Add(1)
}

// EnsureSequenceCounter returns ctx unchanged when it already carries a
// SequenceCounter; otherwise it returns a new ctx with a fresh counter
// installed. Use at any invocation-entry point that may run downstream
// of an upstream middleware which already installed a counter — e.g.,
// the runner's per-A2A-request setup runs after the auth middleware
// (which installs a counter so auth_verify lands seq=1) and must not
// clobber it. See issue #174.
func EnsureSequenceCounter(ctx context.Context) context.Context {
	if SequenceCounterFromContext(ctx) != nil {
		return ctx
	}
	return WithSequenceCounter(ctx, new(SequenceCounter))
}

// SequenceRegistry maps a live per-invocation SequenceCounter to its
// (correlation_id, task_id) key so events emitted OUTSIDE the request
// goroutine's context can advance the SAME counter and stamp a correct,
// gap-free seq. Two emitters need this:
//
//   - The egress proxy (#341): a separate 127.0.0.1 forward proxy with no
//     request ctx. It recovers (correlation_id, task_id) from the subprocess
//     Proxy-Authorization creds (#338) and looks the counter up here.
//   - The MCP consent-resume paths (#366): the loopback OAuth callback and the
//     platform POST /mcp/consent run on a detached browser/platform request;
//     they recover the parked call's (correlation_id, task_id) and seed a ctx
//     from the registered counter so the completion egress is seq'd + attributed.
//
// The counter is registered at request entry (alongside EnsureSequenceCounter)
// and evicted at invocation_complete. A miss returns 0 (the event stays
// seq-less rather than carrying a wrong or duplicated number).
type SequenceRegistry struct {
	mu sync.Mutex
	m  map[string]*SequenceCounter
}

// NewSequenceRegistry returns an empty registry.
func NewSequenceRegistry() *SequenceRegistry {
	return &SequenceRegistry{m: make(map[string]*SequenceCounter)}
}

func seqRegistryKey(correlationID, taskID string) string {
	return correlationID + "\x00" + taskID
}

// Register records the counter under (correlationID, taskID). No-op on a nil
// registry/counter or an all-empty key.
func (r *SequenceRegistry) Register(correlationID, taskID string, c *SequenceCounter) {
	if r == nil || c == nil || (correlationID == "" && taskID == "") {
		return
	}
	r.mu.Lock()
	r.m[seqRegistryKey(correlationID, taskID)] = c
	r.mu.Unlock()
}

// Get returns the counter for (correlationID, taskID), or nil if none is
// registered.
func (r *SequenceRegistry) Get(correlationID, taskID string) *SequenceCounter {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	c := r.m[seqRegistryKey(correlationID, taskID)]
	r.mu.Unlock()
	return c
}

// Evict drops the registration for (correlationID, taskID).
func (r *SequenceRegistry) Evict(correlationID, taskID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.m, seqRegistryKey(correlationID, taskID))
	r.mu.Unlock()
}

// NextSequenceFor advances the registered counter for (correlationID, taskID)
// and returns the new seq, or 0 when no counter is registered (so the caller
// JSON-omits the field rather than emitting a wrong/duplicate seq).
func (r *SequenceRegistry) NextSequenceFor(correlationID, taskID string) int64 {
	if c := r.Get(correlationID, taskID); c != nil {
		return c.Add(1)
	}
	return 0
}
