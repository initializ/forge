package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// TestExecute_FirstInteraction_DoesNotDuplicateMessageFromTaskHistory
// is the regression test for issue #143. The runner pre-appends
// params.Message to task.History at the three tasks/send entry points
// before calling Execute, so the executor must skip that trailing
// entry when loading prior history — otherwise the LLM sees two
// consecutive identical user turns and strict-mode providers
// (gpt-5-nano, Together's Kimi gateway) reject the conversation with
// HTTP 400, surfacing as the canned "something went wrong" fallback
// in the channel UI.
func TestExecute_FirstInteraction_DoesNotDuplicateMessageFromTaskHistory(t *testing.T) {
	var capturedMessages []llm.ChatMessage
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedMessages = req.Messages
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "ok"},
				FinishReason: "stop",
			}, nil
		},
	}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         &mockToolExecutor{},
		MaxIterations: 3,
		ModelName:     "test",
		Provider:      "openai",
	})

	// Simulate the runner's pre-append: task.History contains the same
	// message instance that gets passed as *msg.
	msg := &a2a.Message{
		Role: a2a.MessageRoleUser,
		Parts: []a2a.Part{{
			Kind: a2a.PartKindText,
			Text: "[channel:slack channel_target:C123]\nreview PR #82",
		}},
	}
	task := &a2a.Task{
		ID:      "task-history-dedup",
		History: []a2a.Message{*msg}, // ← runner-style pre-append
	}

	if _, err := exec.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Count user-role messages with the exact prefix from the test
	// message. Pre-fix there were 2 (one from task.History, one from
	// *msg). Post-fix there must be exactly 1.
	userCount := 0
	for _, m := range capturedMessages {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "review PR #82") {
			userCount++
		}
	}
	if userCount != 1 {
		t.Errorf("expected exactly 1 user message matching the runner-pre-appended one (issue #143); "+
			"got %d. captured roles: %v", userCount, summarizeMessages(capturedMessages))
	}
}

// TestExecute_FirstInteraction_HistoryWithPriorTurnsPreserved pins the
// non-regression invariant: when task.History contains real prior
// turns followed by a runner-pre-appended duplicate of *msg, the
// prior turns survive intact and only the trailing duplicate gets
// stripped.
func TestExecute_FirstInteraction_HistoryWithPriorTurnsPreserved(t *testing.T) {
	var capturedMessages []llm.ChatMessage
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedMessages = req.Messages
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "ok"},
				FinishReason: "stop",
			}, nil
		},
	}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client:        client,
		Tools:         &mockToolExecutor{},
		MaxIterations: 3,
		ModelName:     "test",
		Provider:      "openai",
	})

	priorUser := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "first question"}}}
	priorAgent := a2a.Message{Role: a2a.MessageRoleAgent, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "first answer"}}}
	currentUser := a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{{Kind: a2a.PartKindText, Text: "followup question"}}}

	task := &a2a.Task{
		ID: "task-prior-turns",
		// Runner-style: prior turns + the just-received user message.
		History: []a2a.Message{priorUser, priorAgent, currentUser},
	}

	if _, err := exec.Execute(context.Background(), task, &currentUser); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Expected memory shape (after system prompt):
	//   user(first question) → assistant(first answer) → user(followup question)
	// NOT
	//   user(first question) → assistant(first answer) → user(followup) → user(followup)
	var userTexts, agentTexts []string
	for _, m := range capturedMessages {
		switch m.Role {
		case llm.RoleUser:
			userTexts = append(userTexts, m.Content)
		case llm.RoleAssistant:
			agentTexts = append(agentTexts, m.Content)
		}
	}
	if len(userTexts) != 2 {
		t.Errorf("expected 2 user messages (first question + followup); got %d: %v",
			len(userTexts), userTexts)
	}
	if len(agentTexts) != 1 || agentTexts[0] != "first answer" {
		t.Errorf("prior agent turn lost or mangled; got %v", agentTexts)
	}
	if len(userTexts) >= 2 && (userTexts[0] != "first question" || userTexts[1] != "followup question") {
		t.Errorf("user-turn order corrupted; got %v", userTexts)
	}
}

