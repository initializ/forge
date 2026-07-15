package deferpolicy

import (
	"context"
	"testing"
	"time"
)

func TestRegister_AllowsSingleInFlightPerTask(t *testing.T) {
	e := New()
	_, err := e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, err = e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("duplicate Register for same task should error")
	}
}

func TestRegister_DifferentTasksCoexist(t *testing.T) {
	e := New()
	if _, err := e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("task-1: %v", err)
	}
	if _, err := e.Register("task-2", "http_request", Spec{Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("task-2: %v", err)
	}
	if got := e.Pending(); got != 2 {
		t.Errorf("Pending: got %d want 2", got)
	}
}

// TestResolve_ApproveUnblocksWaiter is the happy path: a goroutine
// blocks on WaitCtx, an approver calls Resolve, the waiter wakes
// with the approve decision.
func TestResolve_ApproveUnblocksWaiter(t *testing.T) {
	e := New()
	h, _ := e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second})

	done := make(chan Resolution, 1)
	go func() {
		r, err := h.WaitCtx(context.Background())
		if err != nil {
			t.Errorf("WaitCtx: %v", err)
		}
		done <- r
	}()

	// Give the waiter a moment to enter select.
	time.Sleep(20 * time.Millisecond)

	if err := e.Resolve("task-1", Resolution{Decision: DecisionApprove, Approver: "alice"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case r := <-done:
		if r.Decision != DecisionApprove {
			t.Errorf("decision: got %s want approve", r.Decision)
		}
		if r.Approver != "alice" {
			t.Errorf("approver: got %q", r.Approver)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("waiter did not wake within 1s")
	}

	// Pending count should be zero after resolve.
	if got := e.Pending(); got != 0 {
		t.Errorf("Pending after resolve: got %d want 0", got)
	}
}

// TestResolve_RejectUnblocksWithReject — reject arrives, waiter
// sees the reject decision.
func TestResolve_RejectUnblocksWithReject(t *testing.T) {
	e := New()
	h, _ := e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second})

	done := make(chan Resolution, 1)
	go func() {
		r, _ := h.WaitCtx(context.Background())
		done <- r
	}()
	time.Sleep(20 * time.Millisecond)

	_ = e.Resolve("task-1", Resolution{Decision: DecisionReject, Approver: "bob", Note: "too risky"})

	r := <-done
	if r.Decision != DecisionReject {
		t.Errorf("decision: got %s want reject", r.Decision)
	}
	if r.Note != "too risky" {
		t.Errorf("note: got %q", r.Note)
	}
}

