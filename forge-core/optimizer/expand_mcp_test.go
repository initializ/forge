package optimizer

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/initializ/ctxzip/ccr"

	fmcp "github.com/initializ/forge/forge-core/mcp"
)

// rpc POSTs a JSON-RPC request to the MCP endpoint and returns the decoded frame.
func rpc(t *testing.T, url, method string, id int, params any) fmcp.JSONRPCMessage {
	t.Helper()
	var praw json.RawMessage
	if params != nil {
		praw, _ = json.Marshal(params)
	}
	num := json.Number(strconv.Itoa(id))
	req := fmcp.JSONRPCMessage{Jsonrpc: "2.0", ID: &num, Method: method, Params: praw}
	body, _ := json.Marshal(req)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out fmcp.JSONRPCMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s response %q: %v", method, raw, err)
	}
	return out
}

// TestExpandMCP_EndToEnd compresses a bulky tool result (offloading to the
// store), then drives the MCP endpoint exactly as Claude Code would —
// initialize → tools/list → tools/call — and asserts the original comes back.
func TestExpandMCP_EndToEnd(t *testing.T) {
	c := newTestCompressor(t)

	// Compress something so the store holds an original.
	big := bigJSONText()
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "find failures"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": big},
		}},
		map[string]any{"role": "user", "content": "?"},
	}}
	body, _ := json.Marshal(req)
	out, st, _ := c.Transform(body)
	if st.Markers < 1 {
		t.Fatalf("expected a marker, got %+v", st)
	}
	hash := ccr.ExtractHashes(toolResultContent(t, messagesOf(t, out)[1]))[0]

	// Stand up the MCP endpoint over the same store.
	stats := NewStatsReporter(0)
	h := newExpandMCPHandler(c.Store(), stats, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// initialize — server must agree on the client's protocol version.
	init := rpc(t, srv.URL, fmcp.MethodInitialize, 1, fmcp.InitializeParams{
		ProtocolVersion: fmcp.ProtocolVersion,
		ClientInfo:      fmcp.ClientInfo{Name: "test", Version: "1"},
	})
	var ir fmcp.InitializeResult
	if err := json.Unmarshal(init.Result, &ir); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if ir.ProtocolVersion != fmcp.ProtocolVersion {
		t.Errorf("protocol version = %q, want %q", ir.ProtocolVersion, fmcp.ProtocolVersion)
	}

	// tools/list — must advertise context_expand.
	list := rpc(t, srv.URL, fmcp.MethodToolsList, 2, map[string]any{})
	var lr fmcp.ListToolsResult
	if err := json.Unmarshal(list.Result, &lr); err != nil {
		t.Fatalf("tools/list result: %v", err)
	}
	if len(lr.Tools) != 1 || lr.Tools[0].Name != contextExpandToolName {
		t.Fatalf("tools/list = %+v, want one context_expand", lr.Tools)
	}

	// tools/call — retrieve the original by hash.
	call := rpc(t, srv.URL, fmcp.MethodToolsCall, 3, fmcp.CallToolParams{
		Name:      contextExpandToolName,
		Arguments: json.RawMessage(`{"hash":"` + hash + `"}`),
	})
	var cr fmcp.CallToolResult
	if err := json.Unmarshal(call.Result, &cr); err != nil {
		t.Fatalf("tools/call result: %v", err)
	}
	if cr.IsError || len(cr.Content) == 0 {
		t.Fatalf("tools/call errored or empty: %+v", cr)
	}
	if !strings.Contains(cr.Content[0].Text, "row 200 processed") {
		t.Errorf("expanded content missing offloaded row: %.120s", cr.Content[0].Text)
	}
	if stats.snapshot().Totals.Expansions != 1 {
		t.Errorf("expansion not recorded")
	}

	// Marker text (not bare hash) must also resolve.
	call2 := rpc(t, srv.URL, fmcp.MethodToolsCall, 4, fmcp.CallToolParams{
		Name:      contextExpandToolName,
		Arguments: json.RawMessage(`{"hash":"<<ctxzip:` + hash + ` 392_rows_offloaded>>"}`),
	})
	var cr2 fmcp.CallToolResult
	_ = json.Unmarshal(call2.Result, &cr2)
	if cr2.IsError || len(cr2.Content) == 0 || !strings.Contains(cr2.Content[0].Text, "row 200 processed") {
		t.Errorf("marker-form hash did not resolve: %+v", cr2)
	}
}

func TestExpandMCP_Miss(t *testing.T) {
	c := newTestCompressor(t)
	h := newExpandMCPHandler(c.Store(), nil, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	call := rpc(t, srv.URL, fmcp.MethodToolsCall, 1, fmcp.CallToolParams{
		Name:      contextExpandToolName,
		Arguments: json.RawMessage(`{"hash":"deadbeefcafe"}`),
	})
	var cr fmcp.CallToolResult
	if err := json.Unmarshal(call.Result, &cr); err != nil {
		t.Fatalf("result: %v", err)
	}
	// A miss is a normal (non-error) result telling the model to re-run.
	if cr.IsError || !strings.Contains(cr.Content[0].Text, "No stored content") {
		t.Errorf("unexpected miss result: %+v", cr)
	}
}

func TestExpandMCP_NotificationNoBody(t *testing.T) {
	c := newTestCompressor(t)
	h := newExpandMCPHandler(c.Store(), nil, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// A notification (no id) must get 202 and an empty body.
	note := fmcp.JSONRPCMessage{Jsonrpc: "2.0", Method: fmcp.MethodInitialized}
	body, _ := json.Marshal(note)
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
}
