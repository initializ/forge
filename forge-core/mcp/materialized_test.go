package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

func newUserServer(t *testing.T, url string, schemas []types.MCPToolSchema, platform *types.PlatformConfig) *Server {
	t.Helper()
	srv, err := NewServer(types.MCPServer{
		Name: "atl", Transport: "http", URL: url,
		Auth:  &types.MCPAuth{Type: "user", Ref: "mcp.atlassian"},
		Tools: types.MCPToolFilter{Allow: []string{"*"}, Schemas: schemas},
	}, ServerDeps{HTTPClient: http.DefaultClient, Platform: platform})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// TestServer_MaterializedDescriptors: config tool schemas convert to
// descriptors (input_schema YAML → JSON), and bad names/schemas are
// rejected — the same guards tools/list results get (#317).
func TestServer_MaterializedDescriptors(t *testing.T) {
	pf := &types.PlatformConfig{TokenEndpoint: "https://plat/token", AgentIdentity: "c"}

	srv := newUserServer(t, "https://x", []types.MCPToolSchema{
		{Name: "create_issue", Description: "Create an issue", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}}},
		{Name: "list_issues"}, // no input_schema → defaults to {"type":"object"}
	}, pf)
	descs, err := srv.materializedDescriptors()
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(descs) != 2 || descs[0].Name != "create_issue" {
		t.Fatalf("descs = %+v", descs)
	}
	if !json.Valid(descs[0].InputSchema) || !json.Valid(descs[1].InputSchema) {
		t.Errorf("input schemas must be valid JSON: %s / %s", descs[0].InputSchema, descs[1].InputSchema)
	}

	// Bad name (reserved "__") is rejected.
	bad := newUserServer(t, "https://x", []types.MCPToolSchema{{Name: "a__b", InputSchema: map[string]any{"type": "object"}}}, pf)
	if _, err := bad.materializedDescriptors(); err == nil {
		t.Error("a name containing __ must be rejected")
	}
}

// TestManager_Materialized_UserServer_LazyPerUserConnection is the full
// vertical (#317): a type=user server registers its materialized tools
// WITHOUT a startup connection, then a call establishes a per-user
// connection whose initialize carries THAT user's platform token.
func TestManager_Materialized_UserServer_LazyPerUserConnection(t *testing.T) {
	// Platform token endpoint → per-subject token.
	tokEP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Server, Subject string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + b.Subject, "expires_in": 3600})
	}))
	defer tokEP.Close()

	// MCP server: record the Authorization it sees at initialize.
	var mu sync.Mutex
	var initAuths []string
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			mu.Lock()
			initAuths = append(initAuths, r.Header.Get("Authorization"))
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", "sess")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"mock","version":"1.0"}}}`))
		case MethodInitialized:
			w.WriteHeader(http.StatusAccepted)
		case MethodToolsCall:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"content":[{"type":"text","text":"ok"}]}}`))
		}
	}))
	defer mcpSrv.Close()

	cfg := types.MCPConfig{Servers: []types.MCPServer{{
		Name: "atl", Transport: "http", URL: mcpSrv.URL,
		Auth: &types.MCPAuth{Type: "user", Ref: "mcp.atlassian"},
		Tools: types.MCPToolFilter{
			Allow:   []string{"*"},
			Schemas: []types.MCPToolSchema{{Name: "create_issue", InputSchema: map[string]any{"type": "object"}}},
		},
	}}}
	mgr, err := NewManager(cfg, ManagerDeps{
		HTTPClient: mcpSrv.Client(),
		Platform:   &types.PlatformConfig{TokenEndpoint: tokEP.URL, AgentIdentity: "agent-cred"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = mgr.Stop() }()

	// Tools register WITHOUT any startup connection.
	tools := mgr.Tools()
	if len(tools) != 1 || tools[0].Descriptor.Name != "create_issue" {
		t.Fatalf("materialized tools = %+v, want [create_issue]", tools)
	}
	mu.Lock()
	n := len(initAuths)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("server connected %d times at startup — a type=user server must be lazy", n)
	}

	// A call for a user establishes a per-user connection carrying THEIR token.
	userCtx := auth.WithIdentity(ctx, &auth.Identity{Email: "alice@corp.com"})
	cli, err := tools[0].Resolver.ClientFor(userCtx)
	if err != nil {
		t.Fatalf("ClientFor(alice): %v", err)
	}
	if _, err := cli.CallTool(userCtx, "create_issue", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), initAuths...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "Bearer tok-alice@corp.com" {
		t.Fatalf("initialize Authorization = %v, want [Bearer tok-alice@corp.com] (per-user token)", got)
	}
}
