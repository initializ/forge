package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// deferHarness runs one tool round-trip with DeferToolResultTruncation and
// returns what the AfterToolExec hook saw and what reached the LLM.
func deferHarness(t *testing.T, toolResult string, hook Hook) (hookSawLen int, llmSaw string) {
	t.Helper()
	callCount := 0
	var captured []llm.ChatMessage
	client := &mockLLMClient{chatFunc: func(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
		callCount++
		captured = req.Messages
		if callCount == 1 {
			return &llm.ChatResponse{
				Message: llm.ChatMessage{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
					ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "big", Arguments: `{}`},
				}}},
				FinishReason: "tool_calls",
			}, nil
		}
		return &llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"}, FinishReason: "stop"}, nil
	}}
	tools := &mockToolExecutor{
		executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return toolResult, nil
		},
		toolDefs: []llm.ToolDefinition{{Type: "function", Function: llm.FunctionSchema{Name: "big"}}},
	}

	hooks := NewHookRegistry()
	hooks.Register(AfterToolExec, func(ctx context.Context, hctx *HookContext) error {
		hookSawLen = len(hctx.ToolOutput)
		if hook != nil {
			return hook(ctx, hctx)
		}
		return nil
	})

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: client, Tools: tools, Hooks: hooks,
		CharBudget:                100_000, // → cap 25K, ceiling 400K
		DeferToolResultTruncation: true,
	})
	task := &a2a.Task{ID: "t-defer"}
	if _, err := exec.Execute(context.Background(), task, &a2a.Message{
		Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("go")},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, m := range captured {
		if m.Role == llm.RoleTool {
			llmSaw = m.Content
		}
	}
	return hookSawLen, llmSaw
}

// The live-run-004 fix: with deferred truncation, the AfterToolExec hooks see
// the FULL tool output (no mid-JSON cut), and a hook that shrinks it (the
// compression hook) makes the post-hook cap a no-op.
func TestDeferredTruncation_HookSeesFullOutput(t *testing.T) {
	full := strings.Repeat("x", 60_000) // over the 25K cap, under the ceiling

	compress := func(_ context.Context, hctx *HookContext) error {
		hctx.ToolOutput = "compressed-tiny" // stand-in for the compression hook
		return nil
	}
	hookSaw, llmSaw := deferHarness(t, full, compress)

	if hookSaw != len(full) {
		t.Fatalf("hook saw %d chars, want the full %d (pre-hook cut destroys envelopes)", hookSaw, len(full))
	}
	if llmSaw != "compressed-tiny" {
		t.Fatalf("LLM should see the hook's compressed output, got %d chars", len(llmSaw))
	}
}

// Without a shrinking hook, the normal cap still protects the context window
// — applied after the hooks instead of before.
func TestDeferredTruncation_PostHookCapStillApplies(t *testing.T) {
	full := strings.Repeat("x", 60_000)
	hookSaw, llmSaw := deferHarness(t, full, nil)

	if hookSaw != len(full) {
		t.Fatalf("hook saw %d chars, want %d", hookSaw, len(full))
	}
	if !strings.Contains(llmSaw, "[OUTPUT TRUNCATED") {
		t.Fatal("post-hook cap missing")
	}
	if len(llmSaw) > 26_000 {
		t.Fatalf("LLM saw %d chars, want ~25K cap", len(llmSaw))
	}
}

// Pathological outputs are still bounded BEFORE hooks by the safety ceiling
// (16x cap, absolute 4MB) so hooks never scan unbounded payloads.
func TestDeferredTruncation_SafetyCeiling(t *testing.T) {
	monster := strings.Repeat("x", 500_000) // ceiling = 16*25K = 400K
	hookSaw, _ := deferHarness(t, monster, nil)

	if hookSaw > 401_000 {
		t.Fatalf("hook saw %d chars — safety ceiling (400K) not enforced", hookSaw)
	}
	if hookSaw < 399_000 {
		t.Fatalf("hook saw %d chars — ceiling cut too aggressively", hookSaw)
	}
}
