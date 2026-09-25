package providers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

var testImageBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a} // PNG magic-ish

func imageMessage() llm.ChatMessage {
	return llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: "what is in this image?",
		Parts: []llm.ContentPart{
			llm.NewTextContentPart("what is in this image?"),
			llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", Bytes: testImageBytes}),
		},
	}
}

// TestAnthropic_ImagePartBecomesSourceBlock verifies a multimodal user message
// serializes to an Anthropic content-block array with a base64 image source
// (#255 Phase 2).
func TestAnthropic_ImagePartBecomesSourceBlock(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{Model: "claude-sonnet-5"})
	body := c.toAnthropicRequest(&llm.ChatRequest{Messages: []llm.ChatMessage{imageMessage()}}, false)

	if len(body.Messages) != 1 {
		t.Fatalf("got %d messages", len(body.Messages))
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(body.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("content should be a block array: %v", err)
	}
	if len(blocks) != 2 || blocks[0].Type != "text" || blocks[1].Type != "image" {
		t.Fatalf("blocks = %+v, want [text, image]", blocks)
	}
	src := blocks[1].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" {
		t.Fatalf("image source malformed: %+v", src)
	}
	if src.Data != base64.StdEncoding.EncodeToString(testImageBytes) {
		t.Errorf("image data not base64 of the bytes")
	}
}

// TestAnthropic_TextOnlyWireUnchanged pins that a text-only message still
// marshals its content as a bare JSON string (no block array) — byte-identical
// to the pre-#255 wire and prompt-cache prefix.
func TestAnthropic_TextOnlyWireUnchanged(t *testing.T) {
	c := NewAnthropicClient(llm.ClientConfig{Model: "claude-sonnet-5"})
	body := c.toAnthropicRequest(&llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hello"}}}, false)
	if got := string(body.Messages[0].Content); got != `"hello"` {
		t.Errorf("text-only content = %s, want \"hello\"", got)
	}
}

// TestOpenAI_ImagePartBecomesImageURL verifies a multimodal message serializes
// to an OpenAI content-parts array with an inline data: image_url.
func TestOpenAI_ImagePartBecomesImageURL(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{Model: "gpt-4o"})
	body := c.toOpenAIRequest(&llm.ChatRequest{Messages: []llm.ChatMessage{imageMessage()}}, false)

	parts, ok := body.Messages[0].Content.([]openaiContentPart)
	if !ok {
		t.Fatalf("content should be []openaiContentPart, got %T", body.Messages[0].Content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Fatalf("parts = %+v, want [text, image_url]", parts)
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testImageBytes)
	if parts[1].ImageURL == nil || parts[1].ImageURL.URL != wantURL {
		t.Errorf("image_url = %+v, want %s", parts[1].ImageURL, wantURL)
	}
}

// TestOpenAI_TextOnlyWireUnchanged pins the byte-identical text-only wire: a
// plain string content, no parts array.
func TestOpenAI_TextOnlyWireUnchanged(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{Model: "gpt-4o"})
	body := c.toOpenAIRequest(&llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hello"}}}, false)
	data, err := json.Marshal(body.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"content":"hello"`) {
		t.Errorf("text-only message must marshal content as a bare string; got %s", data)
	}
}

// TestOpenAI_AssistantToolCallOmitsContent pins the existing invariant that an
// assistant tool-call-only turn still omits content entirely (not "").
func TestOpenAI_AssistantToolCallOmitsContent(t *testing.T) {
	c := NewOpenAIClient(llm.ClientConfig{Model: "gpt-4o"})
	msg := llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "x", Arguments: "{}"}}}}
	body := c.toOpenAIRequest(&llm.ChatRequest{Messages: []llm.ChatMessage{msg}}, false)
	data, _ := json.Marshal(body.Messages[0])
	if strings.Contains(string(data), `"content"`) {
		t.Errorf("assistant tool-call-only turn must omit content; got %s", data)
	}
}
