package runtime

import (
	"sync"

	"github.com/initializ/forge/forge-core/llm"
)

// Memory manages per-task conversation history with token budget tracking.
type Memory struct {
	mu           sync.Mutex
	systemPrompt string
	messages     []llm.ChatMessage
	maxChars     int // approximate token budget: 1 token ~ 4 chars
}

// NewMemory creates a Memory with the given system prompt and character budget.
// If maxChars is 0, a default of 200000 (~50K tokens) is used. The budget must
// comfortably exceed the per-message truncation cap so that a single tool result
// plus its surrounding messages fit without triggering aggressive trimming.
func NewMemory(systemPrompt string, maxChars int) *Memory {
	if maxChars == 0 {
		maxChars = 200_000
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
func (m *Memory) Messages() []llm.ChatMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	msgs := make([]llm.ChatMessage, 0, len(m.messages)+1)
	if m.systemPrompt != "" {
		msgs = append(msgs, llm.ChatMessage{
			Role:    llm.RoleSystem,
			Content: m.systemPrompt,
		})
	}
	msgs = append(msgs, m.messages...)
	return msgs
}

// Reset clears the conversation history (keeps the system prompt).
func (m *Memory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}

// trim removes oldest messages when the total character count exceeds budget.
// Messages are removed in structural groups to maintain valid sequences:
//   - An assistant message with tool_calls is always removed together with its
//     subsequent tool-result messages (they form one atomic group).
//   - Orphaned tool-result messages at the front are removed as a group.
//   - A plain user/assistant message is a single-message group.
//
// Trimming stops if removing the next group would leave zero messages,
// preserving at least the last complete group even if it exceeds the budget.
func (m *Memory) trim() {
	for m.totalChars() > m.maxChars && len(m.messages) > 1 {
		// Determine the size of the first message group.
		end := 1
		if m.messages[0].Role == llm.RoleTool {
			// Orphaned tool results — remove all contiguous tool messages.
			for end < len(m.messages) && m.messages[end].Role == llm.RoleTool {
				end++
			}
		} else if len(m.messages[0].ToolCalls) > 0 {
			// Assistant with tool_calls — include all following tool results.
			for end < len(m.messages) && m.messages[end].Role == llm.RoleTool {
				end++
			}
		}
		// Don't remove everything — keep at least one complete group.
		if end >= len(m.messages) {
			break
		}
		m.messages = m.messages[end:]
	}
}

func (m *Memory) totalChars() int {
	total := len(m.systemPrompt)
	for _, msg := range m.messages {
		total += len(msg.Content) + len(msg.Role)
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return total
}
