package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/tools"
)

// mockClient implements mcp.Client for tests.
type mockClient struct {
	tools []mcp.MCPToolDescriptor
	res   *mcp.CallToolResult
	err   error
}

func (m *mockClient) Initialize(context.Context, mcp.ClientInfo) (*mcp.InitializeResult, error) {
	return &mcp.InitializeResult{ProtocolVersion: mcp.ProtocolVersion}, nil
}
func (m *mockClient) Initialized(context.Context) error                          { return nil }
func (m *mockClient) ListTools(context.Context) ([]mcp.MCPToolDescriptor, error) { return m.tools, nil }
func (m *mockClient) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return m.res, m.err
}
func (m *mockClient) Close() error { return nil }

func newAdapter(t *testing.T, c mcp.Client, opts ...func(*MCPTool)) *MCPTool {
	t.Helper()
	a, err := NewMCPTool(MCPToolOpts{
		Server: "srv",
		Descriptor: mcp.MCPToolDescriptor{
			Name:        "echo",
			Description: "echo back",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Client: c,
	})
	if err != nil {
		t.Fatalf("NewMCPTool: %v", err)
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func TestMCPTool_Name_Namespaced(t *testing.T) {
	t.Parallel()
	a := newAdapter(t, &mockClient{})
	if got := a.Name(); got != "srv__echo" {
		t.Errorf("Name() = %q, want srv__echo", got)
	}
}

func TestMCPTool_ImplementsMCPSource(t *testing.T) {
	t.Parallel()
	var t1 tools.Tool = newAdapter(t, &mockClient{})
	if _, ok := t1.(tools.MCPSource); !ok {
		t.Errorf("MCPTool must implement tools.MCPSource")
	}
}

func TestMCPTool_Description_And_InputSchema(t *testing.T) {
	t.Parallel()
	a := newAdapter(t, &mockClient{})
	if a.Description() != "echo back" {
		t.Errorf("Description = %q", a.Description())
	}
	if !strings.Contains(string(a.InputSchema()), `"type":"object"`) {
		t.Errorf("InputSchema lost: %s", string(a.InputSchema()))
	}
	if a.Category() != tools.CategoryAdapter {
		t.Errorf("Category = %v, want CategoryAdapter", a.Category())
	}
}

func TestMCPTool_Execute_Happy(t *testing.T) {
	t.Parallel()
	c := &mockClient{res: &mcp.CallToolResult{
		Content: []mcp.ToolContent{{Type: "text", Text: "hi there"}},
	}}
	a := newAdapter(t, c)
	got, err := a.Execute(context.Background(), json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi there" {
		t.Errorf("got %q", got)
	}
}

// userKey carries a fake subject in ctx for the per-call resolution test.
type userKey struct{}

// resolverStub routes to a client chosen by the ctx's fake subject.
type resolverStub struct {
	byUser map[string]mcp.Client
	err    error
}

func (r resolverStub) ClientFor(ctx context.Context) (mcp.Client, error) {
	if r.err != nil {
		return nil, r.err
	}
	u, _ := ctx.Value(userKey{}).(string)
	return r.byUser[u], nil
}

// TestMCPTool_Execute_ResolvesClientPerCall: with a Resolver, Execute
// picks the connection from the per-call ctx — two users' calls route to
// their own clients (#317 routing seam).
func TestMCPTool_Execute_ResolvesClientPerCall(t *testing.T) {
	t.Parallel()
	alice := &mockClient{res: &mcp.CallToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "alice-result"}}}}
	bob := &mockClient{res: &mcp.CallToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "bob-result"}}}}
	a, err := NewMCPTool(MCPToolOpts{
		Server:     "srv",
		Descriptor: mcp.MCPToolDescriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Resolver:   resolverStub{byUser: map[string]mcp.Client{"alice": alice, "bob": bob}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := a.Execute(context.WithValue(context.Background(), userKey{}, "alice"), json.RawMessage(`{}`))
	if err != nil || got != "alice-result" {
		t.Fatalf("alice: got=%q err=%v", got, err)
	}
	got, err = a.Execute(context.WithValue(context.Background(), userKey{}, "bob"), json.RawMessage(`{}`))
	if err != nil || got != "bob-result" {
		t.Fatalf("bob: got=%q err=%v (each call must route to its own connection)", got, err)
	}
}

// TestMCPTool_Execute_ResolverErrorSurfaces: a resolver that can't produce
// a connection (e.g. no user in ctx / no grant yet) surfaces as a tool
// error rather than a nil-deref.
func TestMCPTool_Execute_ResolverErrorSurfaces(t *testing.T) {
	t.Parallel()
	a, err := NewMCPTool(MCPToolOpts{
		Server:     "srv",
		Descriptor: mcp.MCPToolDescriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Resolver:   resolverStub{err: errors.New("no connection for this user")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a resolver error must surface, not nil-deref")
	}
}

func TestMCPTool_Execute_Truncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 100_000)
	c := &mockClient{res: &mcp.CallToolResult{
		Content: []mcp.ToolContent{{Type: "text", Text: long}},
	}}
	a := newAdapter(t, c, func(t *MCPTool) { t.maxResultChars = 1000 })
	got, err := a.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, truncatedSuffix) {
		t.Errorf("result not truncated: %q…", got[:50])
	}
	// Final string MUST be ≤ maxResultChars (review B16 — previously
	// the cap leaked by +len(truncatedSuffix) bytes).
	if len(got) > 1000 {
		t.Errorf("truncated result %d bytes exceeds maxResultChars=1000", len(got))
	}
}

// With compression enabled the executor stamps tools.WithRelaxedLimits: the
// result cap scales 16x (bounded at 4MB absolute) so the full MCP result
// reaches the compression layer instead of dying at the adapter.
func TestMCPTool_Execute_RelaxedTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 10_000)
	c := &mockClient{res: &mcp.CallToolResult{
		Content: []mcp.ToolContent{{Type: "text", Text: long}},
	}}
	a := newAdapter(t, c, func(t *MCPTool) { t.maxResultChars = 1000 })

	relaxed := tools.WithRelaxedLimits(context.Background())

	// 10K > 1000 cap but < 16K relaxed cap → passes whole.
	got, err := a.Execute(relaxed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != long {
		t.Fatalf("relaxed limits should pass 10K through, got %d chars", len(got))
	}

	// Over even the relaxed cap → still bounded at 16x.
	c.res = &mcp.CallToolResult{
		Content: []mcp.ToolContent{{Type: "text", Text: strings.Repeat("b", 20_000)}},
	}
	got, err = a.Execute(relaxed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, truncatedSuffix) {
		t.Fatal("relaxed limits must still bound pathological output")
	}
	if len(got) > 16_000 {
		t.Fatalf("relaxed result %d bytes exceeds 16x cap", len(got))
	}
}

