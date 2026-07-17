package authgate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitResult captures a WaitCtx return for assertions off the waiter goroutine.
type waitResult struct {
	res Resolution
	err error
}

// awaitInBackground parks a waiter and returns a channel that yields its
// WaitCtx result. It blocks until the gate exists so callers can assert
// ordering deterministically.
func awaitInBackground(t *testing.T, e *Engine, subject, server string, spec Spec) (<-chan waitResult, *Handle, bool) {
	t.Helper()
	h, first, err := e.Await(subject, server, spec)
	if err != nil {
		t.Fatalf("Await(%q,%q): %v", subject, server, err)
	}
	ch := make(chan waitResult, 1)
	go func() {
		r, werr := h.WaitCtx(context.Background())
		ch <- waitResult{r, werr}
	}()
	return ch, h, first
}

func TestAwait_RequiresSubjectAndServer(t *testing.T) {
	e := New()
	if _, _, err := e.Await("", "atl", Spec{}); err == nil {
		t.Error("blank subject must error")
	}
	if _, _, err := e.Await("a@x", "", Spec{}); err == nil {
		t.Error("blank server must error")
	}
}

// A granted signal wakes the parked waiter with DecisionGranted.
func TestResolve_Granted_WakesWaiter(t *testing.T) {
	e := New()
	ch, _, first := awaitInBackground(t, e, "alice@corp.com", "atl", Spec{})
	if !first {
		t.Fatal("the creating Await must report first=true")
	}
	if e.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", e.Pending())
	}

	if err := e.Resolve("alice@corp.com", "atl", DecisionGranted); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("WaitCtx err = %v", got.err)
		}
		if !got.res.Granted() {
			t.Fatalf("decision = %q, want granted", got.res.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not wake after Resolve")
	}
	if e.Pending() != 0 {
		t.Fatalf("Pending after Resolve = %d, want 0", e.Pending())
	}
}

// The decisive fan-out property: N concurrent calls for the same
// {subject, server} share ONE gate — only the first is told to deliver the
// prompt — and ONE Resolve wakes them all.
func TestAwait_FanOut_OnePromptManyWaiters(t *testing.T) {
	e := New()
	const n = 8
	var firstCount int
	chans := make([]<-chan waitResult, n)
	for i := range n {
		ch, _, first := awaitInBackground(t, e, "alice@corp.com", "atl", Spec{})
		if first {
			firstCount++
		}
		chans[i] = ch
	}
	if firstCount != 1 {
		t.Fatalf("first=true returned %d times, want exactly 1 (one prompt per user)", firstCount)
	}
	if e.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1 (all share one gate)", e.Pending())
	}

	if err := e.Resolve("alice@corp.com", "atl", DecisionGranted); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for i, ch := range chans {
		select {
		case got := <-ch:
			if !got.res.Granted() {
				t.Fatalf("waiter %d decision = %q, want granted", i, got.res.Decision)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d did not wake — Resolve must fan out to all", i)
		}
	}
}

// Different subjects (or different servers) are independent gates: resolving
// one must not wake the other.
func TestAwait_DistinctKeysAreIndependent(t *testing.T) {
	e := New()
	chAlice, _, _ := awaitInBackground(t, e, "alice@corp.com", "atl", Spec{})
	chBob, _, bobFirst := awaitInBackground(t, e, "bob@corp.com", "atl", Spec{})
	if !bobFirst {
		t.Fatal("bob is a distinct subject → must be first for his own gate")
	}
	if e.Pending() != 2 {
		t.Fatalf("Pending = %d, want 2 distinct gates", e.Pending())
	}

	if err := e.Resolve("alice@corp.com", "atl", DecisionGranted); err != nil {
		t.Fatalf("Resolve(alice): %v", err)
	}
	select {
	case <-chAlice: // good
	case <-time.After(2 * time.Second):
		t.Fatal("alice did not wake")
	}
	select {
	case got := <-chBob:
		t.Fatalf("bob woke on alice's grant — keys must be independent: %+v", got)
	case <-time.After(150 * time.Millisecond):
		// bob still parked — correct.
	}
}

