package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/types"
)

func TestRunner_MockIntegration(t *testing.T) {
	dir := t.TempDir()

	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "0.1.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Tools: []types.ToolRef{
			{Name: "search"},
		},
	}

	// Find a free port for the test
	port, err := findFreePort()
	if err != nil {
		t.Fatal(err)
	}

	runner, err := NewRunner(RunnerConfig{
		Config:    cfg,
		WorkDir:   dir,
		Port:      port,
		MockTools: true,
		Verbose:   false,
	})
	if err != nil {
		t.Fatalf("NewRunner error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start runner in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.Run(ctx)
	}()

	// Wait for server to be ready
	baseURL := fmt.Sprintf("http://localhost:%d", port)
	waitForServer(t, baseURL, 5*time.Second)

	// Test healthz
	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/healthz")
		if err != nil {
			t.Fatalf("healthz request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status: got %d", resp.StatusCode)
		}
	})

	// Test agent card
	t.Run("agent card", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/.well-known/agent.json")
		if err != nil {
			t.Fatalf("agent card request: %v", err)
		}
		defer resp.Body.Close()

		var card a2a.AgentCard
		json.NewDecoder(resp.Body).Decode(&card) //nolint:errcheck
		if card.Name != "test-agent" {
			t.Errorf("name: got %q", card.Name)
		}
	})

	// Test tasks/send
	t.Run("tasks/send", func(t *testing.T) {
		rpcReq := a2a.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      "1",
			Method:  "tasks/send",
			Params: mustMarshal(a2a.SendTaskParams{
				ID: "t-1",
				Message: a2a.Message{
					Role:  a2a.MessageRoleUser,
					Parts: []a2a.Part{a2a.NewTextPart("hello")},
				},
			}),
		}

		body, _ := json.Marshal(rpcReq)
		resp, err := http.Post(baseURL+"/", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("send request: %v", err)
		}
		defer resp.Body.Close()

		var rpcResp a2a.JSONRPCResponse
		json.NewDecoder(resp.Body).Decode(&rpcResp) //nolint:errcheck

		if rpcResp.Error != nil {
			t.Fatalf("unexpected error: %+v", rpcResp.Error)
		}

		// Extract task from result
		resultData, _ := json.Marshal(rpcResp.Result)
		var task a2a.Task
		json.Unmarshal(resultData, &task) //nolint:errcheck

		if task.ID != "t-1" {
			t.Errorf("task id: got %q", task.ID)
		}
		if task.Status.State != a2a.TaskStateCompleted {
			t.Errorf("state: got %q", task.Status.State)
		}
	})

	// Test tasks/get
	t.Run("tasks/get", func(t *testing.T) {
		rpcReq := a2a.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      "2",
			Method:  "tasks/get",
			Params:  mustMarshal(a2a.GetTaskParams{ID: "t-1"}),
		}

		body, _ := json.Marshal(rpcReq)
		resp, err := http.Post(baseURL+"/", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("get request: %v", err)
		}
		defer resp.Body.Close()

		var rpcResp a2a.JSONRPCResponse
		json.NewDecoder(resp.Body).Decode(&rpcResp) //nolint:errcheck

		if rpcResp.Error != nil {
			t.Fatalf("unexpected error: %+v", rpcResp.Error)
		}
	})

	// Test tasks/cancel
	t.Run("tasks/cancel", func(t *testing.T) {
		rpcReq := a2a.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      "3",
			Method:  "tasks/cancel",
			Params:  mustMarshal(a2a.CancelTaskParams{ID: "t-1"}),
		}

		body, _ := json.Marshal(rpcReq)
		resp, err := http.Post(baseURL+"/", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("cancel request: %v", err)
		}
		defer resp.Body.Close()

		var rpcResp a2a.JSONRPCResponse
		json.NewDecoder(resp.Body).Decode(&rpcResp) //nolint:errcheck

		if rpcResp.Error != nil {
			t.Fatalf("unexpected error: %+v", rpcResp.Error)
		}

		resultData, _ := json.Marshal(rpcResp.Result)
		var task a2a.Task
		json.Unmarshal(resultData, &task) //nolint:errcheck
		if task.Status.State != a2a.TaskStateCanceled {
			t.Errorf("state: got %q, want %q", task.Status.State, a2a.TaskStateCanceled)
		}
	})

	// Shutdown
	cancel()
}

func TestNewRunner_NilConfig(t *testing.T) {
	_, err := NewRunner(RunnerConfig{})
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestNewRunner_DefaultPort(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{
		Config: &types.ForgeConfig{
			AgentID:    "test",
			Version:    "0.1.0",
			Entrypoint: "python main.py",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.cfg.Port != 8080 {
		t.Errorf("default port: got %d, want 8080", runner.cfg.Port)
	}
}

func waitForServer(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("server did not start within %v", timeout)
		default:
		}
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}