func TestMCPTool_Execute_ErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{"unavailable", mcp.ErrTransportUnavailable, "unavailable"},
		{"protocol", mcp.ErrProtocolError, "protocol"},
		{"revoked", mcp.ErrTokenRevoked, "revoked"},
		{"canceled", context.Canceled, "canceled"},
		{"deadline", context.DeadlineExceeded, "canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyToolErr(tc.err); got != tc.reason {
				t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.reason)
			}
		})
	}
}

// TestMCPTool_Audit_NeverLogsBytes pins the no-byte-leak invariant.
// We embed a unique sentinel string in BOTH the args and the result;
// the audit log must contain NEITHER.
const auditSentinelArgs = "PIIBLOCKZZ_ARGS"
const auditSentinelResult = "PIIBLOCKZZ_RESULT"

func TestMCPTool_Audit_NeverLogsBytes(t *testing.T) {
	t.Parallel()
	c := &mockClient{res: &mcp.CallToolResult{
		Content: []mcp.ToolContent{{Type: "text", Text: auditSentinelResult}},
	}}
	var buf safeBuf
	audit := runtime.NewAuditLogger(&buf)
	a, err := NewMCPTool(MCPToolOpts{
		Server: "srv",
		Descriptor: mcp.MCPToolDescriptor{
			Name: "echo", InputSchema: json.RawMessage(`{}`),
		},
		Client: c,
		Audit:  audit,
	})
	if err != nil {
		t.Fatalf("NewMCPTool: %v", err)
	}
	args := []byte(`{"sentinel":"` + auditSentinelArgs + `"}`)
	out, err := a.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, auditSentinelResult) {
		t.Fatal("sentinel missing from RESULT — test setup broken")
	}
	logBytes := buf.String()
	if strings.Contains(logBytes, auditSentinelArgs) {
		t.Errorf("AUDIT LEAK: args sentinel found in audit log:\n%s", logBytes)
	}
	if strings.Contains(logBytes, auditSentinelResult) {
		t.Errorf("AUDIT LEAK: result sentinel found in audit log:\n%s", logBytes)
	}
	// Sanity-check the events ARE there (just without payload).
	for _, want := range []string{"mcp_tool_call", "mcp_tool_result", `"args_size"`, `"result_size"`} {
		if !strings.Contains(logBytes, want) {
			t.Errorf("expected %q in audit log, got: %s", want, logBytes)
		}
	}
}

func TestMCPTool_Audit_OkFalseOnError(t *testing.T) {
	t.Parallel()
	c := &mockClient{err: errors.New("simulated network failure: " + mcp.ErrTransportUnavailable.Error())}
	c.err = mcp.ErrTransportUnavailable
	var buf safeBuf
	a, err := NewMCPTool(MCPToolOpts{
		Server: "s", Descriptor: mcp.MCPToolDescriptor{Name: "t", InputSchema: json.RawMessage(`{}`)},
		Client: c, Audit: runtime.NewAuditLogger(&buf),
	})
	if err != nil {
		t.Fatalf("NewMCPTool: %v", err)
	}
	_, err = a.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	log := buf.String()
	if !strings.Contains(log, `"ok":false`) {
		t.Errorf("expected ok:false, got: %s", log)
	}
	if !strings.Contains(log, `"reason":"unavailable"`) {
		t.Errorf("expected reason:unavailable, got: %s", log)
	}
}

func TestMCPTool_FlattenContent(t *testing.T) {
	t.Parallel()
	got := flattenContent([]mcp.ToolContent{
		{Type: "text", Text: "alpha"},
		{Type: "image", MimeType: "image/png"},
		{Type: "resource"},
		{Type: "exotic"},
	})
	want := "alpha\n[image type/image/png]\n[resource]\n[exotic]"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// safeBuf is a minimal thread-safe writer used by the audit logger
// (which writes from any goroutine).
type safeBuf struct {
	mu  atomic.Int32
	buf strings.Builder
}

func (b *safeBuf) Write(p []byte) (int, error) {
	for !b.mu.CompareAndSwap(0, 1) {
	}
	defer b.mu.Store(0)
	return b.buf.Write(p)
}
func (b *safeBuf) String() string {
	for !b.mu.CompareAndSwap(0, 1) {
	}
	defer b.mu.Store(0)
	return b.buf.String()
}
