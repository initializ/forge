package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// TestExecute_EmptyAssistantTurn_NotPersistedAsInvalidShape is the
// regression test for issue #131. When the LLM hits `finish_reason: length`
// and returns an assistant message with empty content AND no tool calls,
// the executor MUST NOT persist that invalid shape into memory. The
// fix substitutes a placeholder content string so the assistant turn
// remains a valid chat-completions message.
//
// Before the fix, memory ended with the sequence
//
//	... assistant(content="", tool_calls=[]) → user(nudge) → assistant(real)
//
// and persistSession wrote that verbatim to disk. The next request that
// recovered the session sent the empty assistant turn to the provider,
// which rejected the conversation with HTTP 400 — every Slack-thread
// followup turned into "something went wrong while processing your
// request, please try again."
//
// After the fix, the first turn is normalized to
//
//	... assistant(content=emptyAssistantPlaceholder, tool_calls=[]) → user(nudge) → assistant(real)
//
// which strict OpenAI-spec validators accept.
func TestExecute_EmptyAssistantTurn_NotPersistedAsInvalidShape(t *testing.T) {
	call := 0
	client := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			call++
			switch call {
			case 1:
				// Iteration 1: tool call. Produces a tool message that
				// satisfies the "i > 0 → fire continuation nudge" gate
				// on the next iteration's empty response.
				return &llm.ChatResponse{
					Message: llm.ChatMessage{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID:       "tc-1",
							Type:     "function",
							Function: llm.FunctionCall{Name: "echo", Arguments: `{}`},
						}},
					},
					FinishReason: "tool_calls",
				}, nil
			case 2:
				// Iteration 2: empty content, no tool calls,
				// finish_reason=length — the exact shape that causes
				// session poisoning.
				return &llm.ChatResponse{
					Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: ""},
					FinishReason: "length",
				}, nil
			default:
				// Empty-response recovery retry: now produces real content.
				return &llm.ChatResponse{
					Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "here is the summary"},
					FinishReason: "stop",
				}, nil
			}
		},
	}
	tools := &mockToolExecutor{
		executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return "echoed", nil
		},
	}
	// Persist to a temp store so we can inspect the saved messages
	// after Execute returns.
	store, err := NewMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: client, Tools: tools, MaxIterations: 5,
		ModelName: "test", Provider: "openai", Store: store,
	})

	task := &a2a.Task{ID: "task-empty-turn"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{
		{Kind: a2a.PartKindText, Text: "hi"},
	}}
	if _, err := exec.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	saved, err := store.Load(task.ID)
	if err != nil || saved == nil {
		t.Fatalf("Load: err=%v saved=%v", err, saved)
	}

	// Scan every persisted message: no assistant turn may have empty
	// content AND no tool_calls — that is the invalid shape the bug
	// produced.
	for i, m := range saved.Messages {
		if m.Role == llm.RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Errorf("persisted message %d is an assistant with empty content + no tool_calls "+
				"(the issue #131 shape); full sequence: %s", i, summarizeRoles(saved.Messages))
		}
	}

	// Positive check: the placeholder substitution should have happened
	// exactly once (the empty turn). Other assistant turns are unchanged.
	placeholderHits := 0
	for _, m := range saved.Messages {
		if m.Role == llm.RoleAssistant && m.Content == emptyAssistantPlaceholder {
			placeholderHits++
		}
	}
	if placeholderHits != 1 {
		t.Errorf("expected exactly 1 placeholder-substituted assistant turn; got %d. sequence: %s",
			placeholderHits, summarizeRoles(saved.Messages))
	}
}

// TestLoadFromStore_StripsLegacyEmptyAssistantTurns is the
// defense-in-depth half of the fix: even a session written by a
// pre-#131 build (which would have persisted the raw empty-content
// assistant message) gets sanitized on recovery. No on-disk
// migration is required — operators can upgrade without losing
// long-running channel threads, and the bad shape never reaches the
// provider.
func TestLoadFromStore_StripsLegacyEmptyAssistantTurns(t *testing.T) {
	// Construct a SessionData mirroring the persisted shape from the
	// real Slack reproducer in issue #131:
	//   user, assistant(tool_calls), tool, assistant(content="",tc=[]),
	//   user(nudge), assistant(real)
	saved := &SessionData{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "review PR #82"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
				ID: "tc-1", Type: "function",
				Function: llm.FunctionCall{Name: "gh_diff", Arguments: `{}`},
			}}},
			{Role: llm.RoleTool, ToolCallID: "tc-1", Content: "<diff>"},
			// THE POISON — pre-#131 builds wrote this verbatim.
			{Role: llm.RoleAssistant, Content: "", ToolCalls: nil},
			{Role: llm.RoleUser, Content: "Your response was empty. Please provide a brief summary..."},
			{Role: llm.RoleAssistant, Content: "Manual review of PR #82..."},
		},
	}

	mem := NewMemory("system", 100_000, "test")
	mem.LoadFromStore(saved)

	// The poison must be gone.
	for i, m := range mem.Messages() {
		if m.Role == llm.RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Errorf("recovered message %d still has the issue #131 shape; sequence: %s",
				i, summarizeRoles(mem.Messages()))
		}
	}

	// And the real content (both the tool-call assistant and the final
	// summary assistant) must survive — sanitization must NOT be
	// over-eager.
	got := summarizeRoles(mem.Messages())
	for _, want := range []string{"user(review PR", "assistant[tc:tc-1]", "tool", "user(Your response", "assistant(Manual review"} {
		if !strings.Contains(got, want) {
			t.Errorf("recovered sequence missing %q; got %s", want, got)
		}
	}
}

