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

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// helper: build a Server pointing at a mock MCP backend.
func newTestServer(t *testing.T, srv *httptest.Server, spec types.MCPServer, audit *runtime.AuditLogger) *Server {
	t.Helper()
	if spec.Transport == "" {
		spec.Transport = "http"
	}
	if spec.URL == "" {
		spec.URL = srv.URL
	}
	s, err := NewServer(spec, ServerDeps{
		HTTPClient: srv.Client(),
		Audit:      audit,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Shrink backoff for tests.
	s.backoff = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	return s
}

func TestServer_HappyPath_ReachesReady(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, nil)
	s := newTestServer(t, mock, types.MCPServer{
		Name:  "happy",
		Tools: types.MCPToolFilter{Allow: []string{"echo"}},
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Run(ctx) }()

	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatalf("server never reached Ready; state=%s", s.State())
	}
	tools := s.Tools()
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Errorf("tools = %v", tools)
	}
	cancel()
}

func TestServer_VersionMismatch_Failed(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"2020-01-01","serverInfo":{"name":"m","version":"1"}}}`))
	})
	s := newTestServer(t, mock, types.MCPServer{
		Name:     "vmismatch",
		Tools:    types.MCPToolFilter{Allow: []string{"x"}},
		Required: false,
	}, nil)

	if err := s.Run(context.Background()); err != nil {
		t.Errorf("Run with Required=false should return nil, got %v", err)
	}
	if s.State() != StateStopped {
		t.Errorf("state = %s, want Stopped", s.State())
	}
}

func TestServer_RequiredFailed_ReturnsError(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	s := newTestServer(t, mock, types.MCPServer{
		Name:     "req",
		Tools:    types.MCPToolFilter{Allow: []string{"x"}},
		Required: true,
	}, nil)
	err := s.Run(context.Background())
	if err == nil {
		t.Fatalf("expected error for Required=true terminal failure")
	}
	if !strings.Contains(err.Error(), "required mcp server") {
		t.Errorf("err message lacks expected prefix: %v", err)
	}
}

func TestServer_NonRequiredFailed_ReturnsNil(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	s := newTestServer(t, mock, types.MCPServer{
		Name:     "nonreq",
		Tools:    types.MCPToolFilter{Allow: []string{"x"}},
		Required: false,
	}, nil)
	// Even with 3 retries it'll fail; we want nil return.
	err := s.Run(context.Background())
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestServer_MalformedSchema_Failed(t *testing.T) {
	t.Parallel()
	// tools/list returns a tool with garbage schema.
	mock := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case MethodInitialized:
			w.WriteHeader(202)
		case MethodToolsList:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"bad","inputSchema":{"type":"NOTATHING"}}]}}`))
		}
	})
	s := newTestServer(t, mock, types.MCPServer{
		Name:  "badschema",
		Tools: types.MCPToolFilter{Allow: []string{"bad"}},
	}, nil)
	// Limit retries so test is fast.
	s.backoff = []time.Duration{5 * time.Millisecond}
	err := s.Run(context.Background())
	// Required=false default → nil return
	if err != nil {
		t.Errorf("expected nil for non-required, got %v", err)
	}
	if s.State() != StateStopped {
		t.Errorf("state = %s, want Stopped", s.State())
	}
}

func TestServer_ContextCancel_StopsCleanly(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, nil)
	s := newTestServer(t, mock, types.MCPServer{
		Name:  "cancel",
		Tools: types.MCPToolFilter{Allow: []string{"echo"}},
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()
	<-s.Ready()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
	if s.State() != StateStopped {
		t.Errorf("state = %s, want Stopped", s.State())
	}
}

func TestServer_ToolFilter_Allow(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case MethodInitialized:
			w.WriteHeader(202)
		case MethodToolsList:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[
				{"name":"a","inputSchema":{"type":"object"}},
				{"name":"b","inputSchema":{"type":"object"}},
				{"name":"c","inputSchema":{"type":"object"}}
			]}}`))
		}
	})
	s := newTestServer(t, mock, types.MCPServer{
		Name:  "filter",
		Tools: types.MCPToolFilter{Allow: []string{"a", "c"}},
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	<-s.Ready()
	tools := s.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2; got %v", len(tools), tools)
	}
	names := map[string]bool{tools[0].Name: true, tools[1].Name: true}
	if !names["a"] || !names["c"] {
		t.Errorf("filter wrong: %v", names)
	}
}

func TestServer_ToolFilter_WildcardWithDeny(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case MethodInitialized:
			w.WriteHeader(202)
		case MethodToolsList:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[
				{"name":"safe1","inputSchema":{"type":"object"}},
				{"name":"safe2","inputSchema":{"type":"object"}},
				{"name":"drop_table","inputSchema":{"type":"object"}}
			]}}`))
		}
	})
	s := newTestServer(t, mock, types.MCPServer{
		Name:  "wcdeny",
		Tools: types.MCPToolFilter{Allow: []string{"*"}, Deny: []string{"drop_table"}},
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	<-s.Ready()
	tools := s.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2", len(tools))
	}
	for _, tr := range tools {
		if tr.Name == "drop_table" {
			t.Errorf("drop_table should be denied")
		}
	}
}

func TestServer_BackoffSchedule_Pinned(t *testing.T) {
	t.Parallel()
	// Sanity-check the default backoff to ensure the design constants
	// match docs and the integrations doc.
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if len(defaultBackoff) != len(want) {
		t.Fatalf("backoff length = %d, want %d", len(defaultBackoff), len(want))
	}
	for i, d := range defaultBackoff {
		if d != want[i] {
			t.Errorf("defaultBackoff[%d] = %v, want %v", i, d, want[i])
		}
	}
}

func TestServer_AuditEvents_Emitted(t *testing.T) {
	t.Parallel()
	mock := newMockMCPServer(t, nil)

	// Capture audit output.
	var buf strings.Builder
	audit := runtime.NewAuditLogger(&safeWriter{w: &buf})
	s := newTestServer(t, mock, types.MCPServer{
		Name:  "audited",
		Tools: types.MCPToolFilter{Allow: []string{"echo"}},
	}, audit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	<-s.Ready()
	// Give the emitter a tick.
	time.Sleep(20 * time.Millisecond)
	output := buf.String()
	if !strings.Contains(output, "mcp_server_started") {
		t.Errorf("expected mcp_server_started event, got: %s", output)
	}
	if !strings.Contains(output, `"name":"audited"`) {
		t.Errorf("expected server name in event, got: %s", output)
	}
}

// safeWriter wraps a strings.Builder with a mutex so audit emission
// from a goroutine doesn't race the test reader.
type safeWriter struct {
	w *strings.Builder
	c atomic.Int32
}

func (s *safeWriter) Write(p []byte) (int, error) {
	for !s.c.CompareAndSwap(0, 1) {
	}
	defer s.c.Store(0)
	return s.w.Write(p)
}
