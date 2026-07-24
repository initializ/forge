package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/authgate"
)

// A freshly-issued state consumes once and carries its binding.
func TestStateBinder_IssueConsume(t *testing.T) {
	b := newStateBinder(time.Hour)
	state, err := b.Issue("alice@corp.com", "atl", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := b.Consume(state)
	if !ok {
		t.Fatal("freshly issued state must consume")
	}
	if got.subject != "alice@corp.com" || got.server != "atl" || got.session != "sess-1" {
		t.Fatalf("binding = %+v, want alice/atl/sess-1", got)
	}
}

// Replay: a state is single-use — the second Consume finds nothing.
func TestStateBinder_ReplayRejected(t *testing.T) {
	b := newStateBinder(time.Hour)
	state, _ := b.Issue("alice@corp.com", "atl", "sess-1")
	if _, ok := b.Consume(state); !ok {
		t.Fatal("first consume must succeed")
	}
	if _, ok := b.Consume(state); ok {
		t.Fatal("replayed state must be rejected (single-use)")
	}
}

// Unknown/forged state is rejected.
func TestStateBinder_UnknownRejected(t *testing.T) {
	b := newStateBinder(time.Hour)
	if _, ok := b.Consume("never-issued"); ok {
		t.Fatal("forged state must be rejected")
	}
	if _, ok := b.Consume(""); ok {
		t.Fatal("empty state must be rejected")
	}
}

// Expired state is rejected even though it was validly issued.
func TestStateBinder_ExpiredRejected(t *testing.T) {
	b := newStateBinder(time.Hour)
	now := time.Now()
	b.now = func() time.Time { return now }
	state, _ := b.Issue("alice@corp.com", "atl", "sess-1")
	// Advance past the TTL.
	b.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, ok := b.Consume(state); ok {
		t.Fatal("expired state must be rejected")
	}
}

// Issue opportunistically sweeps expired bindings so abandoned flows don't
// accumulate.
func TestStateBinder_SweepsExpired(t *testing.T) {
	b := newStateBinder(time.Minute)
	now := time.Now()
	b.now = func() time.Time { return now }
	_, _ = b.Issue("alice@corp.com", "atl", "s1")
	_, _ = b.Issue("bob@corp.com", "atl", "s2")
	// Jump past TTL and issue a third — the sweep should drop the first two.
	b.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, _ = b.Issue("carol@corp.com", "atl", "s3")
	b.mu.Lock()
	n := len(b.m)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("after sweep, %d bindings remain, want 1 (only carol's)", n)
	}
}

// fakeCompleter records the code exchange and "stores" a grant.
type fakeCompleter struct {
	mu       sync.Mutex
	calls    int
	lastCode string
	err      error
}

func (f *fakeCompleter) complete(_ context.Context, _, _, code, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastCode = code
	return f.err
}

func newCallback(t *testing.T, binder *stateBinder, engine *authgate.Engine, comp *fakeCompleter) http.HandlerFunc {
	t.Helper()
	return makeMCPCallbackHandler(binder, engine, comp.complete, sessionFromRequest, nil, coreruntime.NewSequenceRegistry())
}

