package forgeui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentBuilderTestServer builds a UIServer with a workspace-level
// skill-builder LLM configured (so the agent builder resolves credentials)
// and the given stream func injected.
func agentBuilderTestServer(t *testing.T, stream LLMStreamFunc) *UIServer {
	t.Helper()
	isolateHome(t)
	root := t.TempDir()
	wsConfig := filepath.Join(root, ".forge", "ui.yaml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, wsConfig, `skill_builder:
  provider: openai
  model: gpt-5.4
  api_key_env: OPENAI_API_KEY
`)
	t.Setenv("OPENAI_API_KEY", "test-key")

	return NewUIServer(UIServerConfig{
		Port:          4200,
		WorkDir:       root,
		ExePath:       "/usr/bin/false",
		AgentPort:     9100,
		LLMStreamFunc: stream,
	})
}

func postAgentBuilderChat(t *testing.T, srv *UIServer) (*httptest.ResponseRecorder, []sseEvent) {
	t.Helper()
	body, _ := json.Marshal(AgentBuilderChatRequest{
		Messages: []SkillBuilderMessage{{Role: "user", Content: "build me a PR reviewer"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-builder/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleAgentBuilderChat(w, req)
	return w, parseSSE(w.Body.String())
}

// The agent builder must emit message → agent_draft → done with the draft
// carried on the agent_draft event (never raw JSON in the message).
func TestAgentBuilderChat_EmitsDraft(t *testing.T) {
	envelope := `{"message":"Here's your agent.","agent":{"name":"PR Reviewer","model_provider":"openai","model_name":"gpt-5.4","builtin_tools":["web_fetch"],"skills":[],"system_prompt":"You review PRs."}}`
	srv := agentBuilderTestServer(t, func(_ context.Context, opts LLMStreamOptions) error {
		// The system prompt should enumerate the provider/tool catalog.
		if !strings.Contains(opts.SystemPrompt, "Available Model Providers") {
			t.Errorf("system prompt missing provider catalog")
		}
		opts.OnChunk(envelope)
		opts.OnDone(envelope)
		return nil
	})

	w, events := postAgentBuilderChat(t, srv)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}

	var sawMessage, sawDraft, sawDone bool
	for _, e := range events {
		switch e.event {
		case "message":
			sawMessage = true
			var m map[string]string
			_ = json.Unmarshal([]byte(e.data), &m)
			if strings.Contains(m["content"], "{") {
				t.Errorf("raw JSON leaked into message: %q", m["content"])
			}
		case "agent_draft":
			sawDraft = true
			var d AgentDraft
			if err := json.Unmarshal([]byte(e.data), &d); err != nil {
				t.Fatalf("agent_draft not an AgentDraft: %v", err)
			}
			if d.Name != "PR Reviewer" || d.ModelProvider != "openai" {
				t.Errorf("draft fields wrong: %+v", d)
			}
		case "done":
			sawDone = true
		}
	}
	if !sawMessage || !sawDraft || !sawDone {
		t.Errorf("missing events: message=%v agent_draft=%v done=%v", sawMessage, sawDraft, sawDone)
	}
	if events[len(events)-1].event != "done" {
		t.Errorf("last event = %q, want done", events[len(events)-1].event)
	}
}

// With no workspace/user config and no global credentials, the endpoint
// must refuse with a 400 before streaming (the UI gates on this).
func TestAgentBuilderChat_UnconfiguredReturns400(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	srv := NewUIServer(UIServerConfig{
		Port:          4200,
		WorkDir:       root,
		ExePath:       "/usr/bin/false",
		AgentPort:     9100,
		LLMStreamFunc: func(_ context.Context, _ LLMStreamOptions) error { return nil },
	})
	// Ensure no global provider key leaks in from the test environment.
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	body, _ := json.Marshal(AgentBuilderChatRequest{
		Messages: []SkillBuilderMessage{{Role: "user", Content: "hi"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-builder/chat", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.handleAgentBuilderChat(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
