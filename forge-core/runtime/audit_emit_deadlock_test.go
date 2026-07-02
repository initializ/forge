package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// failingSink always returns a non-nil error from Write. Used to
// exercise the error path in Emit that calls logSinkErrorOnce.
type failingSink struct {
	callCount atomic.Int64
	stats     sinkStats
}

func (f *failingSink) Write(_ context.Context, _ []byte) error {
	f.callCount.Add(1)
	return errors.New("simulated sink failure")
}
func (f *failingSink) Close(_ context.Context) error { return nil }
func (f *failingSink) Name() string                  { return "failing" }
func (f *failingSink) Stats() map[string]int64       { return f.stats.snapshot() }

// captureLogger records Error calls so the test can assert an
// operator-visible warning fired on sink failure.
type captureLogger struct {
	mu     sync.Mutex
	errors []string // Error() and Warn() both funnel here for the test
}

func (l *captureLogger) Info(string, map[string]any)  {}
func (l *captureLogger) Debug(string, map[string]any) {}
func (l *captureLogger) Warn(msg string, _ map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}
func (l *captureLogger) Error(msg string, _ map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}

// TestEmit_NoDeadlockOnSinkErrorWithOpsLog is a regression test for
// the review of PR #220 (reviewer initializ-mk): Emit used to acquire
// a.mu at the top and hold it across sink.Write, so a failing sink
// triggered logSinkErrorOnce, which tries to re-Lock a.mu → self
// -deadlock (sync.Mutex is non-reentrant).
//
// The test wires a failing sink AND an ops logger, then fires N
// concurrent Emits. With the deadlock present, the first sink error
// would hang forever holding a.mu; every subsequent Emit would then
// also block on a.mu.Lock and the test would hit its timeout. With
// the fix (release the lock before sink.Write / logSinkErrorOnce),
// all Emits complete.
func TestEmit_NoDeadlockOnSinkErrorWithOpsLog(t *testing.T) {
	sink := &failingSink{}
	logger := NewAuditLogger(nil)
	// Replace the default writerSink with our failing sink.
	logger.sinks = []Sink{sink}
	opsLog := &captureLogger{}
	logger.SetOpsLogger(opsLog)

	const N = 32
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(N)
		for range N {
			go func() {
				defer wg.Done()
				logger.Emit(AuditEvent{Event: "session_start"})
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK — all Emits returned.
	case <-time.After(5 * time.Second):
		t.Fatalf("Emit deadlock: %d/%d sink.Write calls completed", sink.callCount.Load(), N)
	}

	if sink.callCount.Load() != N {
		t.Errorf("sink.Write fired %d times, want %d", sink.callCount.Load(), N)
	}
	// logSinkErrorOnce dedupes per sink lifetime — expect exactly 1
	// Error line, not N.
	opsLog.mu.Lock()
	defer opsLog.mu.Unlock()
	if len(opsLog.errors) != 1 {
		t.Errorf("expected 1 dedup'd ops-log Error, got %d", len(opsLog.errors))
	}
}

// TestEmit_SigningFailureIsLogged pins the non-blocking-comment fix:
// pre-review, a signer that failed at canonicalize dropped the event
// silently. Post-fix, an Error line hits the ops log so operators can
// see signing failures.
func TestEmit_SigningFailureIsLogged(t *testing.T) {
	logger := NewAuditLogger(nil)
	opsLog := &captureLogger{}
	logger.SetOpsLogger(opsLog)
	logger.SetSigner(&AuditSigner{}) // zero-value signer: Sign on it panics? — we need a variant that fails at canonicalize

	// canonicalBytesForSigning delegates to json.Marshal on the event
	// after blanking Sig. json.Marshal on our AuditEvent shape doesn't
	// fail for normal fields, so to exercise the sigErr branch we'd
	// need a hostile Fields value. Use a channel — encoding/json
	// returns an "unsupported type" error for channels.
	badEvent := AuditEvent{
		Event:  "session_start",
		Fields: map[string]any{"chan": make(chan int)},
	}
	logger.Emit(badEvent)

	opsLog.mu.Lock()
	defer opsLog.mu.Unlock()
	if len(opsLog.errors) == 0 {
		t.Fatal("expected an ops-log error when signing fails")
	}
	found := false
	for _, e := range opsLog.errors {
		if e == "audit signing failed; event dropped" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("wrong error message logged: %v", opsLog.errors)
	}
}