// The happy path: valid state + code → exchange runs → the parked call
// resumes granted.
func TestMCPCallback_HappyPath_ResumesGate(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{}

	// Park a call for alice.
	handle, _, err := engine.Await("alice@corp.com", "atl", authgate.Spec{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	resumed := make(chan authgate.Resolution, 1)
	go func() { r, _ := handle.WaitCtx(context.Background()); resumed <- r }()

	state, _ := binder.Issue("alice@corp.com", "atl", "sess-1")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=authcode123", nil)
	req.Header.Set("X-Forge-Session", "sess-1")
	newCallback(t, binder, engine, comp)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("callback → %d, want 200", rec.Code)
	}
	if comp.calls != 1 || comp.lastCode != "authcode123" {
		t.Fatalf("completer calls=%d code=%q, want 1/authcode123", comp.calls, comp.lastCode)
	}
	select {
	case r := <-resumed:
		if !r.Granted() {
			t.Fatalf("parked call resolved %q, want granted", r.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not resume the parked call")
	}
}

// #366: the callback attributes the completion egress to the still-parked
// invocation — the completer's ctx carries the parked (correlation_id, task_id)
// and shares the invocation's registered seq counter.
func TestMCPCallback_SeedsInvocationContext(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()

	// Park a call carrying a known invocation identity (as Await sets it).
	handle, _, err := engine.Await("alice@corp.com", "atl",
		authgate.Spec{Timeout: time.Hour, TaskID: "task-1", CorrelationID: "corr-1"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = handle.WaitCtx(context.Background()) }()

	// The invocation's seq counter is registered while it's parked.
	reg := coreruntime.NewSequenceRegistry()
	reg.Register("corr-1", "task-1", new(coreruntime.SequenceCounter))

	var gotCtx context.Context
	comp := func(ctx context.Context, _, _, _, _ string) error { gotCtx = ctx; return nil }

	state, _ := binder.Issue("alice@corp.com", "atl", "sess-1")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=c", nil)
	req.Header.Set("X-Forge-Session", "sess-1")
	makeMCPCallbackHandler(binder, engine, comp, sessionFromRequest, nil, reg)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("callback → %d, want 200", rec.Code)
	}
	if gotCtx == nil {
		t.Fatal("completer was not called")
	}
	if got := coreruntime.CorrelationIDFromContext(gotCtx); got != "corr-1" {
		t.Errorf("completion ctx correlation = %q, want corr-1 (attributed to the parked invocation)", got)
	}
	if got := coreruntime.TaskIDFromContext(gotCtx); got != "task-1" {
		t.Errorf("completion ctx task = %q, want task-1", got)
	}
	if n := coreruntime.NextSequence(gotCtx); n != 1 {
		t.Errorf("completion ctx seq = %d, want 1 (shares the registered invocation counter)", n)
	}
}

// Cross-session: a valid state used from a DIFFERENT session is rejected,
// the code is NOT exchanged, and no gate resumes.
func TestMCPCallback_CrossSessionRejected(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{}
	state, _ := binder.Issue("alice@corp.com", "atl", "sess-INITIATED")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=x", nil)
	req.Header.Set("X-Forge-Session", "sess-ATTACKER")
	newCallback(t, binder, engine, comp)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-session callback → %d, want 400", rec.Code)
	}
	if comp.calls != 0 {
		t.Fatal("a cross-session callback must NOT exchange the code")
	}
}

// An empty bound session is rejected fail-closed — the network-exposed
// callback must never downgrade to single-use+expiry alone (finding 2).
func TestMCPCallback_EmptySessionRejected(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{}
	// Issue with an empty session (a config/Issue-side bug on a network-
	// exposed flow) — even a request that also presents no session must fail.
	state, _ := binder.Issue("alice@corp.com", "atl", "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=x", nil)
	// No X-Forge-Session / cookie either.
	newCallback(t, binder, engine, comp)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-session binding → %d, want 400 (fail-closed)", rec.Code)
	}
	if comp.calls != 0 {
		t.Fatal("empty-session callback must NOT exchange the code")
	}
}

// A request that presents no session against a session-bound state is
// rejected (the mandatory cross-session guard, missing-side).
func TestMCPCallback_MissingRequestSessionRejected(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{}
	state, _ := binder.Issue("alice@corp.com", "atl", "sess-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=x", nil)
	// Deliberately no session header/cookie on the request.
	newCallback(t, binder, engine, comp)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing request session → %d, want 400", rec.Code)
	}
	if comp.calls != 0 {
		t.Fatal("a sessionless request must NOT exchange the code")
	}
}

// A replayed callback (state already consumed) is rejected without a second
// exchange.
func TestMCPCallback_ReplayRejected(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{}
	state, _ := binder.Issue("alice@corp.com", "atl", "sess-1")

	mk := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=x", nil)
		req.Header.Set("X-Forge-Session", "sess-1")
		newCallback(t, binder, engine, comp)(rec, req)
		return rec
	}
	if code := mk().Code; code != http.StatusOK {
		t.Fatalf("first callback → %d, want 200", code)
	}
	if code := mk().Code; code != http.StatusBadRequest {
		t.Fatalf("replayed callback → %d, want 400", code)
	}
	if comp.calls != 1 {
		t.Fatalf("code exchanged %d times, want exactly 1 (replay must not re-exchange)", comp.calls)
	}
}

// Missing params are a 400.
func TestMCPCallback_MissingParams(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{}
	h := newCallback(t, binder, engine, comp)
	for _, q := range []string{"?state=x", "?code=y", ""} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/mcp/oauth/callback"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q → %d, want 400", q, rec.Code)
		}
	}
}

// A completer failure (bad code / IdP down) surfaces as 502 and does NOT
// resume the gate — never resume before the grant exists.
func TestMCPCallback_ExchangeFailure_DoesNotResume(t *testing.T) {
	binder := newStateBinder(time.Hour)
	engine := authgate.New()
	comp := &fakeCompleter{err: context.DeadlineExceeded}

	handle, _, _ := engine.Await("alice@corp.com", "atl", authgate.Spec{Timeout: time.Hour})
	resumed := make(chan struct{}, 1)
	go func() { _, _ = handle.WaitCtx(context.Background()); resumed <- struct{}{} }()

	state, _ := binder.Issue("alice@corp.com", "atl", "sess-1")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/mcp/oauth/callback?state="+state+"&code=x", nil)
	req.Header.Set("X-Forge-Session", "sess-1")
	newCallback(t, binder, engine, comp)(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("exchange failure → %d, want 502", rec.Code)
	}
	select {
	case <-resumed:
		t.Fatal("gate must NOT resume when the token exchange failed")
	case <-time.After(150 * time.Millisecond):
		// still parked — correct.
	}
}
