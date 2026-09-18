// Package mcpserver is a minimal MCP server that exposes a set of forge-core
// tools to a coding agent (Claude Code). It bridges any []tools.Tool to MCP over
// two transports — Streamable-HTTP and stdio — sharing one dispatch core and
// reusing forge-core/mcp's wire types so the shapes match what an MCP client
// expects. Modeled on the optimizer's context_expand server
// (forge-core/optimizer/expand_mcp.go).
//
// The surface registers ONE forge MCP server (forge_docs + forge-ops). Over
// stdio it is registered durably with `claude mcp add` at user scope, so Claude
// Code spawns it every session — including a direct resume — the same way the
// optimizer keeps context_expand available.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	fmcp "github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/tools"
)

// mcpPath is where the HTTP transport is mounted.
const mcpPath = "/mcp"

// callTimeout bounds a single tools/call so a wedged forge-op can't hang claude.
const callTimeout = 10 * time.Minute

// Handler serves a fixed toolset over MCP. Stateless (no session id): it
// supports initialize, tools/list, tools/call, ping.
type Handler struct {
	tools map[string]tools.Tool
	order []string
	token string // optional bearer for the HTTP transport
}

// NewHandler builds a Handler over the given toolset. token, when non-empty, is
// required as `Authorization: Bearer <token>` on the HTTP transport.
func NewHandler(toolset []tools.Tool, token string) *Handler {
	h := &Handler{tools: make(map[string]tools.Tool, len(toolset)), token: token}
	for _, t := range toolset {
		if _, dup := h.tools[t.Name()]; dup {
			continue
		}
		h.tools[t.Name()] = t
		h.order = append(h.order, t.Name())
	}
	return h
}

// handle dispatches one request message, returning the result value (to be
// marshaled into a JSON-RPC result) or a JSON-RPC error. Transport-agnostic.
func (h *Handler) handle(ctx context.Context, msg fmcp.JSONRPCMessage) (any, *fmcp.JSONRPCError) {
	switch msg.Method {
	case fmcp.MethodInitialize:
		version := fmcp.ProtocolVersion
		var p fmcp.InitializeParams
		if len(msg.Params) > 0 && json.Unmarshal(msg.Params, &p) == nil && p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
		return fmcp.InitializeResult{
			ProtocolVersion: version,
			Capabilities:    map[string]any{"tools": map[string]any{}},
			ServerInfo:      fmcp.ServerInfo{Name: "forge", Version: "0.1.0"},
		}, nil
	case fmcp.MethodToolsList:
		descs := make([]fmcp.MCPToolDescriptor, 0, len(h.order))
		for _, name := range h.order {
			t := h.tools[name]
			descs = append(descs, fmcp.MCPToolDescriptor{
				Name:        t.Name(),
				Description: t.Description(),
				InputSchema: t.InputSchema(),
			})
		}
		return fmcp.ListToolsResult{Tools: descs}, nil
	case fmcp.MethodToolsCall:
		return h.callTool(ctx, msg)
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &fmcp.JSONRPCError{Code: -32601, Message: "method not found: " + msg.Method}
	}
}

// callTool runs a tools/call, returning a CallToolResult (tool failures are
// carried as IsError results, not JSON-RPC errors) or a params error.
func (h *Handler) callTool(ctx context.Context, msg fmcp.JSONRPCMessage) (any, *fmcp.JSONRPCError) {
	var params fmcp.CallToolParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return nil, &fmcp.JSONRPCError{Code: -32602, Message: "invalid params"}
	}
	tool, ok := h.tools[params.Name]
	if !ok {
		return fmcp.CallToolResult{
			Content: []fmcp.ToolContent{{Type: "text", Text: "unknown tool: " + params.Name}},
			IsError: true,
		}, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out, err := tool.Execute(callCtx, params.Arguments)
	if err != nil {
		text := err.Error()
		if out != "" {
			text = out + "\n\n" + err.Error()
		}
		return fmcp.CallToolResult{Content: []fmcp.ToolContent{{Type: "text", Text: text}}, IsError: true}, nil
	}
	return fmcp.CallToolResult{Content: []fmcp.ToolContent{{Type: "text", Text: out}}}, nil
}

// --- HTTP transport ---

func (h *Handler) authOK(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + h.token
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ServeHTTP implements the MCP Streamable-HTTP contract.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.serveEventStream(w, r)
		return
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
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
	if msg.ID == nil { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr := h.handle(r.Context(), msg)
	if rpcErr != nil {
		writeRPCError(w, msg.ID, rpcErr.Code, rpcErr.Message)
		return
	}
	writeRPCResultValue(w, msg.ID, result)
}

func (h *Handler) serveEventStream(w http.ResponseWriter, r *http.Request) {
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

// --- stdio transport ---

// ServeStdio runs the MCP server over newline-delimited JSON-RPC on in/out
// (Claude Code's stdio transport). It returns when in reaches EOF or ctx is
// canceled. This is the durable path: `claude mcp add ... -- forge mcp-serve`
// makes Claude Code spawn this every session.
func ServeStdio(ctx context.Context, toolset []tools.Tool, in io.Reader, out io.Writer) error {
	h := NewHandler(toolset, "")
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out) // Encode appends a newline → correct stdio framing

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg fmcp.JSONRPCMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = enc.Encode(fmcp.JSONRPCMessage{Jsonrpc: "2.0", Error: &fmcp.JSONRPCError{Code: -32700, Message: "parse error"}})
			continue
		}
		if msg.ID == nil { // notification — no response
			continue
		}
		result, rpcErr := h.handle(ctx, msg)
		resp := fmcp.JSONRPCMessage{Jsonrpc: "2.0", ID: msg.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else if raw, err := json.Marshal(result); err == nil {
			resp.Result = raw
		} else {
			resp.Error = &fmcp.JSONRPCError{Code: -32603, Message: "internal error"}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// --- HTTP server lifecycle ---

// Server is a running forge MCP server bound to a local port (HTTP transport).
type Server struct {
	httpServer *http.Server
	url        string
}

// Start binds a loopback listener and serves the toolset over HTTP.
func Start(toolset []tools.Tool, token string) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("binding forge MCP listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle(mcpPath, NewHandler(toolset, token))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s := &Server{httpServer: srv, url: fmt.Sprintf("http://%s%s", ln.Addr().String(), mcpPath)}
	go func() { _ = srv.Serve(ln) }()
	return s, nil
}

// URL is the MCP endpoint (http://127.0.0.1:PORT/mcp).
func (s *Server) URL() string { return s.url }

// Close shuts the server down.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// --- JSON-RPC response helpers (HTTP) ---

func writeRPCResultValue(w http.ResponseWriter, id *json.Number, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		writeRPCError(w, id, -32603, "internal error")
		return
	}
	resp := fmcp.JSONRPCMessage{Jsonrpc: "2.0", ID: id, Result: raw}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id *json.Number, code int, message string) {
	resp := fmcp.JSONRPCMessage{Jsonrpc: "2.0", ID: id, Error: &fmcp.JSONRPCError{Code: code, Message: message}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
