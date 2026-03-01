package forgeui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupTestServerWithCreate(t *testing.T) (*UIServer, string) {
	t.Helper()
	root := t.TempDir()

	// Create test agent
	agentDir := filepath.Join(root, "test-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `agent_id: test-agent
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

	mockCreate := func(opts AgentCreateOptions) (string, error) {
		dir := filepath.Join(root, opts.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		writeFile(t, filepath.Join(dir, "forge.yaml"), `agent_id: `+opts.Name+`
version: 0.1.0
framework: forge
model:
  provider: `+opts.ModelProvider+`
  name: default
`)
		return dir, nil
	}

	srv := NewUIServer(UIServerConfig{
		Port:       4200,
		WorkDir:    root,
		StartFunc:  mockStart,
		CreateFunc: mockCreate,
		AgentPort:  9100,
	})

	return srv, root
}

func TestHandleCreateAgent(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	body, _ := json.Marshal(AgentCreateOptions{
		Name:          "new-agent",
		ModelProvider: "openai",
		ModelName:     "gpt-4o",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp CreateAgentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp.AgentID != "new-agent" {
		t.Errorf("agent_id = %q, want %q", resp.AgentID, "new-agent")
	}
	if resp.Directory == "" {
		t.Error("directory should not be empty")
	}
}

func TestHandleCreateAgentMissingName(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	body, _ := json.Marshal(AgentCreateOptions{
		ModelProvider: "openai",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleCreateAgentNoFunc(t *testing.T) {
	root := t.TempDir()
	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		AgentPort: 9100,
		// No CreateFunc
	})

	body, _ := json.Marshal(AgentCreateOptions{
		Name:          "test",
		ModelProvider: "openai",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

func TestHandleGetConfig(t *testing.T) {
	srv, root := setupTestServerWithCreate(t)

	expectedContent := `agent_id: test-agent
version: 0.1.0
framework: forge
model:
  provider: openai
  name: gpt-4o
`
	// Overwrite to ensure exact content
	writeFile(t, filepath.Join(root, "test-agent", "forge.yaml"), expectedContent)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/config", nil)
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleGetConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != expectedContent {
		t.Errorf("config content mismatch:\ngot:  %q\nwant: %q", w.Body.String(), expectedContent)
	}
}

func TestHandleUpdateConfig(t *testing.T) {
	srv, root := setupTestServerWithCreate(t)

	newContent := `agent_id: test-agent
version: 0.2.0
framework: forge
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
`
	body, _ := json.Marshal(ConfigUpdateRequest{Content: newContent})

	req := httptest.NewRequest(http.MethodPut, "/api/agents/test-agent/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleUpdateConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ConfigValidateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if !resp.Valid {
		t.Errorf("expected valid config, got errors: %v", resp.Errors)
	}

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(root, "test-agent", "forge.yaml"))
	if err != nil {
		t.Fatalf("reading updated file: %v", err)
	}
	if string(data) != newContent {
		t.Errorf("file content mismatch:\ngot:  %q\nwant: %q", string(data), newContent)
	}
}

func TestHandleUpdateConfigInvalid(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	// Missing agent_id (invalid)
	invalidContent := `version: 0.1.0
framework: forge
`
	body, _ := json.Marshal(ConfigUpdateRequest{Content: invalidContent})

	req := httptest.NewRequest(http.MethodPut, "/api/agents/test-agent/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleUpdateConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp ConfigValidateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp.Valid {
		t.Error("expected invalid config")
	}
	if len(resp.Errors) == 0 {
		t.Error("expected errors for invalid config")
	}
}

func TestHandleValidateConfig(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	// Valid config
	validContent := `agent_id: test
version: 0.1.0
framework: forge
`
	body, _ := json.Marshal(ConfigUpdateRequest{Content: validContent})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/config/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleValidateConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp ConfigValidateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if !resp.Valid {
		t.Errorf("expected valid, got errors: %v", resp.Errors)
	}

	// Invalid config
	invalidContent := `not: valid: yaml: [}`
	body2, _ := json.Marshal(ConfigUpdateRequest{Content: invalidContent})

	req2 := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/config/validate", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.SetPathValue("id", "test-agent")
	w2 := httptest.NewRecorder()
	srv.handleValidateConfig(w2, req2)

	var resp2 ConfigValidateResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp2.Valid {
		t.Error("expected invalid config")
	}
}

func TestHandleListSkills(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	w := httptest.NewRecorder()
	srv.handleListSkills(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var skills []SkillBrowserEntry
	if err := json.NewDecoder(w.Body).Decode(&skills); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(skills) == 0 {
		t.Error("expected non-empty skills list")
	}

	// Verify sorted
	for i := 1; i < len(skills); i++ {
		if skills[i].Name < skills[i-1].Name {
			t.Errorf("skills not sorted: %q before %q", skills[i-1].Name, skills[i].Name)
			break
		}
	}
}

func TestHandleListBuiltinTools(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	w := httptest.NewRecorder()
	srv.handleListBuiltinTools(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var tools []BuiltinToolInfo
	if err := json.NewDecoder(w.Body).Decode(&tools); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(tools) == 0 {
		t.Error("expected non-empty tools list")
	}

	// Verify each tool has name and description
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("tool name should not be empty")
		}
		if tool.Description == "" {
			t.Errorf("tool %q should have a description", tool.Name)
		}
	}
}

func TestHandleGetWizardMeta(t *testing.T) {
	srv, _ := setupTestServerWithCreate(t)

	req := httptest.NewRequest(http.MethodGet, "/api/wizard/meta", nil)
	w := httptest.NewRecorder()
	srv.handleGetWizardMeta(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var meta WizardMetadata
	if err := json.NewDecoder(w.Body).Decode(&meta); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(meta.Providers) == 0 {
		t.Error("expected non-empty providers list")
	}
	if len(meta.Frameworks) == 0 {
		t.Error("expected non-empty frameworks list")
	}
	if len(meta.BuiltinTools) == 0 {
		t.Error("expected non-empty builtin_tools list")
	}
	if len(meta.Skills) == 0 {
		t.Error("expected non-empty skills list")
	}
	if len(meta.Channels) == 0 {
		t.Error("expected non-empty channels list")
	}
	if len(meta.ProviderModels) == 0 {
		t.Error("expected non-empty provider_models map")
	}
	// Verify OpenAI has OAuth models
	if oai, ok := meta.ProviderModels["openai"]; ok {
		if !oai.HasOAuth {
			t.Error("expected openai to have OAuth support")
		}
		if len(oai.OAuth) == 0 {
			t.Error("expected openai OAuth models")
		}
		if len(oai.APIKey) == 0 {
			t.Error("expected openai API key models")
		}
	} else {
		t.Error("expected openai in provider_models")
	}
	if len(meta.WebSearchProviders) == 0 {
		t.Error("expected non-empty web_search_providers list")
	}
}
