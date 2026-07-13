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

// sseEvent is one parsed `event:`/`data:` SSE frame.
type sseEvent struct {
	event string
	data  string
}

func parseSSE(body string) []sseEvent {
	var out []sseEvent
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(frame, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev.event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				ev.data = v
			}
		}
		out = append(out, ev)
	}
	return out
}

func chatTestServer(t *testing.T, stream LLMStreamFunc) (*UIServer, string) {
	t.Helper()
	isolateHome(t)
	root := t.TempDir()
	agentID := "test-agent"
	agentDir := filepath.Join(root, agentID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `agent_id: test-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)
	wsConfig := filepath.Join(root, ".forge", "ui.yaml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, wsConfig, `skill_builder:
  provider: openai
  model: gpt-4o
  api_key_env: OPENAI_API_KEY
`)
	t.Setenv("OPENAI_API_KEY", "test-key")

	srv := NewUIServer(UIServerConfig{
		Port:          4200,
		WorkDir:       root,
		ExePath:       "/usr/bin/false",
		AgentPort:     9100,
		LLMStreamFunc: stream,
	})
	return srv, agentID
}

func postChat(t *testing.T, srv *UIServer, agentID string) []sseEvent {
	t.Helper()
	body, _ := json.Marshal(SkillBuilderChatRequest{
		Messages: []SkillBuilderMessage{{Role: "user", Content: "make a skill"}},
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/agents/"+agentID+"/skill-builder/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", agentID)
	w := httptest.NewRecorder()
	srv.handleSkillBuilderChat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	return parseSSE(w.Body.String())
}

// TestChat_SSESequence_StructuredEnvelope pins the wire contract (#276
// review): a structured {message, skill} response produces progress →
// message → skill_draft → done, with the message and draft on the right
// events (never the raw JSON in the message).
func TestChat_SSESequence_StructuredEnvelope(t *testing.T) {
	envelope := `{"message": "Here you go.", "skill": {"skill_md": "---\nname: t\n---\n# T", "scripts": {}}}`
	srv, agentID := chatTestServer(t, func(_ context.Context, opts LLMStreamOptions) error {
		for _, ch := range envelope {
			opts.OnChunk(string(ch))
		}
		opts.OnDone(envelope)
		return nil
	})

	events := postChat(t, srv, agentID)

	var sawProgress, sawMessage, sawDraft, sawDone bool
	for _, e := range events {
		switch e.event {
		case "progress":
			sawProgress = true
		case "message":
			sawMessage = true
			var m map[string]string
			_ = json.Unmarshal([]byte(e.data), &m)
			if m["content"] != "Here you go." {
				t.Errorf("message content = %q, want %q", m["content"], "Here you go.")
			}
			if strings.Contains(m["content"], "{") {
				t.Errorf("raw JSON leaked into the message: %q", m["content"])
			}
		case "skill_draft":
			sawDraft = true
			var d map[string]any
			_ = json.Unmarshal([]byte(e.data), &d)
			if md, _ := d["skill_md"].(string); !strings.Contains(md, "name: t") {
				t.Errorf("skill_draft skill_md = %q", md)
			}
		case "done":
			sawDone = true
		}
	}
	if !sawProgress || !sawMessage || !sawDraft || !sawDone {
		t.Errorf("missing events: progress=%v message=%v skill_draft=%v done=%v",
			sawProgress, sawMessage, sawDraft, sawDone)
	}
	// done must be last.
	if events[len(events)-1].event != "done" {
		t.Errorf("last event = %q, want done", events[len(events)-1].event)
	}
}

// TestChat_SSESequence_EmptyResponse pins the empty edge the UI handles: an
// empty model response emits no message/skill_draft — only done — so the UI
// clears its pending placeholder without a stale bubble.
func TestChat_SSESequence_EmptyResponse(t *testing.T) {
	srv, agentID := chatTestServer(t, func(_ context.Context, opts LLMStreamOptions) error {
		opts.OnDone("")
		return nil
	})

	events := postChat(t, srv, agentID)
	for _, e := range events {
		if e.event == "message" || e.event == "skill_draft" {
			t.Errorf("empty response should emit neither message nor skill_draft, got %q", e.event)
		}
	}
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("empty response must still terminate with done; events=%+v", events)
	}
}
