package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// #433: the Anthropic streaming path must capture input + prompt-cache tokens
// from message_start (they are NOT on message_delta, which carries only
// output), so a streamed call's final usage matches the non-streaming path
// instead of undercounting to zero input.
func TestAnthropicChatStream_CapturesInputAndCacheTokens(t *testing.T) {
	// event:-framed SSE, exactly as Anthropic streams it. input + cache ride
	// on message_start; output accumulates onto message_delta.
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":4000,"cache_creation_input_tokens":200,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := NewAnthropicClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "claude-sonnet-4-6"})
	ch, err := c.ChatStream(context.Background(), &llm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var content string
	var usage llm.UsageInfo
	for delta := range ch {
		content += delta.Content
		if delta.Usage != nil {
			usage = *delta.Usage // overwrite — the provider emits one complete Usage
		}
	}

	if content != "hi" {
		t.Errorf("content = %q, want %q", content, "hi")
	}
	if usage.InputTokens != 12 {
		t.Errorf("InputTokens = %d, want 12 (from message_start, previously dropped)", usage.InputTokens)
	}
	if usage.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25 (from message_delta)", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 4000 {
		t.Errorf("CacheReadInputTokens = %d, want 4000", usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != 200 {
		t.Errorf("CacheCreationInputTokens = %d, want 200", usage.CacheCreationInputTokens)
	}
	if got := usage.TotalInputTokens(); got != 4212 {
		t.Errorf("TotalInputTokens() = %d, want 4212 (12+4000+200)", got)
	}
	if usage.TotalTokens != 4237 {
		t.Errorf("TotalTokens = %d, want 4237 (input+cache_read+cache_creation+output)", usage.TotalTokens)
	}
}

// A non-cached stream: input + output populate, cache fields stay zero.
func TestAnthropicChatStream_NoCaching(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":30,"output_tokens":1}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := NewAnthropicClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "claude-sonnet-4-6"})
	ch, err := c.ChatStream(context.Background(), &llm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var usage llm.UsageInfo
	for delta := range ch {
		if delta.Usage != nil {
			usage = *delta.Usage
		}
	}
	if usage.InputTokens != 30 || usage.OutputTokens != 8 {
		t.Errorf("usage = %+v, want input=30 output=8", usage)
	}
	if usage.CacheReadInputTokens != 0 || usage.CacheCreationInputTokens != 0 {
		t.Errorf("cache tokens should be zero for a non-cached stream, got read=%d creation=%d",
			usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	}
	if usage.TotalTokens != 38 {
		t.Errorf("TotalTokens = %d, want 38", usage.TotalTokens)
	}
}
