package mcp

import "errors"

// Sentinel errors used across the MCP package. Each is intended for
// use with errors.Is so callers can react to specific classes of
// failure without parsing error strings.
//
// Reason codes for the audit events (mcp_tool_result, mcp_server_failed)
// are derived from these sentinels — keep the set stable.
var (
	// ErrTransportUnavailable signals that the underlying transport
	// (HTTP dial, network, 5xx response) is not reachable. The MCP
	// server itself may be fine; the path to it isn't. Lifecycle:
	// triggers Calling → Degraded → Reconnecting in the Server state
	// machine.
	ErrTransportUnavailable = errors.New("mcp: transport unavailable")

	// ErrProtocolError signals that the wire response was syntactically
	// or semantically wrong (malformed JSON-RPC, 4xx with a JSON-RPC
	// error body, missing required fields). The server understood the
	// frame and rejected it; we should NOT retry.
	ErrProtocolError = errors.New("mcp: protocol error")

	// ErrVersionMismatch signals that the server advertised a
	// protocolVersion the client does not support. The handshake hard-
	// fails; the runtime does NOT negotiate down. Bumping the pinned
	// version is a deliberate PR.
	ErrVersionMismatch = errors.New("mcp: protocol version mismatch")

	// ErrTokenRevoked signals that an OAuth refresh attempt was denied
	// by the authorization server (invalid_grant, expired_token). The
	// runtime cannot self-heal — an operator must re-run
	// `forge mcp login <name>`. Wrapped errors carry the upstream
	// message for forensics.
	ErrTokenRevoked = errors.New("mcp: oauth token revoked")

	// ErrClosed signals operations on a Transport whose Close() has
	// already been called.
	ErrClosed = errors.New("mcp: transport closed")
)
