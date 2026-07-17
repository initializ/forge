// Package authgate implements the AARM R10 auth-required gate — the
// pause-and-resume primitive behind delegated MCP consent (#330).
//
// It is the sibling of deferpolicy (R4c): where DEFER parks the executor
// until a human *approves an action*, authgate parks it until a user
// *completes an OAuth consent* and the platform holds a grant. Both share
// the same shape — a goroutine blocks on a handle, an out-of-band signal
// resolves it, a timeout auto-fails — but the semantics differ enough to
// justify a distinct engine:
//
//   - Keyed by {subject, server}, NOT taskID. A grant is per user per
//     server; once alice consents to "atl", EVERY parked call of hers to
//     "atl" resumes — across tasks/sessions. So one gate fans out to many
//     waiters, and one Resolve wakes them all. (deferpolicy is one waiter
//     per taskID.)
//   - The resolution is binary — granted or timed-out. There is no
//     approver/reject: the "approver" is the OAuth flow itself, and the
//     signal is "a grant now exists," delivered by the platform's consent
//     callback or a bounded re-poll of the delegated resolver.
//
// The gate never mints a token and never sees one: it only unblocks the
// executor so it can re-resolve through the normal delegated path, which
// now succeeds because the grant exists (delegation follows authorization,
// design-tool-registry.md §18.5). Token custody stays with the resolver.
//
// In-process only, like deferpolicy — a restart abandons pending gates
// (each blocked goroutine's stack disappears). The Engine is the seam for
// a future persistent implementation.
package authgate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// defaultTimeout bounds how long a call parks awaiting consent before it
// auto-fails. Consent is a human-in-the-loop step (open a link, approve in
// an IdP), so the window is generous relative to deferpolicy's action
// approvals — but still finite so a never-answered prompt can't wedge a
// tool call forever.
const defaultTimeout = 10 * time.Minute

// Decision is how a parked gate resolved.
type Decision string

const (
	// DecisionGranted: the user consented and the platform now holds a
	// grant — the executor should re-resolve and proceed.
	DecisionGranted Decision = "granted"
	// DecisionTimeout: no consent arrived within the window — the call
	// fails, same as the pre-gate ErrNoToken behavior.
	DecisionTimeout Decision = "timeout"
	// DecisionCanceled: every waiter abandoned the gate (all contexts
	// cancelled) before consent — the gate is torn down with nothing left
	// to resume.
	DecisionCanceled Decision = "canceled"
)

// Resolution is the outcome delivered to every waiter on a gate.
type Resolution struct {
	Decision Decision
	At       time.Time
}

// Granted reports whether the resolution means "proceed."
func (r Resolution) Granted() bool { return r.Decision == DecisionGranted }

// Spec carries the delivery/audit context for a gate. None of these fields
// are part of the dedup key — the key is {subject, server} — they travel
// with the FIRST waiter so the consent prompt can be addressed and audited.
type Spec struct {
	// Timeout overrides defaultTimeout when > 0.
	Timeout time.Duration
	// TaskID / Session identify the request that first tripped the gate,
	// for audit and for routing the consent prompt back to the right
	// conversation. Later joiners' task/session are intentionally dropped —
	// one consent serves them all.
	TaskID  string
	Session string
}

// Handle is a pending gate. Executors obtain one from Engine.Await and
// block on WaitCtx; the resume signal (or the timeout) resolves it,
// broadcasting the Resolution to every waiter at once via a closed channel.
//
// A Handle is shared: concurrent Await calls for the same {subject, server}
// return the SAME *Handle. It is opaque — the engine owns its lifecycle.
type Handle struct {
	eng      *Engine
	key      string
	subject  string
	server   string
	spec     Spec
	deadline time.Time

	// done is closed exactly once, by whichever of Resolve / timeout / the
	// last-waiter-leaves path wins the resolved Once. Closing (not sending)
	// is the fan-out: every WaitCtx selecting on it wakes together.
	done     chan struct{}
	res      Resolution // written before done is closed; read after
	resolved sync.Once

	// waiters counts live WaitCtx callers, guarded by Engine.mu. When it
	// drops to zero before consent, the gate is torn down (DecisionCanceled)
	// so an abandoned prompt doesn't idle to its full timeout.
	waiters int
}

// Subject returns the consenting user the gate is keyed to.
func (h *Handle) Subject() string { return h.subject }

// Server returns the MCP server name the grant is for.
func (h *Handle) Server() string { return h.server }

// Spec returns the delivery/audit context captured at first Await.
func (h *Handle) Spec() Spec { return h.spec }

// Deadline returns the absolute time the gate auto-times-out.
func (h *Handle) Deadline() time.Time { return h.deadline }

// WaitCtx blocks until the gate resolves (consent granted, timeout, or
// cancellation) or ctx is cancelled. On ctx cancellation it detaches this
// waiter — and if it was the last one holding the gate open, tears the gate
// down (DecisionCanceled) so an abandoned prompt doesn't linger to its
// timeout. Returns (Resolution{}, ctx.Err()) on cancellation.
func (h *Handle) WaitCtx(ctx context.Context) (Resolution, error) {
	select {
	case <-h.done:
		return h.res, nil
	case <-ctx.Done():
		h.eng.leave(h.key)
		return Resolution{}, ctx.Err()
	}
}

