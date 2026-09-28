package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// TestResponses_ParsesGeneratedImage verifies the Responses client surfaces a
// model-generated image (an image_generation_call output in response.completed)
// as an image content part on the aggregated response (#255 Phase 5).
func TestResponses_ParsesGeneratedImage(t *testing.T) {
	raw := []byte("\x89PNG\r\n-fake-image-bytes")
	b64 := base64.StdEncoding.EncodeToString(raw)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"output_index\":0,\"content_index\":0,\"delta\":\"Here you go:\",\"type\":\"response.output_text.delta\"}\n\n"))
		_, _ = fmt.Fprintf(w, "data: {\"response\":{\"id\":\"resp-img\",\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"completed\",\"result\":%q}],\"usage\":{\"input_tokens\":5,\"output_tokens\":9,\"total_tokens\":14}},\"type\":\"response.completed\"}\n\n", b64)
	}))
	defer srv.Close()

	client := NewResponsesClient(llm.ClientConfig{APIKey: "sk-test", Model: "gpt-5", BaseURL: srv.URL})
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "draw a cat"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "Here you go:" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.Parts) != 1 {
		t.Fatalf("Parts = %+v, want one image part", resp.Message.Parts)
	}
	part := resp.Message.Parts[0]
	if part.Type != llm.ContentPartImage || part.Media == nil {
		t.Fatalf("part is not an image: %+v", part)
	}
	if part.Media.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", part.Media.MimeType)
	}
	if string(part.Media.Bytes) != string(raw) {
		t.Errorf("image bytes not decoded from base64 result")
	}
}

// TestResponses_ImageGenerationToolOptIn: the image_generation built-in tool is
// sent only when EnableImageGeneration is set, and carries no function name.
func TestResponses_ImageGenerationToolOptIn(t *testing.T) {
	req := &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}}

	off := NewResponsesClient(llm.ClientConfig{Model: "gpt-5"}).buildRequest(req, false)
	for _, tl := range off.Tools {
		if tl.Type == "image_generation" {
			t.Fatal("image_generation tool must NOT be sent when the opt-in is off")
		}
	}

	on := NewResponsesClient(llm.ClientConfig{Model: "gpt-5", EnableImageGeneration: true}).buildRequest(req, false)
	found := false
	for _, tl := range on.Tools {
		if tl.Type == "image_generation" {
			found = true
			if tl.Name != "" {
				t.Errorf("built-in image_generation tool must not carry a name; got %q", tl.Name)
			}
		}
	}
	if !found {
		t.Error("image_generation tool must be sent when EnableImageGeneration is set")
	}
}
