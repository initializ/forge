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
	// Generate a tool result that exceeds the proportional limit.
	// With CharBudget=100_000, the tool limit = 25K, so a 60K result gets truncated.
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
		Client:     client,
		Tools:      tools,
		CharBudget: 100_000, // proportional limit = 25K, so 60K gets truncated
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

func TestProportionalToolTruncation(t *testing.T) {
	tests := []struct {
		name       string
		modelName  string
		charBudget int
		wantLimit  int
	}{
		{"llama3 small context", "llama3", 0, ContextBudgetForModel("llama3") / 4},
		{"gpt-4o large context", "gpt-4o", 0, ContextBudgetForModel("gpt-4o") / 4},
		{"explicit budget", "", 8000, 2000},
		{"floor enforced", "", 4000, 2000}, // 4000/4 = 1000 < 2000 floor
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewLLMExecutor(LLMExecutorConfig{
				Client:     &mockLLMClient{chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) { return nil, nil }},
				ModelName:  tt.modelName,
				CharBudget: tt.charBudget,
			})
			if executor.maxToolResultChars != tt.wantLimit {
				t.Errorf("maxToolResultChars = %d, want %d", executor.maxToolResultChars, tt.wantLimit)
			}
		})
	}
}

func TestToolTruncationUsesProportionalLimit(t *testing.T) {
	// Use a small budget so the tool limit is small (floor of 2K).
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
							{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "big_tool", Arguments: `{}`}},
						},
					},
					FinishReason: "tool_calls",
				}, nil
			}
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "Done"},
				FinishReason: "stop",
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
			return strings.Repeat("x", 5000), nil // 5K chars
		},
		toolDefs: []llm.ToolDefinition{
			{Type: "function", Function: llm.FunctionSchema{Name: "big_tool"}},
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client:     client,
		Tools:      tools,
		CharBudget: 10000, // tool limit = 10000/4 = 2500 (floor 2K), still truncates 5K output
	})

	task := &a2a.Task{ID: "test-proportional"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("do it")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the tool message — it should be truncated to ~2K
	var toolMsg *llm.ChatMessage
	for i := range capturedMessages {
		if capturedMessages[i].Role == llm.RoleTool {
			toolMsg = &capturedMessages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool message")
	}
	if !strings.Contains(toolMsg.Content, "[OUTPUT TRUNCATED") {
		t.Error("tool result should be truncated with proportional limit")
	}
	// Content should be roughly tool limit (2500) + truncation suffix
	if len(toolMsg.Content) > 2700 {
		t.Errorf("tool result too large after truncation: %d chars", len(toolMsg.Content))
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

// ─── Workflow Tracker Tests ──────────────────────────────────────────

func TestToolPhaseClassification(t *testing.T) {
	tests := []struct {
		tool string
		want workflowPhase
	}{
		{"github_clone", phaseSetup},
		{"code_agent_scaffold", phaseSetup},
		{"github_checkout", phaseSetup},
		{"code_agent_read", phaseExplore},
		{"grep_search", phaseExplore},
		{"glob_search", phaseExplore},
		{"directory_tree", phaseExplore},
		{"read_skill", phaseExplore},
		{"github_status", phaseExplore},
		{"github_list_prs", phaseExplore},
		{"github_get_user", phaseExplore},
		{"github_list_stargazers", phaseExplore},
		{"github_list_forks", phaseExplore},
		{"github_pr_author_profiles", phaseExplore},
		{"github_stargazer_profiles", phaseExplore},
		{"code_agent_edit", phaseEdit},
		{"code_agent_write", phaseEdit},
		{"code_agent_patch", phaseEdit},
		{"file_create", phaseEdit},
		{"code_agent_run", phaseEdit},
		{"github_commit", phaseGitOps},
		{"github_push", phaseGitOps},
		{"github_create_pr", phaseGitOps},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			got := toolPhase(tt.tool)
			if got != tt.want {
				t.Errorf("toolPhase(%q) = %d, want %d", tt.tool, got, tt.want)
			}
		})
	}
}

func TestPlanningCheckpointAfter4Reads(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit"})
	writeTools := []string{"code_agent_edit", "code_agent_write"}

	var nudgeCount int
	for i := 0; i < 5; i++ {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
		if msg, ok := wt.generateProactiveNudge(writeTools); ok {
			nudgeCount++
			if !strings.Contains(msg, "PLANNING CHECKPOINT") {
				t.Errorf("iteration %d: expected PLANNING CHECKPOINT nudge, got: %s", i, msg)
			}
		}
	}

	if nudgeCount != 1 {
		t.Errorf("planning checkpoint fired %d times, want exactly 1", nudgeCount)
	}
}

func TestProactiveNudgeEscalation(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit"})
	writeTools := []string{"code_agent_edit"}

	var nudges []string
	for i := 0; i < 9; i++ {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
		if msg, ok := wt.generateProactiveNudge(writeTools); ok {
			nudges = append(nudges, msg)
		}
	}

	if len(nudges) != 3 {
		t.Fatalf("expected 3 nudges, got %d", len(nudges))
	}

	// Nudge 1: planning checkpoint (at consecutive read 4)
	if !strings.Contains(nudges[0], "PLANNING CHECKPOINT") {
		t.Errorf("nudge 0: expected PLANNING CHECKPOINT, got: %s", nudges[0])
	}
	// Nudge 2: transition (at consecutive read 6)
	if !strings.Contains(nudges[1], "Start editing") {
		t.Errorf("nudge 1: expected 'Start editing', got: %s", nudges[1])
	}
	// Nudge 3: urgent (at consecutive read 8)
	if !strings.Contains(nudges[2], "STOP READING") {
		t.Errorf("nudge 2: expected 'STOP READING', got: %s", nudges[2])
	}
}

func TestPlanningCheckpointMentionsOriginTracing(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit"})
	for i := 0; i < 4; i++ {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
	}
	msg, ok := wt.generateProactiveNudge([]string{"code_agent_edit"})
	if !ok {
		t.Fatal("expected planning checkpoint at 4 consecutive reads")
	}
	if !strings.Contains(msg, "traced the error to its origin") {
		t.Errorf("should mention origin tracing, got: %s", msg)
	}
	if !strings.Contains(msg, "read the implementation of every function") {
		t.Errorf("should mention reading implementations, got: %s", msg)
	}
}

func TestNoProactiveNudgeWhenWriting(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit"})
	writeTools := []string{"code_agent_edit"}

	verifyCount := 0
	for i := 0; i < 10; i++ {
		// Alternate read/write — consecutive reads never exceed 1
		if i%2 == 0 {
			wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
		} else {
			wt.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})
		}
		if msg, ok := wt.generateProactiveNudge(writeTools); ok {
			// The one-time verify nudge is expected after the first edit;
			// exploration nudges (PLANNING CHECKPOINT, STOP READING) must not fire.
			if strings.Contains(msg, "VERIFY YOUR FIX") {
				verifyCount++
				continue
			}
			t.Fatalf("unexpected proactive nudge at iteration %d: %s", i, msg)
		}
	}
	if verifyCount != 1 {
		t.Errorf("expected exactly 1 verify nudge, got %d", verifyCount)
	}
}

