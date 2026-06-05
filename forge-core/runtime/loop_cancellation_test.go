package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// Regression tests for issue #88 / FWS-4 — the LLM executor agent loop
// honors ctx cancellation at the iteration boundary and between tool
// calls within an iteration. Without these checks, a tasks/cancel
// signal would only take effect after the next LLM call returned (or
// the next tool call completed), wasting orchestrator spend.

func TestLLMExecutor_AgentLoop_CancelledAtIterationBoundary(t *testing.T) {
	// Set up a client that returns a tool call on the first call,
	// then would loop again. Cancel ctx after the first response, so
	// the second iteration's boundary check exits with ctx.Err().
	chatCalls := 0
	cancelAfterFirstCall := make(chan struct{})

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
				chatCalls++
				if chatCalls == 1 {
					// Signal the cancel goroutine to fire.
					close(cancelAfterFirstCall)
					return &llm.ChatResponse{
						ID: "r1",
						Message: llm.ChatMessage{
							Role: llm.RoleAssistant,
							ToolCalls: []llm.ToolCall{
								{ID: "tc1", Type: "function",
									Function: llm.FunctionCall{Name: "noop", Arguments: "{}"}},
							},
						},
					}, nil
				}
				// Second call would happen after cancel — must not be reached.
				t.Errorf("LLM should not be called after cancel signal")
				return nil, nil
			},
		},
		Tools: &mockToolExecutor{
			executeFunc: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
				return "noop-result", nil
			},
			toolDefs: []llm.ToolDefinition{},
		},
		ModelName: "test",
		Provider:  "test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Fire the cancel as soon as the first LLM call returns; the
	// second iteration's boundary check picks it up.
	go func() {
		<-cancelAfterFirstCall
		cancel()
	}()

	_, err := exec.Execute(ctx,
		&a2a.Task{ID: "t1"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Execute should return context.Canceled, got %v", err)
	}
	if chatCalls != 1 {
		t.Errorf("expected exactly 1 LLM call before cancellation took effect, got %d", chatCalls)
	}
}

func TestLLMExecutor_AgentLoop_CancelledBetweenToolCalls(t *testing.T) {
	// The LLM returns TWO tool calls in one response. The first tool's
	// implementation cancels its own ctx (simulating an orchestrator
	// signal that arrives between tool calls). The second tool MUST
	// NOT be invoked — the between-tool ctx.Err() check enforces that.
	toolCalls := 0
	chatCalls := 0
	var cancelFn context.CancelFunc

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
				chatCalls++
				return &llm.ChatResponse{
					ID: "r1",
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{
							{ID: "tc1", Type: "function",
								Function: llm.FunctionCall{Name: "tool_a", Arguments: "{}"}},
							{ID: "tc2", Type: "function",
								Function: llm.FunctionCall{Name: "tool_b", Arguments: "{}"}},
						},
					},
				}, nil
			},
		},
		Tools: &mockToolExecutor{
			executeFunc: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
				toolCalls++
				if name == "tool_a" {
					// Cancel mid-tool to simulate orchestrator pulling
					// the rug between tool calls within one iteration.
					cancelFn()
				}
				if name == "tool_b" {
					t.Errorf("tool_b must not run after cancel signalled during tool_a")
				}
				return "ok", nil
			},
			toolDefs: []llm.ToolDefinition{},
		},
		ModelName: "test",
		Provider:  "test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancelFn = cancel

	_, err := exec.Execute(ctx,
		&a2a.Task{ID: "t1"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Execute should return context.Canceled when cancelled between tools, got %v", err)
	}
	if toolCalls != 1 {
		t.Errorf("expected exactly 1 tool call (tool_a) before cancellation took effect, got %d", toolCalls)
	}
	if chatCalls != 1 {
		t.Errorf("expected exactly 1 LLM call (no follow-up after cancellation), got %d", chatCalls)
	}
}

func TestLLMExecutor_LLMCallErrorWithCancelledCtx_RoutesToCancellation(t *testing.T) {
	// Regression for FWS-4 v1.1: when ctx was cancelled AND the LLM
	// client returns some non-typed error (the realistic case —
	// net/http wraps inconsistently across DNS / TLS / body paths and
	// the resulting err is rarely structurally errors.Is(context.Canceled)
	// despite being caused by cancellation), the loop must use ctx.Err()
	// to detect cancellation. Without this, every mid-flight cancel
	// surfaces to the orchestrator as state=failed instead of
	// state=canceled. The fix replaces the brittle errors.Is check
	// with a direct ctx.Err() check at the LLM-error site.
	clientCh := make(chan struct{})
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
				close(clientCh) // signal the cancel goroutine
				<-ctx.Done()    // wait for cancellation
				// Return an error that does NOT structurally wrap
				// context.Canceled — matches what net/http actually
				// produces (a non-Unwrap-friendly chain that just
				// stringifies to "context canceled" somewhere inside).
				return nil, fmt.Errorf("openai request: net/http: request canceled")
			},
		},
		Tools:     &mockToolExecutor{toolDefs: []llm.ToolDefinition{}},
		ModelName: "test",
		Provider:  "test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-clientCh
		cancel()
	}()

	_, err := exec.Execute(ctx,
		&a2a.Task{ID: "t1"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("loop should return context.Canceled when ctx is cancelled at the LLM error site; got %v", err)
	}
	if err != nil && err.Error() == "something went wrong while processing your request, please try again" {
		t.Errorf("loop wrapped the ctx error into the generic failure message — executeTask cannot route to invocation_cancelled")
	}
}

func TestLLMExecutor_AgentLoop_AlreadyCancelledCtxExitsImmediately(t *testing.T) {
	// Pre-cancelled ctx must short-circuit before the first LLM call.
	// Otherwise an orchestrator-triggered cancel that arrives during
	// dispatch (between handler entry and Execute call) gets burned
	// on one extra LLM call.
	chatCalls := 0
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
				chatCalls++
				return nil, nil
			},
		},
		Tools:     &mockToolExecutor{toolDefs: []llm.ToolDefinition{}},
		ModelName: "test",
		Provider:  "test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exec.Execute(ctx,
		&a2a.Task{ID: "t1"},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hi")}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Execute on pre-cancelled ctx should return context.Canceled, got %v", err)
	}
	if chatCalls != 0 {
		t.Errorf("LLM must not be called on pre-cancelled ctx, got %d calls", chatCalls)
	}
}
