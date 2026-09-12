package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// withAWSEnv sets the credentials the SigV4 transport reads so the
// bedrock client's requests sign successfully in tests.
func withAWSEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
}

func TestBedrockClient_BaseURLDefaultsFromRegion(t *testing.T) {
	c := NewBedrockClient(llm.ClientConfig{
		Model:     "anthropic.claude-sonnet-4-20250514-v1:0",
		AWSRegion: "eu-west-1",
	})
	want := "https://bedrock-runtime.eu-west-1.amazonaws.com"
	if c.baseURL != want {
		t.Errorf("baseURL = %q; want %q", c.baseURL, want)
	}
}

func TestBedrockClient_ConverseURLEscapesModelID(t *testing.T) {
	c := NewBedrockClient(llm.ClientConfig{
		BaseURL:   "https://example.test",
		Model:     "anthropic.claude-sonnet-4-20250514-v1:0",
		AWSRegion: "us-east-1",
	})
	got := c.converseURL("anthropic.claude-sonnet-4-20250514-v1:0", false)
	want := "https://example.test/model/anthropic.claude-sonnet-4-20250514-v1%3A0/converse"
	if got != want {
		t.Errorf("converseURL = %q; want %q", got, want)
	}
	if s := c.converseURL("m", true); !strings.HasSuffix(s, "/converse-stream") {
		t.Errorf("stream URL = %q; want /converse-stream suffix", s)
	}
}

func TestBedrockClient_FactoryWiring(t *testing.T) {
	cl, err := NewClient(llm.ProviderBedrock, llm.ClientConfig{
		Model:     "amazon.nova-pro-v1:0",
		AWSRegion: "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, ok := cl.(*BedrockClient); !ok {
		t.Fatalf("factory returned %T; want *BedrockClient", cl)
	}
	if cl.ModelID() != "amazon.nova-pro-v1:0" {
		t.Errorf("ModelID = %q", cl.ModelID())
	}
}

func TestBedrockClient_ChatTranslatesRequestAndResponse(t *testing.T) {
	withAWSEnv(t)

	var gotBody converseRequest
	var gotPath, gotEscapedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotEscapedPath = r.URL.EscapedPath()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{
			"output":{"message":{"role":"assistant","content":[
				{"text":"hello "},
				{"toolUse":{"toolUseId":"tu_1","name":"get_weather","input":{"city":"SF"}}}
			]}},
			"stopReason":"tool_use",
			"usage":{"inputTokens":11,"outputTokens":7,"totalTokens":18}
		}`))
	}))
	defer srv.Close()

	c := NewBedrockClient(llm.ClientConfig{
		BaseURL:   srv.URL,
		Model:     "anthropic.claude-sonnet-4-20250514-v1:0",
		AWSRegion: "us-east-1",
	})

	temp := 0.5
	resp, err := c.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleSystem, Content: "be brief"},
			{Role: llm.RoleUser, Content: "weather?"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "tu_0", Function: llm.FunctionCall{Name: "get_weather", Arguments: `{"city":"LA"}`}},
			}},
			{Role: llm.RoleTool, ToolCallID: "tu_0", Content: "72F"},
		},
		Tools: []llm.ToolDefinition{
			{Function: llm.FunctionSchema{Name: "get_weather", Description: "get weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		},
		Temperature: &temp,
		MaxTokens:   256,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// ── path ──
	// The wire path (EscapedPath) must carry %3A for the colon so it
	// matches the SigV4 canonical request; the decoded path has the colon.
	if want := "/model/anthropic.claude-sonnet-4-20250514-v1%3A0/converse"; gotEscapedPath != want {
		t.Errorf("escaped path = %q; want %q", gotEscapedPath, want)
	}
	if want := "/model/anthropic.claude-sonnet-4-20250514-v1:0/converse"; gotPath != want {
		t.Errorf("decoded path = %q; want %q", gotPath, want)
	}

	// ── request translation ──
	if len(gotBody.System) != 1 || gotBody.System[0].Text != "be brief" {
		t.Errorf("system = %+v; want single 'be brief' block", gotBody.System)
	}
	// user, assistant(toolUse), user(toolResult)
	if len(gotBody.Messages) != 3 {
		t.Fatalf("messages len = %d; want 3 (%+v)", len(gotBody.Messages), gotBody.Messages)
	}
	if gotBody.Messages[1].Role != "assistant" || gotBody.Messages[1].Content[0].ToolUse == nil {
		t.Errorf("assistant toolUse not translated: %+v", gotBody.Messages[1])
	}
	if tr := gotBody.Messages[2].Content[0].ToolResult; tr == nil || tr.ToolUseID != "tu_0" || tr.Content[0].Text != "72F" {
		t.Errorf("tool result not translated: %+v", gotBody.Messages[2])
	}
	if gotBody.InferenceConfig == nil || gotBody.InferenceConfig.MaxTokens != 256 || gotBody.InferenceConfig.Temperature == nil || *gotBody.InferenceConfig.Temperature != 0.5 {
		t.Errorf("inferenceConfig = %+v", gotBody.InferenceConfig)
	}
	if gotBody.ToolConfig == nil || len(gotBody.ToolConfig.Tools) != 1 || gotBody.ToolConfig.Tools[0].ToolSpec.Name != "get_weather" {
		t.Errorf("toolConfig = %+v", gotBody.ToolConfig)
	}

	// ── response translation ──
	if resp.Message.Content != "hello " {
		t.Errorf("content = %q; want 'hello '", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].ID != "tu_1" || resp.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool calls = %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q; want tool_calls", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Endpoint == "" {
		t.Error("endpoint not recorded on response")
	}
}

func TestBedrockClient_ChatHTTPError(t *testing.T) {
	withAWSEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad model"}`))
	}))
	defer srv.Close()

	c := NewBedrockClient(llm.ClientConfig{BaseURL: srv.URL, Model: "m", AWSRegion: "us-east-1"})
	_, err := c.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected a status 400 error, got %v", err)
	}
}

