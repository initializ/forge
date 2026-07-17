package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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

func (f *fakeCompleter) complete(_, _, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastCode = code
	return f.err
}

func newCallback(t *testing.T, binder *stateBinder, engine *authgate.Engine, comp *fakeCompleter) http.HandlerFunc {
	t.Helper()
	return makeMCPCallbackHandler(binder, engine, comp.complete, sessionFromRequest, nil)
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
