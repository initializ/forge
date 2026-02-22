package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

type jsonParseTool struct{}

type jsonParseInput struct {
	Data  string `json:"data"`
	Query string `json:"query,omitempty"`
}

func (t *jsonParseTool) Name() string { return "json_parse" }
func (t *jsonParseTool) Description() string {
	return "Parse JSON data and optionally query with dot notation"
}
func (t *jsonParseTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *jsonParseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"data": {"type": "string", "description": "JSON string to parse"},
			"query": {"type": "string", "description": "Dot-notation path to query (e.g. 'user.name')"}
		},
		"required": ["data"]
	}`)
}

func (t *jsonParseTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input jsonParseInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	var parsed any
	if err := json.Unmarshal([]byte(input.Data), &parsed); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}

	if input.Query == "" {
		data, _ := json.MarshalIndent(parsed, "", "  ")
		return string(data), nil
	}

	// Dot-notation query
	result := queryDotNotation(parsed, input.Query)
	data, _ := json.MarshalIndent(result, "", "  ")
	return string(data), nil
}

func queryDotNotation(data any, path string) any {
	parts := strings.Split(path, ".")
	current := data
	for _, part := range parts {
		switch v := current.(type) {
		case map[string]any:
			current = v[part]
		default:
			return nil
		}
	}
	return current
}
