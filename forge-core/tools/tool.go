// Package tools provides the tool plugin system for Forge agents.
// Tools are capabilities that an LLM agent can invoke during execution.
package tools

import (
	"context"
	"encoding/json"

	"github.com/initializ/forge/forge-core/llm"
)

// Category classifies tools by their source/purpose.
type Category string

const (
	CategoryBuiltin Category = "builtin"
	CategoryAdapter Category = "adapter"
	CategoryDev     Category = "dev"
	CategoryCustom  Category = "custom"
)

// Tool is the interface that all tools must implement.
type Tool interface {
	// Name returns the unique tool name.
	Name() string
	// Description returns a human-readable description of the tool.
	Description() string
	// Category returns the tool's category.
	Category() Category
	// InputSchema returns the JSON Schema for the tool's input parameters.
	InputSchema() json.RawMessage
	// Execute runs the tool with the given JSON arguments.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// MCPSource is an optional interface signalling that a tool was
// discovered from an MCP server. The registry uses this to permit
// "__" in the tool's name — that separator is reserved for the
// namespaced form "<server-name>__<tool-name>" so MCP tools cannot
// collide with builtin or adapter tool names. Tools that do NOT
// implement MCPSource are rejected at registration time if their
// name contains "__".
//
// Implementing this is a single no-op method; see
// forge-core/tools/adapters/mcp_tool.go.
type MCPSource interface {
	Tool
	MCPSource() // marker — body is empty
}

// ToLLMDefinition converts a Tool to an llm.ToolDefinition for use with LLM APIs.
func ToLLMDefinition(t Tool) llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.InputSchema(),
		},
	}
}
