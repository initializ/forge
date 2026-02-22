// Package adapters provides tools that integrate with external services.
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/initializ/forge/forge-core/tools"
)

type webhookCallTool struct{}

type webhookCallInput struct {
	URL     string            `json:"url"`
	Payload json.RawMessage   `json:"payload"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (t *webhookCallTool) Name() string            { return "webhook_call" }
func (t *webhookCallTool) Description() string     { return "POST JSON payload to a webhook URL" }
func (t *webhookCallTool) Category() tools.Category { return tools.CategoryAdapter }

func (t *webhookCallTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "Webhook URL to POST to"},
			"payload": {"type": "object", "description": "JSON payload to send"},
			"headers": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Additional HTTP headers"}
		},
		"required": ["url", "payload"]
	}`)
}

func (t *webhookCallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input webhookCallInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, input.URL, bytes.NewReader(input.Payload))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range input.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("webhook call: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	result := map[string]any{
		"status": resp.StatusCode,
		"body":   string(body),
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

// NewWebhookCallTool creates a webhook call tool.
func NewWebhookCallTool() tools.Tool { return &webhookCallTool{} }
