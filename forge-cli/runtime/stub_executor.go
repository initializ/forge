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
	reason    string // optional: why the real executor could not be created
}

// NewStubExecutor creates a StubExecutor for the given framework name.
func NewStubExecutor(framework string) *StubExecutor {
	return &StubExecutor{framework: framework}
}

// NewStubExecutorWithReason creates a StubExecutor with an explanation of why
// the real executor could not be used.
func NewStubExecutorWithReason(framework, reason string) *StubExecutor {
	return &StubExecutor{framework: framework, reason: reason}
}

func (s *StubExecutor) errorMsg() string {
	msg := fmt.Sprintf("no LLM provider available for framework %q", s.framework)
	if s.reason != "" {
		msg += ": " + s.reason
	} else {
		msg += " — set an API key (OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY) in .env or use 'forge brain pull' for local inference"
	}
	return msg
}

// Execute returns an error indicating execution is not configured.
func (s *StubExecutor) Execute(_ context.Context, _ *a2a.Task, _ *a2a.Message) (*a2a.Message, error) {
	return nil, fmt.Errorf("%s", s.errorMsg())
}

// ExecuteStream returns an error indicating execution is not configured.
func (s *StubExecutor) ExecuteStream(_ context.Context, _ *a2a.Task, _ *a2a.Message) (<-chan *a2a.Message, error) {
	return nil, fmt.Errorf("%s", s.errorMsg())
}

// Close is a no-op for StubExecutor.
func (s *StubExecutor) Close() error { return nil }
