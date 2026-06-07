package runtime

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
)

// Sink consumes serialized audit event bytes. Implementations must be
// safe for concurrent use. Sinks should never block the emitter under
// back-pressure for longer than their configured timeout; on timeout
// the sink drops the event and increments its drop counter, never
// returns an error to the caller.
//
// The audit pipeline composes one or more sinks (stderr safety-net +
// optional Unix socket / HTTP sink for export). Each sink is independent;
// a failure on one does not stop emission on the others.
//
// See issue #95 / FWS-7.
type Sink interface {
	// Write delivers a single event. The event is already marshaled
	// NDJSON (one line, trailing newline included). Returns nil even
	// on transient failure; sinks are responsible for their own
	// retry/buffering policy. A non-nil error indicates a permanent
	// sink failure that should be logged once.
	Write(ctx context.Context, eventBytes []byte) error

	// Close flushes any buffered events and releases resources. Called
	// during agent shutdown. Implementations must honor any deadline
	// on the passed context and never block beyond it.
	Close(ctx context.Context) error

	// Name returns a stable identifier ("stderr" / "unix-socket" /
	// "localhost-http") used in self-reporting and operator logs.
	Name() string

	// Stats returns counters describing sink health since process
	// start. Keys are stable strings (writes_ok, drops_timeout,
	// drops_dial, connected); values are monotonic counts or 0/1
	// flags. Used by the periodic audit_export_status emitter and
	// the /health endpoint.
	Stats() map[string]int64
}

// sinkStats is the shared counter set every sink keeps. Embedded into
// sink structs so each has a consistent set of metrics without
// duplicating the bookkeeping. Counters are accessed via atomic
// load/add so Stats() is safe to call concurrently with Write().
type sinkStats struct {
	writesOK     atomic.Int64 // events successfully delivered
	dropsTimeout atomic.Int64 // events dropped because Write missed its deadline
	dropsDial    atomic.Int64 // events dropped because the connection wasn't up
	connected    atomic.Int64 // 1 when a working connection is held, 0 otherwise (sticky 0 for fire-and-forget sinks)
}

func (s *sinkStats) snapshot() map[string]int64 {
	return map[string]int64{
		"writes_ok":     s.writesOK.Load(),
		"drops_timeout": s.dropsTimeout.Load(),
		"drops_dial":    s.dropsDial.Load(),
		"connected":     s.connected.Load(),
	}
}

// writerSink writes events to a plain io.Writer. Used for stderr (the
// production safety net) and for *bytes.Buffer in tests. Writes are
// serialized with an internal mutex so concurrent Emit() calls never
// interleave NDJSON lines mid-stream. The writer's own latency bounds
// Write — if the OS buffers stderr, this is fast; if a test passes a
// slow writer, it's the test's concern, not the audit pipeline's.
type writerSink struct {
	mu    sync.Mutex
	w     io.Writer
	name  string
	stats sinkStats
}

func newWriterSink(w io.Writer, name string) *writerSink {
	return &writerSink{w: w, name: name}
}

func (s *writerSink) Write(_ context.Context, eventBytes []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(eventBytes); err != nil {
		// Writer-backed sinks don't drop on transient error: a partial
		// stderr write is the kernel's problem, not ours. We surface
		// the error so the emitter can log it once per sink lifetime.
		return err
	}
	s.stats.writesOK.Add(1)
	return nil
}

// Close is a no-op for writer sinks. Callers own the writer's
// lifecycle — closing os.Stderr would break the rest of the process.
func (s *writerSink) Close(_ context.Context) error { return nil }

func (s *writerSink) Name() string            { return s.name }
func (s *writerSink) Stats() map[string]int64 { return s.stats.snapshot() }
