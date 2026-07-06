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

// PR #241 review: a byte-offset cut can bisect a <<ctxzip:HASH ...>> marker,
// leaving the model a corrupted pointer it cannot expand even though the
// content is still in the store. The cut must carry whole markers or none.
func TestTruncateToolResult_MarkerBoundary(t *testing.T) {
	marker := "<<ctxzip:ac998fea694b 149_lines_offloaded>>"

	t.Run("under limit unchanged", func(t *testing.T) {
		if got := truncateToolResult("short", 100); got != "short" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("no marker plain cut", func(t *testing.T) {
		got := truncateToolResult(strings.Repeat("x", 200), 100)
		if !strings.HasPrefix(got, strings.Repeat("x", 100)) || !strings.Contains(got, "[OUTPUT TRUNCATED") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("marker straddling the cut is dropped whole", func(t *testing.T) {
		// Cut lands mid-marker: prefix(80) + marker starts at 80, limit 100.
		s := strings.Repeat("a", 80) + marker + strings.Repeat("b", 200)
		got := truncateToolResult(s, 100)
		if strings.Contains(got, CompressionMarkerPrefix) {
			t.Fatalf("broken/partial marker survived the cut: %q", got)
		}
		if !strings.HasPrefix(got, strings.Repeat("a", 80)) {
			t.Fatalf("prefix lost: %q", got)
		}
	})

	t.Run("marker fully before the cut is kept", func(t *testing.T) {
		s := marker + strings.Repeat("b", 300)
		got := truncateToolResult(s, 200)
		if !strings.Contains(got, marker) {
			t.Fatalf("complete marker should survive: %q", got)
		}
	})

	t.Run("unterminated marker before cut is dropped", func(t *testing.T) {
		s := strings.Repeat("a", 50) + "<<ctxzip:deadbeef0000 partial" // no closing >>
		got := truncateToolResult(s+strings.Repeat("b", 200), 100)
		if strings.Contains(got, CompressionMarkerPrefix) {
			t.Fatalf("unterminated marker survived: %q", got)
		}
	})
}

// End-to-end: a compression hook whose output still exceeds the cap, with a
// marker positioned to straddle the byte cut — the LLM must never see a
// partial marker.
func TestDeferredTruncation_NeverSplitsMarker(t *testing.T) {
	// Cap is 25K (CharBudget 100_000). Place the marker straddling 25_000.
	marker := "<<ctxzip:ac998fea694b 149_lines_offloaded>>"
	compressed := strings.Repeat("k", 24_990) + marker + strings.Repeat("t", 5_000)

	hook := func(_ context.Context, hctx *HookContext) error {
		hctx.ToolOutput = compressed
		return nil
	}
	_, llmSaw := deferHarness(t, strings.Repeat("x", 60_000), hook)

	if strings.Contains(llmSaw, CompressionMarkerPrefix) && !strings.Contains(llmSaw, marker) {
		t.Fatalf("LLM saw a partial marker:\n...%s", llmSaw[len(llmSaw)-120:])
	}
	if !strings.Contains(llmSaw, "[OUTPUT TRUNCATED") {
		t.Fatal("cap should still have fired")
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
