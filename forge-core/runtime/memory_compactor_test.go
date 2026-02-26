package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

func TestCompactorBelowThreshold(t *testing.T) {
	c := NewCompactor(CompactorConfig{
		CharBudget:   200_000,
		TriggerRatio: 0.6,
	})

	mem := NewMemory("system", 0, "")
	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "short message"})

	compacted, err := c.MaybeCompact("task-1", mem)
	if err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if compacted {
		t.Error("should not compact when below threshold")
	}
}

func TestCompactorAboveThreshold(t *testing.T) {
	// Use a very small budget so compaction triggers easily
	c := NewCompactor(CompactorConfig{
		CharBudget:   200,
		TriggerRatio: 0.6,
	})

	mem := NewMemory("", 0, "") // no system prompt to keep char count predictable
	// Add enough messages to exceed 120 chars (200 * 0.6)
	for i := 0; i < 10; i++ {
		mem.Append(llm.ChatMessage{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("message number %d with some content", i),
		})
	}

	msgsBefore := len(mem.Messages())

	compacted, err := c.MaybeCompact("task-2", mem)
	if err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if !compacted {
		t.Error("should compact when above threshold")
	}

	msgsAfter := len(mem.Messages())
	if msgsAfter >= msgsBefore {
		t.Errorf("message count should decrease after compaction: before=%d, after=%d", msgsBefore, msgsAfter)
	}

	// Verify summary is set
	msgs := mem.Messages()
	if len(msgs) > 0 && msgs[0].Role == llm.RoleSystem {
		if !strings.Contains(msgs[0].Content, "Conversation Summary") {
			t.Error("system message should contain summary after compaction")
		}
	}
}

func TestCompactorGroupBoundary(t *testing.T) {
	c := NewCompactor(CompactorConfig{
		CharBudget:   300,
		TriggerRatio: 0.5,
	})

	mem := NewMemory("", 0, "")
	// Create a sequence: user, assistant+tool_calls, tool, user, assistant
	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: strings.Repeat("a", 50)})
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleAssistant,
		Content: "",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "test", Arguments: "{}"}},
		},
	})
	mem.Append(llm.ChatMessage{
		Role:       llm.RoleTool,
		Content:    strings.Repeat("b", 50),
		ToolCallID: "call_1",
		Name:       "test",
	})
	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: strings.Repeat("c", 50)})
	mem.Append(llm.ChatMessage{Role: llm.RoleAssistant, Content: strings.Repeat("d", 50)})

	compacted, err := c.MaybeCompact("task-3", mem)
	if err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if !compacted {
		t.Error("should compact")
	}

	// After compaction, remaining messages should not start with a tool result
	mem.mu.Lock()
	if len(mem.messages) > 0 && mem.messages[0].Role == llm.RoleTool {
		t.Error("compaction should not orphan tool results")
	}
	mem.mu.Unlock()
}

func TestCompactorExtractiveSummarize(t *testing.T) {
	c := NewCompactor(CompactorConfig{}) // no LLM client

	messages := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "What is 2+2?"},
		{Role: llm.RoleAssistant, Content: "2+2 equals 4."},
	}

	summary := c.extractiveSummarize(messages, "")
	if summary == "" {
		t.Fatal("extractive summary should not be empty")
	}
	if !strings.Contains(summary, "[user]") {
		t.Error("summary should contain role markers")
	}
	if !strings.Contains(summary, "What is 2+2?") {
		t.Error("summary should contain message content")
	}
}

func TestCompactorExtractiveSummarizeWithExisting(t *testing.T) {
	c := NewCompactor(CompactorConfig{})

	messages := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "new question"},
	}

	summary := c.extractiveSummarize(messages, "previous summary here")
	if !strings.Contains(summary, "previous summary here") {
		t.Error("should incorporate existing summary")
	}
	if !strings.Contains(summary, "new question") {
		t.Error("should include new messages")
	}
}

func TestCompactorLLMSummarize(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Content: "Summary: user asked about math, agent answered 4.",
				},
				FinishReason: "stop",
			}, nil
		},
	}

	c := NewCompactor(CompactorConfig{
		Client: client,
	})

	messages := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "What is 2+2?"},
		{Role: llm.RoleAssistant, Content: "2+2 equals 4."},
	}

	summary, err := c.llmSummarize(messages, "")
	if err != nil {
		t.Fatalf("llmSummarize: %v", err)
	}
	if !strings.Contains(summary, "Summary") {
		t.Errorf("unexpected summary: %q", summary)
	}
}

func TestCompactorLLMFailureFallback(t *testing.T) {
	client := &mockLLMClient{
		chatFunc: func(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, fmt.Errorf("API error")
		},
	}

	c := NewCompactor(CompactorConfig{
		Client: client,
	})

	messages := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "What is 2+2?"},
		{Role: llm.RoleAssistant, Content: "4"},
	}

	summary, err := c.summarize(messages, "")
	if err != nil {
		t.Fatalf("summarize should not error on LLM failure: %v", err)
	}
	if summary == "" {
		t.Error("should fall back to extractive summary")
	}
	// Should be extractive format
	if !strings.Contains(summary, "[user]") {
		t.Error("fallback summary should use extractive format")
	}
}

func TestCompactorFlushToDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	c := NewCompactor(CompactorConfig{
		Store:        store,
		CharBudget:   200,
		TriggerRatio: 0.5,
	})

	mem := NewMemory("", 0, "")
	for i := 0; i < 10; i++ {
		mem.Append(llm.ChatMessage{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("message %d with enough content to trigger compaction", i),
		})
	}

	compacted, err := c.MaybeCompact("flush-test", mem)
	if err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if !compacted {
		t.Fatal("expected compaction to trigger")
	}

	// Verify session was persisted
	loaded, err := store.Load("flush-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("session should be persisted after compaction")
	}
	if loaded.Summary == "" {
		t.Error("persisted session should have a summary")
	}
}

func TestCompactorNoStore(t *testing.T) {
	// Compaction without a store should still work (summary in memory only).
	c := NewCompactor(CompactorConfig{
		CharBudget:   200,
		TriggerRatio: 0.5,
	})

	mem := NewMemory("", 0, "")
	for i := 0; i < 10; i++ {
		mem.Append(llm.ChatMessage{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("message %d with enough content to trigger compaction", i),
		})
	}

	compacted, err := c.MaybeCompact("no-store", mem)
	if err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if !compacted {
		t.Error("compaction should work without a store")
	}

	// Summary should be in memory
	mem.mu.Lock()
	hasSummary := mem.existingSummary != ""
	mem.mu.Unlock()
	if !hasSummary {
		t.Error("in-memory summary should be set even without a store")
	}
}

func TestCompactorMemoryFlusher(t *testing.T) {
	// Mock flusher that captures observations.
	flusher := &mockMemoryFlusher{}

	c := NewCompactor(CompactorConfig{
		CharBudget:    200,
		TriggerRatio:  0.5,
		MemoryFlusher: flusher,
	})

	mem := NewMemory("", 0, "")
	// Add messages with tool results that should be captured.
	mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: "Search for Go docs"})
	mem.Append(llm.ChatMessage{
		Role:    llm.RoleAssistant,
		Content: "",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "search", Arguments: "{}"}},
		},
	})
	mem.Append(llm.ChatMessage{
		Role:       llm.RoleTool,
		Content:    "Found Go documentation at golang.org",
		ToolCallID: "call_1",
		Name:       "search",
	})
	mem.Append(llm.ChatMessage{Role: llm.RoleAssistant, Content: "I found the Go docs for you."})
	// More padding to trigger compaction.
	for i := range 5 {
		mem.Append(llm.ChatMessage{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("padding message %d with enough content", i),
		})
	}

	compacted, err := c.MaybeCompact("flusher-test", mem)
	if err != nil {
		t.Fatalf("MaybeCompact: %v", err)
	}
	if !compacted {
		t.Fatal("expected compaction")
	}

	// Flusher should have received observations.
	if flusher.observation == "" {
		t.Error("memory flusher should receive observations during compaction")
	}
	if !strings.Contains(flusher.observation, "search") {
		t.Errorf("observations should include tool results, got: %s", flusher.observation)
	}
}

func TestCompactorSetMemoryFlusher(t *testing.T) {
	c := NewCompactor(CompactorConfig{})

	if c.memoryFlusher != nil {
		t.Error("flusher should be nil initially")
	}

	flusher := &mockMemoryFlusher{}
	c.SetMemoryFlusher(flusher)

	if c.memoryFlusher == nil {
		t.Error("flusher should be set after SetMemoryFlusher")
	}
}

type mockMemoryFlusher struct {
	observation string
}

func (m *mockMemoryFlusher) AppendDailyLog(_ context.Context, observation string) error {
	m.observation = observation
	return nil
}

func TestFindGroupBoundary(t *testing.T) {
	c := NewCompactor(CompactorConfig{})

	messages := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "c1"}}},
		{Role: llm.RoleTool, Content: "result1", ToolCallID: "c1"},
		{Role: llm.RoleUser, Content: "q2"},
		{Role: llm.RoleAssistant, Content: "answer"},
	}

	// Target index 2 (tool result) should advance to 3 (next user msg)
	idx := c.findGroupBoundary(messages, 2)
	if idx != 3 {
		t.Errorf("expected boundary at 3, got %d", idx)
	}

	// Target index 3 (user msg) should stay at 3
	idx = c.findGroupBoundary(messages, 3)
	if idx != 3 {
		t.Errorf("expected boundary at 3, got %d", idx)
	}

	// Target index 0 should stay at 0 (user msg)
	idx = c.findGroupBoundary(messages, 0)
	if idx != 0 {
		t.Errorf("expected boundary at 0, got %d", idx)
	}
}

func TestTruncateForPrompt(t *testing.T) {
	short := "hello"
	if got := truncateForPrompt(short, 10); got != short {
		t.Errorf("short string should not be truncated: got %q", got)
	}

	long := strings.Repeat("x", 100)
	got := truncateForPrompt(long, 10)
	if len(got) != 13 { // 10 + len("...")
		t.Errorf("expected length 13, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("truncated string should end with ...")
	}
}
