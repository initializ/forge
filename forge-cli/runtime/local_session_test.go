package runtime

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tryview"
	"github.com/initializ/forge/forge-core/types"
)

// TestLocalSession_RunTurn drives one real turn through the in-process executor
// against a mock OpenAI-compatible server — no real provider. It proves the
// assembly (client + tools + hooks + executor) runs and that history
// accumulates across the user and agent messages.
func TestLocalSession_RunTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"2 plus 2 is 4."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)

	cfg := &types.ForgeConfig{
		AgentID: "quickstart",
		Model:   types.ModelRef{Provider: "openai", Name: "gpt-test"},
		Egress:  types.EgressRef{Mode: "dev-open"}, // skip the subprocess proxy in tests
	}
	sess, err := NewLocalSession(context.Background(), LocalSessionOptions{
		Config:  cfg,
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	reply, err := sess.RunTurn(context.Background(), "what is 2+2?", nil)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !strings.Contains(reply, "4") {
		t.Errorf("reply = %q, want it to contain 4", reply)
	}
	if len(sess.history) != 2 {
		t.Fatalf("history len = %d, want 2 (user + agent)", len(sess.history))
	}

	// A second turn appends to the same history.
	if _, err := sess.RunTurn(context.Background(), "and 3+3?", nil); err != nil {
		t.Fatalf("second RunTurn: %v", err)
	}
	if len(sess.history) != 4 {
		t.Errorf("history len after 2 turns = %d, want 4", len(sess.history))
	}
}

// TestLocalSession_RendersToolLoop drives a real tool-calling turn (a mock LLM
// that calls the datetime_now builtin, then answers) with the visible-loop
// renderer attached, and asserts the tool line reaches the renderer's output.
func TestLocalSession_RendersToolLoop(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			// First round: ask to call datetime_now.
			_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"datetime_now","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		// Second round: final answer.
		_, _ = w.Write([]byte(`{"id":"c2","choices":[{"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)

	cfg := &types.ForgeConfig{
		AgentID: "quickstart",
		Model:   types.ModelRef{Provider: "openai", Name: "gpt-test"},
		Egress:  types.EgressRef{Mode: "dev-open"},
	}
	sess, err := NewLocalSession(context.Background(), LocalSessionOptions{Config: cfg, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	var buf bytes.Buffer
	renderer := tryview.New(&buf, false, false, false) // plain text
	sess.AuditLogger().AddSink(renderer)

	if _, err := sess.RunTurn(context.Background(), "what time is it?", nil); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	renderer.FlushSummary()

	got := buf.String()
	if !strings.Contains(got, "datetime_now") {
		t.Errorf("rendered loop = %q, want it to show the datetime_now tool call", got)
	}
	if !strings.Contains(got, "audit") {
		t.Errorf("rendered loop = %q, want the compact audit summary line", got)
	}
}
