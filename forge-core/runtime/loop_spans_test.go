package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/observability"
)

// TestExecuteEmitsHappyPathSpanTree pins the Phase 3 (#104) instrumentation
// shape: a single-turn task with one tool call produces three spans —
// agent.execute (parent), llm.completion (1st turn), tool.<name>,
// llm.completion (2nd turn after tool result). Failing this test means
// the executor's span hierarchy regressed and dashboards keyed on it
// will go blank silently.
func TestExecuteEmitsHappyPathSpanTree(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	callCount := 0
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID:       "tc-1",
							Type:     "function",
							Function: llm.FunctionCall{Name: "echo", Arguments: `{"x":1}`},
						}},
					},
					Usage:        llm.UsageInfo{InputTokens: 100, OutputTokens: 25},
					FinishReason: "tool_calls",
				}, nil
			}
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"},
				Usage:        llm.UsageInfo{InputTokens: 110, OutputTokens: 5},
				FinishReason: "stop",
			}, nil
		},
	}
	tools := &mockToolExecutor{
		executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return "echoed", nil
		},
	}

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         tools,
		MaxIterations: 5,
		ModelName:     "claude-test",
		Provider:      "anthropic",
	})

	task := &a2a.Task{ID: "task-happy"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hi"}}}
	if _, err := exec.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Exactly one outer span.
	root, ok := rec.FindSpan("agent.execute")
	if !ok {
		t.Fatal("missing agent.execute root span")
	}

	// Outer-span attributes — operator dashboards key by these.
	gotTask := false
	gotModel := false
	gotState := ""
	gotIter := -1
	gotSystem := ""
	for _, kv := range root.Attributes() {
		switch string(kv.Key) {
		case observability.AttrForgeTaskID:
			if kv.Value.AsString() == "task-happy" {
				gotTask = true
			}
		case observability.AttrGenAIRequestModel:
			if kv.Value.AsString() == "claude-test" {
				gotModel = true
			}
		case observability.AttrGenAISystem:
			gotSystem = kv.Value.AsString()
		case observability.AttrForgeTaskFinalState:
			gotState = kv.Value.AsString()
		case observability.AttrForgeLoopIteration:
			gotIter = int(kv.Value.AsInt64())
		}
	}
	if !gotTask {
		t.Errorf("agent.execute missing %q", observability.AttrForgeTaskID)
	}
	if !gotModel {
		t.Errorf("agent.execute missing %q", observability.AttrGenAIRequestModel)
	}
	if gotSystem != "anthropic" {
		t.Errorf("agent.execute gen_ai.system = %q; want %q", gotSystem, "anthropic")
	}
	if gotState != "completed" {
		t.Errorf("agent.execute final_state = %q; want %q", gotState, "completed")
	}
	if gotIter != 2 {
		t.Errorf("agent.execute iteration = %d; want 2 (1 turn that produced tools + 1 turn that produced the answer)", gotIter)
	}

	// Two llm.completion spans (one per turn).
	llmSpans := rec.FindSpans("llm.completion")
	if len(llmSpans) != 2 {
		t.Errorf("got %d llm.completion spans; want 2", len(llmSpans))
	}
	// Each llm.completion carries usage tokens — the Phase-3 conformance
	// invariant the audit-event join relies on.
	for _, s := range llmSpans {
		var in, out int64
		var sawSystem bool
		for _, kv := range s.Attributes() {
			switch string(kv.Key) {
			case observability.AttrGenAIUsageInputTokens:
				in = kv.Value.AsInt64()
			case observability.AttrGenAIUsageOutputTokens:
				out = kv.Value.AsInt64()
			case observability.AttrGenAISystem:
				sawSystem = true
			}
		}
		if in == 0 || out == 0 {
			t.Errorf("llm.completion missing usage tokens (in=%d out=%d)", in, out)
		}
		if !sawSystem {
			t.Error("llm.completion missing gen_ai.system attribute")
		}
	}

	// Exactly one tool.echo span.
	toolSpans := rec.FindSpans("tool.echo")
	if len(toolSpans) != 1 {
		t.Errorf("got %d tool.echo spans; want 1", len(toolSpans))
	}

	// Parent relationships: every llm.completion and tool span's parent
	// span id must equal the root agent.execute span's span id. This
	// is the structural invariant trace browsers render the flame graph
	// from.
	rootSpanID := root.SpanContext().SpanID()
	for _, s := range llmSpans {
		if s.Parent().SpanID() != rootSpanID {
			t.Errorf("llm.completion parent span id %s; want %s (agent.execute)", s.Parent().SpanID(), rootSpanID)
		}
	}
	for _, s := range toolSpans {
		if s.Parent().SpanID() != rootSpanID {
			t.Errorf("tool span parent span id %s; want %s (agent.execute)", s.Parent().SpanID(), rootSpanID)
		}
	}
}