// TestExecute_RecoveredSessionRoundTripsThroughStrictValidator pairs
// the persistence-side fix and the recovery-side fix end-to-end. We
// simulate a strict OpenAI-spec provider that returns HTTP 400 on
// any assistant turn with empty content AND no tool calls. The
// session from the first task gets persisted, the executor recovers
// it for the second task, and the second task must succeed — which
// it only does if NEITHER pipeline (persist or recover) lets the
// empty turn reach the strict validator.
func TestExecute_RecoveredSessionRoundTripsThroughStrictValidator(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// Strict provider: rejects conversations containing the bad turn.
	strictClient := &mockLLMClient{
		chatFunc: func(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			for i, m := range req.Messages {
				if m.Role == llm.RoleAssistant && strings.TrimSpace(m.Content) == "" && len(m.ToolCalls) == 0 {
					return nil, errStrictValidatorRejected(i)
				}
			}
			return &llm.ChatResponse{
				Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "ack"},
				FinishReason: "stop",
			}, nil
		},
	}

	// First-task client: forces the empty-turn path so persistence has
	// something to sanitize. After the in-loop recovery, it returns a
	// real response.
	first := 0
	firstTaskClient := &mockLLMClient{
		chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
			first++
			switch first {
			case 1:
				return &llm.ChatResponse{Message: llm.ChatMessage{
					Role: llm.RoleAssistant,
					ToolCalls: []llm.ToolCall{{ID: "tc-1", Type: "function",
						Function: llm.FunctionCall{Name: "noop", Arguments: `{}`}}},
				}, FinishReason: "tool_calls"}, nil
			case 2:
				return &llm.ChatResponse{Message: llm.ChatMessage{
					Role: llm.RoleAssistant, Content: "",
				}, FinishReason: "length"}, nil
			default:
				return &llm.ChatResponse{Message: llm.ChatMessage{
					Role: llm.RoleAssistant, Content: "first-task done",
				}, FinishReason: "stop"}, nil
			}
		},
	}
	tools := &mockToolExecutor{executeFunc: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		return "ok", nil
	}}

	// Task 1 — produces the empty-turn shape in-memory, recovers via
	// the loop's existing empty-response branch, persists.
	exec1 := NewLLMExecutor(LLMExecutorConfig{
		Client: firstTaskClient, Tools: tools, MaxIterations: 5,
		ModelName: "x", Provider: "openai", Store: store,
	})
	task := &a2a.Task{ID: "recovery-roundtrip"}
	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{
		{Kind: a2a.PartKindText, Text: "hi"},
	}}
	if _, err := exec1.Execute(context.Background(), task, msg); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	// Task 2 — same task_id (Slack-thread-style continuation). The
	// session is recovered from disk. The strict validator sees the
	// recovered conversation; if either persistence (Option A2) or
	// recovery (Option B) leaks the empty turn, this fails.
	exec2 := NewLLMExecutor(LLMExecutorConfig{
		Client: strictClient, Tools: tools, MaxIterations: 5,
		ModelName: "x", Provider: "openai", Store: store,
	})
	followup := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{
		{Kind: a2a.PartKindText, Text: "what about the unexported helper?"},
	}}
	if _, err := exec2.Execute(context.Background(), task, followup); err != nil {
		t.Fatalf("recovered followup must succeed; got: %v", err)
	}
}

// TestStripEmptyAssistantTurns_PreservesValidMessages confirms the
// strip pass is surgical — it removes only the bad shape, not
// adjacent legitimate turns.
func TestStripEmptyAssistantTurns_PreservesValidMessages(t *testing.T) {
	in := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "hello"},
		{Role: llm.RoleAssistant, Content: "hi there"},                     // valid — keep
		{Role: llm.RoleAssistant, Content: "", ToolCalls: nil},             // poison — drop
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "tc-1"}}}, // valid — has tool_calls
		{Role: llm.RoleTool, ToolCallID: "tc-1", Content: "result"},
		{Role: llm.RoleAssistant, Content: emptyAssistantPlaceholder}, // post-Option-A2 placeholder — keep
		{Role: llm.RoleUser, Content: "thanks"},
	}
	got := stripEmptyAssistantTurns(in)
	if len(got) != 6 {
		t.Errorf("expected 6 messages after strip, got %d: %s", len(got), summarizeRoles(got))
	}
	for _, m := range got {
		if m.Role == llm.RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Errorf("poison shape survived strip; sequence: %s", summarizeRoles(got))
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────

func summarizeRoles(msgs []llm.ChatMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(" → ")
		}
		switch m.Role {
		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				ids := make([]string, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					ids = append(ids, tc.ID)
				}
				b.WriteString("assistant[tc:" + strings.Join(ids, ",") + "]")
			} else {
				preview := m.Content
				if len(preview) > 20 {
					preview = preview[:20]
				}
				b.WriteString("assistant(" + preview + ")")
			}
		case llm.RoleUser:
			preview := m.Content
			if len(preview) > 20 {
				preview = preview[:20]
			}
			b.WriteString("user(" + preview + ")")
		case llm.RoleTool:
			b.WriteString("tool")
		default:
			b.WriteString(string(m.Role))
		}
	}
	return b.String()
}

func errStrictValidatorRejected(idx int) error {
	return errors.New("strict validator: message at index " + strconvItoa(idx) +
		" is an assistant turn with empty content and no tool_calls (HTTP 400)")
}

// Local helper to avoid colliding with audit_payload_capture.go's itoa
// while keeping this test file zero-import beyond what's already used.
func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
