package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// TestOpenAIClient_RecordsEndpoint pins that a successful Chat records the
// invoked URL on the response, so the llm_call audit event can surface it
// regardless of payload capture.
func TestOpenAIClient_RecordsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "gpt-x"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if want := srv.URL + "/chat/completions"; resp.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", resp.Endpoint, want)
	}
}

// TestAnthropicClient_RecordsEndpoint mirrors the above for Anthropic.
func TestAnthropicClient_RecordsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	c := NewAnthropicClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "claude-x"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if want := srv.URL + "/v1/messages"; resp.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", resp.Endpoint, want)
	}
}

// TestChatError_IncludesEndpoint pins that a non-2xx surfaces the invoked URL
// in the error, so a gateway/base-URL misroute (e.g. the Kong 401) is
// debuggable from the "agent loop error" log even with no llm_call event.
func TestChatError_IncludesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	c := NewAnthropicClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "claude-x"})
	_, err := c.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("want an error on 401")
	}
	if !strings.Contains(err.Error(), srv.URL+"/v1/messages") {
		t.Errorf("error should name the invoked URL; got %q", err.Error())
	}
}
