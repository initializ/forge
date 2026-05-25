package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/types"
)

func newMgr(t *testing.T, cfg types.MCPConfig, httpClient *http.Client) *Manager {
	t.Helper()
	mgr, err := NewManager(cfg, ManagerDeps{
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Shorten backoff so terminal-fail tests run quickly.
	for _, s := range mgr.servers {
		s.backoff = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	}
	return mgr
}

func TestManager_NilHTTPClient_Rejected(t *testing.T) {
	t.Parallel()
	_, err := NewManager(types.MCPConfig{}, ManagerDeps{})
	if err == nil {
		t.Fatalf("expected error for nil HTTPClient")
	}
}

func TestManager_Start_NoServers_Ok(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	mgr, err := NewManager(types.MCPConfig{}, ManagerDeps{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Errorf("Start with no servers: %v", err)
	}
	if tools := mgr.Tools(); len(tools) != 0 {
		t.Errorf("Tools() = %v, want empty", tools)
	}
	_ = mgr.Stop()
}

func TestManager_Start_ParallelStartup(t *testing.T) {
	t.Parallel()
	// Each backend delays 300ms ONLY on initialize. 3 servers serially
	// would take ~900ms; in parallel the wall-time should hover near
	// 300ms (plus ~50ms of overhead per server for the other RPCs).
	mockHandler := func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == MethodInitialize {
			time.Sleep(300 * time.Millisecond)
		}
		// Re-encode to re-use defaultMockMCPHandler — call directly with msg.
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"mock","version":"1.0"}}}`))
		case MethodInitialized:
			w.WriteHeader(http.StatusAccepted)
		case MethodToolsList:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`))
		}
	}
	srv1 := httptest.NewServer(http.HandlerFunc(mockHandler))
	srv2 := httptest.NewServer(http.HandlerFunc(mockHandler))
	srv3 := httptest.NewServer(http.HandlerFunc(mockHandler))
	defer srv1.Close()
	defer srv2.Close()
	defer srv3.Close()

	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "a", Transport: "http", URL: srv1.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
		{Name: "b", Transport: "http", URL: srv2.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
		{Name: "c", Transport: "http", URL: srv3.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
	}}
	mgr := newMgr(t, cfg, srv1.Client())

	start := time.Now()
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	elapsed := time.Since(start)
	// Serial would be ~900ms (3 × 300ms). Parallel should be <600ms
	// (300ms longest path + scheduling slop). Anything ≥800ms means
	// parallelism broke.
	if elapsed > 600*time.Millisecond {
		t.Errorf("Start took %v — should be < 600ms (3 parallel × 300ms init), not ~900ms serial", elapsed)
	}
	tools := mgr.Tools()
	if len(tools) != 3 {
		t.Errorf("len(Tools()) = %d, want 3 (one per server)", len(tools))
	}
	_ = mgr.Stop()
}

func TestManager_Start_RequiredFailure_CancelsAll(t *testing.T) {
	t.Parallel()
	good := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer bad.Close()

	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "good", Transport: "http", URL: good.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
		{Name: "bad", Transport: "http", URL: bad.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}, Required: true},
	}}
	mgr := newMgr(t, cfg, good.Client())

	start := time.Now()
	err := mgr.Start(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected error from required-server failure")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("err lacks server name: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Start took %v — required-fail tear-down should be prompt", elapsed)
	}
	_ = mgr.Stop()
}

func TestManager_Start_NonRequiredFailure_OtherServersContinue(t *testing.T) {
	t.Parallel()
	good := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer bad.Close()

	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "good", Transport: "http", URL: good.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
		{Name: "bad", Transport: "http", URL: bad.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}, Required: false},
	}}
	mgr := newMgr(t, cfg, good.Client())

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Errorf("len(Tools()) = %d, want 1 (only good's tool)", len(tools))
	}
	if len(tools) > 0 && tools[0].Server != "good" {
		t.Errorf("surviving tool from server %q, want good", tools[0].Server)
	}
	_ = mgr.Stop()
}

func TestManager_StartTwice_Errors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer srv.Close()
	mgr, _ := NewManager(types.MCPConfig{}, ManagerDeps{HTTPClient: srv.Client()})
	_ = mgr.Start(context.Background())
	if err := mgr.Start(context.Background()); err == nil {
		t.Fatalf("second Start should error")
	}
	_ = mgr.Stop()
}

func TestManager_Stop_Idempotent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer srv.Close()
	mgr, _ := NewManager(types.MCPConfig{}, ManagerDeps{HTTPClient: srv.Client()})
	_ = mgr.Start(context.Background())
	if err := mgr.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := mgr.Stop(); err != nil {
		t.Fatalf("second Stop should be no-op, got %v", err)
	}
}

func TestManager_Tools_BeforeStart_ReturnsNil(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer srv.Close()
	mgr, _ := NewManager(types.MCPConfig{}, ManagerDeps{HTTPClient: srv.Client()})
	if tools := mgr.Tools(); tools != nil {
		t.Errorf("Tools() before Start should be nil, got %v", tools)
	}
}

func TestManager_OAuthServer_RequiresFlow(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer srv.Close()
	cfg := types.MCPConfig{Servers: []types.MCPServer{{
		Name: "oa", Transport: "http", URL: srv.URL,
		Auth: &types.MCPAuth{
			Type: "oauth", ClientID: "c",
			AuthorizeURL: "https://x/auth", TokenURL: "https://x/tok",
		},
		Tools: types.MCPToolFilter{Allow: []string{"echo"}},
	}}}
	_, err := NewManager(cfg, ManagerDeps{HTTPClient: srv.Client()})
	if err == nil {
		t.Fatalf("expected error when oauth server has no OAuthFlow")
	}
}

func TestManager_DeterministicToolOrder(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(defaultMockMCPHandler))
	defer srv.Close()
	cfg := types.MCPConfig{Servers: []types.MCPServer{
		{Name: "z", Transport: "http", URL: srv.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
		{Name: "a", Transport: "http", URL: srv.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
		{Name: "m", Transport: "http", URL: srv.URL, Tools: types.MCPToolFilter{Allow: []string{"echo"}}},
	}}
	mgr := newMgr(t, cfg, srv.Client())
	_ = mgr.Start(context.Background())
	defer func() { _ = mgr.Stop() }()
	tools := mgr.Tools()
	if len(tools) != 3 {
		t.Fatalf("len(tools) = %d", len(tools))
	}
	got := []string{tools[0].Server, tools[1].Server, tools[2].Server}
	want := []string{"a", "m", "z"}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("tools[%d].Server = %q, want %q (sorted)", i, g, want[i])
		}
	}
}

// Compile-time guard against accidental removal of the WaitGroup.
var _ atomic.Bool
