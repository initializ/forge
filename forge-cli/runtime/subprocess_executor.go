package runtime

import (
	"context"

	"github.com/initializ/forge/forge-core/a2a"
)

// SubprocessExecutor wraps a SubprocessRuntime to implement AgentExecutor.
// It delegates to the runtime's Invoke/Stream methods and extracts the
// message from the returned task.
type SubprocessExecutor struct {
	rt *SubprocessRuntime
}

// NewSubprocessExecutor creates an executor that delegates to the given runtime.
func NewSubprocessExecutor(rt *SubprocessRuntime) *SubprocessExecutor {
	return &SubprocessExecutor{rt: rt}
}

// Execute calls the runtime's Invoke and extracts the status message.
func (s *SubprocessExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error) {
	result, err := s.rt.Invoke(ctx, task.ID, msg)
	if err != nil {
		return nil, err
	}
	return result.Status.Message, nil
}

// ExecuteStream calls the runtime's Stream and converts task updates to messages.
func (s *SubprocessExecutor) ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error) {
	taskCh, err := s.rt.Stream(ctx, task.ID, msg)
	if err != nil {
		return nil, err
	}

	msgCh := make(chan *a2a.Message, 8)
	go func() {
		defer close(msgCh)
		for update := range taskCh {
			if update.Status.Message != nil {
				msgCh <- update.Status.Message
			}
		}
	}()

	return msgCh, nil
}

// Close is a no-op; the subprocess lifecycle is managed by SubprocessRuntime.
func (s *SubprocessExecutor) Close() error { return nil }
