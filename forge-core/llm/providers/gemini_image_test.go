package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// TestGemini_Chat_ParsesGeneratedImage: a generated image arrives as an
// inlineData part in the generateContent response and is surfaced on
// resp.Message.Parts as an image content part (#255).
func TestGemini_Chat_ParsesGeneratedImage(t *testing.T) {
	raw := []byte("\x89PNG\r\n-gemini-image")
	b64 := base64.StdEncoding.EncodeToString(raw)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Native endpoint path + auth header should be present.
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "sk-test" {
			t.Errorf("missing x-goog-api-key header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"role":"model","parts":[
			{"text":"here you go"},
			{"inlineData":{"mimeType":"image/png","data":%q}}
		]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":8,"totalTokenCount":12}}`, b64)
	}))
	defer srv.Close()

	c := NewGeminiClient(llm.ClientConfig{APIKey: "sk-test", Model: "gemini-2.5-flash-image", BaseURL: srv.URL, EnableImageGeneration: true})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "draw a cat"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "here you go" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.Parts) != 1 || resp.Message.Parts[0].Type != llm.ContentPartImage {
		t.Fatalf("parts = %+v, want one image part", resp.Message.Parts)
	}
	if resp.Message.Parts[0].Media.MimeType != "image/png" || string(resp.Message.Parts[0].Media.Bytes) != string(raw) {
		t.Errorf("image part not decoded from inlineData: %+v", resp.Message.Parts[0].Media)
	}
}

// TestGemini_ChatStream_TextAndDone: the SSE stream yields text deltas and a
// terminal Done delta carrying usage + finish reason.
func TestGemini_ChatStream_TextAndDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hel\"}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1,\"totalTokenCount\":3}}\n\n"))
	}))
	defer srv.Close()

	c := NewGeminiClient(llm.ClientConfig{APIKey: "sk-test", Model: "gemini-2.5-flash", BaseURL: srv.URL})
	ch, err := c.ChatStream(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var text string
	var usage *llm.UsageInfo
	var done bool
	for d := range ch {
		text += d.Content
		if d.Done {
			done = true
			usage = d.Usage
		}
	}
	if text != "Hello" {
		t.Errorf("streamed text = %q, want Hello", text)
	}
	if !done {
		t.Error("expected a terminal Done delta")
	}
	if usage == nil || usage.TotalTokens != 3 {
		t.Errorf("usage = %+v, want total 3", usage)
	}
}