func TestStopNudgeWorkflowAware(t *testing.T) {
	makeToolDefs := func(names ...string) []llm.ToolDefinition {
		var defs []llm.ToolDefinition
		for _, n := range names {
			defs = append(defs, llm.ToolDefinition{
				Type:     "function",
				Function: llm.FunctionSchema{Name: n},
			})
		}
		return defs
	}

	allTools := []string{
		"grep_search", "code_agent_read", "code_agent_edit",
		"github_status", "github_commit", "github_push", "github_create_pr",
	}

	tests := []struct {
		name      string
		tools     []string // tools the LLM calls across iterations
		wantMsg   string   // substring expected in the stop nudge (empty = no nudge)
		wantNudge bool     // whether a nudge is expected
	}{
		{
			name:      "only reads → no code changes nudge",
			tools:     []string{"grep_search", "code_agent_read"},
			wantMsg:   "without making any code changes",
			wantNudge: true,
		},
		{
			name:      "edits but no git → complete git nudge",
			tools:     []string{"grep_search", "code_agent_edit"},
			wantMsg:   "stopped before git operations",
			wantNudge: true,
		},
		{
			name:      "edits + git → no nudge (workflow complete)",
			tools:     []string{"grep_search", "code_agent_edit", "github_commit", "github_push"},
			wantMsg:   "",
			wantNudge: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callIdx := 0

			client := &mockLLMClient{
				chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
					callIdx++
					if callIdx <= len(tt.tools) {
						return &llm.ChatResponse{
							Message: llm.ChatMessage{
								Role: llm.RoleAssistant,
								ToolCalls: []llm.ToolCall{
									{
										ID:   fmt.Sprintf("call_%d", callIdx),
										Type: "function",
										Function: llm.FunctionCall{
											Name:      tt.tools[callIdx-1],
											Arguments: `{}`,
										},
									},
								},
							},
							FinishReason: "tool_calls",
						}, nil
					}
					// After all tool calls, stop
					return &llm.ChatResponse{
						Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "Done"},
						FinishReason: "stop",
					}, nil
				},
			}

			tools := &mockToolExecutor{
				executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
					return "ok", nil
				},
				toolDefs: makeToolDefs(allTools...),
			}

			executor := NewLLMExecutor(LLMExecutorConfig{
				Client:         client,
				Tools:          tools,
				MaxIterations:  20,
				WorkflowPhases: []string{"edit", "finalize"},
			})

			task := &a2a.Task{ID: "stop-nudge-" + tt.name}
			msg := &a2a.Message{
				Role:  a2a.MessageRoleUser,
				Parts: []a2a.Part{a2a.NewTextPart("do the task")},
			}

			// Execute — the stop nudge triggers a continuation, which gets
			// a second "Done" response that actually returns.
			resp, err := executor.Execute(context.Background(), task, msg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp == nil {
				t.Fatal("expected response")
			}

			// The nudge was injected as a user message. Find it.
			// We can't directly inspect memory, but we know the LLM
			// received the nudge as the last user message before the
			// final "Done" response. So we check via a capturing client.
			// For simplicity, re-run with capturing.
			callIdx = 0
			var capturedMessages []llm.ChatMessage
			client.chatFunc = func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
				callIdx++
				capturedMessages = append([]llm.ChatMessage{}, req.Messages...)
				if callIdx <= len(tt.tools) {
					return &llm.ChatResponse{
						Message: llm.ChatMessage{
							Role: llm.RoleAssistant,
							ToolCalls: []llm.ToolCall{
								{
									ID:   fmt.Sprintf("call_%d", callIdx),
									Type: "function",
									Function: llm.FunctionCall{
										Name:      tt.tools[callIdx-1],
										Arguments: `{}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					}, nil
				}
				return &llm.ChatResponse{
					Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "Done"},
					FinishReason: "stop",
				}, nil
			}

			executor2 := NewLLMExecutor(LLMExecutorConfig{
				Client:         client,
				Tools:          tools,
				MaxIterations:  20,
				WorkflowPhases: []string{"edit", "finalize"},
			})
			task2 := &a2a.Task{ID: "stop-nudge-capture-" + tt.name}
			_, _ = executor2.Execute(context.Background(), task2, msg)

			// Find the nudge in captured messages
			found := false
			for _, m := range capturedMessages {
				if m.Role == "user" && tt.wantMsg != "" && strings.Contains(m.Content, tt.wantMsg) {
					found = true
					break
				}
			}
			if tt.wantNudge && !found {
				t.Errorf("expected nudge containing %q in messages", tt.wantMsg)
			}
			if !tt.wantNudge {
				// Ensure no continuation nudge was injected
				for _, m := range capturedMessages {
					if m.Role == "user" && strings.Contains(m.Content, "You stopped") {
						t.Errorf("expected no nudge for complete workflow, but got: %s", m.Content)
					}
				}
			}
		})
	}
}

func TestProactiveNudgeInjectedMidLoop(t *testing.T) {
	// Integration test: LLM does 6 grep_search calls, then stops.
	// Verify a planning checkpoint user message appears in the conversation.
	callIdx := 0
	var capturedMessages []llm.ChatMessage

	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			capturedMessages = append([]llm.ChatMessage{}, req.Messages...)

			if callIdx <= 6 {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{
							{
								ID:   fmt.Sprintf("call_%d", callIdx),
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "grep_search",
									Arguments: `{}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				}, nil
			}
			// After 6 tool calls, stop
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "Here's what I found"},
				FinishReason: "stop",
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
			return "some search result", nil
		},
		toolDefs: []llm.ToolDefinition{
			{Type: "function", Function: llm.FunctionSchema{Name: "grep_search"}},
			{Type: "function", Function: llm.FunctionSchema{Name: "code_agent_edit"}},
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client:         client,
		Tools:          tools,
		MaxIterations:  20,
		WorkflowPhases: []string{"edit"},
	})

	task := &a2a.Task{ID: "proactive-nudge-midloop"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("find and fix the bug")},
	}

	_, _ = executor.Execute(context.Background(), task, msg)

	// Verify planning checkpoint appeared in messages (injected after 4th read)
	planCheckpointFound := false
	transitionFound := false
	for _, m := range capturedMessages {
		if m.Role == "user" {
			if strings.Contains(m.Content, "PLANNING CHECKPOINT") {
				planCheckpointFound = true
			}
			if strings.Contains(m.Content, "Start editing") {
				transitionFound = true
			}
		}
	}

	if !planCheckpointFound {
		t.Error("expected PLANNING CHECKPOINT nudge in messages after 4 consecutive reads")
	}
	if !transitionFound {
		t.Error("expected transition nudge in messages after 6 consecutive reads")
	}
}

