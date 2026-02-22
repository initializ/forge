//go:build integration

package channels_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"

	clichannels "github.com/initializ/forge/forge-cli/channels"
	"github.com/initializ/forge/forge-cli/channels/slack"
	"github.com/initializ/forge/forge-cli/channels/telegram"
)

// mockA2AServer returns an httptest server that responds to tasks/send with a
// completed task containing the echoed message.
func mockA2AServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2a.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Logf("mock A2A: decode error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var params a2a.SendTaskParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Logf("mock A2A: params error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Echo back the user message as agent response
		userText := ""
		for _, p := range params.Message.Parts {
			if p.Kind == a2a.PartKindText {
				userText = p.Text
			}
		}

		task := a2a.Task{
			ID: params.ID,
			Status: a2a.TaskStatus{
				State: a2a.TaskStateCompleted,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("echo: " + userText)},
				},
			},
		}

		resp := a2a.NewResponse(req.ID, task)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
}

func TestSlackPlugin_MockA2A(t *testing.T) {
	srv := mockA2AServer(t)
	defer srv.Close()

	plugin := slack.New()
	err := plugin.Init(channels.ChannelConfig{
		Adapter:     "slack",
		WebhookPort: 0,
		Settings: map[string]string{
			"signing_secret": "test-secret",
			"bot_token":      "xoxb-test-token",
		},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Test NormalizeEvent
	rawEvent := []byte(`{
		"team_id": "T123",
		"event": {
			"type": "message",
			"channel": "C456",
			"user": "U789",
			"text": "hello agent",
			"ts": "1234567890.123456"
		}
	}`)

	event, err := plugin.NormalizeEvent(rawEvent)
	if err != nil {
		t.Fatalf("NormalizeEvent: %v", err)
	}

	if event.Channel != "slack" {
		t.Errorf("Channel = %q, want %q", event.Channel, "slack")
	}
	if event.Message != "hello agent" {
		t.Errorf("Message = %q, want %q", event.Message, "hello agent")
	}
	if event.UserID != "U789" {
		t.Errorf("UserID = %q, want %q", event.UserID, "U789")
	}

	// Test Router round-trip with mock A2A
	router := clichannels.NewRouter(srv.URL)
	handler := router.Handler()

	resp, err := handler(context.Background(), event)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if resp.Role != a2a.MessageRoleAgent {
		t.Errorf("response role = %q, want %q", resp.Role, a2a.MessageRoleAgent)
	}

	gotText := ""
	for _, p := range resp.Parts {
		if p.Kind == a2a.PartKindText {
			gotText = p.Text
		}
	}
	if gotText != "echo: hello agent" {
		t.Errorf("response text = %q, want %q", gotText, "echo: hello agent")
	}
}

func TestTelegramPlugin_MockA2A(t *testing.T) {
	srv := mockA2AServer(t)
	defer srv.Close()

	plugin := telegram.New()
	err := plugin.Init(channels.ChannelConfig{
		Adapter: "telegram",
		Settings: map[string]string{
			"bot_token": "123456:ABC-DEF",
			"mode":      "polling",
		},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Test NormalizeEvent
	rawUpdate := []byte(`{
		"update_id": 100,
		"message": {
			"message_id": 42,
			"from": {"id": 99},
			"chat": {"id": 555},
			"text": "hello telegram"
		}
	}`)

	event, err := plugin.NormalizeEvent(rawUpdate)
	if err != nil {
		t.Fatalf("NormalizeEvent: %v", err)
	}

	if event.Channel != "telegram" {
		t.Errorf("Channel = %q, want %q", event.Channel, "telegram")
	}
	if event.Message != "hello telegram" {
		t.Errorf("Message = %q, want %q", event.Message, "hello telegram")
	}
	if event.WorkspaceID != "555" {
		t.Errorf("WorkspaceID = %q, want %q", event.WorkspaceID, "555")
	}

	// Test Router round-trip with mock A2A
	router := clichannels.NewRouter(srv.URL)
	handler := router.Handler()

	resp, err := handler(context.Background(), event)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	gotText := ""
	for _, p := range resp.Parts {
		if p.Kind == a2a.PartKindText {
			gotText = p.Text
		}
	}
	if gotText != "echo: hello telegram" {
		t.Errorf("response text = %q, want %q", gotText, "echo: hello telegram")
	}
}
