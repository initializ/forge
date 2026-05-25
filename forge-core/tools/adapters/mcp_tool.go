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
//
// Returns an error when the descriptor name is empty or contains the
// "__" namespace separator (review B9). The registry's contains-"__"
// admission check accepts ambiguous names like "<server>__" or
// "<server>____foo" otherwise; failing at construction means the
// adapter is never created and the caller can audit the rejection.
func NewMCPTool(opts MCPToolOpts) (*MCPTool, error) {
	if err := validateDescriptorName(opts.Descriptor.Name); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", opts.Server, err)
	}
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
	}, nil
}

// validateDescriptorName enforces the MCP-tool-name contract at the
// adapter boundary (review B9):
//
//   - non-empty (an empty name would produce "<server>__" — the
//     registry's strings.Contains check accepts that, but the LLM
//     and audit log get a tool with no actual name)
//   - no "__" substring (the namespace separator must appear at
//     exactly ONE position in the registered name: between server
//     and tool. A tool name like "foo__bar" produces "<server>__foo__bar"
//     — two separators, ambiguous parse for log consumers and a
//     conflict-vector if another tool happens to share a suffix).
func validateDescriptorName(name string) error {
	if name == "" {
		return errors.New("mcp tool descriptor name is empty")
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("mcp tool descriptor name %q contains \"__\" — reserved for the <server>__<tool> namespace separator", name)
	}
	return nil
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
		// Subtract suffix length so the FINAL string is at most
		// m.maxResultChars (review B16 — previously the cap was
		// +len(truncatedSuffix) bytes over the configured limit).
		cut := m.maxResultChars - len(truncatedSuffix)
		if cut < 0 {
			cut = 0
		}
		out = out[:cut] + truncatedSuffix
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
//	revoked     — OAuth refresh denied (operator must re-login)
//	no_token    — never logged in (review B11; distinct from revoked)
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
	case errors.Is(err, mcp.ErrNoToken):
		return "no_token"
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
