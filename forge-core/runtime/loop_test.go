package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	chatFunc func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error)
}

func (m *mockLLMClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return m.chatFunc(ctx, req)
}

func (m *mockLLMClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLLMClient) ModelID() string { return "test-model" }

// mockToolExecutor implements ToolExecutor for testing.
type mockToolExecutor struct {
	executeFunc func(ctx context.Context, name string, arguments json.RawMessage) (string, error)
	toolDefs    []llm.ToolDefinition
}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	return m.executeFunc(ctx, name, arguments)
}

func (m *mockToolExecutor) ToolDefinitions() []llm.ToolDefinition {
	return m.toolDefs
}

func TestToolResultTruncation(t *testing.T) {
	// Generate a tool result that exceeds maxToolResultChars (50,000)
	largeResult := strings.Repeat("x", 60_000)

	callCount := 0
	var capturedMessages []llm.ChatMessage

	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			capturedMessages = req.Messages

			if callCount == 1 {
				// First call: ask for a tool call
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "big_tool",
									Arguments: `{}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				}, nil
			}

			// Second call: return final response
			return &llm.ChatResponse{
				Message: llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Content: "Done",
				},
				FinishReason: "stop",
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
			return largeResult, nil
		},
		toolDefs: []llm.ToolDefinition{
			{Type: "function", Function: llm.FunctionSchema{Name: "big_tool"}},
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client: client,
		Tools:  tools,
	})

	task := &a2a.Task{ID: "test-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("do it")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}

	// Verify the tool result sent to the LLM on the second call was truncated
	var toolMsg *llm.ChatMessage
	for i := range capturedMessages {
		if capturedMessages[i].Role == llm.RoleTool {
			toolMsg = &capturedMessages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool message in captured messages")
	}

	if len(toolMsg.Content) >= 60_000 {
		t.Errorf("tool result was not truncated: got %d chars", len(toolMsg.Content))
	}

	if !strings.Contains(toolMsg.Content, "[OUTPUT TRUNCATED") {
		t.Error("truncated tool result missing [OUTPUT TRUNCATED] marker")
	}

	if !strings.Contains(toolMsg.Content, "60000") {
		t.Error("truncated tool result should contain original length")
	}
}

func TestToolResultUnderLimitNotTruncated(t *testing.T) {
	smallResult := strings.Repeat("y", 1000)

	callCount := 0
	var capturedMessages []llm.ChatMessage

	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			capturedMessages = req.Messages

			if callCount == 1 {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "small_tool",
									Arguments: `{}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				}, nil
			}

			return &llm.ChatResponse{
				Message: llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Content: "Done",
				},
				FinishReason: "stop",
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
			return smallResult, nil
		},
		toolDefs: []llm.ToolDefinition{
			{Type: "function", Function: llm.FunctionSchema{Name: "small_tool"}},
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client: client,
		Tools:  tools,
	})

	task := &a2a.Task{ID: "test-2"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("do it")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the tool result was NOT truncated
	var toolMsg *llm.ChatMessage
	for i := range capturedMessages {
		if capturedMessages[i].Role == llm.RoleTool {
			toolMsg = &capturedMessages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool message in captured messages")
	}

	if toolMsg.Content != smallResult {
		t.Errorf("expected exact small result, got content of length %d", len(toolMsg.Content))
	}
}

func TestLLMErrorReturnsFriendlyMessage(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, fmt.Errorf("openai error (status 400): {\"error\":{\"message\":\"Invalid parameter\"}}")
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client: client,
	})

	task := &a2a.Task{ID: "test-3"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("hello")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Error should be user-friendly, not containing raw API details
	errStr := err.Error()
	if strings.Contains(errStr, "openai") {
		t.Errorf("error should not contain raw API details, got: %s", errStr)
	}
	if strings.Contains(errStr, "400") {
		t.Errorf("error should not contain status codes, got: %s", errStr)
	}
	if !strings.Contains(errStr, "something went wrong") {
		t.Errorf("error should contain friendly message, got: %s", errStr)
	}
}
