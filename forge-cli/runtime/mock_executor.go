package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
)

// MockExecutor implements AgentExecutor with canned responses.
// It produces the same output format as MockRuntime for backward compatibility.
type MockExecutor struct {
	tools []agentspec.ToolSpec
}

// NewMockExecutor creates a MockExecutor with the given tool specs.
func NewMockExecutor(tools []agentspec.ToolSpec) *MockExecutor {
	return &MockExecutor{tools: tools}
}

// Execute returns a message with mock text content.
func (m *MockExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error) {
	input := extractInputText(msg)
	responseText := fmt.Sprintf("Mock response for: %s", input)

	if len(m.tools) > 0 {
		var names []string
		for _, t := range m.tools {
			names = append(names, t.Name)
		}
		responseText += fmt.Sprintf(" (available tools: %s)", strings.Join(names, ", "))
	}

	return &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: []a2a.Part{a2a.NewTextPart(responseText)},
	}, nil
}

// ExecuteStream wraps Execute as a single-item channel.
func (m *MockExecutor) ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error) {
	ch := make(chan *a2a.Message, 1)
	resp, err := m.Execute(ctx, task, msg)
	if err != nil {
		close(ch)
		return ch, err
	}
	ch <- resp
	close(ch)
	return ch, nil
}

// Close is a no-op for MockExecutor.
func (m *MockExecutor) Close() error { return nil }
