package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// helper: stand up an MCP-shaped httptest server. Returns the URL.
// When handler is nil, every request gets the canned initialize +
// tools/list response.
func newMockMCPServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	if handler == nil {
		handler = defaultMockMCPHandler
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func defaultMockMCPHandler(w http.ResponseWriter, r *http.Request) {
	var msg JSONRPCMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "bad body", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch msg.Method {
	case MethodInitialize:
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"mock","version":"1.0"}}}`))
	case MethodInitialized:
		// notification — no response
		w.WriteHeader(http.StatusAccepted)
	case MethodToolsList:
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object","properties":{"msg":{"type":"string"}}}}]}}`))
	case MethodToolsCall:
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"content":[{"type":"text","text":"echoed"}]}}`))
	default:
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"error":{"code":-32601,"message":"unknown method"}}`))
	}
}

func runClient(t *testing.T, srv *httptest.Server) (*clientImpl, context.CancelFunc) {
	t.Helper()
	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(tr)
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	t.Cleanup(func() { _ = c.Close() })
	return c, cancel
}

func TestClient_Initialize_Happy(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, nil)
	c, cancel := runClient(t, srv)
	defer cancel()
	res, err := c.Initialize(context.Background(), ClientInfo{Name: "forge", Version: "0.12.0"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", res.ProtocolVersion, ProtocolVersion)
	}
}

func TestClient_Initialize_VersionMismatch(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"2024-01-01","serverInfo":{"name":"mock","version":"1"}}}`))
	})
	c, cancel := runClient(t, srv)
	defer cancel()
	_, err := c.Initialize(context.Background(), ClientInfo{Name: "x", Version: "y"})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want ErrVersionMismatch", err)
	}
}

func TestClient_ListTools(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, nil)
	c, cancel := runClient(t, srv)
	defer cancel()
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Errorf("tools = %v", tools)
	}
}

func TestClient_CallTool(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, nil)
	c, cancel := runClient(t, srv)
	defer cancel()
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echoed" {
		t.Errorf("content = %v", res.Content)
	}
}

func TestClient_Notification_NoResponse(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, nil)
	c, cancel := runClient(t, srv)
	defer cancel()
	if err := c.Initialized(context.Background()); err != nil {
		t.Fatalf("Initialized: %v", err)
	}
}

func TestClient_ConcurrentCalls_DemultiplexCorrectly(t *testing.T) {
	t.Parallel()
	// Server delays each response by a different amount to force
	// out-of-order arrival — exercises the response demux.
	var requestCount atomic.Int32
	srv := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		n := requestCount.Add(1)
		if msg.Method == MethodInitialize {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"mock","version":"1"}}}`))
			return
		}
		// Reverse delay so the 4th request returns first.
		time.Sleep(time.Duration(120-int(n)*20) * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"content":[{"type":"text","text":"id-` + msg.ID.String() + `"}]}}`))
	})
	c, cancel := runClient(t, srv)
	defer cancel()
	_, _ = c.Initialize(context.Background(), ClientInfo{Name: "x", Version: "y"})

	const n = 5
	results := make(chan string, n)
	for range n {
		go func() {
			res, _ := c.CallTool(context.Background(), "echo", nil)
			if res != nil && len(res.Content) > 0 {
				results <- res.Content[0].Text
			} else {
				results <- "<empty>"
			}
		}()
	}
	for range n {
		got := <-results
		if !strings.HasPrefix(got, "id-") {
			t.Errorf("unexpected result: %q", got)
		}
	}
}

func TestClient_CtxCancel_AbortsCallTool(t *testing.T) {
	t.Parallel()
	// Server delays 2s — we cancel before it responds.
	srv := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == MethodInitialize {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
			return
		}
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	c, cancel := runClient(t, srv)
	defer cancel()
	if _, err := c.Initialize(context.Background(), ClientInfo{Name: "x", Version: "y"}); err != nil {
		t.Fatal(err)
	}

	callCtx, callCancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := c.CallTool(callCtx, "echo", nil)
		resultCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	callCancel()

	select {
	case err := <-resultCh:
		if err == nil {
			t.Errorf("expected error after ctx cancel, got nil")
		}
	case <-time.After(1 * time.Second):
		t.Errorf("CallTool did not return after ctx cancel")
	}
}

func TestClient_ListTools_FollowsPagination(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		if msg.Method != MethodToolsList {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"error":{"code":-32601,"message":"unknown method"}}`))
			return
		}
		var params ListToolsParams
		_ = json.Unmarshal(msg.Params, &params)
		switch params.Cursor {
		case "":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"a"},{"name":"b"}],"nextCursor":"p2"}}`))
		case "p2":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"c"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"error":{"code":-32602,"message":"bad cursor"}}`))
		}
	})
	c, cancel := runClient(t, srv)
	defer cancel()
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 3 || tools[0].Name != "a" || tools[2].Name != "c" {
		t.Errorf("tools = %v, want a,b,c across two pages", tools)
	}
}

func TestClient_ListTools_RepeatedCursorTerminates(t *testing.T) {
	t.Parallel()
	srv := newMockMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		// Misbehaving server: always returns the same nextCursor.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"tools":[{"name":"loop"}],"nextCursor":"same"}}`))
	})
	c, cancel := runClient(t, srv)
	defer cancel()
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// First page + one page at cursor "same", then the repeat guard stops.
	if len(tools) != 2 {
		t.Errorf("len(tools) = %d, want 2 (repeat-cursor guard)", len(tools))
	}
}