// Engine coordinates pending auth-required gates across the runtime.
// Thread-safe. Every gate is single-flighted per {subject, server}: the
// first Await creates it and is told to deliver the consent prompt; later
// Awaits attach silently.
type Engine struct {
	mu      sync.Mutex
	pending map[string]*Handle
	timers  map[string]*time.Timer
	now     func() time.Time
}

// New constructs an empty Engine.
func New() *Engine {
	return &Engine{
		pending: make(map[string]*Handle),
		timers:  make(map[string]*time.Timer),
		now:     time.Now,
	}
}

// gateKey composes the dedup key. The NUL separator can't appear in an
// email or a server name, so distinct (subject, server) pairs can't collide.
func gateKey(subject, server string) string { return subject + "\x00" + server }

// Await returns the pending gate for {subject, server}, creating it if none
// exists. The bool is true only for the caller that CREATED the gate — that
// caller (and only that caller) should deliver the consent prompt; joiners
// get false and must not re-deliver, or one user would get N prompts for N
// concurrent calls.
//
// subject and server are required. A blank subject means no requesting user
// is in context, which is a caller bug (a gate can't be addressed to nobody).
func (e *Engine) Await(subject, server string, spec Spec) (*Handle, bool, error) {
	if subject == "" {
		return nil, false, errors.New("authgate: subject is required")
	}
	if server == "" {
		return nil, false, errors.New("authgate: server is required")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = defaultTimeout
	}
	key := gateKey(subject, server)

	e.mu.Lock()
	defer e.mu.Unlock()
	if h, ok := e.pending[key]; ok {
		h.waiters++
		return h, false, nil // join the in-flight gate; don't re-prompt
	}
	h := &Handle{
		eng:      e,
		key:      key,
		subject:  subject,
		server:   server,
		spec:     spec,
		deadline: e.now().Add(spec.Timeout),
		done:     make(chan struct{}),
		waiters:  1,
	}
	e.pending[key] = h
	e.timers[key] = time.AfterFunc(spec.Timeout, func() { e.timeout(key) })
	return h, true, nil
}

// Resolve wakes every waiter on {subject, server} with the given decision
// (normally DecisionGranted, from a consent callback). Idempotent — a second
// Resolve, or a Resolve racing the timeout, is a no-op guarded by the gate's
// sync.Once. Returns an error only when no gate is pending (404-like).
func (e *Engine) Resolve(subject, server string, d Decision) error {
	key := gateKey(subject, server)
	e.mu.Lock()
	h, ok := e.pending[key]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("authgate: no pending gate for subject %q server %q", subject, server)
	}
	e.clear(key)
	e.mu.Unlock()

	h.finish(d, e.now())
	return nil
}

// Peek reports whether a gate is pending for {subject, server}. Used by the
// resume endpoint to 404 signals that target no parked call.
func (e *Engine) Peek(subject, server string) (*Handle, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, ok := e.pending[gateKey(subject, server)]
	return h, ok
}

// Pending returns the count of currently-parked gates. For health checks
// and startup logs.
func (e *Engine) Pending() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}

// timeout fires when a gate's window closes with no consent.
func (e *Engine) timeout(key string) {
	e.mu.Lock()
	h, ok := e.pending[key]
	if !ok {
		e.mu.Unlock() // Resolve or leave got here first.
		return
	}
	e.clear(key)
	e.mu.Unlock()
	h.finish(DecisionTimeout, e.now())
}

// leave is called by a waiter whose ctx was cancelled. It decrements the
// gate's live-waiter count; when the last waiter departs before consent, the
// gate is cleared and resolved DecisionCanceled so an abandoned prompt is not
// left idling to its full timeout. If the gate was already resolved (the
// common race: Resolve closed done, then a ctx also fired) it's gone from the
// map and this is a no-op.
func (e *Engine) leave(key string) {
	e.mu.Lock()
	h, ok := e.pending[key]
	if !ok {
		e.mu.Unlock() // already resolved/cleared — nothing to release.
		return
	}
	h.waiters--
	if h.waiters > 0 {
		e.mu.Unlock() // siblings still parked — keep the gate alive.
		return
	}
	e.clear(key)
	e.mu.Unlock()
	h.finish(DecisionCanceled, e.now())
}

// clear removes a gate + its timer under e.mu. Caller holds the lock.
func (e *Engine) clear(key string) {
	if t, ok := e.timers[key]; ok {
		t.Stop()
		delete(e.timers, key)
	}
	delete(e.pending, key)
}

// finish resolves the gate exactly once, publishing res before closing done
// so every WaitCtx observes a fully-written Resolution.
func (h *Handle) finish(d Decision, at time.Time) {
	h.resolved.Do(func() {
		h.res = Resolution{Decision: d, At: at}
		close(h.done)
	})
}