// TestTimeout_UnblocksWithTimeoutDecision is the auto-DENY path:
// the deferral's timeout fires, the waiter wakes with
// DecisionTimeout, Pending returns 0.
func TestTimeout_UnblocksWithTimeoutDecision(t *testing.T) {
	e := New()
	h, _ := e.Register("task-1", "cli_execute", Spec{Timeout: 30 * time.Millisecond})

	done := make(chan Resolution, 1)
	go func() {
		r, _ := h.WaitCtx(context.Background())
		done <- r
	}()

	select {
	case r := <-done:
		if r.Decision != DecisionTimeout {
			t.Errorf("expected timeout decision, got %s", r.Decision)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout did not fire within 500ms")
	}
	if got := e.Pending(); got != 0 {
		t.Errorf("Pending after timeout: got %d want 0", got)
	}
}

// TestResolveBeforeTimeout — if the resolve arrives before the
// timeout, the timer is cancelled and doesn't send a spurious
// second value.
func TestResolveBeforeTimeout(t *testing.T) {
	e := New()
	h, _ := e.Register("task-1", "cli_execute", Spec{Timeout: 200 * time.Millisecond})

	done := make(chan Resolution, 1)
	go func() {
		r, _ := h.WaitCtx(context.Background())
		done <- r
	}()
	time.Sleep(20 * time.Millisecond)

	if err := e.Resolve("task-1", Resolution{Decision: DecisionApprove}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r := <-done
	if r.Decision != DecisionApprove {
		t.Fatalf("expected approve, got %s", r.Decision)
	}
	// Wait past the original timeout deadline; the timer must NOT
	// send a second value.
	time.Sleep(300 * time.Millisecond)
	select {
	case extra := <-done:
		t.Errorf("timer fired after resolve: got %+v", extra)
	default:
		// good — no spurious second value
	}
}

// TestTimeoutBeforeResolve — the timeout fires first; a subsequent
// Resolve call finds no pending deferral (404-shaped error).
func TestTimeoutBeforeResolve(t *testing.T) {
	e := New()
	_, _ = e.Register("task-1", "cli_execute", Spec{Timeout: 20 * time.Millisecond})

	// Sleep past the timeout so autoTimeout runs.
	time.Sleep(80 * time.Millisecond)

	err := e.Resolve("task-1", Resolution{Decision: DecisionApprove})
	if err == nil {
		t.Fatal("expected error resolving after timeout consumed the deferral")
	}
}

// TestResolveUnknownTask — 404 case.
func TestResolveUnknownTask(t *testing.T) {
	e := New()
	err := e.Resolve("nonexistent", Resolution{Decision: DecisionApprove})
	if err == nil {
		t.Fatal("expected error resolving unknown task")
	}
}

// TestWaitCtx_CancelledContext — the waiter's ctx is cancelled
// (e.g. HTTP request abandoned by client); WaitCtx returns the
// ctx error, and the pending state stays until the timeout or an
// explicit Resolve arrives.
func TestWaitCtx_CancelledContext(t *testing.T) {
	e := New()
	h, _ := e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.WaitCtx(ctx)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	err := <-done
	if err == nil {
		t.Fatal("expected ctx-cancelled error")
	}
	// Pending should still be 1 — the deferral survives the
	// caller's disconnect; a subsequent Resolve or the timeout will
	// finalize it.
	if got := e.Pending(); got != 1 {
		t.Errorf("Pending after ctx cancel: got %d want 1", got)
	}
	// Cleanup: resolve to release the timer goroutine.
	_ = e.Resolve("task-1", Resolution{Decision: DecisionTimeout})
}

// TestPeek — the decisions endpoint uses Peek to decide 404 vs 200.
func TestPeek(t *testing.T) {
	e := New()
	if _, ok := e.Peek("task-1"); ok {
		t.Error("Peek on unknown task should return false")
	}
	_, _ = e.Register("task-1", "cli_execute", Spec{Timeout: 5 * time.Second})
	h, ok := e.Peek("task-1")
	if !ok {
		t.Fatal("Peek should return true for registered task")
	}
	if h.Tool() != "cli_execute" {
		t.Errorf("Tool: got %q", h.Tool())
	}
}

// TestDefaultTimeout — Spec.Timeout == 0 applies the 10-minute
// default. We can't wait 10 minutes; check the handle's deadline
// is far in the future.
func TestDefaultTimeout(t *testing.T) {
	e := New()
	h, _ := e.Register("task-1", "cli_execute", Spec{}) // no timeout set

	if h.Deadline().Sub(e.now()) < 9*time.Minute {
		t.Errorf("default timeout should be ~10m, got deadline %s (now %s)",
			h.Deadline(), e.now())
	}
}

// TestHandle_IsApprover pins the #313 allowlist membership check: empty
// allowlist authorizes anyone; a non-empty one requires a listed email
// (case-insensitive) and fails closed on an empty email.
func TestHandle_IsApprover(t *testing.T) {
	e := New()

	noList, _ := e.Register("t-none", "cli_execute", Spec{Timeout: time.Minute})
	if !noList.IsApprover("") || !noList.IsApprover("anyone@x.com") {
		t.Error("empty allowlist must authorize anyone")
	}

	list, _ := e.Register("t-list", "cli_execute", Spec{Timeout: time.Minute, Approvers: []string{"alice@corp.com"}})
	if !list.IsApprover("alice@corp.com") || !list.IsApprover("  ALICE@Corp.com  ") {
		t.Error("listed email (any case/whitespace) must be authorized")
	}
	if list.IsApprover("mallory@evil.com") {
		t.Error("non-listed email must be refused")
	}
	if list.IsApprover("") {
		t.Error("empty email against a non-empty allowlist must fail closed")
	}
}
