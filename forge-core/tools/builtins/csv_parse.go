package builtins

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

type csvParseTool struct{}

type csvParseInput struct {
	Data      string `json:"data"`
	Delimiter string `json:"delimiter,omitempty"`
	Headers   bool   `json:"headers,omitempty"`
}

func (t *csvParseTool) Name() string             { return "csv_parse" }
func (t *csvParseTool) Description() string      { return "Parse CSV data into JSON array" }
func (t *csvParseTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *csvParseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"data": {"type": "string", "description": "CSV data to parse"},
			"delimiter": {"type": "string", "description": "Field delimiter (default comma)"},
			"headers": {"type": "boolean", "description": "First row contains headers (default true)"}
		},
		"required": ["data"]
	}`)
}

func (t *csvParseTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input csvParseInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	reader := csv.NewReader(strings.NewReader(input.Data))
	if input.Delimiter != "" {
		runes := []rune(input.Delimiter)
		if len(runes) > 0 {
			reader.Comma = runes[0]
		}
	}

	records, err := reader.ReadAll()
	if err != nil {
		return "", fmt.Errorf("parsing CSV: %w", err)
	}

	if len(records) == 0 {
		return "[]", nil
	}

	// Default: headers = true (unless explicitly set to false via JSON)
	useHeaders := true
	// Check if headers was explicitly set in the JSON
	var raw map[string]json.RawMessage
	if json.Unmarshal(args, &raw) == nil {
		if _, ok := raw["headers"]; ok {
			useHeaders = input.Headers
		}
	}

	if useHeaders && len(records) > 1 {
		headers := records[0]
		var result []map[string]string
		for _, row := range records[1:] {
			obj := make(map[string]string)
			for i, val := range row {
				if i < len(headers) {
					obj[headers[i]] = val
				}
			}
			result = append(result, obj)
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data), nil
	}

	data, _ := json.MarshalIndent(records, "", "  ")
	return string(data), nil
}
