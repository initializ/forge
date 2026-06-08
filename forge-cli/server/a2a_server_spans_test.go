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
)

// TestHandleJSONRPC_EmitsDispatchSpan pins the Phase 3 (#104) invariant:
// every JSON-RPC inbound request produces a span named "a2a.<method>"
// carrying the method name as forge.a2a.method, plus the FWS-2
// workflow ids when the matching X-Workflow-* headers are present.
//
// The test installs a recording TracerProvider, fires a single request,
// and asserts the resulting span's name, status, and attribute set.
// Failing this means the inbound side of the trace tree is broken —
// every downstream span (Phase 5 cross-process propagation, Phase 4
// audit ↔ trace cross-linking) inherits from it.
func TestHandleJSONRPC_EmitsDispatchSpan(t *testing.T) {
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	body, _ := json.Marshal(a2a.JSONRPCRequest{
		JSONRPC: "2.0", Method: "tasks/send", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(coreruntime.HeaderWorkflowID, "wf-xyz")
	req.Header.Set(coreruntime.HeaderWorkflowStageID, "stage-1")
	req.Header.Set(coreruntime.HeaderWorkflowStepID, "step-7")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	// Span was exported synchronously by the test provider.
	span, ok := rec.FindSpan("a2a.tasks/send")
	if !ok {
		names := []string{}
		for _, s := range rec.Spans() {
			names = append(names, s.Name())
		}
		t.Fatalf("missing a2a.tasks/send span; recorded spans = %v", names)
	}

	var sawMethod, sawWF, sawStage, sawStep bool
	for _, kv := range span.Attributes() {
		switch string(kv.Key) {
		case observability.AttrForgeA2AMethod:
			if kv.Value.AsString() == "tasks/send" {
				sawMethod = true
			}
		case observability.AttrForgeWorkflowID:
			if kv.Value.AsString() == "wf-xyz" {
				sawWF = true
			}
		case observability.AttrForgeWorkflowStageID:
			if kv.Value.AsString() == "stage-1" {
				sawStage = true
			}
		case observability.AttrForgeWorkflowStepID:
			if kv.Value.AsString() == "step-7" {
				sawStep = true
			}
		}
	}
	if !sawMethod {
		t.Errorf("a2a span missing %s=tasks/send", observability.AttrForgeA2AMethod)
	}
	if !sawWF || !sawStage || !sawStep {
		t.Errorf("a2a span missing FWS-2 workflow attrs (wf=%v stage=%v step=%v)", sawWF, sawStage, sawStep)
	}
	if span.Status().Code.String() != "Unset" {
		t.Errorf("happy-path span status = %s; want Unset (only error paths set Error)", span.Status().Code.String())
	}
}

// TestHandleJSONRPC_UnknownMethodSpanIsError confirms the dispatcher
// records "method not found" as an Error status on the span so an
// operator scanning the trace browser sees the misroute without
// reading the body.
func TestHandleJSONRPC_UnknownMethodSpanIsError(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	addr := "127.0.0.1:" + itoaShim(port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	body, _ := json.Marshal(a2a.JSONRPCRequest{
		JSONRPC: "2.0", Method: "no/such/method", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	span, ok := rec.FindSpan("a2a.no/such/method")
	if !ok {
		t.Fatal("missing a2a.no/such/method span")
	}
	if span.Status().Code.String() != "Error" {
		t.Errorf("unknown-method span status = %s; want Error", span.Status().Code.String())
	}
}