// The timeout auto-fails a gate that never gets consent.
func TestTimeout_AutoFails(t *testing.T) {
	e := New()
	ch, h, _ := awaitInBackground(t, e, "alice@corp.com", "atl", Spec{Timeout: 40 * time.Millisecond})
	if h.Deadline().IsZero() {
		t.Error("deadline must be set")
	}
	select {
	case got := <-ch:
		if got.res.Decision != DecisionTimeout {
			t.Fatalf("decision = %q, want timeout", got.res.Decision)
		}
		if got.res.Granted() {
			t.Fatal("a timed-out gate must not report Granted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not auto-time-out")
	}
	if e.Pending() != 0 {
		t.Fatalf("Pending after timeout = %d, want 0", e.Pending())
	}
}

// Resolve on a subject/server with no parked gate is a 404-like error.
func TestResolve_NoGate_Errors(t *testing.T) {
	e := New()
	if err := e.Resolve("nobody@corp.com", "atl", DecisionGranted); err == nil {
		t.Error("Resolve with no pending gate must error")
	}
}

// Resolve is idempotent and race-safe against the timeout: whichever wins,
// the waiter sees exactly one resolution and a second Resolve is a no-op
// error (gate already cleared).
func TestResolve_Idempotent(t *testing.T) {
	e := New()
	ch, _, _ := awaitInBackground(t, e, "alice@corp.com", "atl", Spec{})
	if err := e.Resolve("alice@corp.com", "atl", DecisionGranted); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	<-ch
	if err := e.Resolve("alice@corp.com", "atl", DecisionGranted); err == nil {
		t.Error("second Resolve must error — gate already cleared")
	}
}

// When the last waiter's ctx cancels before consent, the gate is torn down
// (DecisionCanceled) rather than idling to its timeout.
func TestWaitCtx_LastWaiterCancel_TearsDown(t *testing.T) {
	e := New()
	h, first, err := e.Await("alice@corp.com", "atl", Spec{Timeout: time.Hour})
	if err != nil || !first {
		t.Fatalf("Await: first=%v err=%v", first, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan waitResult, 1)
	go func() {
		r, werr := h.WaitCtx(ctx)
		ch <- waitResult{r, werr}
	}()
	// Let the waiter park, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case got := <-ch:
		if got.err == nil {
			t.Fatalf("WaitCtx must return ctx.Err() on cancel, got res=%+v", got.res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitCtx did not return after cancel")
	}
	// The gate must be gone — not left holding a live timer to its hour.
	if e.Pending() != 0 {
		t.Fatalf("Pending after last-waiter cancel = %d, want 0 (gate torn down)", e.Pending())
	}
}

// One cancelled waiter among several must NOT tear down the shared gate —
// its siblings stay parked and still resume on consent.
func TestWaitCtx_OneOfManyCancel_KeepsGate(t *testing.T) {
	e := New()
	// Sibling that stays parked on a background ctx.
	survivor, _, err := e.Await("alice@corp.com", "atl", Spec{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	survCh := make(chan waitResult, 1)
	go func() {
		r, werr := survivor.WaitCtx(context.Background())
		survCh <- waitResult{r, werr}
	}()

	// Second waiter on a cancellable ctx (joins the same gate).
	joiner, first, err := e.Await("alice@corp.com", "atl", Spec{})
	if err != nil || first {
		t.Fatalf("joiner must not be first: first=%v err=%v", first, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	joinCh := make(chan waitResult, 1)
	go func() {
		r, werr := joiner.WaitCtx(ctx)
		joinCh <- waitResult{r, werr}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-joinCh // joiner returns cancelled

	// Gate must still be pending for the survivor.
	if e.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1 (survivor still parked)", e.Pending())
	}
	if err := e.Resolve("alice@corp.com", "atl", DecisionGranted); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case got := <-survCh:
		if !got.res.Granted() {
			t.Fatalf("survivor decision = %q, want granted", got.res.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("survivor did not resume after a sibling cancelled")
	}
}

// Peek reflects pending state for the resume endpoint's 404 decision.
func TestPeek(t *testing.T) {
	e := New()
	if _, ok := e.Peek("alice@corp.com", "atl"); ok {
		t.Error("Peek must be false before any Await")
	}
	ch, _, _ := awaitInBackground(t, e, "alice@corp.com", "atl", Spec{})
	if h, ok := e.Peek("alice@corp.com", "atl"); !ok || h.Subject() != "alice@corp.com" {
		t.Errorf("Peek after Await = (%v, %v)", h, ok)
	}
	_ = e.Resolve("alice@corp.com", "atl", DecisionGranted)
	<-ch
	if _, ok := e.Peek("alice@corp.com", "atl"); ok {
		t.Error("Peek must be false after resolve")
	}
}

// Race sanity: many concurrent Awaits + a Resolve under -race must not
// deadlock or double-close.
func TestConcurrentAwaitResolve_Race(t *testing.T) {
	e := New()
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			h, _, err := e.Await("alice@corp.com", "atl", Spec{Timeout: time.Hour})
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = h.WaitCtx(ctx)
		})
	}
	time.Sleep(30 * time.Millisecond)
	_ = e.Resolve("alice@corp.com", "atl", DecisionGranted)
	wg.Wait()
}
