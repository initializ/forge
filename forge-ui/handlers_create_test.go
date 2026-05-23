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
		ExePath:    "/usr/bin/false",
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

	// PR6: auth_provider_types is server-driven so the frontend doesn't
	// hardcode the list. Phase 2 adds aws_sigv4, gcp_iap, azure_ad — the
	// founding four still appear.
	if len(meta.AuthProviderTypes) != 7 {
		t.Errorf("auth_provider_types len = %d, want 7", len(meta.AuthProviderTypes))
	}
	wantTypes := map[string]bool{
		"none": false, "oidc": false, "http_verifier": false, "custom": false,
		"aws_sigv4": false, "gcp_iap": false, "azure_ad": false,
	}
	for _, a := range meta.AuthProviderTypes {
		if _, ok := wantTypes[a.Type]; !ok {
			t.Errorf("unexpected auth type %q", a.Type)
			continue
		}
		wantTypes[a.Type] = true
		if a.Label == "" || a.Description == "" {
			t.Errorf("auth type %q missing label/description", a.Type)
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("missing required auth type %q", typ)
		}
	}
}

// setupCreateWithCapture returns a UIServer whose CreateFunc records the
// last AgentCreateOptions it received. Used to assert that opts.Auth
// round-trips through the JSON boundary.
func setupCreateWithCapture(t *testing.T) (*UIServer, *AgentCreateOptions) {
	t.Helper()
	root := t.TempDir()
	captured := &AgentCreateOptions{}
	mockCreate := func(opts AgentCreateOptions) (string, error) {
		*captured = opts
		return filepath.Join(root, opts.Name), nil
	}
	srv := NewUIServer(UIServerConfig{
		Port:       4200,
		WorkDir:    root,
		ExePath:    "/usr/bin/false",
		CreateFunc: mockCreate,
		AgentPort:  9100,
	})
	return srv, captured
}

