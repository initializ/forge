package mcp

import "context"

// Transport carries JSON-RPC frames between Forge and a single MCP
// server. Implementations are responsible for the wire encoding,
// per-call timeouts, and propagating ctx cancellation.
//
// Phase 1 has exactly one implementation: HTTPTransport in
// transport_http.go. A future stdio implementation would live in a
// separate file behind a feature gate (per the deferred-stdio
// decision); the interface itself does not change.
//
// Concurrency: Send and Recv MAY be called concurrently from
// different goroutines. Send MUST be safe to call concurrently with
// itself. Recv is typically driven by a single goroutine (the
// Client's response demultiplexer).
type Transport interface {
	// Send writes one frame to the wire. For HTTP this is a single
	// POST; for stdio it would be a single line of JSON. Returns when
	// the frame has been handed off; does NOT block waiting for a
	// response.
	Send(ctx context.Context, msg JSONRPCMessage) error

	// Recv blocks until the next frame arrives or ctx is cancelled.
	// Returns ErrClosed once Close has been called and the queue has
	// drained.
	Recv(ctx context.Context) (JSONRPCMessage, error)

	// Close releases all resources. Idempotent. Frames in flight at
	// Close time are dropped; callers blocked on Recv are unblocked
	// with ErrClosed.
	Close() error
}
