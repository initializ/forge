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

// helper: build a JSON-RPC response frame for a request id.
func okFrame(id string, result string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"result":` + result + `}`
}

func newTransport(t *testing.T, srv *httptest.Server, authFn AuthTokenFunc) *HTTPTransport {
	t.Helper()
	tr, err := NewHTTPTransport(srv.URL, srv.Client(), authFn)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestHTTPTransport_RoundTrip_JSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okFrame("1", `{"ok":true}`)))
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	id := json.Number("1")
	if err := tr.Send(ctx, JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "tools/list"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := tr.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got.ID == nil || got.ID.String() != "1" {
		t.Errorf("response ID = %v, want 1", got.ID)
	}
	if !strings.Contains(string(got.Result), `"ok":true`) {
		t.Errorf("result = %s", string(got.Result))
	}
}

func TestHTTPTransport_SSE_MultiFrame(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: " + okFrame("1", `{"step":1}`) + "\n\n"))
		f.Flush()
		_, _ = w.Write([]byte("data: " + okFrame("2", `{"step":2}`) + "\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	id := json.Number("1")
	if err := tr.Send(ctx, JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for i := range 2 {
		got, err := tr.Recv(ctx)
		if err != nil {
			t.Fatalf("Recv[%d]: %v", i, err)
		}
		if got.ID == nil {
			t.Errorf("frame %d missing id", i)
		}
	}
}

func TestHTTPTransport_4xx_MapsToProtocolError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, ErrProtocolError) {
		t.Fatalf("want ErrProtocolError, got %v", err)
	}
}

func TestHTTPTransport_5xx_MapsToTransportUnavailable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, ErrTransportUnavailable) {
		t.Fatalf("want ErrTransportUnavailable, got %v", err)
	}
}

func TestHTTPTransport_202_NoFrames(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "notifications/initialized"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Recv with a short timeout should NOT produce a frame.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := tr.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded (no frame queued), got %v", err)
	}
}

func TestHTTPTransport_AuthHeader_Injected(t *testing.T) {
	t.Parallel()
	var seen atomic.Value // string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okFrame("1", `{}`)))
	}))
	defer srv.Close()

	tr := newTransport(t, srv, func(_ context.Context) (string, error) { return "TOKABC", nil })
	id := json.Number("1")
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := seen.Load().(string); got != "Bearer TOKABC" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer TOKABC")
	}
}

func TestHTTPTransport_AuthFn_ErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := newTransport(t, srv, func(_ context.Context) (string, error) {
		return "", ErrTokenRevoked
	})
	err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("want ErrTokenRevoked, got %v", err)
	}
}

func TestHTTPTransport_SessionIDRoundtrip(t *testing.T) {
	t.Parallel()
	var seenSID atomic.Value // string from 2nd request
	requestCount := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.Header().Set("Mcp-Session-Id", "sess-xyz")
		} else {
			seenSID.Store(r.Header.Get("Mcp-Session-Id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okFrame("1", `{}`)))
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	ctx := context.Background()
	id := json.Number("1")
	_ = tr.Send(ctx, JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "initialize"})
	if got := tr.SessionID(); got != "sess-xyz" {
		t.Fatalf("SessionID = %q, want sess-xyz", got)
	}
	_ = tr.Send(ctx, JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "tools/list"})
	if got, _ := seenSID.Load().(string); got != "sess-xyz" {
		t.Errorf("2nd req Mcp-Session-Id header = %q, want sess-xyz", got)
	}
}

func TestHTTPTransport_ContextCancelMidFlight(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := tr.Send(ctx, JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("Send took %v after cancel — should return promptly", elapsed)
	}
}

func TestHTTPTransport_CloseUnblocksRecv(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = tr.Close()
	}()
	_, err := tr.Recv(context.Background())
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Recv after Close: err = %v, want ErrClosed", err)
	}
}

func TestNewHTTPTransport_NilClient_Rejected(t *testing.T) {
	t.Parallel()
	_, err := NewHTTPTransport("http://x", nil, nil)
	if err == nil || !errors.Is(err, ErrProtocolError) {
		t.Fatalf("nil client should be rejected with ErrProtocolError, got %v", err)
	}
}

func TestNewHTTPTransport_EmptyURL_Rejected(t *testing.T) {
	t.Parallel()
	c := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).Client()
	_, err := NewHTTPTransport("", c, nil)
	if err == nil || !errors.Is(err, ErrProtocolError) {
		t.Fatalf("empty url should be rejected, got %v", err)
	}
}
