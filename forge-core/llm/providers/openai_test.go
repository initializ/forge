package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestOpenAIClient_OrgIDHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Organization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	client := NewOpenAIClient(llm.ClientConfig{
		APIKey:  "sk-test",
		OrgID:   "org-test-123",
		Model:   "gpt-4o",
		BaseURL: srv.URL,
	})

	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader != "org-test-123" {
		t.Errorf("expected OpenAI-Organization header org-test-123, got %q", gotHeader)
	}
}

func TestOpenAIClient_NoOrgIDHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Organization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	client := NewOpenAIClient(llm.ClientConfig{
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
