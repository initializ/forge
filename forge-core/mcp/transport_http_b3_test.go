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

// TestB3_QueueFullForLong_ClosesTransport — the key new property:
// if the queue stays full longer than overflowTimeout (no Recv
// consumer), push() closes the transport with ErrTransportUnavailable
// instead of silently dropping the oldest frame. Pending callers
// then observe the close via subsequent Recv → ErrClosed and can
// react with a clear error rather than hanging.
func TestB3_QueueFullForLong_ClosesTransport(t *testing.T) {
	t.Parallel()
	// SSE handler emits MORE frames than the queue can hold without
	// a Recv consumer — pushes 32 frames into a 16-slot queue.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for i := range 32 {
			_, _ = w.Write([]byte("data: " + okFrame("1", `{"i":`+itoa(i)+`}`) + "\n\n"))
			f.Flush()
		}
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Shorten so this test runs in milliseconds, not 30s.
	tr.overflowTimeout = 100 * time.Millisecond
	defer func() { _ = tr.Close() }()

	id := json.Number("1")
	// Send with NO Recv consumer. The SSE parser will push 16
	// frames successfully, then block on the 17th. After
	// overflowTimeout, push closes the transport and returns
	// ErrTransportUnavailable. Send propagates that.
	t0 := time.Now()
	err = tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"})
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatal("expected ErrTransportUnavailable from queue overflow")
	}
	if !errors.Is(err, ErrTransportUnavailable) {
		t.Errorf("err = %v, want wrap of ErrTransportUnavailable", err)
	}
	if !strings.Contains(err.Error(), "queue full") {
		t.Errorf("err lacks diagnostic hint: %v", err)
	}
	// Should fire within ~overflowTimeout, not the prior silent-drop
	// behavior where it returned promptly with no error.
	if elapsed < 80*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Errorf("Send took %v, expected ~100ms (overflowTimeout)", elapsed)
	}

	// Subsequent Recv MUST return ErrClosed promptly — pending
	// callers observe the close via this path.
	recvCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := tr.Recv(recvCtx); !errors.Is(err, ErrClosed) {
		t.Errorf("Recv after overflow-close: err = %v, want ErrClosed", err)
	}
}

// TestB3_NoSilentDrop_AllFramesDelivered — under a normal consumer,
// EVERY frame survives. With B3's drop-oldest the test would lose
// frames silently; with the fix backpressure keeps the SSE parser
// in lockstep with the consumer.
func TestB3_NoSilentDrop_AllFramesDelivered(t *testing.T) {
	t.Parallel()
	const total = 128 // 8× queue capacity — overflow guaranteed without consumer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for i := range total {
			_, _ = w.Write([]byte("data: " + okFrame("1", `{"seq":`+itoa(i)+`}`) + "\n\n"))
			f.Flush()
		}
	}))
	defer srv.Close()

	tr := newTransport(t, srv, nil)
	id := json.Number("1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gotSeq := make([]int, 0, total)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := tr.Recv(ctx)
			if err != nil {
				return
			}
			// Pull "seq":N out of the Result.
			s := string(msg.Result)
			idx := strings.Index(s, `"seq":`)
			if idx < 0 {
				continue
			}
			rest := s[idx+len(`"seq":`):]
			endIdx := strings.IndexAny(rest, ",}")
			if endIdx < 0 {
				continue
			}
			n := atoi(rest[:endIdx])
			gotSeq = append(gotSeq, n)
			if len(gotSeq) == total {
				return
			}
		}
	}()
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-done

	if len(gotSeq) != total {
		t.Fatalf("got %d frames, want %d (silent drops would shrink this)", len(gotSeq), total)
	}
	// Verify monotonic ordering — backpressure preserves SSE order.
	for i, n := range gotSeq {
		if n != i {
			t.Fatalf("frame[%d].seq = %d, want %d (out-of-order delivery)", i, n, i)
		}
	}
}

// TestB3_CloseDuringPush_DoesNotPanic — pre-existing race hazard
// that this PR also fixes: closing the queue chan while a goroutine
// was blocked sending to it would panic. The new design uses an
// independent done channel so Close never closes the queue, and
// push selects on done.
func TestB3_CloseDuringPush_DoesNotPanic(t *testing.T) {
	t.Parallel()
	// Server with an SSE response that pushes faster than consumer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for range 32 {
			_, _ = w.Write([]byte("data: " + okFrame("1", `{}`) + "\n\n"))
			f.Flush()
		}
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.overflowTimeout = 200 * time.Millisecond

	// Send is going to fill the queue and block in push.
	id := json.Number("1")
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"})
	}()
	// Race: close mid-overflow. Must NOT panic.
	time.Sleep(50 * time.Millisecond)
	_ = tr.Close()

	select {
	case err := <-sendErr:
		// Either ErrClosed (close arrived first) or
		// ErrTransportUnavailable (overflow fired first) — both are
		// acceptable. The critical property is "no panic."
		if err == nil {
			t.Errorf("expected error from Send after Close, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after Close")
	}
}

// atoi: tiny helper for the seq parser; avoids strconv import here.
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
