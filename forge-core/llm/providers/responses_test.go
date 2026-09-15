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

// TestResponsesClient_DispatchesOnPayloadType_NoEventLines is the regression
// for the Bedrock openai-sigv4 gateway: it streams spec-legal SSE with NO
// `event:` lines, carrying the event type only as a `type` field inside each
// `data:` payload. The old parser routed solely on the `event:` line, so
// `currentEvent` stayed "" and EVERY frame was dropped — empty content, zero
// usage, empty finish reason (the field-observed failure). The parser must
// fall back to the payload `type` and recover both text and usage.
func TestResponsesClient_DispatchesOnPayloadType_NoEventLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// No `event:` lines — type lives inside the JSON, exactly as the
		// gateway sends it.
		_, _ = w.Write([]byte("data: {\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\",\"type\":\"response.output_text.delta\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"output_index\":0,\"content_index\":0,\"delta\":\" world\",\"type\":\"response.output_text.delta\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello world\"}]}],\"usage\":{\"input_tokens\":76,\"output_tokens\":73,\"total_tokens\":149}},\"type\":\"response.completed\"}\n\n"))
	}))
	defer srv.Close()

	client := NewResponsesClient(llm.ClientConfig{
		APIKey:  "sk-test",
		Model:   "openai.gpt-5.4",
		BaseURL: srv.URL,
	})

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Content != "Hello world" {
		t.Errorf("content = %q, want %q — text deltas dropped (no event: lines)", resp.Message.Content, "Hello world")
	}
	if resp.Usage.InputTokens != 76 || resp.Usage.OutputTokens != 73 || resp.Usage.TotalTokens != 149 {
		t.Errorf("usage = %+v, want in=76 out=73 total=149 — response.completed frame dropped", resp.Usage)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want \"stop\" — the empty value was the field symptom", resp.FinishReason)
	}
}

// TestResponsesClient_EventLineDialectStillWorks guards backward compatibility:
// real OpenAI sends BOTH an `event:` line and a payload `type`. The payload-type
// dispatch must not regress that dialect.
func TestResponsesClient_EventLineDialectStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"output_index\":0,\"content_index\":0,\"delta\":\"ok\",\"type\":\"response.output_text.delta\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"id\":\"resp-2\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}},\"type\":\"response.completed\"}\n\n"))
	}))
	defer srv.Close()

	client := NewResponsesClient(llm.ClientConfig{APIKey: "sk-test", Model: "gpt-4o", BaseURL: srv.URL})
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Content != "ok" {
		t.Errorf("content = %q, want \"ok\"", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Errorf("usage total = %d, want 5", resp.Usage.TotalTokens)
	}
}