func TestBedrockClient_ChatStream(t *testing.T) {
	withAWSEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		var buf bytes.Buffer
		buf.Write(eventFrame("messageStart", `{"role":"assistant"}`))
		buf.Write(eventFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Hel"}}`))
		buf.Write(eventFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"lo"}}`))
		buf.Write(eventFrame("contentBlockStop", `{"contentBlockIndex":0}`))
		// A tool call across start + partial-json deltas + stop.
		buf.Write(eventFrame("contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"tu_9","name":"lookup"}}}`))
		buf.Write(eventFrame("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"q\":"}}}`))
		buf.Write(eventFrame("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"\"go\"}"}}}`))
		buf.Write(eventFrame("contentBlockStop", `{"contentBlockIndex":1}`))
		buf.Write(eventFrame("messageStop", `{"stopReason":"tool_use"}`))
		buf.Write(eventFrame("metadata", `{"usage":{"inputTokens":3,"outputTokens":4,"totalTokens":7}}`))
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c := NewBedrockClient(llm.ClientConfig{BaseURL: srv.URL, Model: "m", AWSRegion: "us-east-1"})
	ch, err := c.ChatStream(context.Background(), &llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var text strings.Builder
	var toolCalls []llm.ToolCall
	var finish string
	var usage *llm.UsageInfo
	var done bool
	for d := range ch {
		text.WriteString(d.Content)
		toolCalls = append(toolCalls, d.ToolCalls...)
		if d.FinishReason != "" {
			finish = d.FinishReason
		}
		if d.Usage != nil {
			usage = d.Usage
		}
		if d.Done {
			done = true
		}
	}

	if text.String() != "Hello" {
		t.Errorf("streamed text = %q; want Hello", text.String())
	}
	if len(toolCalls) != 1 || toolCalls[0].ID != "tu_9" || toolCalls[0].Function.Name != "lookup" || toolCalls[0].Function.Arguments != `{"q":"go"}` {
		t.Errorf("tool calls = %+v", toolCalls)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q; want tool_calls", finish)
	}
	if usage == nil || usage.TotalTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}
	if !done {
		t.Error("stream never signaled Done")
	}
}
