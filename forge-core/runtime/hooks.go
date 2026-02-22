package runtime

import (
	"context"

	"github.com/initializ/forge/forge-core/llm"
)

// HookPoint identifies when a hook fires in the agent loop.
type HookPoint int

const (
	BeforeLLMCall  HookPoint = iota
	AfterLLMCall
	BeforeToolExec
	AfterToolExec
	OnError
)

// HookContext carries data available to hooks at each hook point.
type HookContext struct {
	Messages   []llm.ChatMessage
	Response   *llm.ChatResponse
	ToolName   string
	ToolInput  string
	ToolOutput string
	Error      error
}

// Hook is a function invoked at a specific point in the agent loop.
type Hook func(ctx context.Context, hctx *HookContext) error

// HookRegistry manages registered hooks for each hook point.
type HookRegistry struct {
	hooks map[HookPoint][]Hook
}

// NewHookRegistry creates an empty HookRegistry.
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{
		hooks: make(map[HookPoint][]Hook),
	}
}

// Register adds a hook for the given point. Hooks fire in registration order.
func (r *HookRegistry) Register(point HookPoint, h Hook) {
	r.hooks[point] = append(r.hooks[point], h)
}

// Fire invokes all hooks registered for the given point in order.
// If any hook returns an error, execution stops and the error is returned.
func (r *HookRegistry) Fire(ctx context.Context, point HookPoint, hctx *HookContext) error {
	for _, h := range r.hooks[point] {
		if err := h(ctx, hctx); err != nil {
			return err
		}
	}
	return nil
}
