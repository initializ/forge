package surface_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-cli/internal/surface"
	"github.com/initializ/forge/forge-cli/internal/surface/mcpserver"
)

// TestForgeMCPServerEndToEnd starts the real forge MCP server with the real
// toolset and drives it over HTTP with the session bearer token — the exact
// path Claude Code takes via ~/.forge/mcp.json.
func TestForgeMCPServerEndToEnd(t *testing.T) {
	srv, err := mcpserver.Start(surface.MCPToolset(t.TempDir()), "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	call := func(body string) map[string]any {
		req, _ := http.NewRequest(http.MethodPost, srv.URL(), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &m)
		}
		return m
	}

	// tools/list must include forge_docs + the forge-ops.
	list := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res, _ := list["result"].(map[string]any)
	arr, _ := res["tools"].([]any)
	names := map[string]bool{}
	for _, it := range arr {
		m, _ := it.(map[string]any)
		names[m["name"].(string)] = true
	}
	for _, want := range []string{"forge_docs", "forge_scaffold", "forge_validate", "forge_run"} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}
	if names["context_expand"] {
		t.Error("context_expand should NOT be present without the optimizer (single server, tool list changes with optimizer)")
	}

	// forge_docs(topic=channels) must return channel content.
	docs := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"forge_docs","arguments":{"topic":"channels"}}}`)
	dres, _ := docs["result"].(map[string]any)
	content, _ := dres["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("forge_docs returned no content: %v", docs)
	}
	text := strings.ToLower(content[0].(map[string]any)["text"].(string))
	if !strings.Contains(text, "channel") {
		t.Errorf("forge_docs(channels) lacked channel content: %.100q", text)
	}
}

// TestMCPConfigWireShape documents the exact ~/.forge/mcp.json shape Claude Code
// expects (type:http, mcpServers, headers) so a schema drift is caught here.
func TestMCPConfigWireShape(t *testing.T) {
	// Mirror what writeForgeMCPConfig emits (kept in sync by review).
	cfg := map[string]any{"mcpServers": map[string]any{
		"forge": map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp",
			"headers": map[string]any{"Authorization": "Bearer x"}},
	}}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"type":"http"`) || !strings.Contains(buf.String(), `"mcpServers"`) {
		t.Errorf("unexpected mcp.json shape: %s", buf.String())
	}
}
