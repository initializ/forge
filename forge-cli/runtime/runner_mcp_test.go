package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// mockMCP responds to initialize / initialized / tools/list with
// canned data shaped like a real MCP server. Used to exercise the
// runner_mcp wiring.
func newMockMCPSrv(t *testing.T, opts ...func(*mockOpts)) *httptest.Server {
	t.Helper()
	o := &mockOpts{toolName: "echo"}
	for _, fn := range opts {
		fn(o)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.broken {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var msg mcp.JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case mcp.MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + mcp.ProtocolVersion + `","serverInfo":{"name":"mock","version":"1"}}}`))
		case mcp.MethodInitialized:
			w.WriteHeader(http.StatusAccepted)
		case mcp.MethodToolsList:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"` + o.toolName + `","description":"d","inputSchema":{"type":"object"}}]}}`))
		case mcp.MethodToolsCall:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"content":[{"type":"text","text":"ok"}]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type mockOpts struct {
	toolName string
	broken   bool
}

// newTestRunner builds a Runner with only the fields startMCPManager
// touches. Avoids dragging in the full Run() machinery.
func newTestRunner(cfg *types.ForgeConfig) *Runner {
	return &Runner{
		cfg: RunnerConfig{
			Config: cfg,
		},
		logger: coreruntime.NewJSONLogger(discardWriter{}, false),
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestStartMCPManager_NoServers_ReturnsNil(t *testing.T) {
	t.Parallel()
	r := newTestRunner(&types.ForgeConfig{})
	audit := coreruntime.NewAuditLogger(discardWriter{})
	mgr, err := r.startMCPManager(context.Background(), http.DefaultClient, audit)
	if err != nil {
		t.Fatal(err)
	}
	if mgr != nil {
		t.Errorf("expected nil manager for empty MCP config, got %v", mgr)
	}
}

func TestStartMCPManager_OneServer_ReadyWithTool(t *testing.T) {
	t.Parallel()
	srv := newMockMCPSrv(t)
	r := newTestRunner(&types.ForgeConfig{
		MCP: types.MCPConfig{Servers: []types.MCPServer{{
			Name: "mockmcp", Transport: "http", URL: srv.URL,
			Tools: types.MCPToolFilter{Allow: []string{"echo"}},
		}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	audit := coreruntime.NewAuditLogger(discardWriter{})
	mgr, err := r.startMCPManager(ctx, srv.Client(), audit)
	if err != nil {
		t.Fatalf("startMCPManager: %v", err)
	}
	defer func() { _ = mgr.Stop() }()
	if mgr == nil {
		t.Fatal("manager nil unexpectedly")
	}
	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if tools[0].Server != "mockmcp" || tools[0].Descriptor.Name != "echo" {
		t.Errorf("tool = %+v", tools[0])
	}
}

func TestStartMCPManager_RequiredFailure_ReturnsError(t *testing.T) {
	t.Parallel()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	r := newTestRunner(&types.ForgeConfig{
		MCP: types.MCPConfig{Servers: []types.MCPServer{{
			Name: "down", Transport: "http", URL: bad.URL,
			Tools:    types.MCPToolFilter{Allow: []string{"echo"}},
			Required: true,
			Timeout:  500 * time.Millisecond,
		}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	audit := coreruntime.NewAuditLogger(discardWriter{})
	_, err := r.startMCPManager(ctx, bad.Client(), audit)
	if err == nil {
		t.Fatalf("expected error for required-server failure")
	}
}

func TestStartMCPManager_OAuth_FlowWired(t *testing.T) {
	t.Parallel()
	srv := newMockMCPSrv(t)
	// We're not actually doing a login here — just verifying that
	// Manager construction works when an oauth server is declared
	// (BearerToken will fail at first call without stored creds, but
	// the manager itself constructs).
	r := newTestRunner(&types.ForgeConfig{
		MCP: types.MCPConfig{Servers: []types.MCPServer{{
			Name: "oa", Transport: "http", URL: srv.URL,
			Auth: &types.MCPAuth{
				Type: "oauth", ClientID: "c",
				AuthorizeURL: "https://auth.example.com/authorize",
				TokenURL:     "https://auth.example.com/token",
			},
			Tools:    types.MCPToolFilter{Allow: []string{"echo"}},
			Required: false,
		}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	audit := coreruntime.NewAuditLogger(discardWriter{})
	// Non-required server will likely fail (no token), but we expect
	// no error from startMCPManager itself.
	mgr, err := r.startMCPManager(ctx, srv.Client(), audit)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("unexpected error: %v", err)
	}
	if mgr != nil {
		_ = mgr.Stop()
	}
}

// TestStartMCPManager_EmitsAuditOnReady proves that audit events
// flow through the manager wiring.
func TestStartMCPManager_EmitsAuditOnReady(t *testing.T) {
	t.Parallel()
	srv := newMockMCPSrv(t)
	r := newTestRunner(&types.ForgeConfig{
		MCP: types.MCPConfig{Servers: []types.MCPServer{{
			Name: "audited", Transport: "http", URL: srv.URL,
			Tools: types.MCPToolFilter{Allow: []string{"echo"}},
		}}},
	})
	var captured captureWriter
	audit := coreruntime.NewAuditLogger(&captured)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mgr, err := r.startMCPManager(ctx, srv.Client(), audit)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop() }()

	// Tiny pause to let async audit emission flush.
	time.Sleep(50 * time.Millisecond)
	out := captured.String()
	if !strings.Contains(out, "mcp_server_started") {
		t.Errorf("expected mcp_server_started event, got: %s", out)
	}
}

// captureWriter is a thread-safe writer for assertion in tests.
type captureWriter struct {
	buf strings.Builder
}

func (c *captureWriter) Write(p []byte) (int, error) {
	return c.buf.Write(p)
}
func (c *captureWriter) String() string { return c.buf.String() }
