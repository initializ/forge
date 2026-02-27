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
	client             llm.Client
	tools              ToolExecutor
	hooks              *HookRegistry
	systemPrompt       string
	maxIter            int
	compactor          *Compactor
	store              *MemoryStore
	logger             Logger
	modelName          string // resolved model name for context budget
	charBudget         int    // resolved character budget
	maxToolResultChars int    // computed from char budget
}

// LLMExecutorConfig configures the LLM executor.
type LLMExecutorConfig struct {
	Client        llm.Client
	Tools         ToolExecutor
	Hooks         *HookRegistry
	SystemPrompt  string
	MaxIterations int
	Compactor     *Compactor
	Store         *MemoryStore
	Logger        Logger
	ModelName     string // model name for context-aware budgeting
	CharBudget    int    // explicit char budget override (0 = auto from model)
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
	logger := cfg.Logger
	if logger == nil {
		logger = &nopLogger{}
	}

	// Resolve character budget from model name if not explicitly set.
	budget := cfg.CharBudget
	if budget == 0 {
		if cfg.ModelName != "" {
			budget = ContextBudgetForModel(cfg.ModelName)
		} else {
			budget = defaultContextTokens * charsPerToken
		}
	}

	// Tool result limit: 25% of char budget, floor 2K, cap 400K.
	toolLimit := budget / 4
	if toolLimit < 2_000 {
		toolLimit = 2_000
	}
	if toolLimit > 400_000 {
		toolLimit = 400_000
	}

	return &LLMExecutor{
		client:             cfg.Client,
		tools:              cfg.Tools,
		hooks:              hooks,
		systemPrompt:       cfg.SystemPrompt,
		maxIter:            maxIter,
		compactor:          cfg.Compactor,
		store:              cfg.Store,
		logger:             logger,
		modelName:          cfg.ModelName,
		charBudget:         budget,
		maxToolResultChars: toolLimit,
	}
}

// Execute processes a message through the LLM agent loop.
func (e *LLMExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error) {
	mem := NewMemory(e.systemPrompt, e.charBudget, e.modelName)

	// Try to recover session from disk. If found, the disk snapshot
	// supersedes task.History to avoid duplicating messages.
	recovered := false
	if e.store != nil {
		saved, err := e.store.Load(task.ID)
		if err != nil {
			e.logger.Warn("failed to load session from disk", map[string]any{
				"task_id": task.ID, "error": err.Error(),
			})
		} else if saved != nil {
			mem.LoadFromStore(saved)
			recovered = true
			e.logger.Info("session recovered from disk", map[string]any{
				"task_id":  task.ID,
				"messages": len(saved.Messages),
			})
		}
	}

	// Load task history only if not recovered from disk.
	if !recovered {
		for _, histMsg := range task.History {
			mem.Append(a2aMessageToLLM(histMsg))
		}
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
		// Run compaction before LLM call (best-effort).
		if e.compactor != nil {
			if _, err := e.compactor.MaybeCompact(task.ID, mem); err != nil {
				e.logger.Warn("compaction error", map[string]any{
					"task_id": task.ID, "error": err.Error(),
				})
			}
		}

		messages := mem.Messages()

		// Fire BeforeLLMCall hook
		if err := e.hooks.Fire(ctx, BeforeLLMCall, &HookContext{
			Messages:      messages,
			TaskID:        TaskIDFromContext(ctx),
			CorrelationID: CorrelationIDFromContext(ctx),
		}); err != nil {
			return nil, fmt.Errorf("before LLM call hook: %w", err)
		}

		// Call LLM
		req := &llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		}

		resp, err := e.client.Chat(ctx, req)
		if err != nil {
			_ = e.hooks.Fire(ctx, OnError, &HookContext{
				Error:         err,
				TaskID:        TaskIDFromContext(ctx),
				CorrelationID: CorrelationIDFromContext(ctx),
			})
			// Return user-friendly error (raw error is already logged via OnError hook)
			return nil, fmt.Errorf("something went wrong while processing your request, please try again")
		}

		// Fire AfterLLMCall hook
		if err := e.hooks.Fire(ctx, AfterLLMCall, &HookContext{
			Messages:      messages,
			Response:      resp,
			TaskID:        TaskIDFromContext(ctx),
			CorrelationID: CorrelationIDFromContext(ctx),
		}); err != nil {
			return nil, fmt.Errorf("after LLM call hook: %w", err)
		}

		// Append assistant message to memory
		mem.Append(resp.Message)

		// Check if we're done (no tool calls)
		if resp.FinishReason == "stop" || len(resp.Message.ToolCalls) == 0 {
			e.persistSession(task.ID, mem)
			return llmMessageToA2A(resp.Message), nil
		}

		// Execute tool calls
		if e.tools == nil {
			e.persistSession(task.ID, mem)
			return llmMessageToA2A(resp.Message), nil
		}

		for _, tc := range resp.Message.ToolCalls {
			// Fire BeforeToolExec hook
			if err := e.hooks.Fire(ctx, BeforeToolExec, &HookContext{
				ToolName:      tc.Function.Name,
				ToolInput:     tc.Function.Arguments,
				TaskID:        TaskIDFromContext(ctx),
				CorrelationID: CorrelationIDFromContext(ctx),
			}); err != nil {
				return nil, fmt.Errorf("before tool exec hook: %w", err)
			}

			// Execute tool
			result, execErr := e.tools.Execute(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if execErr != nil {
				result = fmt.Sprintf("Error executing tool %s: %s", tc.Function.Name, execErr.Error())
			}

			// Truncate oversized tool results to avoid LLM API errors.
			// Limit is proportional to model context budget (25%, floor 2K, cap 400K).
			if len(result) > e.maxToolResultChars {
				result = result[:e.maxToolResultChars] + "\n\n[OUTPUT TRUNCATED — original length: " + strconv.Itoa(len(result)) + " chars]"
			}

			// Fire AfterToolExec hook
			if err := e.hooks.Fire(ctx, AfterToolExec, &HookContext{
				ToolName:      tc.Function.Name,
				ToolInput:     tc.Function.Arguments,
				ToolOutput:    result,
				Error:         execErr,
				TaskID:        TaskIDFromContext(ctx),
				CorrelationID: CorrelationIDFromContext(ctx),
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

	e.persistSession(task.ID, mem)
	return nil, fmt.Errorf("agent loop exceeded maximum iterations (%d)", e.maxIter)
}

// persistSession saves the current memory state to disk (best-effort).
func (e *LLMExecutor) persistSession(taskID string, mem *Memory) {
	if e.store == nil {
		return
	}
	mem.mu.Lock()
	data := &SessionData{
		TaskID:   taskID,
		Messages: mem.messages,
		Summary:  mem.existingSummary,
	}
	mem.mu.Unlock()

	if err := e.store.Save(data); err != nil {
		e.logger.Warn("failed to persist session", map[string]any{
			"task_id": taskID, "error": err.Error(),
		})
	}
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
