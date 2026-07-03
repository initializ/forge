package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func cacheTestRequest() *llm.ChatRequest {
	return &llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleSystem, Content: "You are a helpful agent."},
			{Role: llm.RoleUser, Content: "hello"},
		},
		Tools: []llm.ToolDefinition{
			{Type: "function", Function: llm.FunctionSchema{Name: "alpha_tool", Parameters: json.RawMessage(`{"type":"object"}`)}},
			{Type: "function", Function: llm.FunctionSchema{Name: "beta_tool", Parameters: json.RawMessage(`{"type":"object"}`)}},
		},
	}
}

func TestAnthropic_PromptCaching_InjectsBreakpoints(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{Model: "claude-sonnet-4-6", PromptCaching: true})
	body := c.toAnthropicRequest(cacheTestRequest(), false)

	// Last tool carries the ephemeral breakpoint; earlier tools do not.
	if body.Tools[0].CacheControl != nil {
		t.Error("first tool must not carry cache_control")
	}
	if body.Tools[1].CacheControl == nil || body.Tools[1].CacheControl.Type != "ephemeral" {
		t.Error("last tool must carry cache_control ephemeral")
	}

	// System becomes block form with a breakpoint.
	blocks, ok := body.System.([]anthropicSystemBlock)
	if !ok {
		t.Fatalf("system should be block array when caching, got %T", body.System)
	}
	if len(blocks) != 1 || blocks[0].Text != "You are a helpful agent." || blocks[0].CacheControl == nil {
		t.Fatalf("system block malformed: %+v", blocks)
	}

	// The serialized body must contain cache_control (wire-level check).
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("serialized request missing cache_control: %s", data)
	}
}

func TestAnthropic_NoPromptCaching_WireFormatUnchanged(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{Model: "claude-sonnet-4-6"})
	body := c.toAnthropicRequest(cacheTestRequest(), false)

	// System stays a plain string — byte-identical to the pre-caching format.
	if _, ok := body.System.(string); !ok {
		t.Fatalf("system should remain a plain string when caching is off, got %T", body.System)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cache_control") {
		t.Errorf("cache_control must not appear when caching is off: %s", data)
	}
	if !strings.Contains(string(data), `"system":"You are a helpful agent."`) {
		t.Errorf("system should serialize as a plain string: %s", data)
	}
}

func TestOpenAI_PromptCacheKey_StableAndGated(t *testing.T) {
	on := NewOpenAIClient(llm.ClientConfig{Model: "gpt-4o", APIKey: "k", PromptCaching: true})
	off := NewOpenAIClient(llm.ClientConfig{Model: "gpt-4o", APIKey: "k"})

	b1 := on.toOpenAIRequest(cacheTestRequest(), false)
	b2 := on.toOpenAIRequest(cacheTestRequest(), false)
	if b1.PromptCacheKey == "" {
		t.Fatal("prompt_cache_key should be set when caching is on")
	}
	if b1.PromptCacheKey != b2.PromptCacheKey {
		t.Errorf("prompt_cache_key not stable: %s vs %s", b1.PromptCacheKey, b2.PromptCacheKey)
	}
	if !strings.HasPrefix(b1.PromptCacheKey, "forge-") {
		t.Errorf("prompt_cache_key should be forge-prefixed: %s", b1.PromptCacheKey)
	}

	// Different system prompt → different key (prefix identity changed).
	altered := cacheTestRequest()
	altered.Messages[0].Content = "You are a different agent."
	b3 := on.toOpenAIRequest(altered, false)
	if b3.PromptCacheKey == b1.PromptCacheKey {
		t.Error("prompt_cache_key should change when the system prompt changes")
	}

	// Gated off → absent from the wire.
	b4 := off.toOpenAIRequest(cacheTestRequest(), false)
	if b4.PromptCacheKey != "" {
		t.Error("prompt_cache_key must be empty when caching is off")
	}
	data, _ := json.Marshal(b4)
	if strings.Contains(string(data), "prompt_cache_key") {
		t.Errorf("prompt_cache_key must not serialize when caching is off: %s", data)
	}
}