func TestFailedToolDoesNotMarkPhaseOK(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit", "finalize"})

	// Successful edit
	wt.recordIteration([]toolIterResult{{Name: "code_agent_edit", Failed: false}})
	if !wt.phaseOK(phaseEdit) {
		t.Error("expected phaseEdit to be OK after successful edit")
	}

	// Failed commit — phaseGitOps is "seen" but NOT "OK"
	wt.recordIteration([]toolIterResult{
		{Name: "github_commit", Failed: true},
		{Name: "github_push", Failed: false},
	})
	if !wt.phaseSeen[phaseGitOps] {
		t.Error("expected phaseGitOps to be seen after attempted commit")
	}
	if wt.phaseOK(phaseGitOps) {
		t.Error("expected phaseGitOps NOT OK because commit failed")
	}
	if !wt.phaseHasError[phaseGitOps] {
		t.Error("expected phaseHasError[phaseGitOps] to be true")
	}
}

func TestReNudgeOnIncompleteWorkflow(t *testing.T) {
	// Simulates the production bug: agent edits, commit fails, agent stops
	// twice. The second stop should still get a nudge because the workflow
	// is incomplete (git ops had errors).
	callIdx := 0
	var lastCapturedMessages []llm.ChatMessage

	toolSequence := []struct {
		name string
		fail bool
	}{
		{"grep_search", false},
		{"code_agent_edit", false},
		{"github_status", false},
		{"github_commit", true}, // fails
		{"github_push", false},  // succeeds but commit didn't
	}

	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			lastCapturedMessages = append([]llm.ChatMessage{}, req.Messages...)

			if callIdx <= len(toolSequence) {
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{
							{
								ID:   fmt.Sprintf("call_%d", callIdx),
								Type: "function",
								Function: llm.FunctionCall{
									Name:      toolSequence[callIdx-1].name,
									Arguments: `{}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				}, nil
			}

			// After tools: stop with text. Do this 3 times to test
			// that we get 2 nudges (re-nudge on incomplete workflow).
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "Not complete yet"},
				FinishReason: "stop",
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
			for _, ts := range toolSequence {
				if ts.name == name && ts.fail {
					return "", fmt.Errorf("no changes staged to commit")
				}
			}
			return "ok", nil
		},
		toolDefs: []llm.ToolDefinition{
			{Type: "function", Function: llm.FunctionSchema{Name: "grep_search"}},
			{Type: "function", Function: llm.FunctionSchema{Name: "code_agent_edit"}},
			{Type: "function", Function: llm.FunctionSchema{Name: "github_status"}},
			{Type: "function", Function: llm.FunctionSchema{Name: "github_commit"}},
			{Type: "function", Function: llm.FunctionSchema{Name: "github_push"}},
			{Type: "function", Function: llm.FunctionSchema{Name: "github_create_pr"}},
		},
	}

	executor := NewLLMExecutor(LLMExecutorConfig{
		Client:         client,
		Tools:          tools,
		MaxIterations:  20,
		WorkflowPhases: []string{"edit", "finalize"},
	})

	task := &a2a.Task{ID: "re-nudge-test"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("fix the bug and create PR")},
	}

	_, _ = executor.Execute(context.Background(), task, msg)

	// Count nudge messages in the final captured messages.
	// With the fix, we should see 2 nudges: first "git ops FAILED",
	// second "stopped AGAIN without calling tools".
	nudgeCount := 0
	hasFailedNudge := false
	hasReNudge := false
	for _, m := range lastCapturedMessages {
		if m.Role == "user" {
			if strings.Contains(m.Content, "FAILED") {
				hasFailedNudge = true
				nudgeCount++
			}
			if strings.Contains(m.Content, "stopped AGAIN") {
				hasReNudge = true
				nudgeCount++
			}
		}
	}

	if !hasFailedNudge {
		t.Error("expected a nudge mentioning git ops FAILED")
	}
	if !hasReNudge {
		t.Error("expected a re-nudge ('stopped AGAIN') on second stop with incomplete workflow")
	}
	if nudgeCount < 2 {
		t.Errorf("expected at least 2 nudges for incomplete workflow, got %d", nudgeCount)
	}
}

func TestNoEditNudgeForQueryOnlySkill(t *testing.T) {
	// Tracker with no edit/finalize phases (query-only or no phases).
	// 6 consecutive reads should NOT fire "start editing" nudge.
	// Only fire gentle nudge at 8.
	for _, phases := range [][]string{{}, {"query"}} {
		t.Run(fmt.Sprintf("phases=%v", phases), func(t *testing.T) {
			wt := newWorkflowTracker(phases)
			writeTools := []string{"code_agent_edit"}

			var nudges []string
			for i := 0; i < 9; i++ {
				wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
				if msg, ok := wt.generateProactiveNudge(writeTools); ok {
					nudges = append(nudges, msg)
				}
			}

			// Should get exactly 1 nudge (gentle at 8), not 3 (plan+transition+urgent)
			if len(nudges) != 1 {
				t.Fatalf("expected 1 gentle nudge, got %d: %v", len(nudges), nudges)
			}
			if !strings.Contains(nudges[0], "provide your analysis") {
				t.Errorf("expected gentle query nudge, got: %s", nudges[0])
			}
			// Must NOT contain edit-specific nudge language
			if strings.Contains(nudges[0], "STOP READING") || strings.Contains(nudges[0], "PLANNING CHECKPOINT") {
				t.Errorf("query-only skill should not get edit nudges, got: %s", nudges[0])
			}
		})
	}
}

func TestVerifyNudgeAfterFirstEdit(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit", "finalize"})
	// Simulate: 3 reads then 1 edit
	for i := 0; i < 3; i++ {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
	}
	wt.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})
	// Next iteration (1 iter since edit) — should fire verify nudge
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read"}})
	msg, ok := wt.generateProactiveNudge([]string{"code_agent_edit"})
	if !ok {
		t.Fatal("expected verification nudge after first edit")
	}
	if !strings.Contains(msg, "VERIFY YOUR FIX") {
		t.Errorf("expected VERIFY YOUR FIX nudge, got: %s", msg)
	}
	// Should not fire again
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read"}})
	_, ok = wt.generateProactiveNudge([]string{"code_agent_edit"})
	if ok {
		t.Error("verification nudge should fire only once")
	}
}

func TestVerifyNudgeNotFiredForFeatures(t *testing.T) {
	// Tracker without edit phase — no verification nudge
	wt := newWorkflowTracker([]string{})
	wt.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read"}})
	_, ok := wt.generateProactiveNudge([]string{"code_agent_edit"})
	if ok {
		t.Error("verification nudge should not fire without edit workflow phase")
	}
}

func TestNoGitNudgeWithoutFinalize(t *testing.T) {
	// Tracker with only edit phase (no finalize).
	// After edits, 4+ iterations should NOT fire git workflow nudge.
	wt := newWorkflowTracker([]string{"edit"})
	writeTools := []string{"code_agent_edit"}

	// Do an edit
	wt.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})

	// 5 more iterations of reads — should NOT trigger git nudge
	var nudges []string
	for i := 0; i < 5; i++ {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
		if msg, ok := wt.generateProactiveNudge(writeTools); ok {
			nudges = append(nudges, msg)
		}
	}

	for _, nudge := range nudges {
		if strings.Contains(nudge, "committed") || strings.Contains(nudge, "git workflow") {
			t.Errorf("should not get git nudge without finalize phase, got: %s", nudge)
		}
	}

	// Also verify stop nudge says "summarize" not "commit/push/PR"
	wt2 := newWorkflowTracker([]string{"edit"})
	wt2.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})
	// workflowIncomplete should be false (edit is OK, finalize not required)
	if wt2.requireFinalize {
		t.Error("requireFinalize should be false for edit-only phases")
	}
	if !wt2.phaseOK(phaseEdit) {
		t.Error("phaseEdit should be OK after successful edit")
	}
}

func TestWorkflowIncompleteWithPhases(t *testing.T) {
	tests := []struct {
		name           string
		phases         []string
		tools          []toolIterResult
		wantIncomplete bool
	}{
		{
			name:           "edit+finalize, nothing done",
			phases:         []string{"edit", "finalize"},
			tools:          []toolIterResult{{Name: "grep_search"}},
			wantIncomplete: true,
		},
		{
			name:           "edit+finalize, edit done",
			phases:         []string{"edit", "finalize"},
			tools:          []toolIterResult{{Name: "code_agent_edit"}},
			wantIncomplete: true, // finalize still missing
		},
		{
			name:   "edit+finalize, both done",
			phases: []string{"edit", "finalize"},
			tools: []toolIterResult{
				{Name: "code_agent_edit"},
				{Name: "github_commit"},
				{Name: "github_push"},
			},
			wantIncomplete: false,
		},
		{
			name:           "edit only, edit done",
			phases:         []string{"edit"},
			tools:          []toolIterResult{{Name: "code_agent_edit"}},
			wantIncomplete: false,
		},
		{
			name:           "edit only, nothing done",
			phases:         []string{"edit"},
			tools:          []toolIterResult{{Name: "grep_search"}},
			wantIncomplete: true,
		},
		{
			name:           "query only, nothing done",
			phases:         []string{"query"},
			tools:          []toolIterResult{{Name: "grep_search"}},
			wantIncomplete: false, // query doesn't require edit or finalize
		},
		{
			name:           "no phases, nothing done",
			phases:         []string{},
			tools:          []toolIterResult{{Name: "grep_search"}},
			wantIncomplete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wt := newWorkflowTracker(tt.phases)
			wt.recordIteration(tt.tools)

			incomplete := (wt.requireEdit && !wt.phaseOK(phaseEdit)) ||
				(wt.requireFinalize && !wt.phaseOK(phaseGitOps))

			if incomplete != tt.wantIncomplete {
				t.Errorf("workflowIncomplete = %v, want %v", incomplete, tt.wantIncomplete)
			}
		})
	}
}

// ─── File Re-read Detection Tests ────────────────────────────────────

func TestExtractReadFilePath(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     string
		wantPath string
	}{
		{"file_read with path", "file_read", `{"path":"/src/main.ts"}`, "/src/main.ts"},
		{"file_read with file_path", "file_read", `{"file_path":"/src/app.go"}`, "/src/app.go"},
		{"code_agent_read with path", "code_agent_read", `{"path":"/lib/utils.js"}`, "/lib/utils.js"},
		{"code_agent_read file_path takes precedence", "code_agent_read", `{"path":"a","file_path":"b"}`, "b"},
		{"grep_search ignored", "grep_search", `{"path":"/src/main.ts"}`, ""},
		{"code_agent_edit ignored", "code_agent_edit", `{"path":"/src/main.ts"}`, ""},
		{"invalid JSON", "file_read", `{bad json`, ""},
		{"missing path fields", "file_read", `{"query":"test"}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReadFilePath(tt.tool, tt.args)
			if got != tt.wantPath {
				t.Errorf("extractReadFilePath(%q, %q) = %q, want %q", tt.tool, tt.args, got, tt.wantPath)
			}
		})
	}
}

func TestReReadNudgeFires(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit"})
	writeTools := []string{"code_agent_edit"}

	// Read the same file twice
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read", FilePath: "/src/web-search.ts"}})
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read", FilePath: "/src/web-search.ts"}})

	msg, ok := wt.generateProactiveNudge(writeTools)
	if !ok {
		t.Fatal("expected re-read nudge to fire")
	}
	if !strings.Contains(msg, "STOP RE-READING FILES") {
		t.Errorf("expected STOP RE-READING FILES nudge, got: %s", msg)
	}
	if !strings.Contains(msg, "/src/web-search.ts") {
		t.Errorf("nudge should mention the re-read file, got: %s", msg)
	}

	// Should not fire again
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read", FilePath: "/src/web-search.ts"}})
	_, ok = wt.generateProactiveNudge(writeTools)
	if ok {
		t.Error("re-read nudge should fire only once")
	}
}

func TestReReadNudgeNotForDifferentFiles(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit"})
	writeTools := []string{"code_agent_edit"}

	// Read 3 different files — no re-reads
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read", FilePath: "/src/a.ts"}})
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read", FilePath: "/src/b.ts"}})
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read", FilePath: "/src/c.ts"}})

	msg, ok := wt.generateProactiveNudge(writeTools)
	// At 3 consecutive reads we're below the planning checkpoint threshold (4),
	// so no nudge should fire at all.
	if ok && strings.Contains(msg, "STOP RE-READING") {
		t.Errorf("should not fire re-read nudge for different files, got: %s", msg)
	}
}

func TestCodeAgentRunIsWriteAction(t *testing.T) {
	if !isWriteActionTool("code_agent_run") {
		t.Error("isWriteActionTool(\"code_agent_run\") should return true")
	}
}

func TestGitNudgeIncludesVerifyReminder(t *testing.T) {
	wt := newWorkflowTracker([]string{"edit", "finalize"})

	// Simulate: edit, then verify nudge fires, then 4+ iterations of reads
	wt.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})
	wt.recordIteration([]toolIterResult{{Name: "code_agent_read"}})
	// Trigger verify nudge
	wt.generateProactiveNudge([]string{"code_agent_edit"})
	if !wt.verifyNudgeDone {
		t.Fatal("expected verifyNudgeDone to be true after verify nudge")
	}

	// Now simulate 4 more read iterations to trigger git nudge
	for range 4 {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
	}

	msg, ok := wt.generateProactiveNudge([]string{"code_agent_edit"})
	if !ok {
		t.Fatal("expected git nudge to fire")
	}
	if !strings.Contains(msg, "BEFORE committing") {
		t.Errorf("git nudge should include verification reminder, got: %s", msg)
	}
	if !strings.Contains(msg, "RUNTIME behavior") {
		t.Errorf("git nudge should mention RUNTIME behavior, got: %s", msg)
	}
	if !strings.Contains(msg, "github_status") {
		t.Errorf("git nudge should still include git workflow steps, got: %s", msg)
	}
}

