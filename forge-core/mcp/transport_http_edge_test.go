package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHTTPTransport_EmptyJSONBody_TreatedAsAck covers the
// "empty 200 body" branch of consumeJSON.
func TestHTTPTransport_EmptyJSONBody_TreatedAsAck(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// no body
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := tr.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("empty body should produce no frame, got %v", err)
	}
}

// TestHTTPTransport_MalformedJSON_ProtocolError covers parse-failure
// in consumeJSON.
func TestHTTPTransport_MalformedJSON_ProtocolError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, ErrProtocolError) {
		t.Fatalf("malformed JSON should yield ErrProtocolError, got %v", err)
	}
}

// TestHTTPTransport_UnknownContentType_ProtocolError covers the
// default branch of consumeResponse.
func TestHTTPTransport_UnknownContentType_ProtocolError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("just text"))
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, ErrProtocolError) {
		t.Fatalf("unknown CT should yield ErrProtocolError, got %v", err)
	}
}

// TestHTTPTransport_QueueOverflow_NoSilentDrop covers the review-B3
// fix: when the response queue fills, push() blocks (not silently
// drops the oldest frame). With a Recv consumer in tandem, all 64
// SSE frames must be delivered — no silent loss.
func TestHTTPTransport_QueueOverflow_NoSilentDrop(t *testing.T) {
	t.Parallel()
	const total = 64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for i := range total {
			_, _ = w.Write([]byte("data: " + okFrame("1", `{"i":`+itoa(i)+`}`) + "\n\n"))
			f.Flush()
		}
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	id := json.Number("1")

	// Start Recv before Send so the consumer keeps the queue draining;
	// without this, push WOULD block until overflowTimeout (which the
	// next test exercises).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gotCh := make(chan int, 1)
	go func() {
		n := 0
		for {
			_, err := tr.Recv(ctx)
			if err != nil {
				gotCh <- n
				return
			}
			n++
			if n == total {
				gotCh <- n
				return
			}
		}
	}()
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	n := <-gotCh
	if n != total {
		t.Errorf("got %d frames, want %d — fix B3 ensures no silent drop", n, total)
	}
}

// itoa avoids strconv import; tiny helper for the overflow test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestHTTPTransport_CloseIdempotent verifies the once.Do guards work.
func TestHTTPTransport_CloseIdempotent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tr := newTransport(t, srv, nil)
	if err := tr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Subsequent Send returns ErrClosed.
	err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", Method: "x"})
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close: err = %v, want ErrClosed", err)
	}
}

// TestReadSummary_TruncatesAndStrips covers the helper.
func TestReadSummary_TruncatesAndStrips(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("ab\n", 200)
	got := readSummary(strings.NewReader(long))
	if strings.Contains(got, "\n") {
		t.Errorf("newlines not stripped")
	}
	if len(got) > 220 {
		t.Errorf("summary too long: %d", len(got))
	}
}
