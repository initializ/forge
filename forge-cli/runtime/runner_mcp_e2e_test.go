package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/adapters"
	"github.com/initializ/forge/forge-core/types"
)

// TestE2E_MultiServerMCP_AgentLoop wires the full Phase 1 path:
//
//	forge.yaml mcp.servers → startMCPManager → Manager.Tools() →
//	adapters.NewMCPTool → tools.Registry → Execute → real RPC.
//
// Two mock MCP servers each expose one tool. We register them as
// MCPTools, invoke each via the registry, and assert:
//
//   - both RPCs reached the right backend
//   - tool names are namespaced "<server>__<tool>"
//   - audit log carries mcp_server_started ×2, mcp_tool_call ×2,
//     mcp_tool_result ×2 — and ZERO byte payload from args/results
//
// This is the integration property called out in the Phase 1
// acceptance criteria — it proves the end-to-end story without
// dragging in the full Runner.Run() loop.
func TestE2E_MultiServerMCP_AgentLoop(t *testing.T) {
	t.Parallel()

	// Unique sentinels we can grep for; the audit log must NOT
	// contain either of these — proves the no-byte-leak invariant
	// holds across the wired path.
	const argSentinelA = "PIIBLOCK_ARG_A_ZZ"
	const argSentinelB = "PIIBLOCK_ARG_B_ZZ"
	const resSentinelA = "PIIBLOCK_RES_A_ZZ"
	const resSentinelB = "PIIBLOCK_RES_B_ZZ"

	// Per-server call counters so we can assert routing.
	var callsA, callsB atomic.Int32

	srvA := mockMCPWithTool(t, "search", resSentinelA, &callsA)
	srvB := mockMCPWithTool(t, "ticket_create", resSentinelB, &callsB)

	r := newTestRunner(&types.ForgeConfig{
		MCP: types.MCPConfig{Servers: []types.MCPServer{
			{Name: "a", Transport: "http", URL: srvA.URL,
				Tools: types.MCPToolFilter{Allow: []string{"search"}}},
			{Name: "b", Transport: "http", URL: srvB.URL,
				Tools: types.MCPToolFilter{Allow: []string{"ticket_create"}}},
		}},
	})

	var captured threadSafeBuf
	audit := coreruntime.NewAuditLogger(&captured)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := r.startMCPManager(ctx, srvA.Client(), audit)
	if err != nil {
		t.Fatalf("startMCPManager: %v", err)
	}
	defer func() { _ = mgr.Stop() }()

	// Register all discovered tools — mirrors the runner.go path.
	reg := tools.NewRegistry()
	for _, h := range mgr.Tools() {
		mt := adapters.NewMCPTool(adapters.MCPToolOpts{
			Server: h.Server, Descriptor: h.Descriptor, Client: h.Client, Audit: audit,
		})
		if err := reg.Register(mt); err != nil {
			t.Fatalf("Register %s: %v", mt.Name(), err)
		}
	}

	// Tools should appear with namespaced names.
	got := reg.List()
	wantNames := []string{"a__search", "b__ticket_create"}
	if len(got) != len(wantNames) {
		t.Fatalf("registry list = %v, want %v", got, wantNames)
	}
	for _, want := range wantNames {
		if reg.Get(want) == nil {
			t.Errorf("missing registered tool %q (got: %v)", want, got)
		}
	}

	// Drive two tool calls in series (like an LLM loop would).
	resultA, err := reg.Execute(ctx, "a__search", json.RawMessage(`{"q":"`+argSentinelA+`"}`))
	if err != nil {
		t.Fatalf("execute a__search: %v", err)
	}
	if !strings.Contains(resultA, resSentinelA) {
		t.Errorf("result a__search lost sentinel: %q", resultA)
	}
	resultB, err := reg.Execute(ctx, "b__ticket_create", json.RawMessage(`{"title":"`+argSentinelB+`"}`))
	if err != nil {
		t.Fatalf("execute b__ticket_create: %v", err)
	}
	if !strings.Contains(resultB, resSentinelB) {
		t.Errorf("result b__ticket_create lost sentinel: %q", resultB)
	}

	if got := callsA.Load(); got != 1 {
		t.Errorf("server A call count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("server B call count = %d, want 1", got)
	}

	// Tiny pause to let async audit emission flush.
	time.Sleep(100 * time.Millisecond)
	log := captured.String()

	// Required events present.
	for _, want := range []string{
		"mcp_server_started",
		"mcp_tool_call",
		"mcp_tool_result",
		`"server":"a"`,
		`"server":"b"`,
		`"tool":"search"`,
		`"tool":"ticket_create"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log missing %q", want)
		}
	}

	// NO byte payload — sentinels must not appear anywhere.
	for _, leak := range []string{argSentinelA, argSentinelB, resSentinelA, resSentinelB} {
		if strings.Contains(log, leak) {
			// Trim for readability if log is large.
			snippet := log
			if len(snippet) > 500 {
				snippet = snippet[:500] + "..."
			}
			t.Errorf("AUDIT LEAK: sentinel %q found in audit log\n%s", leak, snippet)
		}
	}
}

// mockMCPWithTool stands up an httptest MCP server with one tool
// that echoes back a configured response sentinel. callCount is
// incremented on every tools/call so the test can verify routing.
func mockMCPWithTool(t *testing.T, toolName, resSentinel string, callCount *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg mcp.JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case mcp.MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + mcp.ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case mcp.MethodInitialized:
			w.WriteHeader(http.StatusAccepted)
		case mcp.MethodToolsList:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"` + toolName + `","inputSchema":{"type":"object"}}]}}`))
		case mcp.MethodToolsCall:
			callCount.Add(1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"content":[{"type":"text","text":"` + resSentinel + `"}]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// threadSafeBuf is a minimal thread-safe writer for the audit logger,
// which writes from arbitrary goroutines.
type threadSafeBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *threadSafeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *threadSafeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
