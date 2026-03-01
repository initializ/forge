package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
		Framework:  "forge",
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
		defer func() { _ = resp.Body.Close() }()
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
		defer func() { _ = resp.Body.Close() }()

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
		defer func() { _ = resp.Body.Close() }()

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
		defer func() { _ = resp.Body.Close() }()

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
		defer func() { _ = resp.Body.Close() }()

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

func TestDiscoverSkillFiles(t *testing.T) {
	dir := t.TempDir()

	// Create flat skill: skills/search.md
	skillsDir := dir + "/skills"
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillsDir+"/search.md", []byte("# search"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create subdirectory skill: skills/k8s-triage/SKILL.md
	subDir := skillsDir + "/k8s-triage"
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subDir+"/SKILL.md", []byte("# k8s triage"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create main SKILL.md
	if err := os.WriteFile(dir+"/SKILL.md", []byte("# main"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{
		cfg: RunnerConfig{
			Config:  &types.ForgeConfig{},
			WorkDir: dir,
		},
	}

	files := runner.discoverSkillFiles()

	// Should find: skills/search.md, skills/k8s-triage/SKILL.md, SKILL.md
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}

	// Check that all expected files are present
	found := map[string]bool{}
	for _, f := range files {
		found[f] = true
	}
	wantFlat := skillsDir + "/search.md"
	wantSub := subDir + "/SKILL.md"
	wantMain := dir + "/SKILL.md"
	if !found[wantFlat] {
		t.Errorf("missing flat skill: %s", wantFlat)
	}
	if !found[wantSub] {
		t.Errorf("missing subdirectory skill: %s", wantSub)
	}
	if !found[wantMain] {
		t.Errorf("missing main SKILL.md: %s", wantMain)
	}
}

func TestExpandEgressDomains(t *testing.T) {
	envVars := map[string]string{
		"K8S_API_DOMAIN": "my-cluster.eastus.azmk8s.io",
		"MULTI_DOMAINS":  "a.eks.amazonaws.com, b.azmk8s.io",
		"EMPTY_VAR":      "",
	}

	tests := []struct {
		name   string
		domain string
		want   []string
	}{
		{
			name:   "no variable",
			domain: "api.example.com",
			want:   []string{"api.example.com"},
		},
		{
			name:   "dollar variable",
			domain: "$K8S_API_DOMAIN",
			want:   []string{"my-cluster.eastus.azmk8s.io"},
		},
		{
			name:   "braced variable",
			domain: "${K8S_API_DOMAIN}",
			want:   []string{"my-cluster.eastus.azmk8s.io"},
		},
		{
			name:   "wildcard with variable",
			domain: "*.$K8S_API_DOMAIN",
			want:   []string{"*.my-cluster.eastus.azmk8s.io"},
		},
		{
			name:   "unset variable returns nil",
			domain: "$NONEXISTENT_VAR_12345",
			want:   nil,
		},
		{
			name:   "empty variable returns nil",
			domain: "$EMPTY_VAR",
			want:   nil,
		},
		{
			name:   "mixed literal and variable",
			domain: "prefix-${K8S_API_DOMAIN}-suffix",
			want:   []string{"prefix-my-cluster.eastus.azmk8s.io-suffix"},
		},
		{
			name:   "comma separated domains from variable",
			domain: "$MULTI_DOMAINS",
			want:   []string{"a.eks.amazonaws.com", "b.azmk8s.io"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure NONEXISTENT_VAR_12345 is truly unset in OS env
			os.Unsetenv("NONEXISTENT_VAR_12345") //nolint:errcheck
			got := expandEgressDomains(tt.domain, envVars)
			if len(got) != len(tt.want) {
				t.Fatalf("expandEgressDomains(%q) = %v (len %d), want %v (len %d)",
					tt.domain, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("expandEgressDomains(%q)[%d] = %q, want %q", tt.domain, i, got[i], tt.want[i])
				}
			}
		})
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
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}
