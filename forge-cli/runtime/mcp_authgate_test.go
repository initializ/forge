package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/mcp"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/authgate"
)

// userCtx carries a requesting user + a task id so the gate can address the
// consent and flip that task's status. Reuses fakeStatusStore from
// defer_test.go.
func userCtx(subject, taskID string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Email: subject})
	return coreruntime.WithTaskID(ctx, taskID)
}

// A parked call resumes (Await → nil) once consent is signalled, and the
// task is flipped auth-required then restored.
func TestMCPAuthGate_ParksThenResumesOnConsent(t *testing.T) {
	store := newFakeStatusStore()
	store.SetStatus("task-1", a2a.TaskStatus{State: a2a.TaskStateWorking}) // seed prior status
	var mu sync.Mutex
	var delivered int
	gate := &mcpAuthGate{
		engine: authgate.New(),
		store:  store,
		deliverer: func(context.Context, string, string, string, time.Time) error {
			mu.Lock()
			delivered++
			mu.Unlock()
			return nil
		},
	}

	done := make(chan error, 1)
	go func() { done <- gate.Await(userCtx("alice@corp.com", "task-1"), "atl") }()

	// Wait for the call to park, then signal consent.
	waitFor(t, func() bool { _, ok := gate.engine.Peek("alice@corp.com", "atl"); return ok })
	if err := gate.engine.Resolve("alice@corp.com", "atl", authgate.DecisionGranted); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Await after consent must return nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await did not resume after consent")
	}
	mu.Lock()
	d := delivered
	mu.Unlock()
	if d != 1 {
		t.Fatalf("consent delivered %d times, want 1", d)
	}
	// working(seed) → auth-required → working(restored)
	got := store.statuses()
	if len(got) < 3 || got[1] != a2a.TaskStateAuthRequired || got[len(got)-1] != a2a.TaskStateWorking {
		t.Fatalf("status trail = %v, want working→auth-required→working", got)
	}
}

// A gate that never gets consent fails the call with an ErrNoToken-wrapping
// error (so the tool adapter classifies it `no_token`).
func TestMCPAuthGate_TimeoutFailsAsNoToken(t *testing.T) {
	gate := &mcpAuthGate{engine: authgate.New(), store: newFakeStatusStore()}
	// Spec timeout is set inside Await via engine default; force the outcome
	// by resolving as timeout ourselves after it parks.
	done := make(chan error, 1)
	go func() { done <- gate.Await(userCtx("alice@corp.com", "task-1"), "atl") }()
	waitFor(t, func() bool { _, ok := gate.engine.Peek("alice@corp.com", "atl"); return ok })
	_ = gate.engine.Resolve("alice@corp.com", "atl", authgate.DecisionTimeout)

	select {
	case err := <-done:
		if !errors.Is(err, mcp.ErrNoToken) {
			t.Fatalf("timeout must wrap ErrNoToken, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await did not return on timeout")
	}
}

// No requesting user in ctx ⇒ the gate can't be addressed; Await fails
// immediately (no park) with ErrNoToken.
func TestMCPAuthGate_NoSubject_FailsFast(t *testing.T) {
	gate := &mcpAuthGate{engine: authgate.New(), store: newFakeStatusStore()}
	err := gate.Await(context.Background(), "atl")
	if !errors.Is(err, mcp.ErrNoToken) {
		t.Fatalf("no-subject Await must fail as ErrNoToken, got %v", err)
	}
	if gate.engine.Pending() != 0 {
		t.Fatalf("no gate should be parked for a nameless call; Pending=%d", gate.engine.Pending())
	}
}

func TestMCPConsentHandler_StatusCodes(t *testing.T) {
	engine := authgate.New()
	h := makeMCPConsentHandler(engine, nil)

	// 400 — malformed body.
	if code := doConsent(h, "atl", []byte("{not json")); code != http.StatusBadRequest {
		t.Errorf("malformed body → %d, want 400", code)
	}
	// 400 — missing fields.
	if code := doConsent(h, "", mustJSON(map[string]any{"server": "atl"})); code != http.StatusBadRequest {
		t.Errorf("missing subject → %d, want 400", code)
	}
	// 404 — no parked call.
	if code := doConsent(h, "atl", mustJSON(map[string]any{"subject": "nobody@corp.com", "server": "atl"})); code != http.StatusNotFound {
		t.Errorf("no parked call → %d, want 404", code)
	}

	// 200 — a real parked call resolves.
	done := make(chan authgate.Resolution, 1)
	handle, _, err := engine.Await("alice@corp.com", "atl", authgate.Spec{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	go func() { r, _ := handle.WaitCtx(context.Background()); done <- r }()
	if code := doConsent(h, "atl", mustJSON(map[string]any{"subject": "alice@corp.com", "server": "atl"})); code != http.StatusOK {
		t.Fatalf("valid consent → %d, want 200", code)
	}
	select {
	case r := <-done:
		if !r.Granted() {
			t.Fatalf("parked call resolved %q, want granted", r.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consent endpoint did not resume the parked call")
	}
}

// granted:false is an explicit refusal → the parked call fails fast (timeout
// decision) rather than idling to its window.
func TestMCPConsentHandler_ExplicitRefusal(t *testing.T) {
	engine := authgate.New()
	h := makeMCPConsentHandler(engine, nil)
	handle, _, err := engine.Await("alice@corp.com", "atl", authgate.Spec{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan authgate.Resolution, 1)
	go func() { r, _ := handle.WaitCtx(context.Background()); done <- r }()

	refuse := mustJSON(map[string]any{"subject": "alice@corp.com", "server": "atl", "granted": false})
	if code := doConsent(h, "atl", refuse); code != http.StatusOK {
		t.Fatalf("refusal → %d, want 200", code)
	}
	select {
	case r := <-done:
		if r.Granted() {
			t.Fatal("explicit refusal must NOT resume as granted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refusal did not resolve the parked call")
	}
}

// --- helpers ---

func doConsent(h http.HandlerFunc, _ string, body []byte) int {
	req := httptest.NewRequest("POST", "/mcp/consent", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
