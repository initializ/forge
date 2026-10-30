package forgeui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBuildChatParts verifies the chat → A2A part projection: text becomes a
// text part, each attachment a file part with its base64 bytes passed through
// unchanged (#255).
func TestBuildChatParts(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		parts := buildChatParts("hello", nil)
		if len(parts) != 1 || parts[0]["kind"] != "text" || parts[0]["text"] != "hello" {
			t.Fatalf("parts = %+v, want one text part", parts)
		}
	})

	t.Run("text + image attachment", func(t *testing.T) {
		parts := buildChatParts("look", []ChatAttachment{
			{Name: "p.png", MimeType: "image/png", Data: "aW1nLWJhc2U2NA=="},
		})
		if len(parts) != 2 {
			t.Fatalf("parts = %+v, want [text, file]", parts)
		}
		if parts[1]["kind"] != "file" {
			t.Fatalf("second part kind = %v, want file", parts[1]["kind"])
		}
		file, ok := parts[1]["file"].(map[string]any)
		if !ok {
			t.Fatalf("file payload malformed: %+v", parts[1]["file"])
		}
		if file["mimeType"] != "image/png" || file["name"] != "p.png" {
			t.Errorf("file meta = %+v", file)
		}
		if file["bytes"] != "aW1nLWJhc2U2NA==" {
			t.Errorf("base64 bytes must pass through unchanged; got %v", file["bytes"])
		}
	})

	t.Run("attachment only (no text) omits the text part", func(t *testing.T) {
		parts := buildChatParts("", []ChatAttachment{{MimeType: "application/pdf", Data: "JVBERi0="}})
		if len(parts) != 1 || parts[0]["kind"] != "file" {
			t.Fatalf("parts = %+v, want a single file part", parts)
		}
	})
}

// TestHandleChat_RequiresMessageOrAttachment: the relaxed guard — a message
// with neither text nor attachments is a 400; an attachment-only message passes
// validation (and then fails later only because no agent is running).
func TestHandleChat_RequiresMessageOrAttachment(t *testing.T) {
	s, _ := newTestServer(t)

	t.Run("empty message and no attachments → 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/chat", strings.NewReader(`{"message":""}`))
		req.SetPathValue("id", "test-agent")
		rec := httptest.NewRecorder()
		s.handleChat(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "message or attachment") {
			t.Errorf("body = %s", rec.Body.String())
		}
	})

	t.Run("attachment-only passes validation (fails later on no running agent)", func(t *testing.T) {
		body := `{"message":"","attachments":[{"mimeType":"image/png","data":"eA=="}]}`
		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/chat", strings.NewReader(body))
		req.SetPathValue("id", "test-agent")
		rec := httptest.NewRecorder()
		s.handleChat(rec, req)
		// Past the message/attachment guard; the next gate (agent not running)
		// fires instead — i.e. the attachment-only request was NOT rejected for
		// lacking a message.
		if strings.Contains(rec.Body.String(), "message or attachment") {
			t.Errorf("attachment-only must pass the message guard; got %s", rec.Body.String())
		}
	})
}
