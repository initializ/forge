package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Regression tests for issue #88 / FWS-4 — the cancellation registry +
// reason propagation via context.Cause. The registry is the bridge
// between the tasks/cancel handler and the in-flight executeTask
// goroutine; the reason rides on the cancellation cause so the
// goroutine can read it after observing ctx.Done().

func TestCancellationRegistry_RegisterCancelReleaseLifecycle(t *testing.T) {
	reg := NewCancellationRegistry()
	if reg.Len() != 0 {
		t.Fatalf("fresh registry should be empty, got %d", reg.Len())
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	release := reg.Register("task-1", cancel)
	if reg.Len() != 1 {
		t.Errorf("after Register: Len()=%d, want 1", reg.Len())
	}

	if !reg.Cancel("task-1", CancelReasonExternalSignal) {
		t.Errorf("Cancel on registered task should return true")
	}
	if ctx.Err() == nil {
		t.Errorf("ctx should be cancelled after registry.Cancel")
	}

	release()
	if reg.Len() != 0 {
		t.Errorf("after release: Len()=%d, want 0", reg.Len())
	}
}

func TestCancellationRegistry_CancelUnknownTaskIsIdempotent(t *testing.T) {
	// Cancel-after-complete must be a no-op (returns false) so the
	// orchestrator can issue cancels optimistically without races
	// against concurrent completion.
	reg := NewCancellationRegistry()
	if reg.Cancel("never-existed", CancelReasonTimeout) {
		t.Errorf("Cancel on unknown task should return false")
	}
}

func TestCancellationReasonFromCause_RoundTripWithCancelCause(t *testing.T) {
	// Reason flows from handler → registry → CancelCauseFunc →
	// context.Cause → CancellationReasonFromCause. The test simulates
	// the full chain to lock in the contract the runner depends on.
	reg := NewCancellationRegistry()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	release := reg.Register("t", cancel)
	defer release()

	reg.Cancel("t", CancelReasonCostLimitExceeded)
	<-ctx.Done()
	got := CancellationReasonFromCause(ctx)
	if got != CancelReasonCostLimitExceeded {
		t.Errorf("reason = %q, want %q", got, CancelReasonCostLimitExceeded)
	}
}

func TestCancellationReasonFromCause_NoCtxErrReturnsEmpty(t *testing.T) {
	// Before cancellation, the reason helper must return empty — used
	// by the runner to decide whether to emit invocation_complete vs
	// invocation_cancelled. False positives here would misclassify
	// every clean completion as a cancellation.
	ctx := context.Background()
	if got := CancellationReasonFromCause(ctx); got != "" {
		t.Errorf("non-cancelled ctx should return empty reason, got %q", got)
	}
}

func TestCancellationReasonFromCause_DeadlineExceededMapsToTimeout(t *testing.T) {
	// Parent ctx with a deadline that fires before our cancel must
	// classify as timeout — orchestrators set both their own deadlines
	// AND can cancel us via tasks/cancel. Both are "they pulled the
	// rug," but the reason field must say which.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if got := CancellationReasonFromCause(ctx); got != CancelReasonTimeout {
		t.Errorf("deadline-exceeded ctx should map to %q, got %q", CancelReasonTimeout, got)
	}
}

func TestCancellationReasonFromCause_UntypedCancelDefaultsToExternalSignal(t *testing.T) {
	// Plain context.WithCancel (no cause type) is treated as
	// external_signal — covers internal cancellations (e.g. graceful
	// shutdown) that don't classify their cause.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := CancellationReasonFromCause(ctx); got != CancelReasonExternalSignal {
		t.Errorf("untyped cancel should default to %q, got %q", CancelReasonExternalSignal, got)
	}
}

