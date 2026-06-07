package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

// TestHandleJSONRPC_DrainsResponseHeaderStage_OntoResponseWriter is the
// dispatcher-side half of the JSON-RPC vs REST X-Forge-* header parity
// fix. A registered handler writes a fake usage header into the stage;
// after the handler returns, the dispatcher must drain the stage onto
// the response writer's Header() BEFORE writeJSON emits the body.
//
// The X-Forge-* names are exercised here as a sanity check; the
// runtime-level test exercises the real applyForgeUsageHeaders path
// against a real executeTask snapshot.
func TestHandleJSONRPC_DrainsResponseHeaderStage_OntoResponseWriter(t *testing.T) {
	s := NewServer(ServerConfig{Port: 0})
	s.RegisterHandler("test/echo-headers", func(ctx context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		// Mirror the runtime's pattern: pull the stage from ctx, write
		// per-invocation headers onto it, return the JSON-RPC body.
		if stage := ResponseHeaderStageFromContext(ctx); stage != nil {
			stage.Set("X-Forge-Tokens-In", "120")
			stage.Set("X-Forge-Tokens-Out", "45")
			stage.Set("X-Forge-Duration-Ms", "250")
			stage.Set("X-Forge-Model", "claude-sonnet-4-6")
			stage.Set("X-Forge-Provider", "anthropic")
		}
		return a2a.NewResponse(id, map[string]string{"ok": "true"})
	})

	body := `{"jsonrpc":"2.0","method":"test/echo-headers","id":1}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleJSONRPC(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	want := map[string]string{
		"X-Forge-Tokens-In":   "120",
		"X-Forge-Tokens-Out":  "45",
		"X-Forge-Duration-Ms": "250",
		"X-Forge-Model":       "claude-sonnet-4-6",
		"X-Forge-Provider":    "anthropic",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("response %s = %q, want %q (the dispatcher did not drain the stage onto the writer — JSON-RPC handlers cannot stamp headers without this)", k, got, v)
		}
	}
}

// TestHandleJSONRPC_HandlerThatDoesntTouchStage_NoExtraHeaders confirms
// the additive contract: handlers that don't write to the stage produce
// no per-invocation headers. The pre-existing security headers from the
// middleware chain are unaffected (they're written further out).
func TestHandleJSONRPC_HandlerThatDoesntTouchStage_NoExtraHeaders(t *testing.T) {
	s := NewServer(ServerConfig{Port: 0})
	s.RegisterHandler("test/quiet", func(_ context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		return a2a.NewResponse(id, map[string]string{"ok": "true"})
	})

	body := `{"jsonrpc":"2.0","method":"test/quiet","id":1}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleJSONRPC(rec, req)

	for _, k := range []string{"X-Forge-Tokens-In", "X-Forge-Tokens-Out", "X-Forge-Duration-Ms", "X-Forge-Model", "X-Forge-Provider"} {
		if v := rec.Header().Get(k); v != "" {
			t.Errorf("quiet handler must not produce %s; got %q", k, v)
		}
	}
}

// TestHandleJSONRPC_StageIsPerRequest confirms two consecutive requests
// don't share stage state — a stale value from request A must not leak
// into request B. Critical for goroutine-safe correctness when many
// requests interleave on one server.
func TestHandleJSONRPC_StageIsPerRequest(t *testing.T) {
	s := NewServer(ServerConfig{Port: 0})
	call := 0
	s.RegisterHandler("test/counter", func(ctx context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		call++
		if stage := ResponseHeaderStageFromContext(ctx); stage != nil && call == 1 {
			stage.Set("X-Test-Leak", "request-1")
		}
		return a2a.NewResponse(id, nil)
	})

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/",
			strings.NewReader(`{"jsonrpc":"2.0","method":"test/counter","id":1}`))
		rec := httptest.NewRecorder()
		s.handleJSONRPC(rec, req)
		return rec
	}

	r1 := post()
	r2 := post()
	if r1.Header().Get("X-Test-Leak") != "request-1" {
		t.Error("request 1 should carry its own header")
	}
	if r2.Header().Get("X-Test-Leak") != "" {
		t.Errorf("request 2 must not see request 1's stage; got %q", r2.Header().Get("X-Test-Leak"))
	}
}
