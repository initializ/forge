package runtime

import (
	"context"

	"github.com/initializ/forge/forge-core/a2a"
)

// AgentExecutor processes individual messages and returns responses.
// Unlike AgentRuntime (which manages subprocess lifecycle), the executor
// focuses solely on message-level processing. The handler (runner.go)
// manages task lifecycle (submitted -> working -> completed/failed).
type AgentExecutor interface {
	// Execute processes a message in the context of a task and returns a response.
	Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error)
	// ExecuteStream processes a message and returns a channel of response messages.
	ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error)
	// Close releases any resources held by the executor.
	Close() error
}
