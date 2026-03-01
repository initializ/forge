package forgeui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestServer creates a UIServer with a temp workspace for testing.
func newTestServer(t *testing.T) (*UIServer, string) {
	t.Helper()
	dir := t.TempDir()

	broker := NewSSEBroker()
	scanner := NewScanner(dir)
	pm := NewProcessManager(nil, broker, 9100)

	s := &UIServer{
		cfg:     UIServerConfig{WorkDir: dir},
		scanner: scanner,
		pm:      pm,
		broker:  broker,
	}
	return s, dir
}

// createTestAgent creates a minimal agent directory with forge.yaml.
func createTestAgent(t *testing.T, dir, agentID string) string {
	t.Helper()
	agentDir := filepath.Join(dir, agentID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`agent_id: %s
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`, agentID)
	if err := os.WriteFile(filepath.Join(agentDir, "forge.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return agentDir
}

// createTestSession creates a session JSON file in the agent's sessions dir.
func createTestSession(t *testing.T, agentDir, taskID string, createdAt, updatedAt time.Time) {
	t.Helper()
	sessDir := filepath.Join(agentDir, ".forge", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	data := map[string]any{
		"task_id": taskID,
		"messages": []map[string]any{
			{"role": "user", "content": "Hello " + taskID},
			{"role": "assistant", "content": "Hi there!"},
		},
		"created_at": createdAt.Format(time.RFC3339Nano),
		"updated_at": updatedAt.Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	fname := filepath.Join(sessDir, sanitizeForFilename(taskID)+".json")
	if err := os.WriteFile(fname, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHandleChatAgentNotRunning(t *testing.T) {
	s, dir := newTestServer(t)
	createTestAgent(t, dir, "test-agent")

	body := `{"message":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/chat", strings.NewReader(body))
	req.SetPathValue("id", "test-agent")
	rec := httptest.NewRecorder()

	s.handleChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "agent is not running" {
		t.Errorf("expected 'agent is not running', got %q", resp["error"])
	}
}

func TestHandleListSessions(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "test-agent")

	now := time.Now().UTC()
	createTestSession(t, agentDir, "session-old", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	createTestSession(t, agentDir, "session-new", now.Add(-1*time.Hour), now)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/sessions", nil)
	req.SetPathValue("id", "test-agent")
	rec := httptest.NewRecorder()

	s.handleListSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var sessions []SessionInfo
	if err := json.NewDecoder(rec.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Should be sorted newest first.
	if sessions[0].ID != "session-new" {
		t.Errorf("expected newest session first, got %q", sessions[0].ID)
	}
	if sessions[1].ID != "session-old" {
		t.Errorf("expected oldest session second, got %q", sessions[1].ID)
	}

	// Check preview extraction.
	if sessions[0].Preview != "Hello session-new" {
		t.Errorf("expected preview 'Hello session-new', got %q", sessions[0].Preview)
	}
}

func TestHandleListSessionsEmpty(t *testing.T) {
	s, dir := newTestServer(t)
	createTestAgent(t, dir, "test-agent")

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/sessions", nil)
	req.SetPathValue("id", "test-agent")
	rec := httptest.NewRecorder()

	s.handleListSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var sessions []SessionInfo
	if err := json.NewDecoder(rec.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}

	if len(sessions) != 0 {
		t.Errorf("expected empty sessions list, got %d", len(sessions))
	}
}

func TestHandleGetSession(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "test-agent")

	now := time.Now().UTC()
	createTestSession(t, agentDir, "my-session", now, now)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/sessions/my-session", nil)
	req.SetPathValue("id", "test-agent")
	req.SetPathValue("sid", "my-session")
	rec := httptest.NewRecorder()

	s.handleGetSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data["task_id"] != "my-session" {
		t.Errorf("expected task_id 'my-session', got %v", data["task_id"])
	}
}

func TestHandleGetSessionNotFound(t *testing.T) {
	s, dir := newTestServer(t)
	createTestAgent(t, dir, "test-agent")

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/sessions/nonexistent", nil)
	req.SetPathValue("id", "test-agent")
	req.SetPathValue("sid", "nonexistent")
	rec := httptest.NewRecorder()

	s.handleGetSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleChatProxy(t *testing.T) {
	// Start a mock A2A agent that returns SSE events.
	mockAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify JSON-RPC request
		var rpcReq map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if rpcReq["method"] != "tasks/sendSubscribe" {
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}

		// Respond with SSE
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send status event
		statusData, _ := json.Marshal(map[string]any{
			"id": "test-session",
			"status": map[string]any{
				"state": "working",
			},
		})
		_, _ = fmt.Fprintf(w, "event: status\ndata: %s\n\n", statusData)
		flusher.Flush()

		// Send progress event
		progressData, _ := json.Marshal(map[string]any{
			"id": "test-session",
			"status": map[string]any{
				"state": "working",
				"message": map[string]any{
					"role": "agent",
					"parts": []map[string]any{
						{"kind": "data", "data": map[string]any{"name": "web_search", "phase": "start"}},
					},
				},
			},
		})
		_, _ = fmt.Fprintf(w, "event: progress\ndata: %s\n\n", progressData)
		flusher.Flush()

		// Send result event
		resultData, _ := json.Marshal(map[string]any{
			"id": "test-session",
			"status": map[string]any{
				"state": "completed",
				"message": map[string]any{
					"role": "agent",
					"parts": []map[string]any{
						{"kind": "text", "text": "Here is the answer."},
					},
				},
			},
		})
		_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", resultData)
		flusher.Flush()
	}))
	defer mockAgent.Close()

	// Extract the port from the mock server URL.
	// The mock server URL is like http://127.0.0.1:PORT
	urlParts := strings.Split(mockAgent.URL, ":")
	portStr := urlParts[len(urlParts)-1]
	mockPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("failed to parse mock server port: %v", err)
	}

	// Create server with a process manager that knows about the mock agent.
	dir := t.TempDir()
	broker := NewSSEBroker()
	scanner := NewScanner(dir)
	pm := NewProcessManager(nil, broker, 9100)

	// Manually inject the mock agent into process manager.
	pm.mu.Lock()
	pm.agents["mock-agent"] = &managedAgent{
		cancel: func() {},
		port:   mockPort,
	}
	pm.states["mock-agent"] = &AgentInfo{
		ID:     "mock-agent",
		Status: StateRunning,
		Port:   mockPort,
	}
	pm.mu.Unlock()

	s := &UIServer{
		cfg:     UIServerConfig{WorkDir: dir},
		scanner: scanner,
		pm:      pm,
		broker:  broker,
	}

	body := `{"message":"test question","session_id":"test-session"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agents/mock-agent/chat", strings.NewReader(body))
	req.SetPathValue("id", "mock-agent")
	rec := httptest.NewRecorder()

	s.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify SSE content type.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream, got %q", ct)
	}

	// Parse SSE events from response.
	var events []struct {
		eventType string
		data      string
	}

	scanner2 := bufio.NewScanner(rec.Body)
	var currentEvent, currentData string
	for scanner2.Scan() {
		line := scanner2.Text()
		if after, found := strings.CutPrefix(line, "event: "); found {
			currentEvent = after
		} else if after, found := strings.CutPrefix(line, "data: "); found {
			currentData = after
		} else if line == "" && currentEvent != "" {
			events = append(events, struct {
				eventType string
				data      string
			}{currentEvent, currentData})
			currentEvent = ""
			currentData = ""
		}
	}

	// We expect: status, progress, result, done
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events (status, progress, result), got %d", len(events))
	}

	// Verify event types
	expectedTypes := []string{"status", "progress", "result"}
	for i, expected := range expectedTypes {
		if i >= len(events) {
			break
		}
		if events[i].eventType != expected {
			t.Errorf("event %d: expected type %q, got %q", i, expected, events[i].eventType)
		}
	}

	// Verify done event has session_id
	lastEvent := events[len(events)-1]
	if lastEvent.eventType != "done" {
		t.Errorf("last event should be 'done', got %q", lastEvent.eventType)
	}
	var doneData map[string]string
	if err := json.Unmarshal([]byte(lastEvent.data), &doneData); err == nil {
		if doneData["session_id"] != "test-session" {
			t.Errorf("expected session_id 'test-session', got %q", doneData["session_id"])
		}
	}
}

func TestSanitizeForFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"hello-world", "hello-world"},
		{"with spaces", "with_spaces"},
		{"with/slashes", "with_slashes"},
		{"special!@#$%", "special_____"},
		{"dots.and-dashes_ok", "dots.and-dashes_ok"},
	}

	for _, tc := range tests {
		got := sanitizeForFilename(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeForFilename(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestExtractPreview(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Hello there"},
		{"role": "assistant", "content": "Hi!"},
	}
	raw, _ := json.Marshal(messages)

	preview := extractPreview(raw)
	if preview != "Hello there" {
		t.Errorf("expected 'Hello there', got %q", preview)
	}
}

func TestExtractPreviewLong(t *testing.T) {
	longText := strings.Repeat("a", 150)
	messages := []map[string]any{
		{"role": "user", "content": longText},
	}
	raw, _ := json.Marshal(messages)

	preview := extractPreview(raw)
	if len(preview) != 103 { // 100 + "..."
		t.Errorf("expected truncated preview (103 chars), got %d chars", len(preview))
	}
	if !strings.HasSuffix(preview, "...") {
		t.Error("expected preview to end with '...'")
	}
}

func TestHandleStartAgentPassphraseRequired(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "secret-agent")

	// Add encrypted-file to secrets providers in forge.yaml.
	config := `agent_id: secret-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
secrets:
  providers:
    - encrypted-file
`
	if err := os.WriteFile(filepath.Join(agentDir, "forge.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a dummy secrets.enc file so needsPassphrase returns true.
	secretsDir := filepath.Join(agentDir, ".forge")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "secrets.enc"), []byte("encrypted-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Clear FORGE_PASSPHRASE to simulate no passphrase.
	t.Setenv("FORGE_PASSPHRASE", "")
	_ = os.Unsetenv("FORGE_PASSPHRASE")

	// Start without passphrase — should get 400.
	req := httptest.NewRequest(http.MethodPost, "/api/agents/secret-agent/start", nil)
	req.SetPathValue("id", "secret-agent")
	rec := httptest.NewRecorder()

	s.handleStartAgent(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "passphrase required for encrypted secrets" {
		t.Errorf("expected passphrase required error, got %q", resp["error"])
	}
}

func TestHandleStartAgentWithPassphrase(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "secret-agent")

	// Add encrypted-file to secrets providers.
	config := `agent_id: secret-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
secrets:
  providers:
    - encrypted-file
`
	if err := os.WriteFile(filepath.Join(agentDir, "forge.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	secretsDir := filepath.Join(agentDir, ".forge")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "secrets.enc"), []byte("encrypted-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Clear FORGE_PASSPHRASE.
	t.Setenv("FORGE_PASSPHRASE", "")
	_ = os.Unsetenv("FORGE_PASSPHRASE")

	// Provide a mock startFunc that just blocks until cancelled.
	s.pm.startFunc = func(ctx context.Context, agentDir string, port int) error {
		<-ctx.Done()
		return nil
	}

	// Start with passphrase in body — should succeed.
	body := `{"passphrase":"my-secret"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agents/secret-agent/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "secret-agent")
	rec := httptest.NewRecorder()

	s.handleStartAgent(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify FORGE_PASSPHRASE was set.
	if got := os.Getenv("FORGE_PASSPHRASE"); got != "my-secret" {
		t.Errorf("expected FORGE_PASSPHRASE='my-secret', got %q", got)
	}

	// Cleanup: stop the agent.
	_ = s.pm.Stop("secret-agent")
}

func TestNeedsPassphraseDetection(t *testing.T) {
	dir := t.TempDir()

	// Agent without encrypted-file provider — should never need passphrase.
	agentDir := filepath.Join(dir, "agent-plain")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plainConfig := `agent_id: agent-plain
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`
	if err := os.WriteFile(filepath.Join(agentDir, "forge.yaml"), []byte(plainConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	scanner := NewScanner(dir)
	agents, err := scanner.Scan()
	if err != nil {
		t.Fatal(err)
	}
	agent := agents["agent-plain"]
	if agent == nil {
		t.Fatal("agent not found")
	}
	if agent.NeedsPassphrase {
		t.Error("expected NeedsPassphrase=false without encrypted-file provider")
	}

	// Agent with encrypted-file provider and local secrets.enc — should need passphrase.
	encDir := filepath.Join(dir, "agent-enc")
	if err := os.MkdirAll(encDir, 0o755); err != nil {
		t.Fatal(err)
	}
	encConfig := `agent_id: agent-enc
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
secrets:
  providers:
    - encrypted-file
`
	if err := os.WriteFile(filepath.Join(encDir, "forge.yaml"), []byte(encConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	secretsDir := filepath.Join(encDir, ".forge")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "secrets.enc"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	agents, err = scanner.Scan()
	if err != nil {
		t.Fatal(err)
	}
	encAgent := agents["agent-enc"]
	if encAgent == nil {
		t.Fatal("agent-enc not found")
	}
	if !encAgent.NeedsPassphrase {
		t.Error("expected NeedsPassphrase=true with local secrets.enc")
	}
}

func TestGetPort(t *testing.T) {
	broker := NewSSEBroker()
	pm := NewProcessManager(nil, broker, 9100)

	// Not running — should return false.
	_, ok := pm.GetPort("nonexistent")
	if ok {
		t.Error("expected GetPort to return false for nonexistent agent")
	}

	// Manually add a managed agent.
	pm.mu.Lock()
	pm.agents["test"] = &managedAgent{cancel: func() {}, port: 9200}
	pm.mu.Unlock()

	port, ok := pm.GetPort("test")
	if !ok {
		t.Error("expected GetPort to return true for existing agent")
	}
	if port != 9200 {
		t.Errorf("expected port 9200, got %d", port)
	}
}
