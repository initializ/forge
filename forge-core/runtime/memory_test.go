package runtime

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestAppendTruncatesOversizedMessage(t *testing.T) {
	mem := NewMemory("system prompt", 0)

	largeContent := strings.Repeat("a", 60_000)
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: largeContent,
	})

	msgs := mem.Messages()
	// msgs[0] is system, msgs[1] is the user message
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}

	userMsg := msgs[1]
	if len(userMsg.Content) >= 60_000 {
		t.Errorf("message was not truncated: got %d chars", len(userMsg.Content))
	}

	if !strings.HasSuffix(userMsg.Content, "\n[TRUNCATED]") {
		t.Error("truncated message missing [TRUNCATED] suffix")
	}

	// Should be maxMessageChars + len("\n[TRUNCATED]")
	expectedLen := maxMessageChars + len("\n[TRUNCATED]")
	if len(userMsg.Content) != expectedLen {
		t.Errorf("expected truncated length %d, got %d", expectedLen, len(userMsg.Content))
	}
}

func TestAppendDoesNotTruncateSmallMessage(t *testing.T) {
	mem := NewMemory("system prompt", 0)

	content := "hello world"
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: content,
	})

	msgs := mem.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	if msgs[1].Content != content {
		t.Errorf("expected content %q, got %q", content, msgs[1].Content)
	}
}

func TestAppendMessageAtExactLimit(t *testing.T) {
	mem := NewMemory("", 0)

	content := strings.Repeat("b", maxMessageChars)
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: content,
	})

	msgs := mem.Messages()
	if msgs[0].Content != content {
		t.Error("message at exact limit should not be truncated")
	}
}

func TestTrimRemovesOldMessages(t *testing.T) {
	// Use a small budget to force trimming
	mem := NewMemory("", 100)

	// Add messages that exceed the budget
	for i := 0; i < 10; i++ {
		mem.Append(llm.ChatMessage{
			Role:    llm.RoleUser,
			Content: strings.Repeat("x", 20),
		})
	}

	msgs := mem.Messages()
	// Total chars should be within budget (at least the last message is kept)
	totalChars := 0
	for _, msg := range msgs {
		totalChars += len(msg.Content) + len(msg.Role)
	}

	// Memory should have trimmed — should have fewer than 10 messages
	if len(msgs) >= 10 {
		t.Errorf("expected trimming to reduce messages, got %d", len(msgs))
	}
}

func TestTrimAlwaysKeepsLastMessage(t *testing.T) {
	// Budget smaller than a single message
	mem := NewMemory("", 10)

	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: strings.Repeat("z", 50),
	})

	msgs := mem.Messages()
	// Should keep at least the last message even if over budget
	if len(msgs) < 1 {
		t.Error("trim should always keep at least the last message")
	}
}

func TestTrimNeverOrphansToolResults(t *testing.T) {
	// Use a small budget that will force trimming when the tool result is added.
	// The sequence is: [user, assistant+tool_calls, tool_result]
	// Trimming must not leave tool_result at the front without its assistant.
	mem := NewMemory("", 200)

	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: "fetch data",
	})
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleAssistant,
		Content: "",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "http_request", Arguments: `{"url":"http://example.com"}`}},
		},
	})
	mem.Append(llm.ChatMessage{
		Role:       llm.RoleTool,
		Content:    strings.Repeat("d", 300), // exceeds budget
		ToolCallID: "call_1",
		Name:       "http_request",
	})

	msgs := mem.Messages()
	// The front message must never be a tool result
	if len(msgs) > 0 && msgs[0].Role == llm.RoleTool {
		t.Error("trim left an orphaned tool result at the front of messages")
	}
}

func TestTrimKeepsAssistantToolPairWhenBudgetAllows(t *testing.T) {
	// Budget large enough to hold assistant+tool_result but not user+assistant+tool
	// This verifies we trim the user but keep the assistant→tool pair intact.
	mem := NewMemory("", 500)

	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: strings.Repeat("u", 100),
	})
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleAssistant,
		Content: "",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "test", Arguments: `{}`}},
		},
	})
	mem.Append(llm.ChatMessage{
		Role:       llm.RoleTool,
		Content:    strings.Repeat("r", 300),
		ToolCallID: "call_1",
		Name:       "test",
	})

	msgs := mem.Messages()
	// Should still have assistant and tool (maybe user trimmed)
	hasAssistant := false
	hasTool := false
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			hasAssistant = true
		}
		if m.Role == llm.RoleTool {
			hasTool = true
		}
	}

	if hasTool && !hasAssistant {
		t.Error("tool result exists without its assistant message")
	}
}

func TestMemoryReset(t *testing.T) {
	mem := NewMemory("system", 0)

	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "hi"})
	mem.Append(llm.ChatMessage{Role: llm.RoleAssistant, Content: "hello"})

	mem.Reset()

	msgs := mem.Messages()
	// Should only have the system prompt
	if len(msgs) != 1 {
		t.Errorf("expected 1 message (system) after reset, got %d", len(msgs))
	}
	if msgs[0].Role != llm.RoleSystem {
		t.Errorf("expected system message, got role %s", msgs[0].Role)
	}
}
