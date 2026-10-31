package providers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func geminiClient(cfg llm.ClientConfig) *GeminiClient {
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-flash"
	}
	return NewGeminiClient(cfg)
}

// TestGemini_FactoryReturnsNativeClient: provider "gemini" now resolves to the
// native generateContent client, not the OpenAI-compat one (#255).
func TestGemini_FactoryReturnsNativeClient(t *testing.T) {
	c, err := NewClient("gemini", llm.ClientConfig{Model: "gemini-2.5-flash", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*GeminiClient); !ok {
		t.Fatalf("provider gemini resolved to %T, want *GeminiClient", c)
	}
}

func TestGemini_ToRequest_TextSystemAndTools(t *testing.T) {
	c := geminiClient(llm.ClientConfig{})
	req := &llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleSystem, Content: "be terse"},
			{Role: llm.RoleUser, Content: "hi"},
		},
		Tools: []llm.ToolDefinition{{Type: "function", Function: llm.FunctionSchema{
			Name: "search", Description: "find", Parameters: json.RawMessage(`{"type":"object"}`),
		}}},
	}
	out := c.toGeminiRequest(req)

	if out.SystemInstruction == nil || out.SystemInstruction.Parts[0].Text != "be terse" {
		t.Errorf("system → systemInstruction malformed: %+v", out.SystemInstruction)
	}
	if len(out.Contents) != 1 || out.Contents[0].Role != "user" || out.Contents[0].Parts[0].Text != "hi" {
		t.Fatalf("contents = %+v", out.Contents)
	}
	if len(out.Tools) != 1 || len(out.Tools[0].FunctionDeclarations) != 1 || out.Tools[0].FunctionDeclarations[0].Name != "search" {
		t.Errorf("tools → functionDeclarations malformed: %+v", out.Tools)
	}
	// Text-only, no temp/maxtokens/imagegen → no generationConfig.
	if out.GenerationConfig != nil {
		t.Errorf("generationConfig should be omitted for a plain text request, got %+v", out.GenerationConfig)
	}
}

func TestGemini_ToRequest_ImageAndPDFInput(t *testing.T) {
	c := geminiClient(llm.ClientConfig{})
	imgBytes := []byte{1, 2, 3}
	pdfBytes := []byte("%PDF-1.7")
	req := &llm.ChatRequest{Messages: []llm.ChatMessage{{
		Role:    llm.RoleUser,
		Content: "read these",
		Parts: []llm.ContentPart{
			llm.NewTextContentPart("read these"),
			llm.NewMediaContentPart(llm.ContentPartImage, llm.MediaRef{MimeType: "image/png", Bytes: imgBytes}),
			llm.NewMediaContentPart(llm.ContentPartDocument, llm.MediaRef{MimeType: "application/pdf", Bytes: pdfBytes}),
		},
	}}}
	parts := c.toGeminiRequest(req).Contents[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts = %+v, want [text, image inlineData, pdf inlineData]", parts)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" ||
		parts[1].InlineData.Data != base64.StdEncoding.EncodeToString(imgBytes) {
		t.Errorf("image inlineData malformed: %+v", parts[1].InlineData)
	}
	if parts[2].InlineData == nil || parts[2].InlineData.MimeType != "application/pdf" {
		t.Errorf("pdf inlineData malformed: %+v", parts[2].InlineData)
	}
}

func TestGemini_ToRequest_ToolRoundTrip(t *testing.T) {
	c := geminiClient(llm.ClientConfig{})
	req := &llm.ChatRequest{Messages: []llm.ChatMessage{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "search", Type: "function", Function: llm.FunctionCall{Name: "search", Arguments: `{"q":"x"}`}}}},
		{Role: llm.RoleTool, Name: "search", ToolCallID: "search", Content: "found it"},
	}}
	out := c.toGeminiRequest(req)
	// assistant → model content with a functionCall part
	if out.Contents[0].Role != "model" || out.Contents[0].Parts[0].FunctionCall == nil || out.Contents[0].Parts[0].FunctionCall.Name != "search" {
		t.Fatalf("assistant toolcall malformed: %+v", out.Contents[0])
	}
	// tool result → user content with a functionResponse keyed by name
	fr := out.Contents[1].Parts[0].FunctionResponse
	if out.Contents[1].Role != "user" || fr == nil || fr.Name != "search" {
		t.Fatalf("tool result → functionResponse malformed: %+v", out.Contents[1])
	}
	if !strings.Contains(string(fr.Response), "found it") {
		t.Errorf("functionResponse should wrap the result content; got %s", fr.Response)
	}
}

