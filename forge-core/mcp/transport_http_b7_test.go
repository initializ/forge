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
)

// TestB7_SessionID_RotationRejected pins the review-B7 fix.
// A server that returns a DIFFERENT Mcp-Session-Id on a subsequent
// response (after the initial one was captured) is now rejected
// with ErrProtocolError. The previous code overwrote h.sessionID
// on every response — letting a buggy or malicious server splice
// Forge onto a different session mid-stream.
func TestB7_SessionID_RotationRejected(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		// First request: assigns session "alpha". Second: tries to
		// rotate to "beta".
		n := hits.Add(1)
		switch n {
		case 1:
			w.Header().Set("Mcp-Session-Id", "alpha")
		case 2:
			w.Header().Set("Mcp-Session-Id", "beta")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{}}`))
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()

	// First Send captures "alpha".
	id := json.Number("1")
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if got := tr.SessionID(); got != "alpha" {
		t.Fatalf("session id after first Send = %q, want alpha", got)
	}

	// Second Send: server tries to rotate to "beta". Must error.
	id2 := json.Number("2")
	err = tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id2, Method: "x"})
	if err == nil {
		t.Fatal("expected ErrProtocolError on session id rotation")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("err = %v, want wrap of ErrProtocolError", err)
	}
	for _, want := range []string{"Mcp-Session-Id", "alpha", "beta", "session hijack"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
	// The stored session ID must NOT be overwritten — the original
	// captured value stays so downstream auditing still has a stable
	// identifier to refer to.
	if got := tr.SessionID(); got != "alpha" {
		t.Errorf("session id after rotation attempt = %q, want alpha (must not be overwritten)", got)
	}
}

// TestB7_SessionID_SameValueAccepted — the common case where the
// server keeps returning the same Mcp-Session-Id header on every
// response. No error, value unchanged.
func TestB7_SessionID_SameValueAccepted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Mcp-Session-Id", "stable-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{}}`))
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()

	for i := range 5 {
		id := json.Number("1")
		if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"}); err != nil {
			t.Fatalf("Send[%d]: %v", i, err)
		}
		if got := tr.SessionID(); got != "stable-id" {
			t.Errorf("Send[%d]: session id = %q, want stable-id", i, got)
		}
	}
}

// TestB7_SessionID_EmptyHeaderAfterFirstKeepsCapturedValue —
// servers that omit Mcp-Session-Id on later responses (some do this
// once they assume the client is tracking it) must NOT clobber the
// captured value with "".
func TestB7_SessionID_EmptyHeaderAfterFirstKeepsCapturedValue(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if hits.Add(1) == 1 {
			w.Header().Set("Mcp-Session-Id", "first-only")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{}}`))
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()

	id := json.Number("1")
	_ = tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"})
	if got := tr.SessionID(); got != "first-only" {
		t.Fatalf("after first: %q, want first-only", got)
	}
	_ = tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"})
	if got := tr.SessionID(); got != "first-only" {
		t.Errorf("after second (empty header): %q, want first-only — empty must not clobber", got)
	}
}

// TestB7_SessionID_RotationDoesNotConsumeBody — when a rotation is
// detected, the response body is NOT consumed (we return before
// reaching consumeResponse). This means a malicious server can't
// smuggle frames through alongside the rotation attempt. Verify
// no frame appears in the Recv queue.
func TestB7_SessionID_RotationDoesNotConsumeBody(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		n := hits.Add(1)
		if n == 1 {
			w.Header().Set("Mcp-Session-Id", "A")
		} else {
			w.Header().Set("Mcp-Session-Id", "B-hijack")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"smuggled":"frame"}}`))
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(srv.URL, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()

	id := json.Number("1")
	_ = tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: "x"})
	// Drain the legitimate first frame.
	if _, err := tr.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}

	id2 := json.Number("2")
	if err := tr.Send(context.Background(), JSONRPCMessage{Jsonrpc: "2.0", ID: &id2, Method: "x"}); err == nil {
		t.Fatal("expected rotation rejection")
	}
	// No smuggled frame in the queue.
	if got := len(tr.queue); got != 0 {
		t.Errorf("queue has %d frames after rejected rotation — smuggled payload reached the demuxer", got)
	}
}
