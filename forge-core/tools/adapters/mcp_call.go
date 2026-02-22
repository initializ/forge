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

type mcpCallTool struct{}

type mcpCallInput struct {
	ServerURL string          `json:"server_url"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (t *mcpCallTool) Name() string             { return "mcp_call" }
func (t *mcpCallTool) Description() string      { return "Call a tool on an MCP server via JSON-RPC" }
func (t *mcpCallTool) Category() tools.Category { return tools.CategoryAdapter }

func (t *mcpCallTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"server_url": {"type": "string", "description": "MCP server URL"},
			"tool_name": {"type": "string", "description": "Tool name to invoke"},
			"arguments": {"type": "object", "description": "Tool arguments"}
		},
		"required": ["server_url", "tool_name"]
	}`)
}

func (t *mcpCallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input mcpCallInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	// Build JSON-RPC request
	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      input.ToolName,
			"arguments": input.Arguments,
		},
	}

	data, _ := json.Marshal(rpcReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, input.ServerURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mcp call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(body), nil
}

// NewMCPCallTool creates an MCP call tool.
func NewMCPCallTool() tools.Tool { return &mcpCallTool{} }
