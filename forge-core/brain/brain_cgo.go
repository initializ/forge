//go:build brain

package brain

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/initializ/forge/forge-core/llm"
	llamago "github.com/tcpipuk/llama-go"
)

// maxToolResultChars caps the size of tool output fed to the small model.
// 17K chars of search results overwhelms a 0.6B model; 3000 chars is enough
// for the model to synthesize a useful answer.
const maxToolResultChars = 3000

// cgoEngine implements engine using llama-go CGo bindings.
type cgoEngine struct {
	model    *llamago.Model
	llamaCtx *llamago.Context
	mu       sync.Mutex
}

func newEngine(cfg Config) (engine, error) {
	if cfg.ModelPath == "" {
		return nil, fmt.Errorf("brain: model path is required")
	}

	modelOpts := []llamago.ModelOption{
		llamago.WithGPULayers(cfg.GPULayers),
		llamago.WithSilentLoading(),
	}

	model, err := llamago.LoadModel(cfg.ModelPath, modelOpts...)
	if err != nil {
		return nil, fmt.Errorf("brain: load model: %w", err)
	}

	var ctxOpts []llamago.ContextOption
	if cfg.ContextSize > 0 {
		ctxOpts = append(ctxOpts, llamago.WithContext(cfg.ContextSize))
	}
	// When ContextSize is 0, llama-go uses the model's native max (e.g. 32K for Qwen3)
	if cfg.Threads > 0 {
		ctxOpts = append(ctxOpts, llamago.WithThreads(cfg.Threads))
	}

	llamaCtx, err := model.NewContext(ctxOpts...)
	if err != nil {
		_ = model.Close()
		return nil, fmt.Errorf("brain: create context: %w", err)
	}

	return &cgoEngine{
		model:    model,
		llamaCtx: llamaCtx,
	}, nil
}

