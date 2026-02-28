package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// Default compaction settings.
const (
	defaultCharBudget   = 200_000
	defaultTriggerRatio = 0.6
	summaryMaxTokens    = 1024
	summaryTimeout      = 30 * time.Second
	maxExtractiveChars  = 2000
)

// MemoryFlusher is the interface for flushing observations to long-term memory.
// Implemented by memory.Manager.
type MemoryFlusher interface {
	AppendDailyLog(ctx context.Context, observation string) error
}

// CompactorConfig configures a Compactor.
type CompactorConfig struct {
	// Client is the LLM client for abstractive summarization. If nil,
	// only extractive (bullet-point) summarization is used.
	Client llm.Client
	// Store persists sessions to disk after compaction. If nil, compaction
	// still reduces in-memory messages but nothing is flushed.
	Store *MemoryStore
	// Logger for compaction events.
	Logger Logger
	// CharBudget is the total character budget. Compaction triggers when
	// totalChars exceeds CharBudget * TriggerRatio. Default: 200,000.
	CharBudget int
	// TriggerRatio is the fraction of CharBudget at which compaction fires.
	// Default: 0.6.
	TriggerRatio float64
	// MemoryFlusher flushes key observations to long-term memory before
	// compaction discards old messages. Optional.
	MemoryFlusher MemoryFlusher
}

// Compactor manages memory compaction by summarizing old messages and
// optionally flushing to disk.
//
// Each Memory instance is single-threaded per task execution (the agent loop
// is sequential), so holding mem.mu during the LLM summarization call is
// acceptable — no concurrent access occurs.
type Compactor struct {
	client        llm.Client
	store         *MemoryStore
	logger        Logger
	charBudget    int
	triggerRatio  float64
	memoryFlusher MemoryFlusher
}

// NewCompactor creates a Compactor from the given config.
func NewCompactor(cfg CompactorConfig) *Compactor {
	budget := cfg.CharBudget
	if budget <= 0 {
		budget = defaultCharBudget
	}
	ratio := cfg.TriggerRatio
	if ratio <= 0 || ratio >= 1 {
		ratio = defaultTriggerRatio
	}
	logger := cfg.Logger
	if logger == nil {
		logger = &nopLogger{}
	}
	return &Compactor{
		client:        cfg.Client,
		store:         cfg.Store,
		logger:        logger,
		charBudget:    budget,
		triggerRatio:  ratio,
		memoryFlusher: cfg.MemoryFlusher,
	}
}

// SetMemoryFlusher sets the long-term memory flusher. This allows wiring the
// flusher after construction (e.g., when the memory manager depends on the
// same embedder resolution that happens after the compactor is created).
func (c *Compactor) SetMemoryFlusher(f MemoryFlusher) {
	c.memoryFlusher = f
}

// MaybeCompact checks whether the memory exceeds the trigger threshold and,
// if so, compacts the oldest 50% of messages into a summary. Returns true
// if compaction occurred.
//
// The method holds mem.mu for its entire duration including any LLM call.
// This is safe because each Memory is used by a single sequential agent loop.
func (c *Compactor) MaybeCompact(taskID string, mem *Memory) (bool, error) {
	mem.mu.Lock()
	defer mem.mu.Unlock()

	total := mem.totalChars()
	threshold := int(float64(c.charBudget) * c.triggerRatio)

	if total <= threshold {
		return false, nil
	}

	c.logger.Info("compaction triggered", map[string]any{
		"task_id":   taskID,
		"total":     total,
		"threshold": threshold,
		"messages":  len(mem.messages),
	})

	// Take oldest 50% of messages, respecting group boundaries.
	target := len(mem.messages) / 2
	splitIdx := c.findGroupBoundary(mem.messages, target)
	if splitIdx <= 0 || splitIdx >= len(mem.messages) {
		return false, nil
	}

	oldMessages := mem.messages[:splitIdx]

	// Flush key observations to long-term memory before discarding.
	c.flushToLongTermMemory(oldMessages)

	// Summarize the old messages.
	summary, err := c.summarize(oldMessages, mem.existingSummary)
	if err != nil {
		return false, fmt.Errorf("summarization failed: %w", err)
	}

	// Replace old messages with the summary.
	mem.messages = mem.messages[splitIdx:]
	mem.existingSummary = summary

	c.logger.Info("compaction complete", map[string]any{
		"task_id":       taskID,
		"removed":       splitIdx,
		"remaining":     len(mem.messages),
		"summary_chars": len(summary),
	})

	// Flush to disk if store is available.
	c.flushToDisk(taskID, mem)

	return true, nil
}

// summarize produces a summary of the given messages, incorporating any
// existing summary. Tries LLM first, falls back to extractive.
func (c *Compactor) summarize(messages []llm.ChatMessage, existingSummary string) (string, error) {
	if c.client != nil {
		summary, err := c.llmSummarize(messages, existingSummary)
		if err != nil {
			c.logger.Warn("LLM summarization failed, falling back to extractive", map[string]any{
				"error": err.Error(),
			})
			return c.extractiveSummarize(messages, existingSummary), nil
		}
		return summary, nil
	}
	return c.extractiveSummarize(messages, existingSummary), nil
}

