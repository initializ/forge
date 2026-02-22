// Package builtins provides built-in tools available to all agents.
package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/tools"
)

type httpRequestTool struct{}

type httpRequestInput struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

func (t *httpRequestTool) Name() string        { return "http_request" }
func (t *httpRequestTool) Description() string { return "Make HTTP requests (GET, POST, PUT, DELETE)" }
func (t *httpRequestTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *httpRequestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"method": {"type": "string", "enum": ["GET", "POST", "PUT", "DELETE"], "description": "HTTP method"},
			"url": {"type": "string", "description": "URL to send the request to"},
			"headers": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Request headers"},
			"body": {"type": "string", "description": "Request body"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["method", "url"]
	}`)
}

func (t *httpRequestTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input httpRequestInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	timeout := time.Duration(input.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	var bodyReader io.Reader
	if input.Body != "" {
		bodyReader = strings.NewReader(input.Body)
	}

	req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	for k, v := range input.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	result := map[string]any{
		"status":      resp.StatusCode,
		"status_text": resp.Status,
		"body":        string(body),
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}
