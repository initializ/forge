package providers

import (
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// A tool result carrying an image (e.g. a screenshot the model should see, #582)
// must serialize to an Anthropic tool_result whose `content` is an ARRAY of
// blocks — text + image — not a plain string.
func TestAnthropic_ToolResultWithImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	req := &llm.ChatRequest{
		Model: "claude-sonnet-4",
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "look at the screen"},
			{
				Role:       llm.RoleTool,
				ToolCallID: "call_1",
				Content:    "screenshot captured",
				Parts: []llm.ContentPart{
					llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", Bytes: png}),
				},
			},
		},
	}

	out := (&AnthropicClient{model: "claude-sonnet-4"}).toAnthropicRequest(req, false)

	// Find the tool-result message (role "user", content is a block array).
	var toolResult map[string]any
	for _, m := range out.Messages {
		var blocks []map[string]any
		if err := json.Unmarshal(m.Content, &blocks); err != nil || len(blocks) == 0 {
			continue
		}
		if blocks[0]["type"] == "tool_result" {
			toolResult = blocks[0]
		}
	}
	if toolResult == nil {
		t.Fatal("no tool_result block found")
	}
	if toolResult["tool_use_id"] != "call_1" {
		t.Errorf("tool_use_id = %v, want call_1", toolResult["tool_use_id"])
	}
	content, ok := toolResult["content"].([]any)
	if !ok {
		t.Fatalf("tool_result content = %T, want an array of blocks for an image result", toolResult["content"])
	}
	var sawText, sawImage bool
	for _, b := range content {
		blk := b.(map[string]any)
		switch blk["type"] {
		case "text":
			if blk["text"] == "screenshot captured" {
				sawText = true
			}
		case "image":
			sawImage = true
			src := blk["source"].(map[string]any)
			if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] == "" {
				t.Errorf("image source malformed: %+v", src)
			}
		}
	}
	if !sawText {
		t.Error("tool_result should carry the text result as a text block")
	}
	if !sawImage {
		t.Error("tool_result should carry the screenshot as an image block")
	}
}

// A text-only tool result keeps the plain-string content form (wire shape
// unchanged — no regression for the common case).
func TestAnthropic_ToolResultTextOnlyStaysString(t *testing.T) {
	req := &llm.ChatRequest{
		Model: "claude-sonnet-4",
		Messages: []llm.ChatMessage{
			{Role: llm.RoleTool, ToolCallID: "c1", Content: "plain result"},
		},
	}
	out := (&AnthropicClient{model: "claude-sonnet-4"}).toAnthropicRequest(req, false)
	var blocks []map[string]any
	if err := json.Unmarshal(out.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, isArray := blocks[0]["content"].([]any); isArray {
		t.Errorf("text-only tool_result content must stay a string, got an array: %+v", blocks[0]["content"])
	}
	if blocks[0]["content"] != "plain result" {
		t.Errorf("content = %v, want the plain string", blocks[0]["content"])
	}
}
