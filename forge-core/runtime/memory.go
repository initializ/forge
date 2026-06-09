package runtime

import (
	"fmt"
	"strings"
	"sync"

	"github.com/initializ/forge/forge-core/llm"
)

// ModelContextWindows maps model name prefixes to context window sizes (in tokens).
var ModelContextWindows = map[string]int{
	"gpt-4o":        128_000,
	"gpt-4":         128_000,
	"gpt-5":         128_000,
	"gpt-3.5":       16_000,
	"claude-opus":   200_000,
	"claude-sonnet": 200_000,
	"claude-haiku":  200_000,
	"gemini-2.5":    1_000_000,
	"gemini-2.0":    1_000_000,
	"llama3.1":      128_000,
	"llama3":        8_000,
	"mistral":       32_000,
	"codellama":     16_000,
	"deepseek":      64_000,
	"qwen":          32_000,
}

const (
	defaultContextTokens = 128_000
	charsPerToken        = 4
	safetyMargin         = 0.85 // use 85% of context window
)

// ContextBudgetForModel returns the character budget for a given model name.
// Uses prefix matching against known models, falls back to defaultContextTokens.
// Prefixes are checked longest-first to avoid e.g. "llama3" matching before "llama3.1".
func ContextBudgetForModel(model string) int {
	model = strings.ToLower(model)
	bestPrefix := ""
	bestTokens := 0
	for prefix, tokens := range ModelContextWindows {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestTokens = tokens
		}
	}
	if bestPrefix != "" {
		return int(float64(bestTokens) * float64(charsPerToken) * safetyMargin)
	}
	return defaultContextTokens * charsPerToken // 512K chars
}

// Memory manages per-task conversation history with token budget tracking.
type Memory struct {
	mu              sync.Mutex
	systemPrompt    string
	messages        []llm.ChatMessage
	existingSummary string // compacted summary from prior context
	maxChars        int    // approximate token budget: 1 token ~ 4 chars
}

// NewMemory creates a Memory with the given system prompt and character budget.
// If maxChars is 0, the budget is computed from the model name using
// ContextBudgetForModel. If both maxChars and model are zero/empty, a default
// of 512K chars (~128K tokens) is used. The budget must comfortably exceed the
// per-message truncation cap so that a single tool result plus its surrounding
// messages fit without triggering aggressive trimming.
func NewMemory(systemPrompt string, maxChars int, model string) *Memory {
	if maxChars == 0 {
		if model != "" {
			maxChars = ContextBudgetForModel(model)
		} else {
			maxChars = defaultContextTokens * charsPerToken
		}
	}
	return &Memory{
		systemPrompt: systemPrompt,
		maxChars:     maxChars,
	}
}

// maxMessageChars is the per-message size cap (defense in depth).
const maxMessageChars = 50_000

// Append adds a message to the conversation history and trims if over budget.
// Individual messages exceeding maxMessageChars are truncated as a safety net.
func (m *Memory) Append(msg llm.ChatMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(msg.Content) > maxMessageChars {
		msg.Content = msg.Content[:maxMessageChars] + "\n[TRUNCATED]"
	}
	m.messages = append(m.messages, msg)
	m.trim()
}

// Messages returns the full message list with the system prompt prepended.
// If an existing summary is present (from compaction), it is appended to the
// system prompt so the LLM has prior context.
func (m *Memory) Messages() []llm.ChatMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	msgs := make([]llm.ChatMessage, 0, len(m.messages)+1)
	if m.systemPrompt != "" || m.existingSummary != "" {
		content := m.systemPrompt
		if m.existingSummary != "" {
			content += "\n\n## Conversation Summary (prior context)\n" + m.existingSummary
		}
		msgs = append(msgs, llm.ChatMessage{
			Role:    llm.RoleSystem,
			Content: content,
		})
	}
	msgs = append(msgs, m.messages...)
	return msgs
}

