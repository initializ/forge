package providers

import (
	"context"
	"encoding/json"
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

func TestOpenAIClient_MaxTokens(t *testing.T) {
	for _, baseURL := range []string{"", "https://api.openai.com/v1", "https://api.openai.com:443/v1", "https://example.com/v1", "https://api.openai.com.example.com/v1"} {
		for _, stream := range []bool{false, true} {
			for _, limit := range []int{0, 16000} {
				c := NewOpenAIClient(llm.ClientConfig{BaseURL: baseURL})
				body, err := json.Marshal(c.toOpenAIRequest(&llm.ChatRequest{MaxTokens: limit}, stream))
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				key, absent := "max_tokens", "max_completion_tokens"
				if baseURL == "" || baseURL == "https://api.openai.com/v1" || baseURL == "https://api.openai.com:443/v1" {
					key, absent = absent, key
				}
				if _, ok := got[absent]; ok {
					t.Errorf("base=%q stream=%v: unexpected %s", baseURL, stream, absent)
				}
				if limit == 0 {
					if _, ok := got[key]; ok {
						t.Errorf("zero limit emitted %s", key)
					}
				} else if got[key] != float64(limit) {
					t.Errorf("base=%q stream=%v: %s = %v", baseURL, stream, key, got[key])
				}
			}
		}
	}
}
