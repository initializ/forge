package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/types"
)

// apiTool must opt into the "__" namespace via the NamespacedSource marker.
var _ tools.NamespacedSource = (*apiTool)(nil)

func TestAPITool_Name(t *testing.T) {
	tool := NewAPITool("memberservice", "https://x", "", types.APIOp{Name: "reverse_fee", Method: "POST", Path: "/fees/reverse"}, 0)
	if tool.Name() != "memberservice__reverse_fee" {
		t.Errorf("Name = %q, want memberservice__reverse_fee", tool.Name())
	}
	if tool.Category() != tools.CategoryAdapter {
		t.Errorf("Category = %q, want adapter", tool.Category())
	}
}

// POST op: remaining args → JSON body; bearer header from the token env.
func TestAPITool_POSTBody(t *testing.T) {
	t.Setenv("MEMBER_TOK", "s3cret")
	var method, path, auth, ct, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth, ct = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"reversal_id":"rev_1","status":"completed"}`))
	}))
	defer srv.Close()

	tool := NewAPITool("memberservice", srv.URL, "MEMBER_TOK",
		types.APIOp{Name: "reverse_fee", Method: "POST", Path: "/fees/reverse"}, 5*time.Second)
	out, err := tool.Execute(context.Background(), []byte(`{"amount":25,"fee_type":"overdraft"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if method != "POST" || path != "/fees/reverse" {
		t.Errorf("method/path = %s %s, want POST /fees/reverse", method, path)
	}
	if auth != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want Bearer s3cret", auth)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(body, `"amount":25`) || !strings.Contains(body, `"fee_type":"overdraft"`) {
		t.Errorf("body = %q, want amount+fee_type", body)
	}
	if !strings.Contains(out, `"status":200`) || !strings.Contains(out, "rev_1") {
		t.Errorf("out = %q, want status 200 + rev_1", out)
	}
}

// GET op: {account_id} → path substitution; remaining args → query; no body.
func TestAPITool_GETPathAndQuery(t *testing.T) {
	var method, path, rawquery, auth string
	hadBody := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, rawquery, auth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		hadBody = len(b) > 0
		_, _ = w.Write([]byte(`{"account_id":"12345","entries":[]}`))
	}))
	defer srv.Close()

	tool := NewAPITool("memberservice", srv.URL, "",
		types.APIOp{Name: "get_account_history", Method: "GET", Path: "/accounts/{account_id}/history"}, 5*time.Second)
	_, err := tool.Execute(context.Background(), []byte(`{"account_id":"12345","destination":"member_account"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if method != "GET" || path != "/accounts/12345/history" {
		t.Errorf("method/path = %s %s, want GET /accounts/12345/history", method, path)
	}
	if rawquery != "destination=member_account" {
		t.Errorf("query = %q, want destination=member_account", rawquery)
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want empty (no token env)", auth)
	}
	if hadBody {
		t.Error("GET should not send a body")
	}
}

func TestAPITool_InputSchema(t *testing.T) {
	tool := NewAPITool("s", "https://x", "",
		types.APIOp{Name: "op", Method: "POST", Path: "/p", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"amount": map[string]any{"type": "number"}}}}, 0)
	var schema struct {
		Type       string                 `json:"type"`
		Properties map[string]interface{} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	if schema.Type != "object" || schema.Properties["amount"] == nil {
		t.Errorf("schema = %+v, want object with amount", schema)
	}
}
