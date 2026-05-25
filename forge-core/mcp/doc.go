// Package mcp implements Forge's Model Context Protocol client.
//
// Phase 1 scope (v0.12.0):
//
//   - HTTP transport only (Streamable HTTP). Stdio MCP servers are on
//     the roadmap; see docs/mcp/index.md for the deferred-design
//     discussion. The Forge runtime never spawns subprocesses for MCP
//     and this package contains no os/exec dependency — pinned by
//     TestB4_PackageHasNoOsExecImport. The laptop-time browser opener
//     used by OAuth Login lives in forge-cli/cmd/mcp_browser.go and is
//     injected by the caller via OAuthFlow.BrowserOpener (review B4).
//   - Per-server lifecycle managed by a goroutine driving a state
//     machine (Configured → Connecting → Initializing → Discovering →
//     Ready → Calling, with Degraded/Reconnecting for transient
//     failures). See server.go (Commit 3).
//   - Manager (Commit 4) starts servers in parallel and aggregates
//     discovered tools.
//   - Tools surface to the agent's LLM as namespaced "<server>__<tool>"
//     entries via forge-core/tools/adapters/mcp_tool.go (Commit 4).
//
// Wire-protocol version is pinned (see protocol.go). A version
// mismatch from the server hard-fails the handshake; the runtime does
// not negotiate down.
//
// All MCP audit events (mcp_server_started, mcp_tool_call, etc.) live
// in forge-core/runtime/audit.go and carry NO byte payload — only
// sizes, durations, and reason codes. Never log argument or result
// bytes.
package mcp
