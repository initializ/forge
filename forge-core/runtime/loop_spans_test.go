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
	"go.opentelemetry.io/otel/attribute"
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

// TestExecuteStampsCacheTokensOnLLMSpan pins #441: when the provider
// reports prompt-cache tokens, the llm.completion span carries
// gen_ai.usage.cache_read_input_tokens / cache_creation_input_tokens
// and the summed total_input_tokens — so traces show cache stats and
// agree with the llm_call audit event. A non-caching call must NOT emit
// zero-valued cache attributes (span shape stays stable for OpenAI etc.).
func TestExecuteStampsCacheTokensOnLLMSpan(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	SetTracerProvider(tp)
	t.Cleanup(func() {
		ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	callCount := 0
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// Cache-heavy turn: small uncached delta + large cached prefix.
				return &llm.ChatResponse{
					Message: llm.ChatMessage{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
						ID: "tc-1", Type: "function", Function: llm.FunctionCall{Name: "echo", Arguments: `{"x":1}`},
					}}},
					Usage: llm.UsageInfo{
						InputTokens: 12, OutputTokens: 8,
						CacheReadInputTokens: 4000, CacheCreationInputTokens: 200,
					},
					FinishReason: "tool_calls",
				}, nil
			}
			// Non-caching turn: no cache fields.
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"},
				Usage:        llm.UsageInfo{InputTokens: 30, OutputTokens: 5},
				FinishReason: "stop",
			}, nil
		},
	}
	tools := &mockToolExecutor{executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		return "echoed", nil
	}}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: client, Tools: tools, MaxIterations: 5, ModelName: "claude-test", Provider: "anthropic",
	})

	task := &a2a.Task{ID: "task-cache"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hi"}}}
	if _, err := exec.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	llmSpans := rec.FindSpans("llm.completion")
	if len(llmSpans) != 2 {
		t.Fatalf("got %d llm.completion spans; want 2", len(llmSpans))
	}

	// attrsOf collapses a span's int attributes into a lookup keyed by
	// attribute name, with a presence flag so we can distinguish "absent"
	// from "present and zero".
	attrsOf := func(s interface{ Attributes() []attribute.KeyValue }) (map[string]int64, map[string]bool) {
		vals := map[string]int64{}
		present := map[string]bool{}
		for _, kv := range s.Attributes() {
			vals[string(kv.Key)] = kv.Value.AsInt64()
			present[string(kv.Key)] = true
		}
		return vals, present
	}

	// First span (cache-heavy) — carries the cache breakdown + summed total.
	v, p := attrsOf(llmSpans[0])
	if v[observability.AttrGenAIUsageInputTokens] != 12 {
		t.Errorf("input_tokens = %d, want 12 (uncached delta)", v[observability.AttrGenAIUsageInputTokens])
	}
	if v[observability.AttrGenAIUsageCacheReadInputTokens] != 4000 {
		t.Errorf("cache_read = %d, want 4000", v[observability.AttrGenAIUsageCacheReadInputTokens])
	}
	if v[observability.AttrGenAIUsageCacheCreationInputTokens] != 200 {
		t.Errorf("cache_creation = %d, want 200", v[observability.AttrGenAIUsageCacheCreationInputTokens])
	}
	if v[observability.AttrGenAIUsageTotalInputTokens] != 4212 {
		t.Errorf("total_input_tokens = %d, want 4212 (12+4000+200)", v[observability.AttrGenAIUsageTotalInputTokens])
	}

	// Second span (no caching) — cache attributes must be ABSENT, not zero.
	_, p2 := attrsOf(llmSpans[1])
	for _, k := range []string{
		observability.AttrGenAIUsageCacheReadInputTokens,
		observability.AttrGenAIUsageCacheCreationInputTokens,
		observability.AttrGenAIUsageTotalInputTokens,
	} {
		if p2[k] {
			t.Errorf("non-caching span must omit %q, but it was present", k)
		}
	}
	_ = p // first-span presence not asserted individually beyond values above
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
		if string(kv.Key) == observability.AttrErrorType {
			gotErrAttr = true
		}
	}
	if !gotErrAttr {
		t.Errorf("tool.broken missing %q attribute", observability.AttrErrorType)
	}

	// Outer span is "completed" — tool errors aren't fatal.
	root, _ := rec.FindSpan("agent.execute")
	for _, kv := range root.Attributes() {
		if string(kv.Key) == observability.AttrForgeTaskFinalState && kv.Value.AsString() != "completed" {
			t.Errorf("agent.execute final_state = %q; tool errors must not fail the task", kv.Value.AsString())
		}
	}
}

// genAIToolRun drives one tool call through the executor and returns the
// recorder so a test can assert span attributes. isMCP mirrors the registry's
// MCPSource marker: it — not the tool name's "__" shape — decides whether the
// span is typed "extension" and carries mcp.method.name.
func genAIToolRun(t *testing.T, toolName string, isMCP bool) *observability.SpanRecorder {
	t.Helper()
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
					ID:    "resp-1",
					Model: "claude-sonnet-4-6-20990101", // vendor-reported, differs from request model
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID:       "tc-42",
							Type:     "function",
							Function: llm.FunctionCall{Name: toolName, Arguments: `{"city":"Tokyo"}`},
						}},
					},
					Usage:        llm.UsageInfo{InputTokens: 50, OutputTokens: 10},
					FinishReason: "tool_calls",
				}, nil
			}
			return &llm.ChatResponse{
				ID:           "resp-2",
				Model:        "claude-sonnet-4-6-20990101",
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "sunny"},
				Usage:        llm.UsageInfo{InputTokens: 60, OutputTokens: 3},
				FinishReason: "stop",
			}, nil
		},
	}
	tools := &mockToolExecutor{
		executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return "sunny, 20C", nil
		},
		mcpNames: map[string]bool{toolName: isMCP},
	}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         tools,
		MaxIterations: 3,
		ModelName:     "claude-req",
		Provider:      "anthropic",
		AgentID:       "weatherbot",
		AgentVersion:  "1.2.3",
	})
	if _, err := exec.Execute(context.Background(),
		&a2a.Task{ID: "task-genai"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "hi"}}}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return rec
}

