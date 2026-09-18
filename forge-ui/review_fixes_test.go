package forgeui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/tools/builtins"
)

// A build-time provider switch to an unavailable provider must 400 before
// any streaming (the gating branch in handleAgentBuilderChat).
func TestAgentBuilderChat_ProviderUnavailable400(t *testing.T) {
	srv := agentBuilderTestServer(t, func(_ context.Context, _ LLMStreamOptions) error {
		t.Fatal("stream func must not be called for an unavailable provider")
		return nil
	})
	// Only openai is configured; anthropic has no gateway/OAuth/key.
	t.Setenv("ANTHROPIC_API_KEY", "")
	body, _ := json.Marshal(AgentBuilderChatRequest{
		Messages: []SkillBuilderMessage{{Role: "user", Content: "hi"}},
		Provider: "anthropic",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-builder/chat", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.handleAgentBuilderChat(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "anthropic") {
		t.Errorf("error should name the unavailable provider; got %s", w.Body.String())
	}
}

// handleSkillBuilderProviders (workspace-level route) lists the available
// builder LLMs with a default — it had no unit test.
func TestSkillBuilderProviders_ListsDefault(t *testing.T) {
	srv := agentBuilderTestServer(t, nil) // ui.yaml openai + OPENAI_API_KEY set
	req := httptest.NewRequest(http.MethodGet, "/api/skill-builder/providers", nil)
	w := httptest.NewRecorder()
	srv.handleSkillBuilderProviders(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Default   string           `json:"default"`
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Default != "openai" {
		t.Errorf("default = %q, want openai", resp.Default)
	}
	if len(resp.Providers) == 0 || resp.Providers[0]["provider"] != "openai" {
		t.Errorf("providers[0] should be openai; got %+v", resp.Providers)
	}
}

// The builder turn budget allows up to max per window, then refuses, and
// resets when the window rolls over.
func TestTurnBudget_Allow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	b := newTurnBudget(2, time.Minute)
	b.nowFn = func() time.Time { return now }
	// Two separate calls (allow() mutates the counter each time).
	first := b.allow()
	second := b.allow()
	if !first || !second {
		t.Fatal("first two calls should be allowed")
	}
	if b.allow() {
		t.Fatal("third call in the window should be refused")
	}
	now = now.Add(61 * time.Second) // roll the window
	if !b.allow() {
		t.Fatal("call after the window rolls should be allowed")
	}
	// A nil budget is a no-op (always allows).
	var nb *turnBudget
	if !nb.allow() {
		t.Fatal("nil budget must allow")
	}
}

// validateAgentAssets rejects tool/skill names outside the server catalogs.
func TestValidateAgentAssets(t *testing.T) {
	if err := validateAgentAssets(nil, nil); err != nil {
		t.Errorf("empty assets should pass: %v", err)
	}
	if err := validateAgentAssets([]string{"definitely_not_a_tool"}, nil); err == nil {
		t.Error("unknown builtin tool should be rejected")
	}
	if err := validateAgentAssets(nil, []string{"definitely-not-a-skill"}); err == nil {
		t.Error("unknown skill should be rejected")
	}
	// A real builtin tool passes.
	all := builtins.All()
	if len(all) > 0 {
		if err := validateAgentAssets([]string{all[0].Name()}, nil); err != nil {
			t.Errorf("known builtin %q should pass: %v", all[0].Name(), err)
		}
	}
}

// handleCreateAgent rejects a name that slugifies to "" (which would scaffold
// into the workspace root) and an unknown builtin tool — both before the
// CreateFunc runs.
func TestHandleCreateAgent_Guards(t *testing.T) {
	called := false
	srv := NewUIServer(UIServerConfig{
		WorkDir: t.TempDir(),
		CreateFunc: func(AgentCreateOptions) (string, error) {
			called = true
			return "/tmp/x", nil
		},
	})

	post := func(opts AgentCreateOptions) int {
		body, _ := json.Marshal(opts)
		req := httptest.NewRequest(http.MethodPost, "/api/agents", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		srv.handleCreateAgent(w, req)
		return w.Code
	}

	if code := post(AgentCreateOptions{Name: "###", ModelProvider: "openai"}); code != http.StatusBadRequest {
		t.Errorf("empty-slug name: status = %d, want 400", code)
	}
	if code := post(AgentCreateOptions{Name: "Ok Agent", ModelProvider: "openai", BuiltinTools: []string{"nope_not_real"}}); code != http.StatusBadRequest {
		t.Errorf("unknown builtin tool: status = %d, want 400", code)
	}
	if code := post(AgentCreateOptions{Name: "Ok Agent", ModelProvider: "openai", SystemPrompt: strings.Repeat("x", maxSystemPromptBytes+1)}); code != http.StatusBadRequest {
		t.Errorf("oversized system_prompt: status = %d, want 400", code)
	}
	if called {
		t.Error("CreateFunc must not run when validation rejects the request")
	}
}
