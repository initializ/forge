// Package adapters provides tools that call out to external systems.
//
// MCPTool wraps a single MCP server tool and exposes it to the LLM
// executor as a first-class tool. Phase 1 names use the "<server>__
// <tool>" namespacing scheme (decision §3.7 of the recommendations
// doc); this is enforced by tools.Registry.Register, which only
// admits "__" in names belonging to types that implement
// tools.MCPSource.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/tools"
)

// defaultMaxResultChars caps a single MCP tool call result to keep
// chatty MCP servers from blowing the agent context budget. The
// runtime can override via NewMCPTool.MaxResultChars; 0 means use
// this default.
const defaultMaxResultChars = 64 * 1024 // 64 KiB

// truncatedSuffix is appended when MCPTool.Execute clips a long result.
const truncatedSuffix = "\n[truncated]"

// MCPTool implements tools.Tool by delegating to an mcp.Client.
//
// Name format: "<server>__<tool>" — the double-underscore separator
// is reserved for MCP namespacing. Builtins cannot use it.
//
// Audit invariant: Execute emits EventMCPToolCall before invocation
// and EventMCPToolResult after, carrying ONLY sizes, durations, and
// reason codes — never the argument bytes or result content.
type MCPTool struct {
	server     string
	descriptor mcp.MCPToolDescriptor
	client     mcp.Client

	maxResultChars int
	audit          *runtime.AuditLogger
}

// MCPToolOpts configures a new MCPTool.
type MCPToolOpts struct {
	// Server is the MCP server name from forge.yaml (e.g. "linear").
	Server string

	// Descriptor is the tool's discovery payload from tools/list.
	Descriptor mcp.MCPToolDescriptor

	// Client is the per-server JSON-RPC client. Required.
	Client mcp.Client

	// MaxResultChars truncates tool results above this size. 0 ⇒ default.
	MaxResultChars int

	// Audit emits mcp_tool_call / mcp_tool_result events. May be nil
	// for tests; production wiring always passes one.
	Audit *runtime.AuditLogger
}

// NewMCPTool constructs an MCPTool from a discovered descriptor.
func NewMCPTool(opts MCPToolOpts) *MCPTool {
	maxChars := opts.MaxResultChars
	if maxChars <= 0 {
		maxChars = defaultMaxResultChars
	}
	return &MCPTool{
		server:         opts.Server,
		descriptor:     opts.Descriptor,
		client:         opts.Client,
		maxResultChars: maxChars,
		audit:          opts.Audit,
	}
}

// MCPSource marks this tool as MCP-sourced — required so
// tools.Registry admits "__" in the name.
func (m *MCPTool) MCPSource() {}

// Name returns "<server>__<tool>".
func (m *MCPTool) Name() string {
	return m.server + "__" + m.descriptor.Name
}

// Description forwards the MCP server's description.
func (m *MCPTool) Description() string {
	return m.descriptor.Description
}

// Category is always CategoryAdapter — MCP tools are external
// adapters by definition.
func (m *MCPTool) Category() tools.Category { return tools.CategoryAdapter }

// InputSchema returns the JSON Schema from discovery, byte-for-byte.
// The Server's Discovering state has already validated it, so the
// LLM-function-calling layer can trust it without re-parsing.
func (m *MCPTool) InputSchema() json.RawMessage {
	return m.descriptor.InputSchema
}

// Execute invokes the MCP tool over the per-server Client. Emits
// mcp_tool_call / mcp_tool_result audit events that carry NO byte
// payload — only sizes, duration, and reason codes.
func (m *MCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	start := time.Now()
	correlationID := runtime.CorrelationIDFromContext(ctx)

	m.emitCall(correlationID, len(args))
	res, err := m.client.CallTool(ctx, m.descriptor.Name, args)
	durMs := time.Since(start).Milliseconds()

	if err != nil {
		reason := classifyToolErr(err)
		m.emitResult(correlationID, durMs, 0, false, reason)
		return "", fmt.Errorf("mcp %s/%s: %w", m.server, m.descriptor.Name, err)
	}

	out := flattenContent(res.Content)
	if len(out) > m.maxResultChars {
		out = out[:m.maxResultChars] + truncatedSuffix
	}

	if res.IsError {
		m.emitResult(correlationID, durMs, len(out), false, "tool_error")
	} else {
		m.emitResult(correlationID, durMs, len(out), true, "")
	}
	return out, nil
}

// flattenContent collapses an MCP tool response's Content array into
// a single string. Text parts are concatenated with newlines; image
// and resource parts are summarized as "[image type/<mime>]" and
// "[resource]" placeholders so the LLM sees something useful but we
// don't blow the context on binary blobs.
func flattenContent(parts []mcp.ToolContent) string {
	if len(parts) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteByte('\n')
		}
		switch p.Type {
		case "text":
			sb.WriteString(p.Text)
		case "image":
			sb.WriteString("[image type/")
			sb.WriteString(p.MimeType)
			sb.WriteString("]")
		case "resource":
			sb.WriteString("[resource]")
		default:
			// Unknown type — render the type tag without body.
			sb.WriteString("[")
			sb.WriteString(p.Type)
			sb.WriteString("]")
		}
	}
	return sb.String()
}

// classifyToolErr maps an error to a short reason code for the
// mcp_tool_result audit event. Reason codes are part of the audit
// contract; renaming any is a breaking change for ops dashboards.
//
//	unavailable — transport down / 5xx / timeout
//	protocol    — JSON-RPC error response, malformed frame
//	revoked     — OAuth token revoked (operator must re-login)
//	canceled    — ctx canceled by caller
//	unknown     — anything else (should be rare; investigate)
func classifyToolErr(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, mcp.ErrTransportUnavailable):
		return "unavailable"
	case errors.Is(err, mcp.ErrTokenRevoked):
		return "revoked"
	case errors.Is(err, mcp.ErrProtocolError):
		return "protocol"
	default:
		return "unknown"
	}
}

func (m *MCPTool) emitCall(correlationID string, argsSize int) {
	if m.audit == nil {
		return
	}
	m.audit.Emit(runtime.AuditEvent{
		Event:         runtime.EventMCPToolCall,
		CorrelationID: correlationID,
		Fields: map[string]any{
			"server":    m.server,
			"tool":      m.descriptor.Name,
			"args_size": argsSize,
		},
	})
}

func (m *MCPTool) emitResult(correlationID string, durMs int64, resultSize int, ok bool, reason string) {
	if m.audit == nil {
		return
	}
	fields := map[string]any{
		"server":      m.server,
		"tool":        m.descriptor.Name,
		"duration_ms": durMs,
		"result_size": resultSize,
		"ok":          ok,
	}
	if reason != "" {
		fields["reason"] = reason
	}
	m.audit.Emit(runtime.AuditEvent{
		Event:         runtime.EventMCPToolResult,
		CorrelationID: correlationID,
		Fields:        fields,
	})
}

// compile-time check that MCPTool satisfies both interfaces.
var (
	_ tools.Tool      = (*MCPTool)(nil)
	_ tools.MCPSource = (*MCPTool)(nil)
)
