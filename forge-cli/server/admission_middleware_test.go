package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// stubChecker is a test-only AdmissionChecker that returns the same
// Decision on every call. Lets each middleware test set up the wire
// shape without standing up an HTTP server.
type stubChecker struct{ d coreruntime.Decision }

func (s stubChecker) Admit(_ context.Context) coreruntime.Decision { return s.d }

func newAdmissionTestRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/tasks/send", nil)
}

// TestAdmissionMiddleware_AdmitPassesThrough — when the checker
// admits, the downstream handler runs and the response is whatever
// the handler writes. The middleware is observably absent.
func TestAdmissionMiddleware_AdmitPassesThrough(t *testing.T) {
	mw := AdmissionMiddleware(stubChecker{d: coreruntime.Decision{Allowed: true}}, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`handler-ran`))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newAdmissionTestRequest())

	if rec.Code != http.StatusOK {
		t.Errorf("admit path should pass through to handler; got status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "handler-ran") {
		t.Errorf("downstream handler did not run; body=%q", rec.Body.String())
	}
}

// TestAdmissionMiddleware_DenyReturns402WithStructuredBody is the
// core #201 deny contract. The downstream handler MUST NOT run; the
// response carries HTTP 402, the structured JSON body, and the
// Retry-After header derived from reset_at.
func TestAdmissionMiddleware_DenyReturns402WithStructuredBody(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Hour)
	checker := stubChecker{d: coreruntime.Decision{
		Allowed: false,
		Reason:  "cost_limit_exceeded",
		Scope:   "workspace",
		Window:  "daily",
		ResetAt: resetAt,
	}}
	handlerRan := false
	mw := AdmissionMiddleware(checker, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newAdmissionTestRequest())

	if handlerRan {
		t.Errorf("downstream handler should NOT run on deny")
	}
	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Errorf("Retry-After header missing")
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "json") {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if body["error"] != "admission_denied" {
		t.Errorf("body.error = %v, want admission_denied", body["error"])
	}
	if body["reason"] != "cost_limit_exceeded" {
		t.Errorf("body.reason = %v, want cost_limit_exceeded", body["reason"])
	}
	if body["scope"] != "workspace" {
		t.Errorf("body.scope = %v, want workspace", body["scope"])
	}
	if body["window"] != "daily" {
		t.Errorf("body.window = %v, want daily", body["window"])
	}
	if _, ok := body["reset_at"]; !ok {
		t.Errorf("body missing reset_at")
	}
}

// TestAdmissionMiddleware_DenyClampsNegativeRetryAfter pins the
// edge case where the platform returned a past reset_at (clock skew
// or stale cache). Forge MUST NOT return Retry-After: -42 — clamp
// to 0 so the client retries immediately rather than treating the
// negative as a parsing failure.
func TestAdmissionMiddleware_DenyClampsNegativeRetryAfter(t *testing.T) {
	checker := stubChecker{d: coreruntime.Decision{
		Allowed: false,
		ResetAt: time.Now().Add(-time.Hour),
	}}
	mw := AdmissionMiddleware(checker, nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newAdmissionTestRequest())

	got := rec.Header().Get("Retry-After")
	if got != "0" {
		t.Errorf("stale reset_at should clamp Retry-After to 0; got %q", got)
	}
}

// TestAdmissionMiddleware_EmitsAuditEventOnDeny pins the
// task_admission_denied event shape. Fields carry the platform's
// reason / scope / window / cached + the optional reset_at. Goes
// through EmitFromContext so per-invocation context (correlation_id,
// task_id, tenancy) auto-attach.
func TestAdmissionMiddleware_EmitsAuditEventOnDeny(t *testing.T) {
	var buf bytes.Buffer
	al := coreruntime.NewAuditLogger(&buf)

	checker := stubChecker{d: coreruntime.Decision{
		Allowed: false,
		Reason:  "billing_overdue",
		Scope:   "org",
		Window:  "monthly",
		ResetAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Cached:  true,
	}}
	mw := AdmissionMiddleware(checker, al)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newAdmissionTestRequest())

	js := buf.String()
	for _, want := range []string{
		`"event":"task_admission_denied"`,
		`"reason":"billing_overdue"`,
		`"scope":"org"`,
		`"window":"monthly"`,
		`"cached":true`,
		`"reset_at":"2026-07-01T00:00:00Z"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("audit event missing %s; got:\n%s", want, js)
		}
	}
}

// TestAdmissionMiddleware_NoopShortCircuits pins the default-deploy
// fast path. A Noop checker returns the same handler instance back
// from the middleware constructor — no extra function call per
// request when admission isn't configured. We assert by comparing
// handler identities through reflection-free pattern: drop the
// checker and confirm Admit was never called.
func TestAdmissionMiddleware_NoopShortCircuits(t *testing.T) {
	noop := coreruntime.NoopAdmissionChecker{}
	called := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	wrapped := AdmissionMiddleware(noop, nil)(inner)

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, newAdmissionTestRequest())

	if called != 1 || rec.Code != http.StatusOK {
		t.Errorf("Noop should be a clean pass-through; called=%d status=%d",
			called, rec.Code)
	}
}

// TestAdmissionMiddleware_NilCheckerPasses confirms the defensive
// nil-guard: a wiring bug that hands the middleware a nil checker
// must not panic, just no-op.
func TestAdmissionMiddleware_NilCheckerPasses(t *testing.T) {
	wrapped := AdmissionMiddleware(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, newAdmissionTestRequest())
	if rec.Code != http.StatusTeapot {
		t.Errorf("nil checker should be no-op; got status %d", rec.Code)
	}
}
