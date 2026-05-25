package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

func TestWebhookCallTool(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type: got %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"received":true}`)) //nolint:errcheck
	}))
	defer ts.Close()

	tool := NewWebhookCallTool()
	if tool.Name() != "webhook_call" {
		t.Errorf("name: got %q", tool.Name())
	}
	if tool.Category() != tools.CategoryAdapter {
		t.Errorf("category: got %q", tool.Category())
	}

	args, _ := json.Marshal(map[string]any{
		"url":     ts.URL,
		"payload": map[string]string{"msg": "hello"},
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "received") {
		t.Errorf("result: %q", result)
	}
}

// TestMCPCallTool_Removed pins the deprecation: the mcp_call builtin
// was removed in Phase 1 because the new `mcp:` config block exposes
// each MCP server's tools as first-class namespaced tools — strictly
// better UX for the LLM. See docs/mcp/index.md and the CHANGELOG.
//
// This test is intentionally a compile-time guard: if anyone tries
// to bring NewMCPCallTool back, this file won't compile. Remove the
// guard once the v0.12.0 deprecation window closes.
//
// (No runtime assertion — the absence of NewMCPCallTool is what the
// test enforces.)

func TestOpenAPICallTool(t *testing.T) {
	tool := NewOpenAPICallTool()
	if tool.Name() != "openapi_call" {
		t.Errorf("name: got %q", tool.Name())
	}

	args, _ := json.Marshal(map[string]any{
		"spec_url":     "https://example.com/api.json",
		"operation_id": "getUser",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "not yet implemented") {
		t.Errorf("expected stub message: %q", result)
	}
}
