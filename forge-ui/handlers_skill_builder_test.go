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

// isolateHome relocates os.UserHomeDir() to a temp directory for the
// duration of the test. Required because uiconfig.LoadSkillBuilderLLM's
// tier-2 fallback resolves the user config via os.UserHomeDir; without
// isolation, a real ~/.forge/ui.yaml on the dev machine would change
// what these tests observe.
func isolateHome(t *testing.T) {
	t.Helper()
	fake := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", fake); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("HOME", origHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	})
}

func setupTestServerWithSkillBuilder(t *testing.T) (*UIServer, string) {
	t.Helper()
	root := t.TempDir()

	// Isolate HOME so uiconfig's tier-2 user fallback can't accidentally
	// pick up the dev machine's ~/.forge/ui.yaml during tests.
	isolateHome(t)

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

	mockStream := func(ctx context.Context, opts LLMStreamOptions) error {
		response := "Here is your skill:\n```skill.md\n---\nname: test-skill\ndescription: A test skill\n---\n\n# Test Skill\n\n## Tool: test_tool\n\nA test tool.\n```\n"
		for _, ch := range response {
			opts.OnChunk(string(ch))
		}
		opts.OnDone(response)
		return nil
	}

	mockSave := func(opts SkillSaveOptions) (*SkillSaveResult, error) {
		skillDir := filepath.Join(opts.AgentDir, "skills", opts.SkillName)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(opts.SkillMD), 0o644); err != nil {
			return nil, err
		}
		return &SkillSaveResult{
			Path: "skills/" + opts.SkillName + "/SKILL.md",
		}, nil
	}

	srv := NewUIServer(UIServerConfig{
		Port:          4200,
		WorkDir:       root,
		ExePath:       "/usr/bin/false",
		LLMStreamFunc: mockStream,
		SkillSaveFunc: mockSave,
		AgentPort:     9100,
	})

	return srv, root
}

func TestSkillBuilderProvider(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/skill-builder/provider", nil)
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderProvider(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp["provider"] != "openai" {
		t.Errorf("provider = %q, want %q", resp["provider"], "openai")
	}
	// Per issue #92: no hardcoded codegen upgrade. The agent-fallback path
	// returns the operator's configured model verbatim. (Pre-#92 this was
	// gpt-4.1 regardless of agent config.)
	if model, _ := resp["model"].(string); model != "gpt-4o" {
		t.Errorf("model = %q, want %q (no codegen upgrade)", model, "gpt-4o")
	}
	// Falling through to agent fallback should surface the deprecation
	// warning so the UI can prompt the operator to configure workspace
	// settings.
	if source, _ := resp["source"].(string); source != "agent_fallback" {
		t.Errorf("source = %q, want agent_fallback", source)
	}
	if warning, _ := resp["warning"].(string); warning == "" {
		t.Errorf("agent_fallback path should emit a deprecation warning")
	}
}

// TestSkillBuilderProvider_AgentFallback_PreservesConfiguredModel pins the
// post-#92 behavior: the skill builder reports the operator-configured
// model verbatim — no SkillBuilderCodegenModel upgrade to claude-opus-4-6
// (or gpt-4.1 for openai). Pre-#92 the agent's configured model was
// overridden for the skill builder's LLM call, which broke any agent
// pointed at a custom OpenAI-compatible endpoint that didn't host the
// hardcoded "stronger" model.
func TestSkillBuilderProvider_AgentFallback_PreservesConfiguredModel(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()

	agentDir := filepath.Join(root, "anthropic-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `agent_id: anthropic-agent
version: 0.1.0
framework: forge
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
`)

	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		AgentPort: 9100,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/agents/anthropic-agent/skill-builder/provider", nil)
	req.SetPathValue("id", "anthropic-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderProvider(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp["provider"] != "anthropic" {
		t.Errorf("provider = %q, want %q", resp["provider"], "anthropic")
	}
	if resp["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q (no codegen upgrade)", resp["model"], "claude-sonnet-4-20250514")
	}
}

func TestSkillBuilderContext(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/test-agent/skill-builder/context", nil)
	w := httptest.NewRecorder()
	srv.handleSkillBuilderContext(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp["system_prompt"] == "" {
		t.Error("expected non-empty system_prompt")
	}
}

func TestSkillBuilderChatNoFunc(t *testing.T) {
	root := t.TempDir()

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

	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		AgentPort: 9100,
		// No LLMStreamFunc
	})

	body, _ := json.Marshal(SkillBuilderChatRequest{
		Messages: []SkillBuilderMessage{{Role: "user", Content: "hello"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderChat(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

func TestSkillBuilderChatMissingAgent(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	body, _ := json.Marshal(SkillBuilderChatRequest{
		Messages: []SkillBuilderMessage{{Role: "user", Content: "hello"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/nonexistent/skill-builder/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderChat(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestSkillBuilderValidateValid(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	validSkill := `---
name: test-skill
description: A test skill
---

# Test Skill

## Tool: test_tool

A test tool.
`
	body, _ := json.Marshal(SkillBuilderValidateRequest{SkillMD: validSkill})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderValidate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result SkillValidationResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
}

func TestSkillBuilderValidateMissingName(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	invalidSkill := `---
description: A test skill
---

# Test Skill
`
	body, _ := json.Marshal(SkillBuilderValidateRequest{SkillMD: invalidSkill})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderValidate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var result SkillValidationResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid result for missing name")
	}

	hasNameError := false
	for _, e := range result.Errors {
		if e.Field == "name" {
			hasNameError = true
			break
		}
	}
	if !hasNameError {
		t.Error("expected error for field 'name'")
	}
}

func TestSkillBuilderValidateInvalidYAML(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	invalidSkill := `---
name: [invalid yaml
---
`
	body, _ := json.Marshal(SkillBuilderValidateRequest{SkillMD: invalidSkill})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderValidate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var result SkillValidationResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid result for bad YAML")
	}
}

func TestSkillBuilderSaveSuccess(t *testing.T) {
	srv, root := setupTestServerWithSkillBuilder(t)

	validSkill := `---
name: new-skill
description: A new skill
---

# New Skill

## Tool: new_tool

A new tool.
`
	body, _ := json.Marshal(SkillBuilderSaveRequest{
		SkillName: "new-skill",
		SkillMD:   validSkill,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/save", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderSave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp SkillSaveResult
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp.Path != "skills/new-skill/SKILL.md" {
		t.Errorf("path = %q, want %q", resp.Path, "skills/new-skill/SKILL.md")
	}

	// Verify file was created
	skillPath := filepath.Join(root, "test-agent", "skills", "new-skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("reading saved skill: %v", err)
	}
	if string(data) != validSkill {
		t.Errorf("saved content mismatch:\ngot:  %q\nwant: %q", string(data), validSkill)
	}
}

func TestSkillBuilderSaveNoFunc(t *testing.T) {
	root := t.TempDir()

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

	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   root,
		AgentPort: 9100,
		// No SkillSaveFunc
	})

	body, _ := json.Marshal(SkillBuilderSaveRequest{
		SkillName: "test",
		SkillMD:   "---\nname: test\ndescription: test\n---\n# Test\n## Tool: t\nA tool.\n",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/save", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderSave(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

func TestSkillBuilderSaveValidationFirst(t *testing.T) {
	srv, _ := setupTestServerWithSkillBuilder(t)

	// Invalid content (no name)
	body, _ := json.Marshal(SkillBuilderSaveRequest{
		SkillName: "bad-skill",
		SkillMD:   "---\ndescription: test\n---\n",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/skill-builder/save", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-agent")
	w := httptest.NewRecorder()
	srv.handleSkillBuilderSave(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
