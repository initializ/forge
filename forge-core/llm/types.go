// Package llm provides canonical types for LLM chat interactions.
// These types are provider-agnostic; each provider translates to/from
// its native API format.
package llm

import "encoding/json"

// Role constants for chat messages.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ChatMessage represents a single message in a chat conversation.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall represents an LLM request to invoke a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall contains the function name and arguments for a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ToolDefinition describes a tool available to the LLM.
type ToolDefinition struct {
	Type     string         `json:"type"` // always "function"
	Function FunctionSchema `json:"function"`
}

// FunctionSchema describes a function's name, description, and parameters.
type FunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ChatRequest is a provider-agnostic chat completion request.
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []ChatMessage    `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
}

// ChatResponse is a provider-agnostic chat completion response.
type ChatResponse struct {
	ID           string      `json:"id"`
	Message      ChatMessage `json:"message"`
	Usage        UsageInfo   `json:"usage"`
	FinishReason string      `json:"finish_reason"`
	// Endpoint is the URL the client POSTed to (base URL + provider path).
	// Set by the provider client so the llm_call audit event can record the
	// invoked path even when payload capture is off. Internal only (json:"-").
	Endpoint string `json:"-"`
}

// StreamDelta represents a single chunk in a streaming response.
type StreamDelta struct {
	Content      string     `json:"content,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Done         bool       `json:"done,omitempty"`
	Usage        *UsageInfo `json:"usage,omitempty"`
}

// UsageInfo contains token usage information.
//
// Field naming aligns with OTel GenAI semantic conventions
// (gen_ai.usage.input_tokens / gen_ai.usage.output_tokens) so audit
// consumers can correlate Forge audit events with OTel traces without
// a translation table. See issue #87 / FWS-3.
type UsageInfo struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
