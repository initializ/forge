package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-cli/internal/tryview"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/settings"
	"github.com/initializ/forge/forge-core/types"
)

// isolateUserSettings points the user settings layer at a nonexistent temp file
// so the runtime gateway overlay never reads the developer's real
// ~/.forge/settings.json (which may redirect base_url/auth for this provider).
func isolateUserSettings(t *testing.T) {
	t.Helper()
	t.Setenv(settings.EnvUserSettings, filepath.Join(t.TempDir(), "no-settings.json"))
}

// TestLocalSession_GatewayAutoRefreshesExpiredToken pins the fix for "don't call
// the LLM with an expired gateway token": when the cached token is expired, the
// overlay re-runs the api_key_helper (auto-login) and the FRESH token — not the
// stale one — is what reaches the gateway. User-layer settings, no login gate.
func TestLocalSession_GatewayAutoRefreshesExpiredToken(t *testing.T) {
	var gotAuth atomic.Value // string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-x","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	oauth.SetCredentialsDir(filepath.Join(dir, "creds"))
	t.Cleanup(func() { oauth.SetCredentialsDir("") })

	freshJWT := func() string {
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		return enc(`{"alg":"none"}`) + "." + enc(`{"exp":`+exp+`}`) + "." + enc("s")
	}()
	helper := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '"+freshJWT+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Pre-seed an EXPIRED token under the (helper, env) key the overlay looks up.
	if err := oauth.SaveCredentials(GatewayCredKey(helper, nil), &oauth.Token{
		AccessToken: "STALE-EXPIRED", ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// User-layer settings: anthropic gateway → mock server, bearer, the helper.
	userFile := filepath.Join(dir, "settings.json")
	body := `{"models":{"gateways":[{"provider":"anthropic","base_url":"` + srv.URL +
		`","auth_scheme":"bearer","api_key_helper":"` + helper + `"}]}}`
	if err := os.WriteFile(userFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvUserSettings, userFile)

	cfg := &types.ForgeConfig{
		AgentID: "quickstart",
		Model:   types.ModelRef{Provider: "anthropic", Name: "claude-x"},
		Egress:  types.EgressRef{Mode: "dev-open"},
	}
	sess, err := NewLocalSession(context.Background(), LocalSessionOptions{Config: cfg, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	if _, err := sess.RunTurn(context.Background(), "hi", nil); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	auth, _ := gotAuth.Load().(string)
	if auth == "Bearer STALE-EXPIRED" {
		t.Fatal("the EXPIRED token was sent — overlay must refresh before the call")
	}
	if auth != "Bearer "+freshJWT {
		t.Errorf("gateway got Authorization %q, want the freshly-minted token", auth)
	}
}

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
	isolateUserSettings(t)

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
	isolateUserSettings(t)

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
