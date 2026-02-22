package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

func TestVerifySlackSignature_Valid(t *testing.T) {
	secret := "test-signing-secret"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := []byte(`{"type":"event_callback","event":{"text":"hello"}}`)

	baseString := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	if !verifySlackSignature(secret, timestamp, body, sig) {
		t.Error("expected valid signature to pass")
	}
}

func TestVerifySlackSignature_Invalid(t *testing.T) {
	if verifySlackSignature("secret", "12345", []byte("body"), "v0=wrong") {
		t.Error("expected invalid signature to fail")
	}
}

func TestVerifySlackSignature_Empty(t *testing.T) {
	if verifySlackSignature("", "", nil, "") {
		t.Error("expected empty inputs to fail")
	}
}

func TestNormalizeEvent(t *testing.T) {
	raw := `{
		"team_id": "T1234",
		"event": {
			"type": "message",
			"channel": "C0123456",
			"user": "U789",
			"text": "hello world",
			"ts": "1234567890.123456",
			"thread_ts": "1234567890.000001"
		}
	}`

	p := New()
	event, err := p.NormalizeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeEvent() error: %v", err)
	}

	if event.Channel != "slack" {
		t.Errorf("Channel = %q, want slack", event.Channel)
	}
	if event.WorkspaceID != "C0123456" {
		t.Errorf("WorkspaceID = %q, want C0123456", event.WorkspaceID)
	}
	if event.UserID != "U789" {
		t.Errorf("UserID = %q, want U789", event.UserID)
	}
	if event.ThreadID != "1234567890.000001" {
		t.Errorf("ThreadID = %q, want 1234567890.000001", event.ThreadID)
	}
	if event.Message != "hello world" {
		t.Errorf("Message = %q, want 'hello world'", event.Message)
	}
}

func TestNormalizeEvent_NoThread(t *testing.T) {
	raw := `{
		"team_id": "T1234",
		"event": {
			"type": "message",
			"channel": "C0123456",
			"user": "U789",
			"text": "top-level message",
			"ts": "1234567890.123456"
		}
	}`

	p := New()
	event, err := p.NormalizeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeEvent() error: %v", err)
	}

	// Should fall back to ts when thread_ts is empty
	if event.ThreadID != "1234567890.123456" {
		t.Errorf("ThreadID = %q, want 1234567890.123456", event.ThreadID)
	}
}

func TestURLVerificationChallenge(t *testing.T) {
	p := New()
	p.signingSecret = "test-secret"
	p.botToken = "xoxb-test"
	p.webhookPort = 0
	p.webhookPath = "/slack/events"

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := `{"type":"url_verification","challenge":"test-challenge-value"}`

	sig := computeSignature("test-secret", timestamp, []byte(body))

	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", sig)

	rr := httptest.NewRecorder()

	handler := p.makeWebhookHandler(nil)
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if resp["challenge"] != "test-challenge-value" {
		t.Errorf("challenge = %q, want test-challenge-value", resp["challenge"])
	}
}

func TestBotMessageSkipped(t *testing.T) {
	p := New()
	p.signingSecret = "test-secret"
	p.botToken = "xoxb-test"

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := `{"type":"event_callback","event":{"type":"message","channel":"C123","user":"U123","text":"bot msg","ts":"1.1","bot_id":"B123"}}`

	sig := computeSignature("test-secret", timestamp, []byte(body))

	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", sig)

	handlerCalled := false
	handler := p.makeWebhookHandler(func(_ context.Context, _ *channels.ChannelEvent) (*a2a.Message, error) {
		handlerCalled = true
		return nil, nil
	})

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if handlerCalled {
		t.Error("handler should not be called for bot messages")
	}
}

func TestSendResponse(t *testing.T) {
	// Mock Slack API
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test-token" {
			t.Errorf("Authorization = %q, want 'Bearer xoxb-test-token'", r.Header.Get("Authorization"))
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(body, &payload) //nolint:errcheck

		if payload["channel"] != "C0123456" {
			t.Errorf("channel = %v, want C0123456", payload["channel"])
		}
		if payload["thread_ts"] != "1234567890.000001" {
			t.Errorf("thread_ts = %v, want 1234567890.000001", payload["thread_ts"])
		}
		if payload["mrkdwn"] != true {
			t.Errorf("mrkdwn = %v, want true", payload["mrkdwn"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C0123456",
		ThreadID:    "1234567890.000001",
	}

	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart("hello from agent")},
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
		if !strings.Contains(text, "*bold*") {
			t.Errorf("expected *bold* in text, got %q", text)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C0123456",
		ThreadID:    "1234567890.000001",
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

// computeSignature generates a valid Slack signature for testing.
func computeSignature(secret, timestamp string, body []byte) string {
	baseString := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

