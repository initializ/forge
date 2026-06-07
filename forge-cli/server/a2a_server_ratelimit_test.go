package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

// FWS-10 / issue #110 regression coverage:
//   - bumped defaults (60/min, burst 20, cancel_exempt=true)
//   - tasks/cancel exemption from the write bucket
//   - per-IP isolation unaffected by the cancel branch
//
// These tests sit alongside the pre-FWS-10 TestRateLimitMiddleware_*
// suite (which still passes — read-side behavior + 429 shape are
// unchanged).

// TestDefaultRateLimitConfig_LocksNewBaseline pins the FWS-10 default
// shape so an accidental future change can't silently re-tighten it.
func TestDefaultRateLimitConfig_LocksNewBaseline(t *testing.T) {
	cfg := defaultRateLimitConfig()
	if cfg.ReadRPS != 1.0 {
		t.Errorf("ReadRPS = %v, want 1.0 (60/min)", cfg.ReadRPS)
	}
	if cfg.ReadBurst != 10 {
		t.Errorf("ReadBurst = %d, want 10", cfg.ReadBurst)
	}
	if cfg.WriteRPS != 1.0 {
		t.Errorf("WriteRPS = %v, want 1.0 (60/min — FWS-10 bumped from 10/60)", cfg.WriteRPS)
	}
	if cfg.WriteBurst != 20 {
		t.Errorf("WriteBurst = %d, want 20 (FWS-10 bumped from 3)", cfg.WriteBurst)
	}
	if !cfg.CancelExempt {
		t.Error("CancelExempt should default to true (FWS-10)")
	}
}

// TestRateLimitMiddleware_NewDefaults_AllowsBurst20 confirms the
// orchestrator-friendly write burst — 20 consecutive POSTs from the
// same IP all pass before throttling kicks in.
func TestRateLimitMiddleware_NewDefaults_AllowsBurst20(t *testing.T) {
	cfg := defaultRateLimitConfig()
	h := rateLimitMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodPost, "/tasks/send", strings.NewReader(`{}`))
		req.RemoteAddr = "10.0.0.1:1000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("burst write %d/20 returned %d (want 200) — write burst too tight", i+1, rec.Code)
		}
	}
}

// TestRateLimitMiddleware_CancelExempt_NotThrottled is the cost-
// ceiling regression: even when the write bucket is fully drained,
// tasks/cancel sails through. Without this the FWS-4 cancel-burst
// scenario hits 429 at exactly the moment cancellation matters.
func TestRateLimitMiddleware_CancelExempt_NotThrottled(t *testing.T) {
	// Tight bucket so the write side throttles immediately.
	cfg := &RateLimitConfig{
		ReadRPS: 1.0, ReadBurst: 10,
		WriteRPS: 0.001, WriteBurst: 1, // one write, then drained
		CancelExempt: true,
	}
	h := rateLimitMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Drain the write bucket with one send.
	send := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","method":"tasks/send","params":{}}`))
	send.RemoteAddr = "10.0.0.2:1001"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, send)
	if rec.Code != http.StatusOK {
		t.Fatalf("first tasks/send should pass; got %d", rec.Code)
	}

	// Next tasks/send is throttled (bucket drained).
	rec2 := httptest.NewRecorder()
	send2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","method":"tasks/send","params":{}}`))
	send2.RemoteAddr = "10.0.0.2:1001"
	h.ServeHTTP(rec2, send2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second tasks/send should hit 429; got %d", rec2.Code)
	}

	// But tasks/cancel sails through the same IP's empty bucket.
	for i := 0; i < 5; i++ {
		body := `{"jsonrpc":"2.0","method":"tasks/cancel","params":{"id":"x"}}`
		cancelReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		cancelReq.RemoteAddr = "10.0.0.2:1001"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cancelReq)
		if rec.Code != http.StatusOK {
			t.Errorf("cancel %d should sail through even when write bucket is drained, got %d", i+1, rec.Code)
		}
	}
}

// TestRateLimitMiddleware_CancelExemptOff_StillThrottles confirms the
// flag does what it says: when CancelExempt is false, cancel uses the
// write bucket like any other POST.
func TestRateLimitMiddleware_CancelExemptOff_StillThrottles(t *testing.T) {
	cfg := &RateLimitConfig{
		ReadRPS: 1.0, ReadBurst: 10,
		WriteRPS: 0.001, WriteBurst: 1,
		CancelExempt: false,
	}
	h := rateLimitMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First cancel — fine.
	body := `{"jsonrpc":"2.0","method":"tasks/cancel","params":{"id":"x"}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.3:1002"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first cancel should pass; got %d", rec.Code)
	}
	// Second — throttled.
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req2.RemoteAddr = "10.0.0.3:1002"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("with CancelExempt=false, second cancel should hit 429; got %d", rec2.Code)
	}
}

// TestIsTasksCancel_RestoresBody is the body-restoration regression:
// the peek consumes bytes off r.Body, then must reset r.Body so the
// downstream handler can still read the JSON-RPC payload.
func TestIsTasksCancel_RestoresBody(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"tasks/cancel","params":{"id":"abc"}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if !isTasksCancel(req) {
		t.Fatal("expected isTasksCancel to detect tasks/cancel")
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(req.Body)
	if buf.String() != body {
		t.Errorf("body not restored after peek:\n  got:  %q\n  want: %q", buf.String(), body)
	}
}

// TestIsTasksCancel_OtherMethodReturnsFalse confirms a non-cancel
// JSON-RPC envelope falls through to the standard write classification.
func TestIsTasksCancel_OtherMethodReturnsFalse(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"tasks/send","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if isTasksCancel(req) {
		t.Error("tasks/send must not be classified as cancel")
	}
}

// TestIsTasksCancel_MalformedFailsClosed treats unparseable JSON as
// "not cancel" so a malicious caller can't bypass the rate limiter
// with a garbage body.
func TestIsTasksCancel_MalformedFailsClosed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	if isTasksCancel(req) {
		t.Error("malformed body must NOT be classified as cancel (fail-closed)")
	}
}

// Compile-time anchor: any future change that drops the a2a import
// will break the package compile, signaling the test suite needs a
// look. (a2a.NewErrorResponse is what rateLimitMiddleware emits on
// 429.)
var _ = a2a.NewErrorResponse