func TestCancellationReasonFromCause_AllDocumentedReasonsRoundTrip(t *testing.T) {
	// IsValid lists the documented values — every documented value
	// must round-trip through the registry. Catches the case of a
	// new reason added to IsValid but missed in the cause wiring.
	reasons := []CancellationReason{
		CancelReasonWorkflowFailure,
		CancelReasonCostLimitExceeded,
		CancelReasonTimeout,
		CancelReasonExternalSignal,
	}
	for _, want := range reasons {
		t.Run(string(want), func(t *testing.T) {
			reg := NewCancellationRegistry()
			ctx, cancel := context.WithCancelCause(context.Background())
			release := reg.Register("t", cancel)
			defer release()

			reg.Cancel("t", want)
			<-ctx.Done()
			if got := CancellationReasonFromCause(ctx); got != want {
				t.Errorf("reason round-trip: got %q want %q", got, want)
			}
			if !want.IsValid() {
				t.Errorf("documented reason %q failed IsValid", want)
			}
		})
	}
}

func TestCancellationReason_IsValid_RejectsUnknown(t *testing.T) {
	if CancellationReason("rumored").IsValid() {
		t.Errorf("unknown reason should not be IsValid")
	}
	if CancellationReason("").IsValid() {
		t.Errorf("empty reason should not be IsValid")
	}
}

func TestCancellationRegistry_ReleaseAfterOverwrite_DoesNotPopNewer(t *testing.T) {
	// Concurrent retries (or buggy callers) can register the same
	// task ID twice. The older release must NOT pop the newer
	// registration — otherwise the newer invocation becomes
	// uncancellable. Pointer identity on the entry wrapper gives us
	// this guarantee; this test locks in the behavior.
	reg := NewCancellationRegistry()
	_, cancel1 := context.WithCancelCause(context.Background())
	release1 := reg.Register("dup", cancel1)

	_, cancel2 := context.WithCancelCause(context.Background())
	release2 := reg.Register("dup", cancel2) // overwrites entry1

	release1() // stale release — must NOT remove entry2
	if reg.Len() != 1 {
		t.Errorf("stale release should not pop newer entry, got Len=%d", reg.Len())
	}
	if !reg.Cancel("dup", CancelReasonExternalSignal) {
		t.Errorf("newer entry should still be cancellable after stale release")
	}
	release2()
	if reg.Len() != 0 {
		t.Errorf("after correct release: Len()=%d, want 0", reg.Len())
	}
}

func TestCancellationRegistry_ConcurrentRegisterCancelSafe(t *testing.T) {
	// 100 goroutines each register, cancel, and release. No data
	// races, no panics, all tasks end up unregistered.
	reg := NewCancellationRegistry()
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancelCause(context.Background())
			taskID := "task-" + string(rune('A'+i%26)) + "-" + string(rune('0'+i%10))
			release := reg.Register(taskID, cancel)
			reg.Cancel(taskID, CancelReasonExternalSignal)
			<-ctx.Done()
			release()
		}()
	}
	wg.Wait()
	if reg.Len() != 0 {
		t.Errorf("after concurrent register/cancel/release: Len()=%d, want 0", reg.Len())
	}
}

func TestCancelledByOrchestrator_ErrorMessageCarriesReason(t *testing.T) {
	// The sentinel's Error() text is informational; logs and debug
	// dumps should surface the reason so an operator triaging a
	// cancellation can read it without unwrapping the typed error.
	err := &cancelledByOrchestrator{Reason: CancelReasonWorkflowFailure}
	if got := err.Error(); got != "invocation cancelled: workflow_failure" {
		t.Errorf("Error() = %q, want %q", got, "invocation cancelled: workflow_failure")
	}
}

func TestCancelledByOrchestrator_ErrorsAsExtractsViaErrorsAs(t *testing.T) {
	// CancellationReasonFromCause depends on errors.As working
	// against the sentinel. Test it directly so a future Error()
	// signature change doesn't silently break the unwrap.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cancel(&cancelledByOrchestrator{Reason: CancelReasonWorkflowFailure})

	var c *cancelledByOrchestrator
	if !errors.As(context.Cause(ctx), &c) {
		t.Fatalf("errors.As should extract *cancelledByOrchestrator from cause")
	}
	if c.Reason != CancelReasonWorkflowFailure {
		t.Errorf("extracted reason = %q, want %q", c.Reason, CancelReasonWorkflowFailure)
	}
}
