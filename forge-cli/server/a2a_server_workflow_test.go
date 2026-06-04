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
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Regression test for issue #86 / FWS-2: the JSON-RPC dispatcher must
// extract X-Workflow-* / X-Invocation-Caller correlation headers and
// inject the resulting WorkflowContext into the ctx passed to method
// handlers.

func TestHandleJSONRPC_ExtractsWorkflowHeadersIntoContext(t *testing.T) {
	// Pick a free port deterministically (httptest doesn't help here
	// because we need the real Server's dispatcher).
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Port:            port,
		Host:            "127.0.0.1",
		ShutdownTimeout: 1 * time.Second,
		AgentCard: &a2a.AgentCard{
			Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0",
		},
	})

	// Capture the ctx the handler receives so the test can inspect it.
	var capturedCtx context.Context
	srv.RegisterHandler("test/echo", func(ctx context.Context, id any, params json.RawMessage) *a2a.JSONRPCResponse {
		capturedCtx = ctx
		return a2a.NewResponse(id, map[string]string{"ok": "1"})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	// Wait for listener.
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
		JSONRPC: "2.0",
		Method:  "test/echo",
		Params:  json.RawMessage(`{}`),
		ID:      "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(coreruntime.HeaderWorkflowID, "wf-orchestrated")
	req.Header.Set(coreruntime.HeaderWorkflowStageID, "stage-deploy")
	req.Header.Set(coreruntime.HeaderWorkflowStepID, "step-3")
	req.Header.Set(coreruntime.HeaderInvocationCaller, "initializ-orchestrator")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	if capturedCtx == nil {
		t.Fatal("handler was not invoked")
	}
	wc := coreruntime.WorkflowContextFromContext(capturedCtx)
	if wc.WorkflowID != "wf-orchestrated" {
		t.Errorf("WorkflowID = %q, want wf-orchestrated", wc.WorkflowID)
	}
	if wc.StageID != "stage-deploy" || wc.StepID != "step-3" || wc.InvocationCaller != "initializ-orchestrator" {
		t.Errorf("WorkflowContext fields = %+v, want all four populated from headers", wc)
	}
}

func TestHandleJSONRPC_MissingHeadersYieldZeroWorkflowContext(t *testing.T) {
	// Backward compat: direct A2A invocation (no headers) must produce
	// an IsZero WorkflowContext — audit events then omit the workflow
	// fields entirely, matching pre-FWS-2 consumers' expectations.
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
	srv.RegisterHandler("test/echo", func(ctx context.Context, id any, params json.RawMessage) *a2a.JSONRPCResponse {
		capturedCtx = ctx
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
		JSONRPC: "2.0", Method: "test/echo", Params: json.RawMessage(`{}`), ID: "1",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Workflow-* headers.

	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()

	if capturedCtx == nil {
		t.Fatal("handler was not invoked")
	}
	wc := coreruntime.WorkflowContextFromContext(capturedCtx)
	if !wc.IsZero() {
		t.Errorf("no headers should yield IsZero WorkflowContext, got %+v", wc)
	}
}

// itoaShim avoids depending on the package's other test helper.
func itoaShim(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
