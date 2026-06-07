package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

// TestRunner_JSONRPC_TasksSend_RejectsLegacyTypeDiscriminator is the
// regression test for issue #119 — the canonical scenario where an
// operator hits a Forge dev runner with `curl` carrying parts that
// use the pre-0.3.0 `"type"` discriminator instead of the spec-correct
// `"kind"`. Pre-fix: 200 OK + a confused "your message didn't come
// through" agent response. Post-fix: a clear JSON-RPC error
// identifying the spec divergence so the caller can act on it without
// reading source.
func TestRunner_JSONRPC_TasksSend_RejectsLegacyTypeDiscriminator(t *testing.T) {
	dir := t.TempDir()
	cfg := &types.ForgeConfig{
		AgentID:    "msg-validation-jsonrpc",
		Version:    "0.1.0",
		Framework:  "forge",
		Entrypoint: "python main.py",
		Tools:      []types.ToolRef{{Name: "search"}},
	}
	port, err := findFreePort()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{
		Config: cfg, WorkDir: dir, Port: port, MockTools: true,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	waitForServer(t, baseURL, 5*time.Second)
	token, _ := auth.LoadToken(dir)

	// Exact payload shape from issue #119: parts use `"type"` instead
	// of `"kind"`. encoding/json drops `type` (the Part struct only
	// knows `kind`), so the Part decodes with empty Kind + populated
	// Text. Pre-fix: the executor saw a kind-less part and the LLM
	// produced a confused reply. Post-fix: Validate rejects.
	body := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tasks/send",
		"params": {
			"id": "t-legacy-type",
			"message": {
				"role": "user",
				"parts": [{ "type": "text", "text": "Reply with a one-line hello." }]
			}
		}
	}`)
	resp, err := authPost(baseURL+"/", token, body)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (JSON-RPC errors are still HTTP 200; the error is in the envelope)", resp.StatusCode)
	}
	var rpcResp a2a.JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error == nil {
		t.Fatalf("expected a JSON-RPC error rejecting the malformed message; got result=%v", rpcResp.Result)
	}
	if rpcResp.Error.Code != a2a.ErrCodeInvalidParams {
		t.Errorf("error.code = %d, want %d (InvalidParams)", rpcResp.Error.Code, a2a.ErrCodeInvalidParams)
	}
	// The error message must be actionable — name the spec divergence
	// the operator's payload exhibited so they can fix it without
	// digging through source. Anything that just says "invalid params"
	// is a regression.
	msg := rpcResp.Error.Message
	for _, want := range []string{`"type"`, `"kind"`, "pre-0.3.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error.message must name the type/kind mistake; want substring %q, got %q", want, msg)
		}
	}
}

// TestRunner_JSONRPC_TasksSend_SpecCompliantPayloadStillWorks confirms
// the validator doesn't over-reject — the well-formed A2A 0.3.0 shape
// continues to succeed end-to-end (status 200, no JSON-RPC error,
// task in completed state). Without this companion test, an
// over-eager validation rewrite that broke the happy path would
// pass the rejection test above and ship.
func TestRunner_JSONRPC_TasksSend_SpecCompliantPayloadStillWorks(t *testing.T) {
	dir := t.TempDir()
	cfg := &types.ForgeConfig{
		AgentID:    "msg-validation-happy",
		Version:    "0.1.0",
		Framework:  "forge",
		Entrypoint: "python main.py",
		Tools:      []types.ToolRef{{Name: "search"}},
	}
	port, err := findFreePort()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{
		Config: cfg, WorkDir: dir, Port: port, MockTools: true,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	waitForServer(t, baseURL, 5*time.Second)
	token, _ := auth.LoadToken(dir)

	// Same payload, but with the spec-correct `"kind"` discriminator.
	body := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tasks/send",
		"params": {
			"id": "t-spec-correct",
			"message": {
				"role": "user",
				"parts": [{ "kind": "text", "text": "Reply with a one-line hello." }]
			}
		}
	}`)
	resp, err := authPost(baseURL+"/", token, body)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var rpcResp a2a.JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("spec-correct payload must not produce an error; got %+v", rpcResp.Error)
	}
	resultBytes, _ := json.Marshal(rpcResp.Result)
	var task a2a.Task
	if err := json.Unmarshal(resultBytes, &task); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task state = %q, want %q", task.Status.State, a2a.TaskStateCompleted)
	}
}

// TestRunner_JSONRPC_TasksSend_RejectsEmptyParts confirms the
// validator catches the other Message-level shape failures: missing
// role and empty parts array. Without this the validator could
// regress to handling only the type-vs-kind case and miss the other
// classes of malformed message Message.Validate documents.
func TestRunner_JSONRPC_TasksSend_RejectsEmptyParts(t *testing.T) {
	dir := t.TempDir()
	cfg := &types.ForgeConfig{
		AgentID:    "msg-validation-empty-parts",
		Version:    "0.1.0",
		Framework:  "forge",
		Entrypoint: "python main.py",
		Tools:      []types.ToolRef{{Name: "search"}},
	}
	port, err := findFreePort()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{
		Config: cfg, WorkDir: dir, Port: port, MockTools: true,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	waitForServer(t, baseURL, 5*time.Second)
	token, _ := auth.LoadToken(dir)

	body := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tasks/send",
		"params": {
			"id": "t-empty-parts",
			"message": { "role": "user", "parts": [] }
		}
	}`)
	resp, err := authPost(baseURL+"/", token, body)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var rpcResp a2a.JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpcResp.Error == nil {
		t.Fatalf("empty parts must produce an error; got result=%v", rpcResp.Result)
	}
	if !strings.Contains(rpcResp.Error.Message, "parts") {
		t.Errorf("error must name the parts violation; got %q", rpcResp.Error.Message)
	}
}
