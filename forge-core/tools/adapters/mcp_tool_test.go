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

// TestMCPTool_Audit_StampsSequence guards the fix for MCP events landing
// seq-less: Execute must emit via EmitFromContext so mcp_tool_call /
// mcp_tool_result carry the per-invocation `seq` from the ctx counter. Without
// it, same-second call/result pairs sort ambiguously in consumers (a result
// renders before its own call).
func TestMCPTool_Audit_StampsSequence(t *testing.T) {
	t.Parallel()
	c := &mockClient{res: &mcp.CallToolResult{
		Content: []mcp.ToolContent{{Type: "text", Text: "ok"}},
	}}
	var buf safeBuf
	a, err := NewMCPTool(MCPToolOpts{
		Server:     "srv",
		Descriptor: mcp.MCPToolDescriptor{Name: "echo", InputSchema: json.RawMessage(`{}`)},
		Client:     c, Audit: runtime.NewAuditLogger(&buf),
	})
	if err != nil {
		t.Fatalf("NewMCPTool: %v", err)
	}
	// The A2A handler installs this counter per request; simulate it.
	ctx := runtime.EnsureSequenceCounter(context.Background())
	if _, err := a.Execute(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	var callSeq, resultSeq int
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var e struct {
			Event string `json:"event"`
			Seq   int    `json:"seq"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		switch e.Event {
		case "mcp_tool_call":
			callSeq = e.Seq
		case "mcp_tool_result":
			resultSeq = e.Seq
		}
	}
	if callSeq == 0 || resultSeq == 0 {
		t.Fatalf("expected non-zero seq on both events, got call=%d result=%d; log:\n%s", callSeq, resultSeq, buf.String())
	}
	if callSeq >= resultSeq {
		t.Errorf("call seq (%d) must precede result seq (%d)", callSeq, resultSeq)
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

// gateStub records Await calls and returns a scripted outcome. nil err ⇒
// "granted" (caller retries); non-nil ⇒ give up.
type gateStub struct {
	calls   atomic.Int32
	err     error
	onAwait func() // optional side effect on grant (e.g. flip the client to succeed)
}

func (g *gateStub) Await(context.Context, string) error {
	g.calls.Add(1)
	if g.onAwait != nil {
		g.onAwait()
	}
	return g.err
}

// TestMCPTool_Execute_CallTimeNoToken_Parks is the forge#376 regression: an
// ErrNoToken raised by CallTool (per-request auth transports — the token is
// attached per frame, so the 403 lands on tools/call, not at establish) must
// route through the auth gate and retry, exactly like an establish-time
// ErrNoToken. Before the fix this path never consulted the gate and failed
// hard with reason=no_token.
func TestMCPTool_Execute_CallTimeNoToken_Parks(t *testing.T) {
	t.Parallel()
	// Client 403s on the first CallTool, succeeds after consent (the gate's
	// onAwait flips it), modelling "grant now exists → retry resolves it".
	c := &mockClient{err: mcp.ErrNoToken}
	gate := &gateStub{onAwait: func() {
		c.err = nil
		c.res = &mcp.CallToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "ok-after-consent"}}}
	}}
	a := newAdapter(t, c, func(m *MCPTool) { m.authGate = gate })

	got, err := a.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("expected park+retry to succeed, got err=%v", err)
	}
	if gate.calls.Load() != 1 {
		t.Fatalf("auth gate consulted %d times, want 1 (a call-time no-token must park)", gate.calls.Load())
	}
	if got != "ok-after-consent" {
		t.Fatalf("got %q, want the post-consent retry result", got)
	}
}

// TestMCPTool_Execute_CallTimeNoToken_GateGivesUp: when Await returns an error
// (timeout / cancel / no requesting user), the call fails as no_token — no
// second CallTool, no regression from prior fail-hard behavior.
func TestMCPTool_Execute_CallTimeNoToken_GateGivesUp(t *testing.T) {
	t.Parallel()
	c := &mockClient{err: mcp.ErrNoToken}
	gate := &gateStub{err: errors.New("consent timed out")}
	a := newAdapter(t, c, func(m *MCPTool) { m.authGate = gate })

	if _, err := a.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a gate give-up must surface as an error")
	}
	if gate.calls.Load() != 1 {
		t.Fatalf("gate consulted %d times, want exactly 1", gate.calls.Load())
	}
}

// TestMCPTool_Execute_CallTimeError_NoSpuriousPark: a NON-ErrNoToken CallTool
// error returns immediately without touching the gate.
func TestMCPTool_Execute_CallTimeError_NoSpuriousPark(t *testing.T) {
	t.Parallel()
	c := &mockClient{err: errors.New("upstream 500")}
	gate := &gateStub{}
	a := newAdapter(t, c, func(m *MCPTool) { m.authGate = gate })

	if _, err := a.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a non-auth CallTool error must surface")
	}
	if gate.calls.Load() != 0 {
		t.Fatalf("gate consulted %d times for a non-auth error, want 0", gate.calls.Load())
	}
}

// TestMCPTool_Execute_NoGate_NoTokenSurfaces: with no gate wired, a call-time
// ErrNoToken surfaces as before (nil-gate safety).
func TestMCPTool_Execute_NoGate_NoTokenSurfaces(t *testing.T) {
	t.Parallel()
	a := newAdapter(t, &mockClient{err: mcp.ErrNoToken})
	if _, err := a.Execute(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, mcp.ErrNoToken) {
		t.Fatalf("want ErrNoToken to surface with no gate, got %v", err)
	}
}
