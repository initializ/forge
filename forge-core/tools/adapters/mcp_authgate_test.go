package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/initializ/forge/forge-core/mcp"
)

// grantOnResumeResolver returns ErrNoToken until the auth gate "grants",
// then hands back a client — modeling the real per-user pool, which 401s
// with no grant and establishes the connection once one exists.
type grantOnResumeResolver struct {
	granted *bool
	client  mcp.Client
}

func (r grantOnResumeResolver) ClientFor(context.Context) (mcp.Client, error) {
	if !*r.granted {
		return nil, mcp.ErrNoToken
	}
	return r.client, nil
}

// fakeGate models the runtime gate: Await flips the grant (as a real
// consent + platform grant would) and returns its configured error.
type fakeGate struct {
	granted    *bool
	err        error
	awaitCalls int
	sawServer  string
}

func (g *fakeGate) Await(_ context.Context, server string) error {
	g.awaitCalls++
	g.sawServer = server
	if g.err == nil {
		*g.granted = true
	}
	return g.err
}

// A delegated call with no grant parks on the gate; when the gate grants,
// Execute re-resolves and the call proceeds (#330).
func TestMCPTool_Execute_AuthGate_ParksThenResumes(t *testing.T) {
	t.Parallel()
	granted := false
	client := &mockClient{res: &mcp.CallToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "ok-after-consent"}}}}
	gate := &fakeGate{granted: &granted}
	tool, err := NewMCPTool(MCPToolOpts{
		Server:     "atl",
		Descriptor: mcp.MCPToolDescriptor{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Resolver:   grantOnResumeResolver{granted: &granted, client: client},
		AuthGate:   gate,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute after consent must succeed, got %v", err)
	}
	if got != "ok-after-consent" {
		t.Fatalf("result = %q, want ok-after-consent", got)
	}
	if gate.awaitCalls != 1 {
		t.Fatalf("Await called %d times, want 1", gate.awaitCalls)
	}
	if gate.sawServer != "atl" {
		t.Fatalf("gate saw server %q, want atl", gate.sawServer)
	}
}

// When the gate gives up (timeout / no consent), the call fails — the pause
// is bounded, not indefinite.
func TestMCPTool_Execute_AuthGate_TimeoutFails(t *testing.T) {
	t.Parallel()
	granted := false
	gate := &fakeGate{granted: &granted, err: errors.New("authgate: consent timed out")}
	tool, err := NewMCPTool(MCPToolOpts{
		Server:     "atl",
		Descriptor: mcp.MCPToolDescriptor{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Resolver:   grantOnResumeResolver{granted: &granted},
		AuthGate:   gate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a gate that never grants must fail the call, not hang or succeed")
	}
	if gate.awaitCalls != 1 {
		t.Fatalf("Await called %d times, want 1", gate.awaitCalls)
	}
}

// With no gate wired (nil), ErrNoToken surfaces directly — the pre-#330
// behavior for non-delegated servers and standalone/tests.
func TestMCPTool_Execute_NoGate_ErrNoTokenSurfaces(t *testing.T) {
	t.Parallel()
	granted := false
	tool, err := NewMCPTool(MCPToolOpts{
		Server:     "atl",
		Descriptor: mcp.MCPToolDescriptor{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Resolver:   grantOnResumeResolver{granted: &granted},
		// AuthGate deliberately nil.
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, mcp.ErrNoToken) {
		t.Fatalf("without a gate, ErrNoToken must surface; got %v", err)
	}
}

// The gate is only consulted for ErrNoToken — a different resolver error
// (e.g. transport) is NOT parked; it fails immediately.
func TestMCPTool_Execute_AuthGate_OnlyForNoToken(t *testing.T) {
	t.Parallel()
	granted := false
	gate := &fakeGate{granted: &granted}
	tool, err := NewMCPTool(MCPToolOpts{
		Server:     "atl",
		Descriptor: mcp.MCPToolDescriptor{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Resolver:   resolverStub{err: mcp.ErrTransportUnavailable},
		AuthGate:   gate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, mcp.ErrTransportUnavailable) {
		t.Fatalf("transport error must surface unchanged; got %v", err)
	}
	if gate.awaitCalls != 0 {
		t.Fatalf("gate must NOT be consulted for non-auth errors; Await called %d times", gate.awaitCalls)
	}
}