// Chat performs non-streaming inference.
func (e *cgoEngine) Chat(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (*chatResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	llamaMsgs := convertMessages(msgs, tools)

	chatOpts := llamago.ChatOptions{}
	if opts.MaxTokens > 0 {
		chatOpts.MaxTokens = llamago.Int(opts.MaxTokens)
	}
	if opts.Temperature > 0 {
		chatOpts.Temperature = llamago.Float32(opts.Temperature)
	}
	if len(opts.Stop) > 0 {
		chatOpts.StopWords = opts.Stop
	}

	resp, err := e.llamaCtx.Chat(ctx, llamaMsgs, chatOpts)
	if err != nil {
		return nil, fmt.Errorf("brain: chat: %w", err)
	}

	result := &chatResult{
		Content:    resp.Content,
		Reasoning:  resp.ReasoningContent,
		StopReason: "stop",
	}

	// Parse tool calls from the response content, validating against the actual tool list
	if len(tools) > 0 {
		if toolCalls, parseErr := parseToolCalls(resp.Content); parseErr == nil && len(toolCalls) > 0 {
			// Filter out hallucinated tools not in the provided list
			validCalls := filterValidToolCalls(toolCalls, tools)
			if len(validCalls) > 0 {
				result.ToolCalls = validCalls
				result.Content = stripToolCallJSON(resp.Content)
				result.StopReason = "tool_calls"
			}
		}
	}

	return result, nil
}

// ChatStream performs streaming inference.
func (e *cgoEngine) ChatStream(ctx context.Context, msgs []llm.ChatMessage, tools []llm.ToolDefinition, opts chatOpts) (<-chan streamChunk, error) {
	e.mu.Lock()

	llamaMsgs := convertMessages(msgs, tools)

	chatOpts := llamago.ChatOptions{}
	if opts.MaxTokens > 0 {
		chatOpts.MaxTokens = llamago.Int(opts.MaxTokens)
	}
	if opts.Temperature > 0 {
		chatOpts.Temperature = llamago.Float32(opts.Temperature)
	}
	if len(opts.Stop) > 0 {
		chatOpts.StopWords = opts.Stop
	}

	// ChatStream returns (<-chan ChatDelta, <-chan error)
	deltaCh, errCh := e.llamaCtx.ChatStream(ctx, llamaMsgs, chatOpts)

	out := make(chan streamChunk, 16)
	go func() {
		defer e.mu.Unlock()
		defer close(out)

		for {
			select {
			case delta, ok := <-deltaCh:
				if !ok {
					// Delta channel closed — send final done chunk
					select {
					case out <- streamChunk{Done: true}:
					case <-ctx.Done():
					}
					return
				}
				chunk := streamChunk{
					Content: delta.Content,
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			case err, ok := <-errCh:
				if ok && err != nil {
					select {
					case out <- streamChunk{Err: err}:
					case <-ctx.Done():
					}
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// Close frees the model and context resources.
func (e *cgoEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.llamaCtx != nil {
		_ = e.llamaCtx.Close()
	}
	if e.model != nil {
		_ = e.model.Close()
	}
	return nil
}

// convertMessages converts forge messages to llama-go messages, injecting tool
// definitions into the system prompt and /no_think to disable chain-of-thought.
func convertMessages(msgs []llm.ChatMessage, tools []llm.ToolDefinition) []llamago.ChatMessage {
	var llamaMsgs []llamago.ChatMessage

	for i, msg := range msgs {
		role := msg.Role

		switch role {
		case llm.RoleTool:
			// llama-go doesn't have a tool role; convert to user message.
			// Truncate long tool results to avoid overwhelming the small model.
			toolContent := msg.Content
			if len(toolContent) > maxToolResultChars {
				toolContent = toolContent[:maxToolResultChars] + "\n...[truncated]"
			}
			content := fmt.Sprintf("[Tool Result: %s]\n%s", msg.Name, toolContent)
			llamaMsgs = append(llamaMsgs, llamago.ChatMessage{
				Role:    "user",
				Content: content,
			})
			continue

		case llm.RoleSystem:
			content := msg.Content
			// Inject tool definitions into the first system message
			if i == 0 && len(tools) > 0 {
				content += "\n\n" + FormatToolPrompt(tools)
			}
			llamaMsgs = append(llamaMsgs, llamago.ChatMessage{
				Role:    "system",
				Content: content,
			})
			continue

		case llm.RoleAssistant:
			content := msg.Content
			// Append tool call info to assistant content
			if len(msg.ToolCalls) > 0 {
				var parts []string
				if content != "" {
					parts = append(parts, content)
				}
				for _, tc := range msg.ToolCalls {
					parts = append(parts, fmt.Sprintf(`[Tool Call: %s] %s`, tc.Function.Name, tc.Function.Arguments))
				}
				content = strings.Join(parts, "\n")
			}
			llamaMsgs = append(llamaMsgs, llamago.ChatMessage{
				Role:    "assistant",
				Content: content,
			})
			continue
		}

		// Default: user messages pass through
		llamaMsgs = append(llamaMsgs, llamago.ChatMessage{
			Role:    role,
			Content: msg.Content,
		})
	}

	// If no system message existed but we have tools, prepend one
	if len(tools) > 0 {
		hasSystem := false
		for _, m := range llamaMsgs {
			if m.Role == "system" {
				hasSystem = true
				break
			}
		}
		if !hasSystem {
			systemMsg := llamago.ChatMessage{
				Role:    "system",
				Content: FormatToolPrompt(tools),
			}
			llamaMsgs = append([]llamago.ChatMessage{systemMsg}, llamaMsgs...)
		}
	}

	// Inject /no_think into the last user message to disable Qwen3 thinking mode.
	// This prevents wasted tokens on <think> blocks in the response.
	for i := len(llamaMsgs) - 1; i >= 0; i-- {
		if llamaMsgs[i].Role == "user" {
			llamaMsgs[i].Content = "/no_think\n" + llamaMsgs[i].Content
			break
		}
	}

	return llamaMsgs
}

// filterValidToolCalls returns only tool calls whose names match a tool in the list.
// This prevents the model from hallucinating non-existent tools like "response".
func filterValidToolCalls(calls []llm.ToolCall, tools []llm.ToolDefinition) []llm.ToolCall {
	validNames := make(map[string]bool, len(tools))
	for _, t := range tools {
		validNames[t.Function.Name] = true
	}

	var valid []llm.ToolCall
	for _, tc := range calls {
		if validNames[tc.Function.Name] {
			valid = append(valid, tc)
		}
	}
	return valid
}

// stripToolCallJSON removes the tool call JSON block from response content.
func stripToolCallJSON(content string) string {
	idx := strings.Index(content, `{"name":`)
	if idx >= 0 {
		return strings.TrimSpace(content[:idx])
	}
	return content
}
