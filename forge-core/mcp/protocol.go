package mcp

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion pins the MCP wire-protocol version Forge speaks.
// The Initialize handshake hard-fails when the server returns a
// different value — the runtime does NOT negotiate down. Bumping this
// constant is a deliberate PR with tests and docs updated together.
const ProtocolVersion = "2025-06-18"

// JSON-RPC 2.0 method names used in the MCP wire protocol. These are
// stable strings; renaming any of them is a breaking change.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "notifications/initialized" // notification — no id, no response
	MethodToolsList   = "tools/list"
	MethodToolsCall   = "tools/call"
)

// JSONRPCMessage is the union frame for JSON-RPC 2.0 over MCP.
//
// A single frame can be:
//   - request:      ID set, Method set, Params optional
//   - response:     ID set, Result OR Error set (never both)
//   - notification: Method set, ID nil
//
// We intentionally keep Result/Params as json.RawMessage so we don't
// lose fidelity round-tripping schemas to the LLM function-calling
// layer or losing precision on numeric IDs.
type JSONRPCMessage struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is the error object on a JSON-RPC 2.0 response.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Validate returns a non-nil error when the frame is missing fields
// that JSON-RPC 2.0 requires. Cheap structural check; does NOT enforce
// MCP-level semantics (those live in client.go).
func (m JSONRPCMessage) Validate() error {
	if m.Jsonrpc != "2.0" {
		return fmt.Errorf("%w: jsonrpc field must be \"2.0\" (got %q)", ErrProtocolError, m.Jsonrpc)
	}
	hasID := m.ID != nil
	hasMethod := m.Method != ""
	hasResult := len(m.Result) > 0
	hasError := m.Error != nil

	switch {
	case hasMethod && !hasID:
		// notification — must not carry result/error
		if hasResult || hasError {
			return fmt.Errorf("%w: notification frame must not carry result or error", ErrProtocolError)
		}
	case hasMethod && hasID:
		// request — must not carry result/error
		if hasResult || hasError {
			return fmt.Errorf("%w: request frame must not carry result or error", ErrProtocolError)
		}
	case !hasMethod && hasID:
		// response — must carry result XOR error
		if hasResult == hasError { // both true or both false
			return fmt.Errorf("%w: response frame must carry result OR error (xor)", ErrProtocolError)
		}
	default:
		return fmt.Errorf("%w: empty frame (no method, no id)", ErrProtocolError)
	}
	return nil
}

// ClientInfo is the payload sent in the initialize request.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerInfo is the payload received in the initialize response.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams is the params field of an initialize request.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}

// InitializeResult is the result field of an initialize response.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
}

// ListToolsResult is the result field of a tools/list response.
type ListToolsResult struct {
	Tools []MCPToolDescriptor `json:"tools"`
}

// CallToolParams is the params field of a tools/call request.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult is the result field of a tools/call response.
type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent is one piece of a tool's response payload.
type ToolContent struct {
	Type     string          `json:"type"` // "text" | "image" | "resource"
	Text     string          `json:"text,omitempty"`
	MimeType string          `json:"mimeType,omitempty"`
	Data     string          `json:"data,omitempty"`     // base64 for image/resource
	Resource json.RawMessage `json:"resource,omitempty"` // resource reference
}
