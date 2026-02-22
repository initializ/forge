package runtime

import (
	"context"

	"github.com/initializ/forge/forge-core/a2a"
)

// AgentRuntime abstracts the agent execution backend. Implementations include
// SubprocessRuntime (real agent process) and MockRuntime (canned responses).
type AgentRuntime interface {
	// Start launches the agent backend.
	Start(ctx context.Context) error
	// Invoke sends a synchronous task request and returns the completed task.
	Invoke(ctx context.Context, taskID string, msg *a2a.Message) (*a2a.Task, error)
	// Stream sends a streaming task request and returns a channel of task updates.
	Stream(ctx context.Context, taskID string, msg *a2a.Message) (<-chan *a2a.Task, error)
	// Healthy reports whether the agent backend is responsive.
	Healthy(ctx context.Context) bool
	// Stop shuts down the agent backend.
	Stop() error
	// Restart stops and restarts the agent backend.
	Restart(ctx context.Context) error
}