// llmSummarize uses the LLM client to produce an abstractive summary.
func (c *Compactor) llmSummarize(messages []llm.ChatMessage, existingSummary string) (string, error) {
	// Build the prompt for summarization.
	var sb strings.Builder
	sb.WriteString("Summarize the following conversation concisely. ")
	sb.WriteString("Preserve key facts, decisions, tool results, and action items. ")
	sb.WriteString("Output only the summary, no preamble.\n\n")

	if existingSummary != "" {
		sb.WriteString("## Existing Summary (incorporate and update)\n")
		sb.WriteString(existingSummary)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Conversation to summarize\n")
	for _, msg := range messages {
		fmt.Fprintf(&sb, "[%s]: %s\n", msg.Role, truncateForPrompt(msg.Content, 500))
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&sb, "  -> tool_call: %s(%s)\n", tc.Function.Name, truncateForPrompt(tc.Function.Arguments, 200))
		}
	}

	temp := 0.3
	ctx, cancel := context.WithTimeout(context.Background(), summaryTimeout)
	defer cancel()

	resp, err := c.client.Chat(ctx, &llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: sb.String()},
		},
		Temperature: &temp,
		MaxTokens:   summaryMaxTokens,
	})
	if err != nil {
		return "", err
	}

	return resp.Message.Content, nil
}

// extractiveSummarize builds a bullet-point summary without an LLM.
func (c *Compactor) extractiveSummarize(messages []llm.ChatMessage, existingSummary string) string {
	var sb strings.Builder

	if existingSummary != "" {
		sb.WriteString(existingSummary)
		sb.WriteString("\n\n")
	}

	for _, msg := range messages {
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		content := msg.Content
		if len(content) > maxExtractiveChars {
			content = content[:maxExtractiveChars] + "..."
		}
		if content != "" {
			fmt.Fprintf(&sb, "- [%s] %s\n", msg.Role, content)
		}
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&sb, "- [tool_call] %s\n", tc.Function.Name)
		}
	}

	return sb.String()
}

// findGroupBoundary finds the nearest valid split point at or after target
// that respects tool-call group boundaries. An assistant message with
// tool_calls must not be separated from its subsequent tool-result messages.
func (c *Compactor) findGroupBoundary(messages []llm.ChatMessage, target int) int {
	if target >= len(messages) {
		return len(messages)
	}

	idx := target
	for idx < len(messages) {
		// If we're at a tool result, advance past it — splitting here
		// would orphan it from its assistant message.
		if messages[idx].Role == llm.RoleTool {
			idx++
			continue
		}
		return idx
	}
	return idx
}

// flushToDisk persists the current memory state to the store.
// Errors are logged but not returned — persistence is best-effort.
func (c *Compactor) flushToDisk(taskID string, mem *Memory) {
	if c.store == nil {
		return
	}

	data := &SessionData{
		TaskID:   taskID,
		Messages: mem.messages,
		Summary:  mem.existingSummary,
	}

	if err := c.store.Save(data); err != nil {
		c.logger.Warn("failed to flush session to disk", map[string]any{
			"task_id": taskID,
			"error":   err.Error(),
		})
	}
}

// extractiveLimit returns the character limit for tool result extraction.
// Research tools get a higher limit to preserve more of their reports.
func extractiveLimit(toolName string) int {
	if strings.Contains(toolName, "research") {
		return 5000
	}
	return maxExtractiveChars
}

// flushToLongTermMemory extracts key observations from messages being
// compacted and appends them to the long-term daily log.
func (c *Compactor) flushToLongTermMemory(messages []llm.ChatMessage) {
	if c.memoryFlusher == nil {
		return
	}

	var observations strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleTool:
			// Tool results contain factual observations worth preserving.
			limit := extractiveLimit(msg.Name)
			content := msg.Content
			if len(content) > limit {
				content = content[:limit] + "..."
			}
			if content != "" {
				tag := fmt.Sprintf("[tool:%s]", msg.Name)
				if strings.Contains(msg.Name, "research") {
					tag = fmt.Sprintf("[research][tool:%s]", msg.Name)
				}
				fmt.Fprintf(&observations, "- %s %s\n", tag, content)
			}
		case llm.RoleAssistant:
			// Capture key decisions from assistant responses.
			if msg.Content != "" && len(msg.ToolCalls) == 0 {
				content := msg.Content
				if len(content) > maxExtractiveChars {
					content = content[:maxExtractiveChars] + "..."
				}
				fmt.Fprintf(&observations, "- [decision] %s\n", content)
			}
		}
	}

	if observations.Len() == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.memoryFlusher.AppendDailyLog(ctx, observations.String()); err != nil {
		c.logger.Warn("failed to flush observations to long-term memory", map[string]any{
			"error": err.Error(),
		})
	}
}

// truncateForPrompt truncates a string to maxLen for use in prompts.
func truncateForPrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// nopLogger is a no-op Logger used when no logger is provided.
type nopLogger struct{}

func (n *nopLogger) Info(_ string, _ map[string]any)  {}
func (n *nopLogger) Warn(_ string, _ map[string]any)  {}
func (n *nopLogger) Error(_ string, _ map[string]any) {}
func (n *nopLogger) Debug(_ string, _ map[string]any) {}