// LoadFromStore restores memory state from a persisted SessionData.
// It runs the loaded messages through sanitizeMessages, which strips
// the two known kinds of corruption that cause strict providers to
// reject the recovered conversation:
//
//  1. Orphaned tool_calls — assistant messages whose tool_calls have
//     no matching tool result (Responses API: "No tool output found
//     for function call").
//  2. Empty assistant turns — assistant messages with both empty
//     content AND no tool_calls (issue #131). The OpenAI
//     chat-completions schema considers that shape invalid; Moonshot,
//     hosted OpenRouter, and OpenAI strict mode return HTTP 400 if a
//     recovered conversation contains one. Such turns appear when the
//     provider hits `finish_reason: length` and the in-loop empty-
//     response recovery fires — pre-#131 builds persisted the empty
//     turn alongside the recovered real response. Stripping on load
//     rescues sessions written by those builds without a migration.
func (m *Memory) LoadFromStore(data *SessionData) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = sanitizeMessages(data.Messages)
	m.existingSummary = data.Summary
}

// sanitizeMessages strips the known kinds of corruption from a loaded
// message slice (see LoadFromStore). The passes are kept separate so
// a future caller that only needs one (or wants to add another) can
// compose them individually.
func sanitizeMessages(msgs []llm.ChatMessage) []llm.ChatMessage {
	msgs = sanitizeToolCalls(msgs)
	msgs = stripEmptyAssistantTurns(msgs)
	msgs = collapseConsecutiveDuplicates(msgs)
	return msgs
}

// sanitizeToolCalls removes tool calls from assistant messages that have
// no matching tool result in the message history.
func sanitizeToolCalls(msgs []llm.ChatMessage) []llm.ChatMessage {
	// Build set of tool call IDs that have results.
	answered := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == llm.RoleTool && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}

	// Strip unanswered tool calls.
	for i := range msgs {
		if msgs[i].Role != llm.RoleAssistant || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		var kept []llm.ToolCall
		for _, tc := range msgs[i].ToolCalls {
			if answered[tc.ID] {
				kept = append(kept, tc)
			}
		}
		if len(kept) != len(msgs[i].ToolCalls) {
			msgs[i].ToolCalls = kept
		}
	}
	return msgs
}