// TestExecuteStampsGenAIAgentAndCompletionAttrs asserts the semconv
// identity/operation attributes on the agent.execute + llm.completion spans.
func TestExecuteStampsGenAIAgentAndCompletionAttrs(t *testing.T) {
	rec := genAIToolRun(t, "weather", false)

	root, ok := rec.FindSpan("agent.execute")
	if !ok {
		t.Fatal("missing agent.execute span")
	}
	wantRoot := map[string]string{
		observability.AttrGenAIProviderName:   "anthropic",
		observability.AttrGenAIAgentID:        "weatherbot",
		observability.AttrGenAIAgentName:      "weatherbot",
		observability.AttrGenAIAgentVersion:   "1.2.3",
		observability.AttrGenAIConversationID: "task-genai",
	}
	for k, want := range wantRoot {
		if got, ok := findAttr(root, k); !ok || got != want {
			t.Errorf("agent.execute %s = %q (ok=%v); want %q", k, got, ok, want)
		}
	}

	llmSpan, ok := rec.FindSpan("llm.completion")
	if !ok {
		t.Fatal("missing llm.completion span")
	}
	if got, _ := findAttr(llmSpan, observability.AttrGenAIOperationName); got != observability.OpChat {
		t.Errorf("llm.completion operation.name = %q; want %q", got, observability.OpChat)
	}
	if got, _ := findAttr(llmSpan, observability.AttrGenAIProviderName); got != "anthropic" {
		t.Errorf("llm.completion provider.name = %q; want anthropic", got)
	}
	if got, ok := findAttr(llmSpan, observability.AttrGenAIResponseModel); !ok || got != "claude-sonnet-4-6-20990101" {
		t.Errorf("llm.completion response.model = %q (ok=%v); want the vendor-reported model", got, ok)
	}
	if got, _ := findAttr(llmSpan, observability.AttrGenAIResponseID); got == "" {
		t.Error("llm.completion missing gen_ai.response.id")
	}
}

// TestExecuteStampsGenAIToolAttrs asserts the semconv gen_ai.tool.* attrs on
// a function-tool span, and that mcp.method.name is absent for it.
func TestExecuteStampsGenAIToolAttrs(t *testing.T) {
	rec := genAIToolRun(t, "weather", false)

	toolSpan, ok := rec.FindSpan("tool.weather")
	if !ok {
		t.Fatal("missing tool.weather span")
	}
	want := map[string]string{
		observability.AttrGenAIOperationName: observability.OpExecuteTool,
		observability.AttrGenAIToolName:      "weather",
		observability.AttrGenAIToolCallID:    "tc-42",
		observability.AttrGenAIToolType:      observability.ToolTypeFunction,
	}
	for k, wantVal := range want {
		if got, ok := findAttr(toolSpan, k); !ok || got != wantVal {
			t.Errorf("tool.weather %s = %q (ok=%v); want %q", k, got, ok, wantVal)
		}
	}
	if _, ok := findAttr(toolSpan, observability.AttrMCPMethodName); ok {
		t.Error("mcp.method.name must not be set on a non-MCP (function) tool")
	}
}

// TestExecuteMCPToolSpanUsesExtensionType asserts an MCP-namespaced tool
// ("<server>__<tool>") is typed "extension" and carries mcp.method.name.
func TestExecuteMCPToolSpanUsesExtensionType(t *testing.T) {
	rec := genAIToolRun(t, "linear__create_issue", true)

	toolSpan, ok := rec.FindSpan("tool.linear__create_issue")
	if !ok {
		t.Fatal("missing tool.linear__create_issue span")
	}
	if got, _ := findAttr(toolSpan, observability.AttrGenAIToolType); got != observability.ToolTypeExtension {
		t.Errorf("gen_ai.tool.type = %q; want %q for an MCP tool", got, observability.ToolTypeExtension)
	}
	if got, ok := findAttr(toolSpan, observability.AttrMCPMethodName); !ok || got != observability.MCPMethodToolsCall {
		t.Errorf("mcp.method.name = %q (ok=%v); want %q", got, ok, observability.MCPMethodToolsCall)
	}
}

// TestExecuteNamespacedNonMCPToolNotTypedAsExtension guards the fix for the
// name-heuristic edge: a NON-MCP namespaced tool (API per-op "<api>__<op>")
// shares the "__" shape but must be typed "function" with no mcp.method.name,
// because classification is driven by the registry's MCPSource marker.
func TestExecuteNamespacedNonMCPToolNotTypedAsExtension(t *testing.T) {
	rec := genAIToolRun(t, "weatherapi__forecast", false)

	toolSpan, ok := rec.FindSpan("tool.weatherapi__forecast")
	if !ok {
		t.Fatal("missing tool.weatherapi__forecast span")
	}
	if got, _ := findAttr(toolSpan, observability.AttrGenAIToolType); got != observability.ToolTypeFunction {
		t.Errorf("gen_ai.tool.type = %q; want %q for a non-MCP namespaced tool", got, observability.ToolTypeFunction)
	}
	if _, ok := findAttr(toolSpan, observability.AttrMCPMethodName); ok {
		t.Error("mcp.method.name must not be set on a non-MCP namespaced tool")
	}
}
