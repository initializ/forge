package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
)

// MockRuntime implements AgentRuntime without a real subprocess. It returns
// canned responses based on the agent's tool specs. Useful for testing the
// A2A protocol layer without needing Python or other frameworks installed.
type MockRuntime struct {
	tools []agentspec.ToolSpec
}

// NewMockRuntime creates a MockRuntime with the given tool specs.
func NewMockRuntime(tools []agentspec.ToolSpec) *MockRuntime {
	return &MockRuntime{tools: tools}
}

func (m *MockRuntime) Start(ctx context.Context) error   { return nil }
func (m *MockRuntime) Stop() error                        { return nil }
func (m *MockRuntime) Restart(ctx context.Context) error  { return nil }
func (m *MockRuntime) Healthy(ctx context.Context) bool   { return true }

// Invoke returns a completed task with mock text content.
func (m *MockRuntime) Invoke(ctx context.Context, taskID string, msg *a2a.Message) (*a2a.Task, error) {
	input := extractInputText(msg)
	responseText := fmt.Sprintf("Mock response for: %s", input)

	if len(m.tools) > 0 {
		var names []string
		for _, t := range m.tools {
			names = append(names, t.Name)
		}
		responseText += fmt.Sprintf(" (available tools: %s)", strings.Join(names, ", "))
	}

	task := &a2a.Task{
		ID: taskID,
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []a2a.Part{a2a.NewTextPart(responseText)},
			},
		},
		Artifacts: []a2a.Artifact{
			{
				Name:  "response",
				Parts: []a2a.Part{a2a.NewTextPart(responseText)},
			},
		},
	}
	return task, nil
}

// Stream wraps Invoke as a single-item channel.
func (m *MockRuntime) Stream(ctx context.Context, taskID string, msg *a2a.Message) (<-chan *a2a.Task, error) {
	ch := make(chan *a2a.Task, 1)
	task, err := m.Invoke(ctx, taskID, msg)
	if err != nil {
		close(ch)
		return ch, err
	}
	ch <- task
	close(ch)
	return ch, nil
}

func extractInputText(msg *a2a.Message) string {
	var parts []string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	if len(parts) == 0 {
		return "<no text>"
	}
	return strings.Join(parts, " ")
}
