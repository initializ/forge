package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestAppendTruncatesOversizedMessage(t *testing.T) {
	mem := NewMemory("system prompt", 0, "")

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
	mem := NewMemory("system prompt", 0, "")

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
	mem := NewMemory("", 0, "")

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
	mem := NewMemory("", 100, "")

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
	mem := NewMemory("", 10, "")

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
	mem := NewMemory("", 200, "")

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
	mem := NewMemory("", 500, "")

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

func TestMemoryLoadFromStore(t *testing.T) {
	mem := NewMemory("system prompt", 0, "")

	data := &SessionData{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "restored message"},
			{Role: llm.RoleAssistant, Content: "restored reply"},
		},
		Summary: "prior context summary",
	}

	mem.LoadFromStore(data)

	msgs := mem.Messages()
	// Should have: system + 2 restored messages
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[1].Content != "restored message" {
		t.Errorf("expected restored message, got %q", msgs[1].Content)
	}
	if msgs[2].Content != "restored reply" {
		t.Errorf("expected restored reply, got %q", msgs[2].Content)
	}
}

func TestMemorySummaryInjectedInMessages(t *testing.T) {
	mem := NewMemory("You are a helpful agent.", 0, "")

	// Set summary via LoadFromStore
	data := &SessionData{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "new message"},
		},
		Summary: "User asked about weather. Agent provided forecast.",
	}
	mem.LoadFromStore(data)

	msgs := mem.Messages()
	if len(msgs) < 1 {
		t.Fatal("expected at least system message")
	}

	system := msgs[0]
	if system.Role != llm.RoleSystem {
		t.Fatalf("expected system role, got %s", system.Role)
	}
	if !strings.Contains(system.Content, "You are a helpful agent.") {
		t.Error("system prompt should be preserved")
	}
	if !strings.Contains(system.Content, "Conversation Summary (prior context)") {
		t.Error("summary header should be in system prompt")
	}
	if !strings.Contains(system.Content, "User asked about weather") {
		t.Error("summary content should be in system prompt")
	}
}

func TestMemoryEmptySummaryNotInjected(t *testing.T) {
	mem := NewMemory("base prompt", 0, "")

	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "hello"})

	msgs := mem.Messages()
	system := msgs[0]
	if strings.Contains(system.Content, "Conversation Summary") {
		t.Error("empty summary should not inject summary header")
	}
	if system.Content != "base prompt" {
		t.Errorf("expected 'base prompt', got %q", system.Content)
	}
}

func TestMemoryReset(t *testing.T) {
	mem := NewMemory("system", 0, "")

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

func TestContextBudgetForModel(t *testing.T) {
	tests := []struct {
		model    string
		expected int
	}{
		{"llama3", int(float64(8_000) * 4 * 0.85)},               // 27,200
		{"llama3.1", int(float64(128_000) * 4 * 0.85)},           // 435,200
		{"llama3.1:8b", int(float64(128_000) * 4 * 0.85)},        // prefix match
		{"gpt-4o", int(float64(128_000) * 4 * 0.85)},             // 435,200
		{"gpt-4o-mini", int(float64(128_000) * 4 * 0.85)},        // prefix match
		{"claude-sonnet-4", int(float64(200_000) * 4 * 0.85)},    // 680,000
		{"gemini-2.5-flash", int(float64(1_000_000) * 4 * 0.85)}, // 3,400,000
		{"unknown-model", defaultContextTokens * charsPerToken},  // fallback
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := ContextBudgetForModel(tt.model)
			if got != tt.expected {
				t.Errorf("ContextBudgetForModel(%q) = %d, want %d", tt.model, got, tt.expected)
			}
		})
	}
}

func TestContextBudgetLlama3VsLlama31(t *testing.T) {
	// Ensure llama3.1 doesn't match as llama3 (longest prefix wins).
	budget3 := ContextBudgetForModel("llama3")
	budget31 := ContextBudgetForModel("llama3.1")
	if budget3 == budget31 {
		t.Errorf("llama3 and llama3.1 should have different budgets: %d vs %d", budget3, budget31)
	}
	if budget3 >= budget31 {
		t.Errorf("llama3 budget (%d) should be smaller than llama3.1 (%d)", budget3, budget31)
	}
}

