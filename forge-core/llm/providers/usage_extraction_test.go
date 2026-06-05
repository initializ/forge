package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// Regression tests for issue #87 / FWS-3 — every provider must
// populate the normalized UsageInfo.InputTokens / OutputTokens /
// TotalTokens from its native response shape so the audit layer can
// emit accurate llm_call events regardless of which provider served
// the call. The OpenAI-compatible path also serves Ollama.

func TestAnthropic_PopulatesUsageWithOTelAlignedNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_test",
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 42, "output_tokens": 17},
		})
	}))
	defer srv.Close()

	c := NewAnthropicClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "claude-3-5-sonnet"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{
		Model:    "claude-3-5-sonnet",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Usage.InputTokens != 42 {
		t.Errorf("InputTokens = %d, want 42", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 17 {
		t.Errorf("OutputTokens = %d, want 17", resp.Usage.OutputTokens)
	}
	if resp.Usage.TotalTokens != 59 {
		t.Errorf("TotalTokens = %d, want 59 (Anthropic doesn't return total — provider computes input+output)", resp.Usage.TotalTokens)
	}
}

func TestOpenAI_PopulatesUsageWithOTelAlignedNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-1",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     7,
				"completion_tokens": 3,
				"total_tokens":      10,
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAIClient(llm.ClientConfig{APIKey: "x", BaseURL: srv.URL, Model: "gpt-4o-mini"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{
		Model:    "gpt-4o-mini",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// OpenAI wire format still uses prompt_tokens / completion_tokens
	// (provider-specific), but the normalized UsageInfo we expose to
	// audit consumers uses OTel-aligned input_tokens / output_tokens.
	if resp.Usage.InputTokens != 7 {
		t.Errorf("InputTokens (mapped from prompt_tokens) = %d, want 7", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 3 {
		t.Errorf("OutputTokens (mapped from completion_tokens) = %d, want 3", resp.Usage.OutputTokens)
	}
	if resp.Usage.TotalTokens != 10 {
		t.Errorf("TotalTokens = %d, want 10", resp.Usage.TotalTokens)
	}
}

func TestOllama_NoUsage_LeavesZerosForAuditUnavailableFlag(t *testing.T) {
	// Some self-hosted Ollama models don't include token counts in the
	// response. The provider must not invent values — leave zeros so
	// the audit layer flags tokens_unavailable=true on the llm_call
	// event rather than billing for a free call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "ollama-1",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
			// usage field deliberately absent
		})
	}))
	defer srv.Close()

	c := NewOllamaClient(llm.ClientConfig{BaseURL: srv.URL, Model: "llama3"})
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{
		Model:    "llama3",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		t.Errorf("usage-less response should leave zeros so audit layer sets tokens_unavailable=true, got %+v", resp.Usage)
	}
}