func TestGemini_ToRequest_ImageGenModalitiesOptIn(t *testing.T) {
	req := &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "draw a cat"}}}

	off := geminiClient(llm.ClientConfig{}).toGeminiRequest(req)
	if off.GenerationConfig != nil && len(off.GenerationConfig.ResponseModalities) > 0 {
		t.Error("responseModalities must be absent when image generation is off")
	}
	on := geminiClient(llm.ClientConfig{EnableImageGeneration: true}).toGeminiRequest(req)
	if on.GenerationConfig == nil || len(on.GenerationConfig.ResponseModalities) != 2 ||
		on.GenerationConfig.ResponseModalities[1] != "IMAGE" {
		t.Errorf("responseModalities should be [TEXT, IMAGE] when enabled; got %+v", on.GenerationConfig)
	}
}

// TestGemini_ToRequest_SanitizesToolSchema is the regression for the live 400:
// Gemini rejects JSON-Schema keywords like additionalProperties on tool
// parameters. They must be stripped at every schema level (incl. nested), while
// a property legitimately NAMED "additionalProperties" is preserved.
func TestGemini_ToRequest_SanitizesToolSchema(t *testing.T) {
	schema := json.RawMessage(`{
      "type": "object",
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#",
      "properties": {
        "query": {"type": "string"},
        "opts": {"type": "object", "additionalProperties": true, "properties": {"deep": {"type": "boolean"}}},
        "additionalProperties": {"type": "string"}
      },
      "required": ["query"]
    }`)
	c := geminiClient(llm.ClientConfig{})
	out := c.toGeminiRequest(&llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
		Tools:    []llm.ToolDefinition{{Type: "function", Function: llm.FunctionSchema{Name: "search", Parameters: schema}}},
	})
	got := out.Tools[0].FunctionDeclarations[0].Parameters

	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["additionalProperties"]; ok {
		t.Error("top-level additionalProperties keyword must be stripped")
	}
	if _, ok := m["$schema"]; ok {
		t.Error("$schema must be stripped")
	}
	props := m["properties"].(map[string]any)
	// Nested keyword stripped...
	opts := props["opts"].(map[string]any)
	if _, ok := opts["additionalProperties"]; ok {
		t.Error("nested additionalProperties keyword must be stripped")
	}
	// ...but a property NAMED additionalProperties and real properties survive.
	if _, ok := props["additionalProperties"]; !ok {
		t.Error("a property literally named additionalProperties must be preserved")
	}
	if _, ok := props["query"]; !ok {
		t.Error("real properties must be preserved")
	}
	if req, _ := m["required"].([]any); len(req) != 1 || req[0] != "query" {
		t.Errorf("required must be preserved: %v", m["required"])
	}
}

func TestGemini_ParseResponse_TextToolAndUsage(t *testing.T) {
	body := `{
      "candidates": [{"content": {"role":"model","parts": [
        {"text": "hello"},
        {"functionCall": {"name": "search", "args": {"q": "x"}}}
      ]}, "finishReason": "STOP"}],
      "usageMetadata": {"promptTokenCount": 7, "candidatesTokenCount": 3, "totalTokenCount": 10}
    }`
	resp, err := parseGeminiResponse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hello" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("toolcalls = %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" { // a functionCall present → tool_calls
		t.Errorf("finish = %q, want tool_calls", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 10 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGemini_ParseResponse_SafetyBlockNoCandidates(t *testing.T) {
	// Zero candidates must not error or panic.
	resp, err := parseGeminiResponse(strings.NewReader(`{"candidates": []}`))
	if err != nil {
		t.Fatalf("zero candidates should not error: %v", err)
	}
	if resp.Message.Content != "" || len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected empty assistant turn, got %+v", resp.Message)
	}
}

func TestGemini_SetHeaders_Auth(t *testing.T) {
	t.Run("default → x-goog-api-key", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://x/y", nil)
		geminiClient(llm.ClientConfig{APIKey: "sk-123"}).setHeaders(req)
		if req.Header.Get("x-goog-api-key") != "sk-123" {
			t.Errorf("x-goog-api-key = %q", req.Header.Get("x-goog-api-key"))
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("Authorization must not be set on the default scheme")
		}
	})
	t.Run("bearer scheme → Authorization, no native key", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://x/y", nil)
		geminiClient(llm.ClientConfig{APIKey: "jwt", AuthScheme: llm.AuthSchemeBearer}).setHeaders(req)
		if req.Header.Get("Authorization") != "Bearer jwt" {
			t.Errorf("Authorization = %q", req.Header.Get("Authorization"))
		}
		if req.Header.Get("x-goog-api-key") != "" {
			t.Error("native x-goog-api-key must be suppressed under the bearer scheme")
		}
	})
}