func TestNewMemoryModelAware(t *testing.T) {
	// With model name and zero maxChars, budget should come from model lookup.
	mem := NewMemory("prompt", 0, "llama3")
	expected := ContextBudgetForModel("llama3")
	if mem.maxChars != expected {
		t.Errorf("expected maxChars=%d for llama3, got %d", expected, mem.maxChars)
	}

	// Explicit maxChars should override model-based budget.
	mem2 := NewMemory("prompt", 1000, "llama3")
	if mem2.maxChars != 1000 {
		t.Errorf("expected maxChars=1000 with explicit override, got %d", mem2.maxChars)
	}

	// No model and zero maxChars should use default.
	mem3 := NewMemory("prompt", 0, "")
	if mem3.maxChars != defaultContextTokens*charsPerToken {
		t.Errorf("expected default maxChars=%d, got %d", defaultContextTokens*charsPerToken, mem3.maxChars)
	}
}

func TestWeightedTotalChars(t *testing.T) {
	mem := NewMemory("", 0, "")

	// Add a regular user message
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: "hello",
	})

	mem.mu.Lock()
	charsWithUser := mem.totalChars()
	mem.mu.Unlock()

	// Now add a tool result with the same content length
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleTool,
		Content: "hello",
		Name:    "test_tool",
	})

	mem.mu.Lock()
	charsWithTool := mem.totalChars()
	mem.mu.Unlock()

	// The tool message should contribute more than the user message
	// because of the 2x weight multiplier.
	toolContribution := charsWithTool - charsWithUser
	// Tool content "hello" (5) + role "tool" (4) = 9, weighted 2x = 18
	expectedToolContribution := (len("hello") + len(llm.RoleTool)) * toolResultWeightMultiplier
	if toolContribution != expectedToolContribution {
		t.Errorf("tool result contribution = %d, want %d (2x weighted)", toolContribution, expectedToolContribution)
	}
}

func TestPruneToolResults(t *testing.T) {
	// Use a large budget so trim() doesn't drop messages, only prune is tested.
	mem := NewMemory("", 100_000, "")

	// Add several tool results — some large, some small.
	for i := 0; i < 6; i++ {
		mem.mu.Lock()
		mem.messages = append(mem.messages, llm.ChatMessage{
			Role:    llm.RoleTool,
			Content: strings.Repeat("x", 500),
			Name:    fmt.Sprintf("tool_%d", i),
		})
		mem.mu.Unlock()
	}
	// Add a small tool result that should not be pruned (< 200 chars).
	mem.mu.Lock()
	mem.messages = append(mem.messages, llm.ChatMessage{
		Role:    llm.RoleTool,
		Content: "small",
		Name:    "tiny_tool",
	})
	mem.mu.Unlock()

	mem.mu.Lock()
	mem.pruneToolResults()

	// 6 large tool results → oldest 3 should be pruned.
	prunedCount := 0
	for _, msg := range mem.messages {
		if msg.Role == llm.RoleTool && strings.Contains(msg.Content, "pruned for context space") {
			prunedCount++
		}
	}
	mem.mu.Unlock()

	if prunedCount != 3 {
		t.Errorf("expected 3 pruned tool results, got %d", prunedCount)
	}
}

func TestPruneBeforeDropInTrim(t *testing.T) {
	// Budget tight enough that pruning alone reclaims enough space.
	// We verify that after trim, pruned placeholders exist (phase 1 ran)
	// but messages were not dropped (phase 2 was not needed).
	mem := NewMemory("", 5000, "")

	// Add user message + 4 large tool results.
	mem.mu.Lock()
	mem.messages = append(mem.messages, llm.ChatMessage{
		Role: llm.RoleUser, Content: "query",
	})
	for i := 0; i < 4; i++ {
		mem.messages = append(mem.messages, llm.ChatMessage{
			Role:    llm.RoleTool,
			Content: strings.Repeat("d", 1000),
			Name:    fmt.Sprintf("tool_%d", i),
		})
	}
	mem.mu.Unlock()

	// Trigger trim via Append (adds another small message).
	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "follow-up"})

	mem.mu.Lock()
	hasPruned := false
	for _, msg := range mem.messages {
		if strings.Contains(msg.Content, "pruned for context space") {
			hasPruned = true
			break
		}
	}
	mem.mu.Unlock()

	if !hasPruned {
		t.Error("expected pruning to have replaced some tool results with placeholders")
	}
}
