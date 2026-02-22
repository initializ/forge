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

func TestMCPCallTool(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":"tool result"}}`)) //nolint:errcheck
	}))
	defer ts.Close()

	tool := NewMCPCallTool()
	if tool.Name() != "mcp_call" {
		t.Errorf("name: got %q", tool.Name())
	}

	args, _ := json.Marshal(map[string]any{
		"server_url": ts.URL,
		"tool_name":  "test_tool",
		"arguments":  map[string]string{"key": "val"},
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "tool result") {
		t.Errorf("result: %q", result)
	}
}

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
