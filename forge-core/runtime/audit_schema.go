package runtime

import (
	"context"
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
