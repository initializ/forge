package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// ToolExecutor provides tool execution capabilities to the engine.
// The tools.Registry satisfies this interface via Go structural typing.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error)
	ToolDefinitions() []llm.ToolDefinition
}

// LLMExecutor implements AgentExecutor using an LLM client with tool calling.
type LLMExecutor struct {
	client       llm.Client
	tools        ToolExecutor
	hooks        *HookRegistry
	systemPrompt string
	maxIter      int
}

// LLMExecutorConfig configures the LLM executor.
type LLMExecutorConfig struct {
	Client       llm.Client
	Tools        ToolExecutor
	Hooks        *HookRegistry
	SystemPrompt string
	MaxIterations int
}

// NewLLMExecutor creates a new LLMExecutor with the given configuration.
func NewLLMExecutor(cfg LLMExecutorConfig) *LLMExecutor {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = 10
	}
	hooks := cfg.Hooks
	if hooks == nil {
		hooks = NewHookRegistry()
	}
	return &LLMExecutor{
		client:       cfg.Client,
		tools:        cfg.Tools,
		hooks:        hooks,
		systemPrompt: cfg.SystemPrompt,
		maxIter:      maxIter,
	}
}

// Execute processes a message through the LLM agent loop.
func (e *LLMExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error) {
	mem := NewMemory(e.systemPrompt, 0)

	// Load task history into memory
	for _, histMsg := range task.History {
		mem.Append(a2aMessageToLLM(histMsg))
	}

	// Append the new user message
	mem.Append(a2aMessageToLLM(*msg))

	// Build tool definitions
	var toolDefs []llm.ToolDefinition
	if e.tools != nil {
		toolDefs = e.tools.ToolDefinitions()
	}

	// Agent loop
	for i := 0; i < e.maxIter; i++ {
		messages := mem.Messages()

		// Fire BeforeLLMCall hook
		if err := e.hooks.Fire(ctx, BeforeLLMCall, &HookContext{Messages: messages}); err != nil {
			return nil, fmt.Errorf("before LLM call hook: %w", err)
		}

		// Call LLM
		req := &llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		}

		resp, err := e.client.Chat(ctx, req)
		if err != nil {
			_ = e.hooks.Fire(ctx, OnError, &HookContext{Error: err})
			// Return user-friendly error (raw error is already logged via OnError hook)
			return nil, fmt.Errorf("Something went wrong while processing your request. Please try again.")
		}

		// Fire AfterLLMCall hook
		if err := e.hooks.Fire(ctx, AfterLLMCall, &HookContext{
			Messages: messages,
			Response: resp,
		}); err != nil {
			return nil, fmt.Errorf("after LLM call hook: %w", err)
		}

		// Append assistant message to memory
		mem.Append(resp.Message)

		// Check if we're done (no tool calls)
		if resp.FinishReason == "stop" || len(resp.Message.ToolCalls) == 0 {
			return llmMessageToA2A(resp.Message), nil
		}

		// Execute tool calls
		if e.tools == nil {
			return llmMessageToA2A(resp.Message), nil
		}

		for _, tc := range resp.Message.ToolCalls {
			// Fire BeforeToolExec hook
			if err := e.hooks.Fire(ctx, BeforeToolExec, &HookContext{
				ToolName:  tc.Function.Name,
				ToolInput: tc.Function.Arguments,
			}); err != nil {
				return nil, fmt.Errorf("before tool exec hook: %w", err)
			}

			// Execute tool
			result, execErr := e.tools.Execute(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if execErr != nil {
				result = fmt.Sprintf("Error executing tool %s: %s", tc.Function.Name, execErr.Error())
			}

			// Truncate oversized tool results to avoid LLM API errors.
			// Use a limit below maxMessageChars so the suffix fits within the memory cap.
			const maxToolResultChars = 49_000 // ~12K tokens, leaves room for truncation suffix
			if len(result) > maxToolResultChars {
				result = result[:maxToolResultChars] + "\n\n[OUTPUT TRUNCATED — original length: " + strconv.Itoa(len(result)) + " chars]"
			}

			// Fire AfterToolExec hook
			if err := e.hooks.Fire(ctx, AfterToolExec, &HookContext{
				ToolName:   tc.Function.Name,
				ToolInput:  tc.Function.Arguments,
				ToolOutput: result,
				Error:      execErr,
			}); err != nil {
				return nil, fmt.Errorf("after tool exec hook: %w", err)
			}

			// Append tool result to memory
			mem.Append(llm.ChatMessage{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}

	return nil, fmt.Errorf("agent loop exceeded maximum iterations (%d)", e.maxIter)
}

// ExecuteStream runs the tool-calling loop non-streaming, then emits the final
// response as a single message on the channel. True word-by-word streaming is v2.
func (e *LLMExecutor) ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error) {
	ch := make(chan *a2a.Message, 1)
	go func() {
		defer close(ch)
		resp, err := e.Execute(ctx, task, msg)
		if err != nil {
			ch <- &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []a2a.Part{a2a.NewTextPart("Error: " + err.Error())},
			}
			return
		}
		ch <- resp
	}()
	return ch, nil
}

// Close is a no-op for LLMExecutor.
func (e *LLMExecutor) Close() error { return nil }

// a2aMessageToLLM converts an A2A message to an LLM chat message.
func a2aMessageToLLM(msg a2a.Message) llm.ChatMessage {
	role := llm.RoleUser
	if msg.Role == a2a.MessageRoleAgent {
		role = llm.RoleAssistant
	}

	var textParts []string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText && p.Text != "" {
			textParts = append(textParts, p.Text)
		}
	}

	return llm.ChatMessage{
		Role:    role,
		Content: strings.Join(textParts, "\n"),
	}
}

// llmMessageToA2A converts an LLM chat message to an A2A message.
func llmMessageToA2A(msg llm.ChatMessage) *a2a.Message {
	role := a2a.MessageRoleAgent
	if msg.Role == llm.RoleUser {
		role = a2a.MessageRoleUser
	}

	return &a2a.Message{
		Role:  role,
		Parts: []a2a.Part{a2a.NewTextPart(msg.Content)},
	}
}
