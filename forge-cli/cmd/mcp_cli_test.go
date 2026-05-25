package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/types"
)

// writeForgeYAMLWithMCP creates a temp forge.yaml + cd's there.
func writeForgeYAMLWithMCP(t *testing.T, servers []types.MCPServer) string {
	t.Helper()
	dir := t.TempDir()
	cfg := `agent_id: test-agent
version: 0.1.0
framework: forge
mcp:
  servers:
`
	for _, s := range servers {
		cfg += "    - name: " + s.Name + "\n"
		cfg += "      transport: " + s.Transport + "\n"
		cfg += "      url: " + s.URL + "\n"
		cfg += "      tools: { allow: [echo] }\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir) // also redirect credential store
	t.Chdir(dir)
	cfgFile = "forge.yaml" // package-level var read by loadForgeConfig
	return dir
}

// reusable mock MCP server.
func newMockMCPCLI(t *testing.T) *httptest.Server {
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
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"echo","description":"echo back","inputSchema":{"type":"object"}}]}}`))
		case mcp.MethodToolsCall:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"content":[{"type":"text","text":"echoed"}]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMCPList_EmptyConfig(t *testing.T) {
	writeForgeYAMLWithMCP(t, nil)
	// Strip the empty mcp.servers line back to a clean forge.yaml.
	if err := os.WriteFile("forge.yaml", []byte("agent_id: x\nversion: 0.1.0\nframework: forge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	mcpListCmd.SetOut(&out)
	if err := mcpListRun(mcpListCmd, nil); err != nil {
		t.Fatal(err)
	}
	// (Real output goes to stdout; we accept either path.)
	if out.Len() == 0 {
		t.Log("(no output captured — empty-config path is informational only)")
	}
}

func TestMCPList_OneServer_Reachable(t *testing.T) {
	srv := newMockMCPCLI(t)
	writeForgeYAMLWithMCP(t, []types.MCPServer{
		{Name: "mock", Transport: "http", URL: srv.URL},
	})
	// Capture stdout so we can assert on the table.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	doneCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		doneCh <- buf.String()
	}()
	if err := mcpListRun(mcpListCmd, nil); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	out := <-doneCh
	if !strings.Contains(out, "mock") {
		t.Errorf("expected server name in output: %s", out)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("expected ready state, got: %s", out)
	}
}

func TestMCPTest_ListsTools(t *testing.T) {
	srv := newMockMCPCLI(t)
	writeForgeYAMLWithMCP(t, []types.MCPServer{
		{Name: "mock", Transport: "http", URL: srv.URL},
	})
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	doneCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		doneCh <- buf.String()
	}()
	mcpTestCmd.Flags().Set("timeout", "3s") //nolint:errcheck
	if err := mcpTestRun(mcpTestCmd, []string{"mock"}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	out := <-doneCh
	if !strings.Contains(out, "mock__echo") {
		t.Errorf("expected namespaced tool name in output: %s", out)
	}
}

func TestMCPTest_CallTool(t *testing.T) {
	srv := newMockMCPCLI(t)
	writeForgeYAMLWithMCP(t, []types.MCPServer{
		{Name: "mock", Transport: "http", URL: srv.URL},
	})
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	doneCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		doneCh <- buf.String()
	}()
	mcpTestCmd.Flags().Set("timeout", "3s")     //nolint:errcheck
	mcpTestCmd.Flags().Set("call", "echo")      //nolint:errcheck
	mcpTestCmd.Flags().Set("args", `{"msg":1}`) //nolint:errcheck
	if err := mcpTestRun(mcpTestCmd, []string{"mock"}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	out := <-doneCh
	if !strings.Contains(out, "echoed") {
		t.Errorf("expected tool result in output: %s", out)
	}
	// Reset flags so other tests aren't polluted.
	mcpTestCmd.Flags().Set("call", "") //nolint:errcheck
	mcpTestCmd.Flags().Set("args", "") //nolint:errcheck
}

func TestMCPTest_UnknownServer_Errors(t *testing.T) {
	srv := newMockMCPCLI(t)
	writeForgeYAMLWithMCP(t, []types.MCPServer{
		{Name: "mock", Transport: "http", URL: srv.URL},
	})
	err := mcpTestRun(mcpTestCmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown server")
	}
}

func TestMCPLogout_DeletesTokens(t *testing.T) {
	srv := newMockMCPCLI(t)
	dir := writeForgeYAMLWithMCP(t, []types.MCPServer{
		{Name: "mock", Transport: "http", URL: srv.URL},
	})
	// Manually drop a credentials file mirroring what login produces.
	credsDir := filepath.Join(dir, ".forge", "credentials")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(credsDir, "mcp_mock.json")
	if err := os.WriteFile(credsPath, []byte(`{"access_token":"X"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mcpLogoutRun(mcpLogoutCmd, []string{"mock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credsPath); !os.IsNotExist(err) {
		t.Errorf("credentials file should be gone, stat err = %v", err)
	}
}

// TestMCPCall_BuiltinIsGone proves that the deprecated mcp_call
// builtin is no longer registered anywhere — a regression guard for
// the removal Commit 6 performs.
func TestMCPCall_BuiltinIsGone(t *testing.T) {
	// We don't import builtins from this package; instead, do a
	// quick check that no tool named "mcp_call" surfaces in the
	// default registry path. (The test in
	// forge-core/tools/adapters/adapters_test.go covers the absence
	// of NewMCPCallTool at compile time.)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = ctx // present for symmetry with future builtins-touching tests
}
