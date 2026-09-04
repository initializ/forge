package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/types"
)

// TestRunner_KillSwitch_RefusesNewWorkOnEveryIngress is the kill-switch
// contract test: once admin/kill trips the gate, NONE of the four new-work
// ingress paths may admit a task. It guards specifically against Finding 1 of
// the #439 review — the JSON-RPC gate landing but the two REST mirrors
// (POST /tasks/send, POST /tasks/sendSubscribe) staying open.
func TestRunner_KillSwitch_RefusesNewWorkOnEveryIngress(t *testing.T) {
	dir := t.TempDir()
	cfg := &types.ForgeConfig{
		AgentID:    "kill-switch-gate",
		Version:    "0.1.0",
		Framework:  "forge",
		Entrypoint: "python main.py",
		Tools:      []types.ToolRef{{Name: "search"}},
	}
	port, err := findFreePort()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Config: cfg, WorkDir: dir, Port: port, MockTools: true})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	waitForServer(t, baseURL, 5*time.Second)
	token, _ := auth.LoadToken(dir)

	// Trip the kill switch. Idle agent → cancelled=0, but the call must still
	// succeed and flip the gate (and, per Finding 2, emit admin_kill regardless).
	killBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"admin/kill","params":{"reason":"test"}}`)
	resp, err := authPost(baseURL+"/", token, killBody)
	if err != nil {
		t.Fatalf("admin/kill: %v", err)
	}
	var killResp a2a.JSONRPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&killResp)
	_ = resp.Body.Close()
	if killResp.Error != nil {
		t.Fatalf("admin/kill returned error: %+v", killResp.Error)
	}
	result, _ := killResp.Result.(map[string]any)
	if killed, _ := result["killed"].(bool); !killed {
		t.Fatalf("admin/kill result should report killed=true, got %v", killResp.Result)
	}

	send := []byte(`{"jsonrpc":"2.0","id":2,"method":"tasks/send","params":{"id":"t-after-kill","message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}`)

	// 1. JSON-RPC tasks/send → Unavailable error, work NOT admitted.
	r1, err := authPost(baseURL+"/", token, send)
	if err != nil {
		t.Fatalf("tasks/send: %v", err)
	}
	var rpc a2a.JSONRPCResponse
	_ = json.NewDecoder(r1.Body).Decode(&rpc)
	_ = r1.Body.Close()
	if rpc.Error == nil {
		t.Fatalf("JSON-RPC tasks/send after kill must error; got result=%v", rpc.Result)
	}
	if rpc.Error.Code != a2a.ErrCodeUnavailable {
		t.Errorf("JSON-RPC error.code = %d, want %d (Unavailable)", rpc.Error.Code, a2a.ErrCodeUnavailable)
	}

	// 2. REST POST /tasks/send → 503, work NOT admitted (the Finding-1 gap).
	rest := []byte(`{"task":{"id":"t-rest-after-kill","message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}`)
	r2, err := authPost(baseURL+"/tasks/send", token, rest)
	if err != nil {
		t.Fatalf("REST tasks/send: %v", err)
	}
	_ = r2.Body.Close()
	if r2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("REST POST /tasks/send after kill: status = %d, want 503", r2.StatusCode)
	}

	// 3. REST POST /tasks/sendSubscribe → 503.
	r3, err := authPost(baseURL+"/tasks/sendSubscribe", token, rest)
	if err != nil {
		t.Fatalf("REST tasks/sendSubscribe: %v", err)
	}
	_ = r3.Body.Close()
	if r3.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("REST POST /tasks/sendSubscribe after kill: status = %d, want 503", r3.StatusCode)
	}
}
