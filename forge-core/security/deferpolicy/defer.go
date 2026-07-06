// Package defer implements governance R4c — the DEFER
// authorization decision.
//
// Where DENY refuses an action outright and STEP_UP asks the caller
// to re-authenticate, DEFER says "I cannot decide this autonomously —
// pause the executor, hand this to an external authority (a human
// on-call, an approvals system), and resume when a decision
// arrives." The primitive R4c introduces is executor **pause-and-
// resume**: a goroutine blocks on a decision channel, the A2A task
// status flips to `deferred` in the store so parallel callers see
// it, and the goroutine unblocks when either the decisions endpoint
// resolves the deferral or the configured timeout auto-denies it.
//
// This is in-process only — a Forge restart abandons any pending
// deferrals (each blocked goroutine's stack disappears). For
// deployments that need cross-restart persistence, the Engine
// interface is intentionally the seam: a future "persistent" impl
// can serialize pending deferrals to disk / DB and rehydrate on
// startup. See docs/security/defer-decisions.md.
package deferpolicy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Decision is the resolution kind. Uppercase enum values map 1:1 to
// what the operator/approver sends on `POST /tasks/{id}/decisions`.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
	DecisionTimeout Decision = "timeout" // set only by the internal timer
)

// Resolution is the decision + metadata that unblocks a pending
// deferral. Emitted onto Handle.wait, consumed by the executor
// goroutine when it resumes.
type Resolution struct {
	Decision Decision
	Approver string // free-form; typically a user id or email
	Note     string // optional operator-supplied justification
	At       time.Time
}

// Handle represents a pending deferral. The executor obtains one
// via Engine.Await and blocks on WaitCtx. The decisions endpoint
// obtains one via Engine.LookUp and calls Resolve.
//
// Handle is intentionally opaque to callers — the engine owns its
// lifecycle including cleanup. Never construct a Handle outside
// the engine.
type Handle struct {
	taskID   string
	tool     string
	spec     Spec
	deadline time.Time
	// wait is buffered(1) so a resolver that reaches it before the
	// waiter never blocks. Cleanup is done by the engine after the
	// waiter drains it.
	wait     chan Resolution
	resolved sync.Once
}

// Spec is the deferral parameters carried from a policy hook into
// the engine. Deliberately a value type (not a pointer) so a caller
// can safely stash a copy for audit.
type Spec struct {
	To                 string
	Timeout            time.Duration
	ContextForApprover string
}

// TaskID returns the task the deferral is for. Used by the runner
// to update task status.
func (h *Handle) TaskID() string { return h.taskID }

// Tool returns the tool name being deferred.
func (h *Handle) Tool() string { return h.tool }

// Spec returns the deferral parameters. Audit event source.
func (h *Handle) Spec() Spec { return h.spec }

// Deadline returns the absolute time the timeout auto-DENYs.
func (h *Handle) Deadline() time.Time { return h.deadline }

// WaitCtx blocks until either a Resolution arrives (via
// engine.Resolve) or the ctx is cancelled. Returns the Resolution
// on success; on ctx cancellation returns (Resolution{}, ctx.Err()).
//
// Called by the executor goroutine inside the BeforeToolExec hook.
func (h *Handle) WaitCtx(ctx context.Context) (Resolution, error) {
	select {
	case r := <-h.wait:
		return r, nil
	case <-ctx.Done():
		return Resolution{}, ctx.Err()
	}
}

// Engine coordinates pending deferrals across the runtime.
//
// Thread-safe. All accessors take the internal mutex; the resolution
// channels are buffered(1) so a resolver never blocks on a slow
// waiter (the waiter drains the channel before completing).
type Engine struct {
	mu      sync.Mutex
	pending map[string]*Handle
	now     func() time.Time
	// timers keyed by taskID; kept so the engine can cancel a
	// pending timeout when a decision arrives before it fires.
	timers map[string]*time.Timer
}

// New constructs an empty Engine.
func New() *Engine {
	return &Engine{
		pending: make(map[string]*Handle),
		timers:  make(map[string]*time.Timer),
		now:     time.Now,
	}
}

// Register creates a new pending deferral for the given task and
// starts the timeout timer. Returns an error when a deferral is
// already pending for this task ID (the executor shouldn't call
// Register twice for the same task without an intervening resolve).
//
// On timeout fire, the engine emits a Resolution{Decision:
// DecisionTimeout} onto the handle's wait channel. Callers of
// WaitCtx observe the same channel as an explicit resolve.
func (e *Engine) Register(taskID, tool string, spec Spec) (*Handle, error) {
	if taskID == "" {
		return nil, errors.New("defer: taskID is required")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 10 * time.Minute
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.pending[taskID]; exists {
		return nil, fmt.Errorf("defer: task %q already has a pending deferral", taskID)
	}
	h := &Handle{
		taskID:   taskID,
		tool:     tool,
		spec:     spec,
		deadline: e.now().Add(spec.Timeout),
		wait:     make(chan Resolution, 1),
	}
	e.pending[taskID] = h

	// Timeout auto-DENY. The AfterFunc runs on a fresh goroutine, so
	// it must lock e.mu to look up the handle. Guard against the
	// race where Resolve already fired: sync.Once on Handle.
	e.timers[taskID] = time.AfterFunc(spec.Timeout, func() {
		e.autoTimeout(taskID)
	})
	return h, nil
}

// Resolve finalizes the deferral for taskID with the given decision.
// Idempotent — a second call for the same task is a no-op (returns
// nil error, no channel send). Returns an error only when no
// deferral is pending for the task (404-like).
//
// The decisions endpoint calls this on a POST arriving from an
// approver.
func (e *Engine) Resolve(taskID string, r Resolution) error {
	e.mu.Lock()
	h, ok := e.pending[taskID]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("defer: no pending deferral for task %q", taskID)
	}
	// Cancel timeout timer while still under the lock — even if the
	// timer already fired, Stop returns false and the timer's
	// autoTimeout will see the sync.Once already consumed.
	if t, hasTimer := e.timers[taskID]; hasTimer {
		t.Stop()
		delete(e.timers, taskID)
	}
	delete(e.pending, taskID)
	e.mu.Unlock()

	if r.At.IsZero() {
		r.At = e.now()
	}
	// resolved.Do guards against a race between Resolve and
	// autoTimeout; whichever gets there first wins.
	h.resolved.Do(func() {
		h.wait <- r
	})
	return nil
}

// Peek returns whether a deferral is pending for taskID. Used by
// the decisions endpoint to reject requests targeting non-deferred
// tasks with a 404.
func (e *Engine) Peek(taskID string) (*Handle, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, ok := e.pending[taskID]
	return h, ok
}

// autoTimeout is invoked by the timer goroutine when a deferral's
// window closes without a decision.
func (e *Engine) autoTimeout(taskID string) {
	e.mu.Lock()
	h, ok := e.pending[taskID]
	if !ok {
		// Resolve got here first; nothing to do.
		e.mu.Unlock()
		return
	}
	delete(e.pending, taskID)
	delete(e.timers, taskID)
	e.mu.Unlock()

	h.resolved.Do(func() {
		h.wait <- Resolution{
			Decision: DecisionTimeout,
			At:       e.now(),
		}
	})
}

// Pending returns the count of currently-pending deferrals. Used by
// runtime health checks and startup logs.
func (e *Engine) Pending() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}
