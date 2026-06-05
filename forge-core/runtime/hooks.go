package runtime

import (
	"context"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// HookPoint identifies when a hook fires in the agent loop.
type HookPoint int

const (
	BeforeLLMCall HookPoint = iota
	AfterLLMCall
	BeforeToolExec
	AfterToolExec
	OnError
)

// HookContext carries data available to hooks at each hook point.
//
// LLMCallDuration / ToolExecDuration / Provider / Model are populated
// at the call site (loop.go) before the After* hook fires, so audit
// emitters can tag llm_call and tool_exec events with wall-clock
// timing and provider attribution. See issue #87 / FWS-3.
type HookContext struct {
	Messages      []llm.ChatMessage
	Response      *llm.ChatResponse
	ToolName      string
	ToolInput     string
	ToolOutput    string
	Error         error
	TaskID        string
	CorrelationID string

	// LLMCallDuration is the wall-clock time spent in the provider
	// client.Chat call. Populated for AfterLLMCall hooks.
	LLMCallDuration time.Duration
	// Provider / Model identify the LLM provider + model used for the
	// call. Populated for AfterLLMCall hooks so audit + A2A-header
	// emitters can stamp attribution without re-walking config.
	Provider string
	Model    string
	// ToolExecDuration is the wall-clock time spent executing the tool.
	// Populated for AfterToolExec hooks.
	ToolExecDuration time.Duration
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

// ProgressEvent describes a progress update during task execution.
type ProgressEvent struct {
	Phase   string // "tool_start", "tool_end"
	Tool    string
	Message string
}

// ProgressEmitter is a callback that emits progress events to the client.
type ProgressEmitter func(event ProgressEvent)

type progressEmitterKey struct{}

// WithProgressEmitter stores a ProgressEmitter in the context.
func WithProgressEmitter(ctx context.Context, emitter ProgressEmitter) context.Context {
	return context.WithValue(ctx, progressEmitterKey{}, emitter)
}

// ProgressEmitterFromContext retrieves the ProgressEmitter from the context, or nil.
func ProgressEmitterFromContext(ctx context.Context) ProgressEmitter {
	if e, ok := ctx.Value(progressEmitterKey{}).(ProgressEmitter); ok {
		return e
	}
	return nil
}