// stripEmptyAssistantTurns drops assistant messages that have both
// empty content AND no tool_calls. See LoadFromStore for the full
// rationale (issue #131). This is the defense-in-depth half of the
// fix — the Execute loop now substitutes a placeholder content
// string before persisting so newly-written sessions never contain
// the bad shape, but sessions already on disk from pre-#131 builds
// (and any future regression of the source-side guard) still get
// rescued on load.
func stripEmptyAssistantTurns(msgs []llm.ChatMessage) []llm.ChatMessage {
	out := msgs[:0]
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// collapseConsecutiveDuplicates drops adjacent same-role messages with
// identical content (issue #143). Pre-fix the runner pre-appended
// params.Message to task.History before calling Execute, and the
// !recovered path of Execute then also appended *msg — producing two
// consecutive identical user turns at the start of every fresh
// session. Strict-mode providers (gpt-5-nano and other OpenAI
// reasoning models, Together's Kimi gateway) reject consecutive
// same-role messages.
//
// The source-side fix lives at loop.go's Execute; this is the
// defense-in-depth pass that rescues sessions already on disk from
// pre-fix builds without an `rm` workaround.
//
// Surgical by design — only drops EXACT same-role + same-content
// pairs. Legitimate consecutive same-role sequences (workflow nudges
// where the loop appends a user-role nudge after an existing user
// message; assistant follow-on turns from finish_reason=length
// recoveries) have DIFFERENT content and are preserved.
//
// Messages with tool_calls (assistant) or tool_call_id (tool) are
// never collapsed — those are structurally distinct turns even when
// content matches superficially.
func collapseConsecutiveDuplicates(msgs []llm.ChatMessage) []llm.ChatMessage {
	if len(msgs) < 2 {
		return msgs
	}
	out := msgs[:1]
	for i := 1; i < len(msgs); i++ {
		prev := out[len(out)-1]
		cur := msgs[i]
		if prev.Role == cur.Role &&
			prev.Content == cur.Content &&
			len(prev.ToolCalls) == 0 && len(cur.ToolCalls) == 0 &&
			prev.ToolCallID == "" && cur.ToolCallID == "" {
			// Adjacent same-role + same-content + tool-call-free
			// pair → drop the second.
			continue
		}
		out = append(out, cur)
	}
	return out
}

// Reset clears the conversation history (keeps the system prompt).
func (m *Memory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}

// trim removes oldest messages when the total character count exceeds budget.
// It first prunes old tool results into compact placeholders (preserving signal),
// then drops oldest message groups if still over budget.
//
// The first user message (the task request) is always preserved so the LLM
// retains its objective even after aggressive trimming.
//
// Messages are removed in structural groups to maintain valid sequences:
//   - An assistant message with tool_calls is always removed together with its
//     subsequent tool-result messages (they form one atomic group).
//   - Orphaned tool-result messages at the front are removed as a group.
//   - A plain user/assistant message is a single-message group.
//
// Trimming stops if removing the next group would leave zero messages,
// preserving at least the last complete group even if it exceeds the budget.
func (m *Memory) trim() {
	// Phase 1: Replace old tool results with placeholders (preserve signal).
	if m.totalChars() > m.maxChars {
		m.pruneToolResults()
	}

	// Find the first user message to pin (the task request).
	pinEnd := 0
	for i, msg := range m.messages {
		if msg.Role == llm.RoleUser {
			pinEnd = i + 1
			break
		}
	}

	// Phase 2: Drop oldest message groups after the pinned prefix.
	for m.totalChars() > m.maxChars && len(m.messages) > pinEnd+1 {
		// Start trimming from the first non-pinned message.
		idx := pinEnd
		end := idx + 1
		if m.messages[idx].Role == llm.RoleTool {
			// Orphaned tool results — remove all contiguous tool messages.
			for end < len(m.messages) && m.messages[end].Role == llm.RoleTool {
				end++
			}
		} else if len(m.messages[idx].ToolCalls) > 0 {
			// Assistant with tool_calls — include all following tool results.
			for end < len(m.messages) && m.messages[end].Role == llm.RoleTool {
				end++
			}
		}
		// Don't remove everything — keep at least one complete group after pin.
		if end >= len(m.messages) {
			break
		}
		m.messages = append(m.messages[:idx], m.messages[end:]...)
	}
}

// pruneToolResults replaces tool result content in older messages with
// compact placeholders, preserving the fact that a tool was called while
// reclaiming most of the space. Only prunes the oldest 50% of tool results
// that exceed 200 chars.
func (m *Memory) pruneToolResults() {
	// Find all tool result indices with substantial content.
	var toolIndices []int
	for i, msg := range m.messages {
		if msg.Role == llm.RoleTool && len(msg.Content) > 200 {
			toolIndices = append(toolIndices, i)
		}
	}

	// Prune oldest 50% of large tool results.
	pruneCount := len(toolIndices) / 2
	for j := 0; j < pruneCount; j++ {
		idx := toolIndices[j]
		name := m.messages[idx].Name
		origLen := len(m.messages[idx].Content)
		m.messages[idx].Content = fmt.Sprintf("[Tool result from %s — %d chars, pruned for context space]", name, origLen)
	}
}

// toolResultWeightMultiplier weights tool results at 2x in char counting
// because tool results contain structured data that tokenizes less efficiently.
const toolResultWeightMultiplier = 2

func (m *Memory) totalChars() int {
	total := len(m.systemPrompt)
	if m.existingSummary != "" {
		total += len(m.existingSummary)
	}
	for _, msg := range m.messages {
		if msg.Role == llm.RoleTool {
			// Tool results tokenize less efficiently — weight at 2x.
			total += (len(msg.Content) + len(msg.Role)) * toolResultWeightMultiplier
		} else {
			total += len(msg.Content) + len(msg.Role)
		}
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return total
}
