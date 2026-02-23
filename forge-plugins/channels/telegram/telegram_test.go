package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

func TestNormalizeEvent(t *testing.T) {
	raw := `{
		"update_id": 100,
		"message": {
			"message_id": 42,
			"from": {"id": 12345},
			"chat": {"id": 67890},
			"text": "hello bot"
		}
	}`

	p := New()
	event, err := p.NormalizeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeEvent() error: %v", err)
	}

	if event.Channel != "telegram" {
		t.Errorf("Channel = %q, want telegram", event.Channel)
	}
	if event.WorkspaceID != "67890" {
		t.Errorf("WorkspaceID = %q, want 67890", event.WorkspaceID)
	}
	if event.UserID != "12345" {
		t.Errorf("UserID = %q, want 12345", event.UserID)
	}
	if event.ThreadID != "42" {
		t.Errorf("ThreadID = %q, want 42", event.ThreadID)
	}
	if event.Message != "hello bot" {
		t.Errorf("Message = %q, want 'hello bot'", event.Message)
	}
}

func TestNormalizeEvent_NoMessage(t *testing.T) {
	raw := `{"update_id": 100}`

	p := New()
	_, err := p.NormalizeEvent([]byte(raw))
	if err == nil {
		t.Fatal("expected error for update with no message")
	}
}

func TestNormalizeEvent_InvalidJSON(t *testing.T) {
	p := New()
	_, err := p.NormalizeEvent([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSendResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(body, &payload) //nolint:errcheck

		if payload["chat_id"] != "67890" {
			t.Errorf("chat_id = %v, want 67890", payload["chat_id"])
		}
		if payload["reply_to_message_id"] != "42" {
			t.Errorf("reply_to_message_id = %v, want 42", payload["reply_to_message_id"])
		}
		if payload["parse_mode"] != "HTML" {
			t.Errorf("parse_mode = %v, want HTML", payload["parse_mode"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "67890",
		ThreadID:    "42",
	}

	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("agent reply")},
	}

	err := p.SendResponse(event, msg)
	if err != nil {
		t.Fatalf("SendResponse() error: %v", err)
	}
}

func TestSendResponse_MarkdownConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(body, &payload) //nolint:errcheck

		text, _ := payload["text"].(string)
		if !strings.Contains(text, "<b>bold</b>") {
			t.Errorf("expected <b>bold</b> in text, got %q", text)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "67890",
		ThreadID:    "42",
	}

	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("this is **bold** text")},
	}

	err := p.SendResponse(event, msg)
	if err != nil {
		t.Fatalf("SendResponse() error: %v", err)
	}
}

func TestPollingGetUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"ok":true,"result":[{"update_id":1,"message":{"message_id":10,"from":{"id":1},"chat":{"id":2},"text":"poll msg"}}]}`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(resp)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "test-token"
	p.apiBase = srv.URL

	updates, err := p.getUpdates(context.Background(), 0)
	if err != nil {
		t.Fatalf("getUpdates() error: %v", err)
	}

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	if updates[0].Message.Text != "poll msg" {
		t.Errorf("message text = %q, want 'poll msg'", updates[0].Message.Text)
	}
}

func TestWebhookHandler(t *testing.T) {
	p := New()
	p.botToken = "test-token"

	var receivedEvent *channels.ChannelEvent
	done := make(chan struct{})

	handler := p.makeWebhookHandler(func(_ context.Context, event *channels.ChannelEvent) (*a2a.Message, error) {
		receivedEvent = event
		close(done)
		return &a2a.Message{
			Role:  a2a.MessageRoleAgent,
			Parts: []a2a.Part{a2a.NewTextPart("ok")},
		}, nil
	})

	body := `{"update_id":1,"message":{"message_id":10,"from":{"id":1},"chat":{"id":2},"text":"webhook msg"}}`
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(body))
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}

	// Wait for the async goroutine (with timeout)
	select {
	case <-done:
	case <-context.Background().Done():
		t.Fatal("timed out waiting for handler")
	}

	if receivedEvent == nil {
		t.Fatal("handler was not called")
	}
	if receivedEvent.Message != "webhook msg" {
		t.Errorf("event.Message = %q, want 'webhook msg'", receivedEvent.Message)
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name string
		msg  *a2a.Message
		want string
	}{
		{"nil message", nil, "(no response)"},
		{"single text", &a2a.Message{Parts: []a2a.Part{a2a.NewTextPart("hello")}}, "hello"},
		{"multiple text", &a2a.Message{Parts: []a2a.Part{a2a.NewTextPart("a"), a2a.NewTextPart("b")}}, "a\nb"},
		{"no text parts", &a2a.Message{Parts: []a2a.Part{a2a.NewDataPart(42)}}, "(no text response)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.msg)
			if got != tt.want {
				t.Errorf("extractText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInit_Defaults(t *testing.T) {
	p := New()

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token-123")

	cfg := channels.ChannelConfig{
		Adapter: "telegram",
		Settings: map[string]string{
			"bot_token_env": "TELEGRAM_BOT_TOKEN",
		},
	}

	err := p.Init(cfg)
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	if p.botToken != "test-token-123" {
		t.Errorf("botToken = %q, want test-token-123", p.botToken)
	}
	if p.mode != "polling" {
		t.Errorf("mode = %q, want polling", p.mode)
	}
	if p.webhookPort != defaultWebhookPort {
		t.Errorf("webhookPort = %d, want %d", p.webhookPort, defaultWebhookPort)
	}
}

func TestInit_MissingToken(t *testing.T) {
	p := New()
	cfg := channels.ChannelConfig{
		Adapter:  "telegram",
		Settings: map[string]string{},
	}

	err := p.Init(cfg)
	if err == nil {
		t.Fatal("expected error for missing bot_token")
	}
}

func TestSendChatAction(t *testing.T) {
	var receivedAction string
	var receivedChatID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		json.Unmarshal(body, &payload) //nolint:errcheck
		receivedChatID = payload["chat_id"]
		receivedAction = payload["action"]
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "test-token"
	p.apiBase = srv.URL

	err := p.sendChatAction("12345", "typing")
	if err != nil {
		t.Fatalf("sendChatAction() error: %v", err)
	}
	if receivedChatID != "12345" {
		t.Errorf("chat_id = %q, want 12345", receivedChatID)
	}
	if receivedAction != "typing" {
		t.Errorf("action = %q, want typing", receivedAction)
	}
}

func TestStartTypingIndicator(t *testing.T) {
	var actionCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count sendChatAction calls (path contains sendChatAction)
		if strings.Contains(r.URL.Path, "sendChatAction") {
			actionCount++
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "test-token"
	p.apiBase = srv.URL

	ctx := context.Background()
	stop := p.startTypingIndicator(ctx, "67890")

	// The first typing action is sent immediately
	// Give it a moment to process
	time.Sleep(100 * time.Millisecond)

	if actionCount < 1 {
		t.Errorf("expected at least 1 typing action, got %d", actionCount)
	}

	stop()
}

func TestInit_InvalidMode(t *testing.T) {
	p := New()

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	cfg := channels.ChannelConfig{
		Adapter: "telegram",
		Settings: map[string]string{
			"bot_token_env": "TELEGRAM_BOT_TOKEN",
			"mode":          "invalid",
		},
	}

	err := p.Init(cfg)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}
