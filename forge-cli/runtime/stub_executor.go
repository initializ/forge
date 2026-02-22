package runtime

import (
	"context"
	"fmt"

	"github.com/initializ/forge/forge-core/a2a"
)

// StubExecutor implements AgentExecutor by returning an error indicating
// that no LLM configuration is available. Used as a fallback when no
// provider is configured for a custom framework agent.
type StubExecutor struct {
	framework string
}

// NewStubExecutor creates a StubExecutor for the given framework name.
func NewStubExecutor(framework string) *StubExecutor {
	return &StubExecutor{framework: framework}
}

// Execute returns an error indicating execution is not configured.
func (s *StubExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error) {
	return nil, fmt.Errorf("agent execution not configured for framework %q", s.framework)
}

// ExecuteStream returns an error indicating execution is not configured.
func (s *StubExecutor) ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error) {
	return nil, fmt.Errorf("agent execution not configured for framework %q", s.framework)
}

// Close is a no-op for StubExecutor.
func (s *StubExecutor) Close() error { return nil }
