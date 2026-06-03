package forgeui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServerForSettings(t *testing.T) *UIServer {
	t.Helper()
	isolateHome(t)
	root := t.TempDir()
	return NewUIServer(UIServerConfig{
		Port:    4200,
		WorkDir: root,
	})
}

func TestGetSkillBuilderSettings_Unset(t *testing.T) {
	srv := newTestServerForSettings(t)

	req := httptest.NewRequest(http.MethodGet, "/api/settings/skill-builder", nil)
	w := httptest.NewRecorder()
	srv.handleGetSkillBuilderSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["source"] != "unset" {
		t.Errorf("source = %q, want unset", resp["source"])
	}
	if resp["has_key"] != false {
		t.Errorf("has_key = %v, want false", resp["has_key"])
	}
	providers, _ := resp["providers"].([]any)
	if len(providers) == 0 {
		t.Errorf("providers list should be populated")
	}
}

func TestPutSkillBuilderSettings_PersistsAndEchoes(t *testing.T) {
	srv := newTestServerForSettings(t)

	// Operator submits a workspace-level config.
	body := map[string]string{
		"provider":    "openai",
		"model":       "gpt-4.1",
		"base_url":    "https://openrouter-ish.example.com/v1",
		"api_key_env": "WORKSPACE_LLM_KEY",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k, want := range map[string]string{
		"provider":    "openai",
		"model":       "gpt-4.1",
		"base_url":    "https://openrouter-ish.example.com/v1",
		"api_key_env": "WORKSPACE_LLM_KEY",
		"source":      "workspace",
	} {
		if got, _ := resp[k].(string); got != want {
			t.Errorf("response[%q] = %q, want %q", k, got, want)
		}
	}

	// File on disk reflects the same shape.
	path := filepath.Join(srv.cfg.WorkDir, ".forge", "ui.yaml")
	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ui.yaml: %v", err)
	}
	for _, want := range []string{
		"provider: openai",
		"model: gpt-4.1",
		"base_url: https://openrouter-ish.example.com/v1",
		"api_key_env: WORKSPACE_LLM_KEY",
	} {
		if !strings.Contains(string(raw2), want) {
			t.Errorf("ui.yaml missing %q:\n%s", want, raw2)
		}
	}
}

func TestPutSkillBuilderSettings_RejectsInvalidProvider(t *testing.T) {
	srv := newTestServerForSettings(t)

	body := `{"provider":"bogus","model":"x"}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown provider") {
		t.Errorf("error body should mention unknown provider, got: %s", w.Body.String())
	}
}

func TestPutSkillBuilderSettings_RejectsMissingModel(t *testing.T) {
	srv := newTestServerForSettings(t)

	body := `{"provider":"openai"}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "model is required") {
		t.Errorf("expected model-required error, got: %s", w.Body.String())
	}
}

func TestPutSkillBuilderSettings_PersistsAPIKeyToEnvFile(t *testing.T) {
	srv := newTestServerForSettings(t)

	body := `{"provider":"openai","model":"gpt-4.1","api_key":"sk-from-modal"}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if has, _ := resp["has_key"].(bool); !has {
		t.Errorf("has_key should be true after persisting key, got %+v", resp)
	}

	// .env file written under .forge/ with mode 0600 and the right key.
	envPath := filepath.Join(srv.cfg.WorkDir, ".forge", ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(raw), "OPENAI_API_KEY=sk-from-modal") {
		t.Errorf("env file missing key:\n%s", raw)
	}
	if info, _ := os.Stat(envPath); info.Mode().Perm() != 0o600 {
		t.Errorf("env file perm = %o, want 0600", info.Mode().Perm())
	}

	// .gitignore for .forge/ auto-created.
	giPath := filepath.Join(srv.cfg.WorkDir, ".forge", ".gitignore")
	gi, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(gi), ".env") {
		t.Errorf(".gitignore should protect .env:\n%s", gi)
	}

	// The api_key value MUST NOT leak into ui.yaml.
	yamlPath := filepath.Join(srv.cfg.WorkDir, ".forge", "ui.yaml")
	yamlRaw, _ := os.ReadFile(yamlPath)
	if strings.Contains(string(yamlRaw), "sk-from-modal") {
		t.Errorf("API key leaked into ui.yaml — must be .env only:\n%s", yamlRaw)
	}
}

func TestPutSkillBuilderSettings_APIKeyUsesCustomEnvVarName(t *testing.T) {
	srv := newTestServerForSettings(t)

	body := `{"provider":"openai","model":"gpt-4.1","api_key_env":"WORKSPACE_LLM_KEY","api_key":"sk-custom"}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	envPath := filepath.Join(srv.cfg.WorkDir, ".forge", ".env")
	raw, _ := os.ReadFile(envPath)
	if !strings.Contains(string(raw), "WORKSPACE_LLM_KEY=sk-custom") {
		t.Errorf("expected key under custom env name, got:\n%s", raw)
	}
	if strings.Contains(string(raw), "OPENAI_API_KEY=") {
		t.Errorf("default OPENAI_API_KEY should NOT be written when api_key_env is set:\n%s", raw)
	}
}

func TestPutSkillBuilderSettings_OmitAPIKeyLeavesEnvFileUntouched(t *testing.T) {
	srv := newTestServerForSettings(t)

	// First write a key.
	_ = srv // satisfy linter
	body := `{"provider":"openai","model":"gpt-4.1","api_key":"sk-original"}`
	w := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("first PUT failed: %d %s", w.Code, w.Body.String())
	}

	// Second PUT updates the model but omits api_key. The .env file
	// should keep the original key.
	body2 := `{"provider":"openai","model":"gpt-4.1-mini"}`
	w2 := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(w2, httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(body2)))
	if w2.Code != http.StatusOK {
		t.Fatalf("second PUT failed: %d %s", w2.Code, w2.Body.String())
	}

	raw, _ := os.ReadFile(filepath.Join(srv.cfg.WorkDir, ".forge", ".env"))
	if !strings.Contains(string(raw), "OPENAI_API_KEY=sk-original") {
		t.Errorf("omit-api_key second PUT should preserve existing key, got:\n%s", raw)
	}
}

func TestGetSkillBuilderSettings_AfterPut_ReturnsWorkspaceSource(t *testing.T) {
	srv := newTestServerForSettings(t)

	put := `{"provider":"anthropic","model":"claude-sonnet-4"}`
	wp := httptest.NewRecorder()
	srv.handlePutSkillBuilderSettings(wp, httptest.NewRequest(http.MethodPut, "/api/settings/skill-builder", strings.NewReader(put)))
	if wp.Code != http.StatusOK {
		t.Fatalf("PUT failed: %d %s", wp.Code, wp.Body.String())
	}

	wg := httptest.NewRecorder()
	srv.handleGetSkillBuilderSettings(wg, httptest.NewRequest(http.MethodGet, "/api/settings/skill-builder", nil))
	if wg.Code != http.StatusOK {
		t.Fatalf("GET failed: %d %s", wg.Code, wg.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(wg.Body).Decode(&resp)
	if resp["source"] != "workspace" {
		t.Errorf("source = %q, want workspace", resp["source"])
	}
	if resp["provider"] != "anthropic" || resp["model"] != "claude-sonnet-4" {
		t.Errorf("GET response did not reflect PUT: %+v", resp)
	}
}
