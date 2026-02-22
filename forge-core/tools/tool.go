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
