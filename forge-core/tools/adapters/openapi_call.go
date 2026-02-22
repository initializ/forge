package adapters

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/tools"
)

type openapiCallTool struct{}

type openapiCallInput struct {
	SpecURL     string          `json:"spec_url"`
	OperationID string          `json:"operation_id"`
	Params      json.RawMessage `json:"params,omitempty"`
}

func (t *openapiCallTool) Name() string { return "openapi_call" }
func (t *openapiCallTool) Description() string {
	return "Call an OpenAPI endpoint by operation ID (stub)"
}
func (t *openapiCallTool) Category() tools.Category { return tools.CategoryAdapter }

func (t *openapiCallTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"spec_url": {"type": "string", "description": "URL to OpenAPI spec"},
			"operation_id": {"type": "string", "description": "Operation ID to invoke"},
			"params": {"type": "object", "description": "Parameters for the operation"}
		},
		"required": ["spec_url", "operation_id"]
	}`)
}

func (t *openapiCallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input openapiCallInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	return fmt.Sprintf(`{"error": "OpenAPI call not yet implemented. Spec: %s, Operation: %s"}`, input.SpecURL, input.OperationID), nil
}

// NewOpenAPICallTool creates an OpenAPI call tool.
func NewOpenAPICallTool() tools.Tool { return &openapiCallTool{} }
