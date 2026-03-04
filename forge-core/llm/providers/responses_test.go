package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestResponsesClient_OrgIDHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Organization")
		w.Header().Set("Content-Type", "text/event-stream")
		// Minimal streaming response
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer srv.Close()

	client := NewResponsesClient(llm.ClientConfig{
		APIKey:  "sk-test",
		OrgID:   "org-resp-789",
		Model:   "gpt-4o",
		BaseURL: srv.URL,
	})

	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "org-resp-789" {
		t.Errorf("expected OpenAI-Organization header org-resp-789, got %q", gotHeader)
	}
}

func TestResponsesClient_NoOrgIDHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Organization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer srv.Close()

	client := NewResponsesClient(llm.ClientConfig{
		APIKey:  "sk-test",
		Model:   "gpt-4o",
		BaseURL: srv.URL,
	})

	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "" {
		t.Errorf("expected no OpenAI-Organization header, got %q", gotHeader)
	}
}
