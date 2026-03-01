package forgeui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupTestServer(t *testing.T) (*UIServer, string) {
	t.Helper()
	root := t.TempDir()

	// Create test agent
	agentDir := filepath.Join(root, "test-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `
agent_id: test-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`)

	mockStart := func(ctx context.Context, agentDir string, port int) error {
		<-ctx.Done()
		return nil
	}

	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		StartFunc: mockStart,
		AgentPort: 9100,
	})

	return srv, root
}

func TestHandleListAgents(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()
	srv.handleListAgents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var agents []*AgentInfo
	if err := json.NewDecoder(w.Body).Decode(&agents); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	if agents[0].ID != "test-agent" {
		t.Errorf("agent id = %q, want %q", agents[0].ID, "test-agent")
	}
}

func TestHandleGetAgent(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent", nil)
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleGetAgent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var agent AgentInfo
	if err := json.NewDecoder(w.Body).Decode(&agent); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if agent.ID != "test-agent" {
		t.Errorf("agent id = %q, want %q", agent.ID, "test-agent")
	}
}

func TestHandleGetAgentNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleGetAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleHealth(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

func TestHandleRescan(t *testing.T) {
	srv, root := setupTestServer(t)

	// Add a second agent
	agentDir := filepath.Join(root, "agent-two")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `
agent_id: agent-two
version: 0.1.0
framework: forge
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
`)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/rescan", nil)
	w := httptest.NewRecorder()
	srv.handleRescan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var agents []*AgentInfo
	if err := json.NewDecoder(w.Body).Decode(&agents); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(agents) != 2 {
		t.Fatalf("expected 2 agents after rescan, got %d", len(agents))
	}
}
