package mcp

import (
	"encoding/json"
	"fmt"
)

// ServerState tracks a single MCP server's position in its lifecycle
// state machine. See server.go (Commit 3) for the driver and
// server_state.go for the legal transition table.
//
// The String() form is what surfaces in `forge mcp list` output and in
// audit events; keep it stable — operators grep on it.
type ServerState int

const (
	// StateConfigured — parsed from forge.yaml, not yet started. Initial
	// state set by NewServer.
	StateConfigured ServerState = iota

	// StateConnecting — opening the underlying Transport (HTTP dial,
	// OAuth refresh). Next: Initializing | Failed.
	StateConnecting

	// StateInitializing — performing the JSON-RPC initialize handshake
	// (including the "initialized" notification). Hard-fails on
	// protocol-version mismatch. Next: Discovering | Failed.
	StateInitializing

	// StateDiscovering — calling tools/list and validating each
	// descriptor's input schema as JSON Schema draft-07. A malformed
	// schema fails the SERVER, not the LLM call. Next: Ready | Failed.
	StateDiscovering

	// StateReady — handshake complete, tools registered, server idle.
	// Next: Calling | Stopped.
	StateReady

	// StateCalling — at least one tools/call is in flight. Concurrent
	// calls do not reset the state; we transition back to Ready only
	// when all in-flight calls resolve. Next: Ready | Degraded |
	// Stopped.
	StateCalling

	// StateDegraded — transient transport error mid-call. Will attempt
	// to reconnect per the backoff schedule. Next: Reconnecting |
	// Stopped.
	StateDegraded

	// StateReconnecting — re-running the connect+initialize+discover
	// chain after backoff. Next: Initializing (on transport open) |
	// Failed (on backoff exhaustion).
	StateReconnecting

	// StateFailed — terminal failure. Required=true servers cause the
	// Manager to cancel its parent context (i.e., the agent exits).
	// Required=false servers simply have their tools removed from the
	// registry. Next: Stopped.
	StateFailed

	// StateStopped — terminal. Reached after ctx cancel or after Failed.
	// No outbound transitions.
	StateStopped
)

// String returns the lowercase event-log form (e.g., "ready",
// "reconnecting"). The values are part of the audit-event contract;
// renaming any of them is a breaking change for downstream consumers.
func (s ServerState) String() string {
	switch s {
	case StateConfigured:
		return "configured"
	case StateConnecting:
		return "connecting"
	case StateInitializing:
		return "initializing"
	case StateDiscovering:
		return "discovering"
	case StateReady:
		return "ready"
	case StateCalling:
		return "calling"
	case StateDegraded:
		return "degraded"
	case StateReconnecting:
		return "reconnecting"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// MCPToolDescriptor is the parsed form of a single entry in the
// tools/list response. The Phase 1 adapter (Commit 4) wraps each
// descriptor in a tools.Tool that the LLM executor consumes directly.
//
// InputSchema is a raw JSON Schema (draft-07) used both for runtime
// validation and as the parameter spec advertised to the LLM. We keep
// it as json.RawMessage rather than a parsed Go type so we never lose
// fidelity round-tripping it to the OpenAI/Anthropic function-calling
// shape.
type MCPToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// Annotations are the MCP spec's optional tool behavior hints
	// (readOnlyHint / destructiveHint / idempotentHint). Advisory metadata
	// from the server — surfaced so platform-side discovery can seed
	// side-effect classifications; the runtime does not act on them.
	Annotations *MCPToolAnnotations `json:"annotations,omitempty"`
}

// MCPToolAnnotations mirrors the MCP tool annotations object. Pointers so
// "absent" and "false" stay distinguishable.
type MCPToolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
}
