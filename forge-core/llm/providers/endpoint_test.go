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

// TestSanitizeEndpoint strips userinfo so a gateway base URL with embedded
// credentials never leaks into the recorded URL.
func TestSanitizeEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://user:pass@gw.internal/v1/messages": "https://gw.internal/v1/messages",
		"https://gw.internal/v1/messages":           "https://gw.internal/v1/messages",
		"http://token@gw:8443/responses":            "http://gw:8443/responses",
		"::not-a-url::":                             "::not-a-url::", // unparseable → unchanged
	}
	for in, want := range cases {
		if got := sanitizeEndpoint(in); got != want {
			t.Errorf("sanitizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChat_EndpointAndErrorHaveNoUserinfo pins that neither the recorded
// Endpoint nor the error string ever carries the password from a base URL with
// embedded credentials.
func TestChat_EndpointAndErrorHaveNoUserinfo(t *testing.T) {
	// Success: Endpoint is sanitized.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer ok.Close()
	// Inject userinfo into the base URL the client uses.
	base := strings.Replace(ok.URL, "http://", "http://user:secret@", 1)
	c := NewOpenAIClient(llm.ClientConfig{APIKey: "x", BaseURL: base, Model: "gpt-x"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if strings.Contains(resp.Endpoint, "secret") || strings.Contains(resp.Endpoint, "user:") {
		t.Errorf("Endpoint leaked userinfo: %q", resp.Endpoint)
	}

	// Failure: the error string is sanitized too.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fail.Close()
	failBase := strings.Replace(fail.URL, "http://", "http://user:secret@", 1)
	c2 := NewOpenAIClient(llm.ClientConfig{APIKey: "x", BaseURL: failBase, Model: "gpt-x"})
	_, err = c2.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("want an error on 401")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaked userinfo: %q", err.Error())
	}
}

// TestResponsesClient_RecordsEndpoint covers the OAuth/streaming-aggregation
// path: the aggregated ChatResponse carries the invoked URL.
func TestResponsesClient_RecordsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewResponsesClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "gpt-x"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if want := srv.URL + "/responses"; resp.Endpoint != want {
		t.Errorf("responses endpoint = %q, want %q", resp.Endpoint, want)
	}
}
