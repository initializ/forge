package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// socketSinkName is the stable Name() value used in stats + status
// events. Operators grep "sink=unix-socket" in audit_export_status to
// answer "is the export path healthy?"
const socketSinkName = "unix-socket"

// Default timing for the socket sink. Exposed as package-level
// constants so tests can reference them; the production wiring reads
// AuditExportConfig and overrides via NewSocketSink's parameters when
// the user passes non-zero values.
const (
	defaultSocketWriteTimeout = 50 * time.Millisecond
	defaultSocketDialTimeout  = 1 * time.Second
	socketBackoffInitial      = 100 * time.Millisecond
	socketBackoffMax          = 5 * time.Second
)

// socketSink delivers events to a local Unix Domain Socket. The
// design contract (per issue #95 / FWS-7):
//
//   - Lazy + reconnecting. The socket need not exist at construction
//     time. First Write attempts to dial; on failure, increments the
//     drop-dial counter and returns nil so the emitter is not blocked.
//   - Per-write timeout. If the socket Write does not complete within
//     the configured timeout (default 50ms), the event is dropped and
//     the drop-timeout counter increments.
//   - No buffering. Buffering belongs in the sidecar; the sink is
//     fire-and-forget. A slow sidecar can never back-pressure Forge.
//   - One persistent connection. On EPIPE / write error / closed
//     connection, the conn is cleared and the next Write re-dials.
//   - Exponential backoff between failed dials: 100ms initial, 5s max.
//     During backoff Writes drop without dialing, so a down sidecar
//     does not slow the emit path beyond a cheap clock check.
//
// All state is protected by mu. The mutex is held for the duration of
// each Write, including the bounded I/O; this serializes writes per
// connection but is fine because (a) audit emission is already
// serialized at the AuditLogger level by its own mutex, and (b) the
// per-write timeout keeps the critical section tiny.
type socketSink struct {
	path         string
	writeTimeout time.Duration
	dialTimeout  time.Duration

	mu         sync.Mutex
	conn       net.Conn
	nextDialAt time.Time     // wall clock; Writes before this time skip the dial
	backoff    time.Duration // current backoff window; doubles on failure
	closed     bool

	stats sinkStats
}

// NewSocketSink constructs a Unix Domain Socket sink. Zero values for
// writeTimeout / dialTimeout fall back to defaults. The socket is NOT
// dialed eagerly — the first Write triggers the connection attempt.
//
// Returns nil if path is empty (caller should not register an empty
// sink). Path validation (length, parent dir exists) is deliberately
// deferred to dial time: a sidecar that creates its socket lazily
// shouldn't cause the agent to fail at startup.
func NewSocketSink(path string, writeTimeout, dialTimeout time.Duration) Sink {
	if path == "" {
		return nil
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultSocketWriteTimeout
	}
	if dialTimeout <= 0 {
		dialTimeout = defaultSocketDialTimeout
	}
	return &socketSink{
		path:         path,
		writeTimeout: writeTimeout,
		dialTimeout:  dialTimeout,
		backoff:      socketBackoffInitial,
	}
}

func (s *socketSink) Name() string            { return socketSinkName }
func (s *socketSink) Stats() map[string]int64 { return s.stats.snapshot() }

// Write delivers one NDJSON-framed event. Behavior matrix:
//
//	conn nil, in backoff window  → drop-dial, return nil
//	conn nil, dial succeeds       → install conn, attempt write
//	conn nil, dial fails          → drop-dial, schedule next-dial, return nil
//	conn ok, write succeeds       → writes_ok++
//	conn ok, write times out      → drop-timeout, close conn, schedule next-dial
//	conn ok, write returns error  → drop on write-error, close conn, schedule next-dial
//
// Returns nil in every transient case so the caller's fan-out loop
// keeps going. A non-nil error is reserved for "sink is permanently
// dead" — currently only emitted when Close has been called.
func (s *socketSink) Write(ctx context.Context, eventBytes []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("audit socket sink is closed")
	}

	// Phase 1: ensure we have a working connection (or accept the drop).
	if s.conn == nil {
		if !time.Now().After(s.nextDialAt) {
			s.stats.dropsDial.Add(1)
			return nil
		}
		dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout)
		var d net.Dialer
		conn, err := d.DialContext(dialCtx, "unix", s.path)
		cancel()
		if err != nil {
			s.scheduleRetryLocked()
			s.stats.dropsDial.Add(1)
			return nil
		}
		s.conn = conn
		s.stats.connected.Store(1)
		s.backoff = socketBackoffInitial // reset on success
	}

	// Phase 2: write within the per-event deadline.
	deadline := time.Now().Add(s.writeTimeout)
	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		s.dropConnLocked()
		s.stats.dropsTimeout.Add(1)
		return nil
	}
	if _, err := s.conn.Write(eventBytes); err != nil {
		// timeout, EPIPE, closed conn — all map to "reconnect on next call"
		s.dropConnLocked()
		if isTimeoutError(err) {
			s.stats.dropsTimeout.Add(1)
		} else {
			// Treat write errors as a dial-class drop (the connection
			// is gone; the next call has to re-dial). Operators reading
			// counters care about "is the path up?" — write errors and
			// dial failures are the same answer.
			s.stats.dropsDial.Add(1)
		}
		return nil
	}
	s.stats.writesOK.Add(1)
	return nil
}

// dropConnLocked closes the current conn (if any), clears it, and
// schedules a backoff before the next dial. Must hold s.mu.
func (s *socketSink) dropConnLocked() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.stats.connected.Store(0)
	s.scheduleRetryLocked()
}

// scheduleRetryLocked doubles the current backoff (capped at the max)
// and sets nextDialAt to "now + backoff". Must hold s.mu.
func (s *socketSink) scheduleRetryLocked() {
	s.nextDialAt = time.Now().Add(s.backoff)
	if s.backoff < socketBackoffMax {
		s.backoff *= 2
		if s.backoff > socketBackoffMax {
			s.backoff = socketBackoffMax
		}
	}
}

// Close marks the sink dead and drops any held connection. Pending
// in-flight Writes from concurrent goroutines complete (they hold the
// mutex); subsequent Writes return ErrSinkClosed. Honors ctx by
// imposing the ctx deadline on the close I/O.
func (s *socketSink) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn == nil {
		return nil
	}
	// Pull the deadline from ctx if present so a fast-shutdown path
	// doesn't block on a hung peer.
	if d, ok := ctx.Deadline(); ok {
		_ = s.conn.SetDeadline(d)
	}
	err := s.conn.Close()
	s.conn = nil
	s.stats.connected.Store(0)
	if err != nil && err != io.EOF {
		return fmt.Errorf("closing audit socket %s: %w", s.path, err)
	}
	return nil
}

// isTimeoutError matches both net.Error.Timeout() and context deadline
// errors. The std-lib doesn't surface a single sentinel for "write
// missed its deadline" across all kernels, so we check the interface.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}