// TestLoadFromStore_CollapsesConsecutiveDuplicateUserMessages is the
// defense-in-depth half of the fix. A session written by a pre-#143
// build (containing the runner-pre-append duplicate) gets sanitized
// on load — operators upgrading don't need to `rm` their session
// files to recover working channel threads.
func TestLoadFromStore_CollapsesConsecutiveDuplicateUserMessages(t *testing.T) {
	saved := &SessionData{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "review PR #82"},
			{Role: llm.RoleUser, Content: "review PR #82"}, // duplicate — the issue #143 shape
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
				ID: "tc-1", Type: "function",
				Function: llm.FunctionCall{Name: "code_review_diff", Arguments: `{}`},
			}}},
			{Role: llm.RoleTool, ToolCallID: "tc-1", Content: "<review>"},
			{Role: llm.RoleAssistant, Content: "Here is the review..."},
		},
	}

	mem := NewMemory("system", 100_000, "test")
	mem.LoadFromStore(saved)

	got := mem.Messages()
	// Expected: the duplicate-user pair collapses to one; everything else preserved.
	userMessages := 0
	for _, m := range got {
		if m.Role == llm.RoleUser && m.Content == "review PR #82" {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Errorf("expected exactly 1 user(review PR #82) after collapse; got %d. sequence: %v",
			userMessages, summarizeMessagesShort(got))
	}
	// The downstream conversation must survive untouched.
	if len(got) < 4 {
		t.Errorf("collapse removed too much; expected user → assistant(tc) → tool → assistant, got %v",
			summarizeMessagesShort(got))
	}
}

// TestSanitizeMessages_LeavesDistinctConsecutiveUserMessagesAlone is
// the over-collapse guard: a legitimate user → user sequence (e.g.
// the workflow tracker's "Your response was empty" nudge appearing
// after a real user turn) must NOT get merged. Only EXACT
// content-identical adjacent duplicates collapse.
func TestSanitizeMessages_LeavesDistinctConsecutiveUserMessagesAlone(t *testing.T) {
	saved := &SessionData{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "first message"},
			{Role: llm.RoleUser, Content: "Your response was empty. Please provide a brief summary..."},
			{Role: llm.RoleAssistant, Content: "ok"},
		},
	}
	mem := NewMemory("system", 100_000, "test")
	mem.LoadFromStore(saved)
	got := mem.Messages()

	userCount := 0
	for _, m := range got {
		if m.Role == llm.RoleUser {
			userCount++
		}
	}
	if userCount != 2 {
		t.Errorf("distinct consecutive user messages must survive; got %d user turns: %v",
			userCount, summarizeMessagesShort(got))
	}
}

// TestCollapseConsecutiveDuplicates_DoesNotMergeToolBoundaryMatches
// guards a corner case: a user message whose Content happens to equal
// the previous tool message's Content must not be dropped (different
// roles).
func TestCollapseConsecutiveDuplicates_DoesNotMergeToolBoundaryMatches(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "tc-1"}}},
		{Role: llm.RoleTool, ToolCallID: "tc-1", Content: "result"},
		{Role: llm.RoleUser, Content: "result"}, // same content as tool but role-distinct
	}
	got := collapseConsecutiveDuplicates(in)
	if len(got) != 3 {
		t.Errorf("role-distinct adjacent same-content messages must not collapse; got %d, want 3", len(got))
	}
}

func summarizeMessages(msgs []llm.ChatMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(" → ")
		}
		preview := m.Content
		if len(preview) > 30 {
			preview = preview[:30]
		}
		b.WriteString(string(m.Role) + "(" + preview + ")")
	}
	return b.String()
}

func summarizeMessagesShort(msgs []llm.ChatMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, string(m.Role))
	}
	return "[" + strings.Join(parts, " → ") + "]"
}

// Sanity check: silence unused-import for json/encoding (the test
// file may not call json directly today but the convention in this
// package keeps imports stable across edits).
var _ = json.RawMessage{}
