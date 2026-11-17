package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// mediaMockToolExecutor is a ToolExecutor that also implements MediaToolExecutor,
// returning an image from ExecuteWithMedia (e.g. a screenshot tool).
type mediaMockToolExecutor struct {
	png []byte
}

func (m *mediaMockToolExecutor) Execute(context.Context, string, json.RawMessage) (string, error) {
	return "captured", nil
}
func (m *mediaMockToolExecutor) ExecuteWithMedia(_ context.Context, _ string, _ json.RawMessage) (string, []llm.MediaRef, error) {
	return "screenshot captured", []llm.MediaRef{{MimeType: "image/png", Bytes: m.png}}, nil
}
func (m *mediaMockToolExecutor) ToolDefinitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{{Type: "function", Function: llm.FunctionSchema{Name: "screenshot"}}}
}
func (m *mediaMockToolExecutor) IsMCPTool(string) bool { return false }

// When a tool returns media via the MediaToolExecutor path, the executor must
// attach it as image Parts on the tool-result message the model sees next (#582).
func TestExecute_ToolResultCarriesImageParts(t *testing.T) {
	var secondCallMessages []llm.ChatMessage
	callCount := 0
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID: "call_1", Type: "function",
							Function: llm.FunctionCall{Name: "screenshot", Arguments: `{}`},
						}},
					},
					FinishReason: "tool_calls",
				}, nil
			}
			secondCallMessages = req.Messages
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "I see a login screen"},
				FinishReason: "stop",
			}, nil
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client: client,
		Tools:  &mediaMockToolExecutor{png: []byte{0x89, 'P', 'N', 'G', 9, 9}},
	})

	task := &a2a.Task{ID: "t-media"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("screenshot the screen")}}
	if _, err := executor.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var toolMsg *llm.ChatMessage
	for i := range secondCallMessages {
		if secondCallMessages[i].Role == llm.RoleTool && secondCallMessages[i].ToolCallID == "call_1" {
			toolMsg = &secondCallMessages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool-result message on the second LLM call")
	}
	if toolMsg.Content != "screenshot captured" {
		t.Errorf("tool-result text = %q, want %q", toolMsg.Content, "screenshot captured")
	}
	var sawImage bool
	for _, p := range toolMsg.Parts {
		if p.Type == llm.ContentPartImage && p.Media != nil && len(p.Media.Bytes) > 0 && p.Media.MimeType == "image/png" {
			sawImage = true
		}
	}
	if !sawImage {
		t.Errorf("tool-result message must carry the screenshot as an image Part; parts=%+v", toolMsg.Parts)
	}
}