func TestGitNudgeNoVerifyReminderWithoutEditPhase(t *testing.T) {
	// Tracker with finalize but NOT edit — git nudge should NOT include verify reminder
	wt := newWorkflowTracker([]string{"finalize"})

	// Mark edit as done (even though not required)
	wt.recordIteration([]toolIterResult{{Name: "code_agent_edit"}})
	// 5 iterations of reads
	for range 5 {
		wt.recordIteration([]toolIterResult{{Name: "grep_search"}})
	}

	msg, ok := wt.generateProactiveNudge([]string{"code_agent_edit"})
	if !ok {
		t.Fatal("expected git nudge to fire")
	}
	if strings.Contains(msg, "BEFORE committing") {
		t.Errorf("git nudge without edit requirement should not include verify reminder, got: %s", msg)
	}
	if !strings.Contains(msg, "github_status") {
		t.Errorf("git nudge should still include git workflow steps, got: %s", msg)
	}
}

func TestNoStopNudgeForQAConversation(t *testing.T) {
	// When no workflow phases are configured and the agent only uses
	// explore-phase tools (e.g. web_search), it's a Q&A conversation.
	// The stop nudge should NOT fire — the agent's text response is the answer.
	makeToolDefs := func(names ...string) []llm.ToolDefinition {
		var defs []llm.ToolDefinition
		for _, n := range names {
			defs = append(defs, llm.ToolDefinition{
				Type:     "function",
				Function: llm.FunctionSchema{Name: n},
			})
		}
		return defs
	}

	callIdx := 0
	var capturedMessages []llm.ChatMessage

	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			capturedMessages = append([]llm.ChatMessage{}, req.Messages...)
			if callIdx == 1 {
				// First call: LLM calls web_search
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "web_search",
									Arguments: `{"query":"top news today"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				}, nil
			}
			// Second call: LLM provides answer
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "Here are the top headlines..."},
				FinishReason: "stop",
			}, nil
		},
	}

	tools := &mockToolExecutor{
		executeFunc: func(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
			return `{"results":[{"title":"News headline"}]}`, nil
		},
		toolDefs: makeToolDefs("web_search"),
	}

	// No workflow phases — this is a general-purpose agent
	executor := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         tools,
		MaxIterations: 20,
	})

	task := &a2a.Task{ID: "qa-no-nudge"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("what are the top news?")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}

	// Verify no "You stopped" nudge was injected
	for _, m := range capturedMessages {
		if m.Role == "user" && strings.Contains(m.Content, "You stopped") {
			t.Errorf("Q&A conversation should not get stop nudge, but got: %s", m.Content)
		}
	}

	// Should have exactly 2 LLM calls (tool call + answer), not 3 (+ nudge response)
	if callIdx != 2 {
		t.Errorf("expected 2 LLM calls for Q&A, got %d", callIdx)
	}
}
