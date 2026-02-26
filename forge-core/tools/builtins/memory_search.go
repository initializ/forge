package builtins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/memory"
	"github.com/initializ/forge/forge-core/tools"
)

type memorySearchTool struct {
	mgr *memory.Manager
}

// NewMemorySearchTool creates a memory_search tool backed by a Manager.
// This tool is registered conditionally (not via All()) since it needs
// a Manager instance.
func NewMemorySearchTool(mgr *memory.Manager) tools.Tool {
	return &memorySearchTool{mgr: mgr}
}

type memorySearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type memorySearchOutput struct {
	Text      string  `json:"text"`
	Source    string  `json:"source"`
	LineStart int     `json:"line_start"`
	LineEnd   int     `json:"line_end"`
	Score     float64 `json:"score"`
}

func (t *memorySearchTool) Name() string { return "memory_search" }
func (t *memorySearchTool) Description() string {
	return "Search long-term agent memory for relevant context from past interactions and curated facts"
}
func (t *memorySearchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *memorySearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query describing the information to find"},
			"max_results": {"type": "integer", "description": "Maximum number of results to return (default: 5)", "default": 5}
		},
		"required": ["query"]
	}`)
}

func (t *memorySearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input memorySearchInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	if input.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if input.MaxResults <= 0 {
		input.MaxResults = 5
	}

	results, err := t.mgr.Search(ctx, input.Query)
	if err != nil {
		return "", fmt.Errorf("memory search: %w", err)
	}

	// Limit results
	if len(results) > input.MaxResults {
		results = results[:input.MaxResults]
	}

	output := make([]memorySearchOutput, len(results))
	for i, r := range results {
		output[i] = memorySearchOutput{
			Text:      r.Chunk.Content,
			Source:    r.Chunk.Source,
			LineStart: r.Chunk.LineStart,
			LineEnd:   r.Chunk.LineEnd,
			Score:     r.Score,
		}
	}

	data, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("marshalling results: %w", err)
	}
	return string(data), nil
}
