package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

// stubTool is a minimal tools.Tool for exercising the MCP bridge.
type stubTool struct {
	name string
	out  string
	err  error
}

func (s stubTool) Name() string                 { return s.name }
func (s stubTool) Description() string          { return s.name + " does a thing" }
func (s stubTool) Category() tools.Category     { return tools.CategoryDev }
func (s stubTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return s.out, s.err
}

func post(t *testing.T, h http.Handler, token, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v (%s)", err, rec.Body.String())
		}
	}
	return out
}

func TestToolsListAndCall(t *testing.T) {
	h := NewHandler([]tools.Tool{stubTool{name: "forge_docs", out: "hi"}}, "")

	list := post(t, h, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res, _ := list["result"].(map[string]any)
	toolsArr, _ := res["tools"].([]any)
	if len(toolsArr) != 1 {
		t.Fatalf("expected 1 tool, got %v", res)
	}

	call := post(t, h, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"forge_docs","arguments":{}}}`)
	cres, _ := call["result"].(map[string]any)
	content, _ := cres["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content: %v", call)
	}
	first, _ := content[0].(map[string]any)
	if first["text"] != "hi" {
		t.Errorf("text = %v, want hi", first["text"])
	}
}

func TestUnknownTool(t *testing.T) {
	h := NewHandler([]tools.Tool{stubTool{name: "forge_docs"}}, "")
	call := post(t, h, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	cres, _ := call["result"].(map[string]any)
	if cres["isError"] != true {
		t.Errorf("unknown tool should be isError: %v", call)
	}
}

func TestBearerTokenEnforced(t *testing.T) {
	h := NewHandler([]tools.Tool{stubTool{name: "forge_docs"}}, "secret")
	// No token → 401.
	req := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
	// Correct token → OK.
	post(t, h, "secret", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
}

func TestInitialize(t *testing.T) {
	h := NewHandler(nil, "")
	init := post(t, h, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res, _ := init["result"].(map[string]any)
	si, _ := res["serverInfo"].(map[string]any)
	if si["name"] != "forge" {
		t.Errorf("server name = %v, want forge", si["name"])
	}
}

func TestServeStdioRoundTrip(t *testing.T) {
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"forge_docs","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), []tools.Tool{stubTool{name: "forge_docs", out: "hello"}}, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// initialize + tools/list + tools/call = 3 responses (the notification gets none).
	if len(lines) != 3 {
		t.Fatalf("expected 3 responses, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"serverInfo"`) || !strings.Contains(lines[0], `"forge"`) {
		t.Errorf("initialize response wrong: %s", lines[0])
	}
	if !strings.Contains(lines[2], "hello") {
		t.Errorf("tools/call response missing tool output: %s", lines[2])
	}
}