func TestHandleCreateAgent_WithAuthPayload(t *testing.T) {
	srv, captured := setupCreateWithCapture(t)

	body := []byte(`{
		"name": "auth-test-agent",
		"model_provider": "openai",
		"auth": {
			"mode": "oidc",
			"settings": {
				"issuer": "https://login.example.com",
				"audience": "api://forge"
			}
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
	if captured.Auth == nil {
		t.Fatal("captured Auth is nil — opts.Auth did not round-trip")
	}
	if captured.Auth.Mode != "oidc" {
		t.Errorf("Auth.Mode = %q, want oidc", captured.Auth.Mode)
	}
	if captured.Auth.Settings["issuer"] != "https://login.example.com" {
		t.Errorf("issuer = %v, want https://login.example.com", captured.Auth.Settings["issuer"])
	}
	if captured.Auth.Settings["audience"] != "api://forge" {
		t.Errorf("audience = %v, want api://forge", captured.Auth.Settings["audience"])
	}
}

// --- Server-side auth payload validation (review #9) ---
//
// Without this, the wizard could write malformed auth: blocks to disk
// (e.g., oidc without issuer/audience) — creation succeeds, `forge run`
// fails later, user confused.

func TestHandleCreateAgent_OIDCMissingIssuerRejected(t *testing.T) {
	srv, captured := setupCreateWithCapture(t)

	// Audience set but no issuer — should fail validation before
	// CreateFunc is invoked.
	body := []byte(`{
		"name": "bad-oidc",
		"model_provider": "openai",
		"auth": {
			"mode": "oidc",
			"settings": { "audience": "api://forge" }
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	// CreateFunc must NOT have been called — the agent directory should
	// never exist on disk for malformed auth.
	if captured.Name != "" {
		t.Errorf("CreateFunc was called despite validation failure (captured: %+v)", captured)
	}
	if !strings.Contains(w.Body.String(), "issuer is required") {
		t.Errorf("error body should name the missing field, got: %s", w.Body.String())
	}
}

func TestHandleCreateAgent_OIDCMissingAudienceRejected(t *testing.T) {
	srv, captured := setupCreateWithCapture(t)

	body := []byte(`{
		"name": "bad-oidc-2",
		"model_provider": "openai",
		"auth": {
			"mode": "oidc",
			"settings": { "issuer": "https://login.example.com" }
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if captured.Name != "" {
		t.Errorf("CreateFunc called despite validation failure")
	}
	if !strings.Contains(w.Body.String(), "audience is required") {
		t.Errorf("error body should name missing audience, got: %s", w.Body.String())
	}
}

func TestHandleCreateAgent_OIDCBothFieldsAccepted(t *testing.T) {
	// Regression: valid oidc payload still works end-to-end.
	srv, captured := setupCreateWithCapture(t)
	body := []byte(`{
		"name": "good-oidc",
		"model_provider": "openai",
		"auth": {
			"mode": "oidc",
			"settings": {
				"issuer": "https://login.example.com",
				"audience": "api://forge"
			}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	if captured.Auth == nil || captured.Auth.Mode != "oidc" {
		t.Errorf("expected captured Auth.Mode = oidc, got %+v", captured.Auth)
	}
}

func TestHandleCreateAgent_HTTPVerifierMissingURLRejected(t *testing.T) {
	srv, _ := setupCreateWithCapture(t)
	body := []byte(`{
		"name": "bad-http",
		"model_provider": "openai",
		"auth": { "mode": "http_verifier", "settings": {} }
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "url is required") {
		t.Errorf("error body should name missing url, got: %s", w.Body.String())
	}
}

func TestHandleCreateAgent_UnknownAuthModeRejected(t *testing.T) {
	srv, _ := setupCreateWithCapture(t)
	body := []byte(`{
		"name": "weird",
		"model_provider": "openai",
		"auth": { "mode": "ldap", "settings": {} }
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unknown type") {
		t.Errorf("error should name unknown-type, got: %s", w.Body.String())
	}
}

func TestHandleCreateAgent_NoneAndCustomSkipValidation(t *testing.T) {
	// mode "none" and "custom" don't produce providers — there's
	// nothing to validate. They must round-trip cleanly.
	for _, mode := range []string{"none", "custom"} {
		t.Run(mode, func(t *testing.T) {
			srv, captured := setupCreateWithCapture(t)
			body := []byte(`{
				"name": "agent-` + mode + `",
				"model_provider": "openai",
				"auth": { "mode": "` + mode + `", "settings": {} }
			}`)
			req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
			w := httptest.NewRecorder()
			srv.handleCreateAgent(w, req)
			if w.Code != http.StatusCreated {
				t.Errorf("mode %q: status = %d, want 201", mode, w.Code)
			}
			if captured.Auth == nil || captured.Auth.Mode != mode {
				t.Errorf("Auth.Mode = %v, want %q", captured.Auth, mode)
			}
		})
	}
}

// TestHandleCreateAgent_NestedClaimMapShape pins the contract the
// frontend's buildAuthPayload promises (app.js): a flat `groups_claim`
// from the wizard is translated client-side into the nested shape
// `settings.claim_map.groups`. The backend doesn't run that
// translation — but if the frontend ever regresses (and app.js is
// hand-edited, so it's plausible), the request shape arriving here
// would change and downstream YAML rendering would silently drop the
// custom groups claim.
//
// This test documents what the backend EXPECTS to receive after the
// JS translation runs. If a future contributor changes app.js's
// buildAuthPayload to send the flat shape, this test still passes
// (we accept anything in Settings) but a documented JS-side test
// in app.js would catch the drift. Until JS tests exist, this is the
// canonical reference for the post-translation payload shape.
// Review #12.5.
func TestHandleCreateAgent_NestedClaimMapShape(t *testing.T) {
	srv, captured := setupCreateWithCapture(t)

	// Shape AFTER the JS translation runs: groups_claim has become
	// claim_map.groups. This is what the backend should see.
	body := []byte(`{
		"name": "agent-with-roles",
		"model_provider": "openai",
		"auth": {
			"mode": "oidc",
			"settings": {
				"issuer":   "https://login.example.com",
				"audience": "api://forge",
				"claim_map": { "groups": "roles" }
			}
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	if captured.Auth == nil {
		t.Fatal("captured Auth is nil")
	}
	cm, ok := captured.Auth.Settings["claim_map"].(map[string]any)
	if !ok {
		t.Fatalf("claim_map not present or wrong type: %v", captured.Auth.Settings["claim_map"])
	}
	if cm["groups"] != "roles" {
		t.Errorf("claim_map.groups = %v, want roles", cm["groups"])
	}

	// Negative half: the FLAT shape (pre-translation) should NOT
	// round-trip as if it had been translated. If the frontend ever
	// stops calling buildAuthPayload, the backend receives the raw
	// form and the YAML will be missing claim_map entirely.
	if _, hasFlat := captured.Auth.Settings["groups_claim"]; hasFlat {
		t.Errorf("settings contains flat groups_claim — frontend translation may have skipped")
	}
}

func TestHandleCreateAgent_FlatGroupsClaim_PreservedAsExtraField(t *testing.T) {
	// Counterpart: if the frontend sends the FLAT shape by mistake,
	// the backend accepts the request (no validation rejects unknown
	// keys) but the claim_map nested mapping is missing — which means
	// the generated forge.yaml won't include claim_map.groups, and the
	// agent runtime will fall back to the default "groups" claim.
	//
	// This documents the failure mode so if anyone investigates
	// "my custom group claim isn't being read," this test is the
	// breadcrumb.
	srv, captured := setupCreateWithCapture(t)

	body := []byte(`{
		"name": "agent-flat",
		"model_provider": "openai",
		"auth": {
			"mode": "oidc",
			"settings": {
				"issuer": "https://login.example.com",
				"audience": "api://forge",
				"groups_claim": "roles"
			}
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	// Request is structurally valid (issuer + audience present). It just
	// won't behave as the user expected at runtime.
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if _, hasNested := captured.Auth.Settings["claim_map"]; hasNested {
		t.Error("backend should not synthesize claim_map from groups_claim — that's the frontend's job")
	}
}

func TestHandleCreateAgent_WithoutAuthPayload(t *testing.T) {
	// Omitting `auth` from the request is still a valid create — the
	// agent gets anonymous access by default.
	srv, captured := setupCreateWithCapture(t)

	body := []byte(`{"name": "no-auth-agent", "model_provider": "openai"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusCreated)
	}
	if captured.Auth != nil {
		t.Errorf("Auth = %v, want nil for omitted field", captured.Auth)
	}
}
