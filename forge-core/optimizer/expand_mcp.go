package optimizer

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/ctxzip/ccr"

	fmcp "github.com/initializ/forge/forge-core/mcp"
)

// expandMCPPath is where the context_expand MCP server is mounted. Claude Code
// connects to it with:
//
//	claude mcp add --transport http forge-optimizer http://127.0.0.1:8787/mcp
//
// It runs in the SAME process as the proxy so it reads the identical ctxzip
// store the compressor wrote to (bbolt is single-writer — one process must own
// it). This is what makes compression safe end-to-end: the model retrieves any
// offloaded original by the hash in its marker.
const expandMCPPath = "/mcp"

// expandToolInputSchema is the JSON Schema for the context_expand tool input.
const expandToolInputSchema = `{
	"type": "object",
	"properties": {
		"hash": {"type": "string", "description": "The hash from a <<ctxzip:HASH ...>> marker (the marker text itself is also accepted)"}
	},
	"required": ["hash"]
}`

const expandToolDescription = "Retrieve the original content behind a <<ctxzip:HASH ...>> compression marker. " +
	"Earlier tool results may contain such markers where bulky content was compressed away; " +
	"call this with the hash to see the full original data."

// expandMCPHandler is a minimal MCP Streamable-HTTP server exposing the single
// context_expand tool. It is intentionally stateless (no session id) and
// supports only what a tool-only server needs: initialize, tools/list,
// tools/call, ping. It reuses forge-core/mcp's wire types so the shapes match
// exactly what Forge's own MCP client expects.
type expandMCPHandler struct {
	store ccr.Store
	stats *StatsReporter
	log   *slog.Logger
}

func newExpandMCPHandler(store ccr.Store, stats *StatsReporter, log *slog.Logger) *expandMCPHandler {
	return &expandMCPHandler{store: store, stats: stats, log: log}
}

func (h *expandMCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// The optional server→client SSE stream. We have no server-initiated
		// messages, but hold the stream open (spec-compliant) so clients that
		// require it stay happy; clients that don't simply never GET here.
		h.serveEventStream(w, r)
		return
	case http.MethodDelete:
		// Session teardown — we are stateless, so just acknowledge.
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
		// fallthrough to JSON-RPC handling below
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var msg fmcp.JSONRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}

	// Notifications carry no id and expect no response body.
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch msg.Method {
	case fmcp.MethodInitialize:
		h.handleInitialize(w, msg)
	case fmcp.MethodToolsList:
		h.handleToolsList(w, msg)
	case fmcp.MethodToolsCall:
		h.handleToolsCall(w, msg)
	case "ping":
		writeRPCResult(w, msg.ID, json.RawMessage(`{}`))
	default:
		writeRPCError(w, msg.ID, -32601, "method not found: "+msg.Method)
	}
}

func (h *expandMCPHandler) handleInitialize(w http.ResponseWriter, msg fmcp.JSONRPCMessage) {
	// Echo the client's requested protocol version to guarantee agreement;
	// fall back to the version Forge pins.
	version := fmcp.ProtocolVersion
	var p fmcp.InitializeParams
	if len(msg.Params) > 0 && json.Unmarshal(msg.Params, &p) == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	result := fmcp.InitializeResult{
		ProtocolVersion: version,
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      fmcp.ServerInfo{Name: "forge-optimizer", Version: "0.1.0"},
	}
	writeRPCResultValue(w, msg.ID, result)
}

func (h *expandMCPHandler) handleToolsList(w http.ResponseWriter, msg fmcp.JSONRPCMessage) {
	result := fmcp.ListToolsResult{
		Tools: []fmcp.MCPToolDescriptor{{
			Name:        contextExpandToolName,
			Description: expandToolDescription,
			InputSchema: json.RawMessage(expandToolInputSchema),
		}},
	}
	writeRPCResultValue(w, msg.ID, result)
}

func (h *expandMCPHandler) handleToolsCall(w http.ResponseWriter, msg fmcp.JSONRPCMessage) {
	var params fmcp.CallToolParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		writeRPCError(w, msg.ID, -32602, "invalid params")
		return
	}
	if params.Name != contextExpandToolName {
		writeRPCResultValue(w, msg.ID, fmcp.CallToolResult{
			Content: []fmcp.ToolContent{{Type: "text", Text: "unknown tool: " + params.Name}},
			IsError: true,
		})
		return
	}

	var input struct {
		Hash string `json:"hash"`
	}
	_ = json.Unmarshal(params.Arguments, &input)
	hash := normalizeHash(input.Hash)
	if hash == "" {
		writeRPCResultValue(w, msg.ID, fmcp.CallToolResult{
			Content: []fmcp.ToolContent{{Type: "text", Text: "hash is required"}},
			IsError: true,
		})
		return
	}

	entry, ok := h.store.Get(hash)
	if h.stats != nil {
		h.stats.RecordExpansion(ok)
	}
	if h.log != nil {
		h.log.Info("context_expand", "hash", hash, "hit", ok, "bytes", len(entry.Original))
	}
	if !ok {
		// A miss is not fatal: the disk / original command is the source of
		// truth. Say so, as a normal (non-error) result the model can act on.
		writeRPCResultValue(w, msg.ID, fmcp.CallToolResult{
			Content: []fmcp.ToolContent{{Type: "text", Text: fmt.Sprintf(
				"No stored content for hash %s (expired or evicted). "+
					"Re-run the tool that produced the original output to regenerate it.", hash)}},
		})
		return
	}
	writeRPCResultValue(w, msg.ID, fmcp.CallToolResult{
		Content: []fmcp.ToolContent{{Type: "text", Text: string(entry.Original)}},
	})
}

// serveEventStream holds an SSE stream open with periodic keepalive comments,
// closing when the client disconnects. We never push server-initiated messages.
func (h *expandMCPHandler) serveEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": ok\n\n")
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// normalizeHash tolerates the model passing a whole marker instead of the bare
// hash: "<<ctxzip:abc123 51_rows_offloaded>>", "ctxzip:abc123", "hash=abc123",
// or "abc123:51" all normalize to "abc123". Mirrors compress.normalizeHash.
func normalizeHash(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<<")
	s = strings.TrimPrefix(s, "ctxzip:")
	s = strings.TrimPrefix(s, "hash=")
	s = strings.TrimSuffix(s, ">>")
	if i := strings.IndexAny(s, " ,:"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// --- JSON-RPC response helpers ---

func writeRPCResultValue(w http.ResponseWriter, id *json.Number, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		writeRPCError(w, id, -32603, "internal error")
		return
	}
	writeRPCResult(w, id, raw)
}

func writeRPCResult(w http.ResponseWriter, id *json.Number, result json.RawMessage) {
	resp := fmcp.JSONRPCMessage{Jsonrpc: "2.0", ID: id, Result: result}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id *json.Number, code int, message string) {
	resp := fmcp.JSONRPCMessage{Jsonrpc: "2.0", ID: id, Error: &fmcp.JSONRPCError{Code: code, Message: message}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
