package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"go.opentelemetry.io/otel/baggage"
)

// waitForListen blocks until the given addr accepts a TCP connection,
// or fails the test after 2s. Used by every propagation test below to
// give the server's listener time to bind before firing the request.
func waitForListen(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not start listening on %s within 2s", addr)
}

// baggageMemberValue extracts a single baggage member's value from
// ctx. Returns the empty string when the member is absent — callers
// then assert against the expected value with a == comparison.
func baggageMemberValue(ctx context.Context, key string) string {
	return baggage.FromContext(ctx).Member(key).Value()
}

// TestHandleJSONRPC_InboundTraceparent_AdoptsUpstreamTrace pins the
// Phase 5 (#106) invariant: when an upstream caller sends a request
// with a `traceparent` header, the dispatcher span MUST become a
// child of that upstream span — same trace_id, parent span_id =
// upstream span_id. Multi-hop A2A flows (orchestrator → agent →
// downstream agent) then display as a single trace in the backend.
//
// Without this, every hop starts a new root trace, and the operator
// has to manually correlate by correlation_id — defeating the point
// of distributed tracing.
func TestHandleJSONRPC_InboundTraceparent_AdoptsUpstreamTrace(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() {
		coreruntime.ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Port: port, Host: "127.0.0.1", ShutdownTimeout: 1 * time.Second,
		AgentCard: &a2a.AgentCard{
			Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0",
		},
	})
	srv.RegisterHandler("tasks/send", func(_ context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		return a2a.NewResponse(id, map[string]string{"ok": "1"})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	addr := "127.0.0.1:" + itoaShim(port)
	waitForListen(t, addr)

	// Synthesize a well-formed W3C traceparent header. Format:
	//   <version>-<trace_id (32 hex)>-<parent_span_id (16 hex)>-<flags>
	// The `01` flag means "sampled" — required for the inbound
	// dispatch span to also be recorded.
	const upstreamTraceID = "0af7651916cd43dd8448eb211c80319c"
	const upstreamSpanID = "b7ad6b7169203331"
	const traceparent = "00-" + upstreamTraceID + "-" + upstreamSpanID + "-01"

	body, _ := json.Marshal(a2a.JSONRPCRequest{
		JSONRPC: "2.0", Method: "tasks/send", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", traceparent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	span, ok := rec.FindSpan("a2a.tasks/send")
	if !ok {
		t.Fatal("missing a2a.tasks/send span")
	}
	gotTrace := span.SpanContext().TraceID().String()
	if gotTrace != upstreamTraceID {
		t.Errorf("dispatcher span trace_id = %q; want %q (upstream's)", gotTrace, upstreamTraceID)
	}
	gotParent := span.Parent().SpanID().String()
	if gotParent != upstreamSpanID {
		t.Errorf("dispatcher span parent_span_id = %q; want %q (upstream's span_id)", gotParent, upstreamSpanID)
	}
	if !span.SpanContext().IsSampled() {
		t.Error("dispatcher span must inherit the upstream's sampled flag")
	}
}

// TestHandleJSONRPC_NoInboundTraceparent_StartsRoot confirms the
// backward-compatibility invariant — when there is no `traceparent`
// header, the dispatcher span is a fresh root, same as pre-Phase-5
// behavior. Direct A2A invocations (no orchestrator above) must keep
// working unchanged.
func TestHandleJSONRPC_NoInboundTraceparent_StartsRoot(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() {
		coreruntime.ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Port: port, Host: "127.0.0.1", ShutdownTimeout: 1 * time.Second,
		AgentCard: &a2a.AgentCard{
			Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0",
		},
	})
	srv.RegisterHandler("tasks/send", func(_ context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		return a2a.NewResponse(id, map[string]string{"ok": "1"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	addr := "127.0.0.1:" + itoaShim(port)
	waitForListen(t, addr)

	body, _ := json.Marshal(a2a.JSONRPCRequest{
		JSONRPC: "2.0", Method: "tasks/send", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately NO traceparent header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	span, ok := rec.FindSpan("a2a.tasks/send")
	if !ok {
		t.Fatal("missing a2a.tasks/send span")
	}
	// A root span has an invalid (zero) parent span context.
	if span.Parent().IsValid() {
		t.Errorf("dispatcher must be a root span when no traceparent inbound; got parent %+v", span.Parent())
	}
	if !span.SpanContext().TraceID().IsValid() {
		t.Error("dispatcher span must still have a valid (newly-generated) trace_id")
	}
}

// TestHandleJSONRPC_MalformedTraceparent_StartsRoot guards against a
// failure mode where a misformatted upstream header propagates a
// broken context. The W3C propagator silently ignores malformed
// traceparent values (returns the ctx unchanged); the dispatcher
// must then start a fresh root span rather than carry the broken
// context forward. Without this guard, an upstream bug ships a span
// tree rooted on garbage.
func TestHandleJSONRPC_MalformedTraceparent_StartsRoot(t *testing.T) {
	tp, rec := observability.NewTestTracerProvider()
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() {
		coreruntime.ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Port: port, Host: "127.0.0.1", ShutdownTimeout: 1 * time.Second,
		AgentCard: &a2a.AgentCard{
			Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0",
		},
	})
	srv.RegisterHandler("tasks/send", func(_ context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		return a2a.NewResponse(id, map[string]string{"ok": "1"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	addr := "127.0.0.1:" + itoaShim(port)
	waitForListen(t, addr)

	body, _ := json.Marshal(a2a.JSONRPCRequest{
		JSONRPC: "2.0", Method: "tasks/send", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", "not-a-real-traceparent")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	span, ok := rec.FindSpan("a2a.tasks/send")
	if !ok {
		t.Fatal("missing a2a.tasks/send span")
	}
	if span.Parent().IsValid() {
		t.Errorf("malformed traceparent must not produce a parented span; got parent %+v", span.Parent())
	}
}

// TestHandleJSONRPC_InboundBaggage_PropagatesToHandlerContext confirms
// the OTel baggage propagator (the other half of the composite
// installed by Phase 0) also makes it through the dispatcher. Phase 5
// is about end-to-end propagation; baggage is the standard channel
// for application-level identifiers (tenant_id, user_id, ab_test
// bucket) that need to travel with the trace context.
func TestHandleJSONRPC_InboundBaggage_PropagatesToHandlerContext(t *testing.T) {
	tp, _ := observability.NewTestTracerProvider()
	coreruntime.SetTracerProvider(tp)
	t.Cleanup(func() {
		coreruntime.ResetTracerProviderForTest()
		_ = tp.Shutdown(context.Background())
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Port: port, Host: "127.0.0.1", ShutdownTimeout: 1 * time.Second,
		AgentCard: &a2a.AgentCard{
			Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0",
		},
	})
	var capturedCtx context.Context
	srv.RegisterHandler("test/echo", func(ctx context.Context, id any, _ json.RawMessage) *a2a.JSONRPCResponse {
		capturedCtx = ctx
		return a2a.NewResponse(id, map[string]string{"ok": "1"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	addr := "127.0.0.1:" + itoaShim(port)
	waitForListen(t, addr)

	body, _ := json.Marshal(a2a.JSONRPCRequest{
		JSONRPC: "2.0", Method: "test/echo", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("baggage", "tenant_id=acme,ab_bucket=control")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	if capturedCtx == nil {
		t.Fatal("handler was not invoked")
	}
	// The ctx the handler sees must carry the upstream baggage so
	// downstream outbound HTTP (through otelhttp on the egress
	// transport) re-injects it on the next hop.
	bag := baggageMemberValue(capturedCtx, "tenant_id")
	if bag != "acme" {
		t.Errorf("tenant_id baggage = %q; want %q", bag, "acme")
	}
	if v := baggageMemberValue(capturedCtx, "ab_bucket"); v != "control" {
		t.Errorf("ab_bucket baggage = %q; want %q", v, "control")
	}
}
