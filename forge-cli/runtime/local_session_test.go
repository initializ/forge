package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