// TestExecuteRecordsLLMErrorOnSpan confirms that when the provider's
// Chat() returns an error, the llm.completion span records it (status
// = Error, error event present) AND the outer agent.execute span's
// final_state is "failed."
func TestExecuteRecordsLLMErrorOnSpan(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, errors.New("upstream 500")
		},
	}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         &mockToolExecutor{},
		MaxIterations: 3,
		ModelName:     "x",
		Provider:      "openai",
	})
	_, err := exec.Execute(context.Background(),
		&a2a.Task{ID: "task-err"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hi"}}})
	if err == nil {
		t.Fatal("expected Execute to return an error")
	}

	llmSpan, ok := rec.FindSpan("llm.completion")
	if !ok {
		t.Fatal("missing llm.completion span")
	}
	if llmSpan.Status().Code.String() != "Error" {
		t.Errorf("llm.completion status = %s; want Error", llmSpan.Status().Code.String())
	}
	if len(llmSpan.Events()) == 0 {
		t.Error("llm.completion expected at least one event (RecordError)")
	}

	root, ok := rec.FindSpan("agent.execute")
	if !ok {
		t.Fatal("missing agent.execute span on error path")
	}
	for _, kv := range root.Attributes() {
		if string(kv.Key) == observability.AttrForgeTaskFinalState {
			if kv.Value.AsString() != "failed" {
				t.Errorf("agent.execute final_state = %q; want %q", kv.Value.AsString(), "failed")
			}
		}
	}
}

// TestExecuteRecordsToolErrorOnSpan confirms the tool.<name> span
// captures Error status when the tool returns an error. The executor
// keeps running (tool errors are surfaced as text to the LLM, not
// fatal), so the agent.execute final_state should still be
// "completed" — the tool span carries the failure detail.
func TestExecuteRecordsToolErrorOnSpan(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	call := 0
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			call++
			if call == 1 {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID:       "tc-1",
							Type:     "function",
							Function: llm.FunctionCall{Name: "broken", Arguments: `{}`},
						}},
					},
					Usage:        llm.UsageInfo{InputTokens: 50, OutputTokens: 10},
					FinishReason: "tool_calls",
				}, nil
			}
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "ok"},
				Usage:        llm.UsageInfo{InputTokens: 60, OutputTokens: 3},
				FinishReason: "stop",
			}, nil
		},
	}
	tools := &mockToolExecutor{
		executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return "", fmt.Errorf("tool exploded")
		},
	}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         tools,
		MaxIterations: 3,
		ModelName:     "x",
		Provider:      "openai",
	})
	if _, err := exec.Execute(context.Background(),
		&a2a.Task{ID: "task-tool-err"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hi"}}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	toolSpan, ok := rec.FindSpan("tool.broken")
	if !ok {
		t.Fatal("missing tool.broken span")
	}
	if toolSpan.Status().Code.String() != "Error" {
		t.Errorf("tool.broken status = %s; want Error", toolSpan.Status().Code.String())
	}
	gotErrAttr := false
	for _, kv := range toolSpan.Attributes() {
		if string(kv.Key) == observability.AttrForgeToolError {
			gotErrAttr = true
		}
	}
	if !gotErrAttr {
		t.Errorf("tool.broken missing %q attribute", observability.AttrForgeToolError)
	}

	// Outer span is "completed" — tool errors aren't fatal.
	root, _ := rec.FindSpan("agent.execute")
	for _, kv := range root.Attributes() {
		if string(kv.Key) == observability.AttrForgeTaskFinalState && kv.Value.AsString() != "completed" {
			t.Errorf("agent.execute final_state = %q; tool errors must not fail the task", kv.Value.AsString())
		}
	}
}
