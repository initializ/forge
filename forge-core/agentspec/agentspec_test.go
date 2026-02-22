package agentspec

import (
	"encoding/json"
	"testing"
)

func TestAgentSpec_JSONRoundTrip(t *testing.T) {
	spec := AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "Test Agent",
		Description:  "A test agent",
		Runtime: &RuntimeConfig{
			Image:      "python:3.11-slim",
			Entrypoint: []string{"python", "main.py"},
			Port:       8080,
			Env:        map[string]string{"LOG_LEVEL": "info"},
		},
		Tools: []ToolSpec{
			{Name: "web-search", Description: "Search the web"},
		},
		PolicyScaffold: &PolicyScaffold{
			Guardrails: []Guardrail{
				{Type: "content_filter", Config: map[string]any{"blocked": true}},
			},
		},
		Identity: &Identity{
			Issuer:   "https://auth.example.com",
			Audience: "forge-agents",
			Scopes:   []string{"read", "write"},
		},
		A2A: &A2AConfig{
			Endpoint: "/a2a",
			Skills: []A2ASkill{
				{ID: "search", Name: "Web Search", Description: "Search the web", Tags: []string{"search"}},
			},
			Capabilities: &A2ACapabilities{Streaming: true},
		},
		Model: &ModelConfig{
			Provider:   "openai",
			Name:       "gpt-4",
			Version:    "latest",
			Parameters: map[string]any{"temperature": 0.7},
		},
		ToolInterfaceVersion:  "1.0",
		SkillsSpecVersion:     "agentskills-v1",
		ForgeSkillsExtVersion: "1.0",
		EgressProfile:         "strict",
		EgressMode:            "deny-all",
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got AgentSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.AgentID != spec.AgentID {
		t.Errorf("AgentID = %q, want %q", got.AgentID, spec.AgentID)
	}
	if got.Runtime.Port != 8080 {
		t.Errorf("Runtime.Port = %d, want 8080", got.Runtime.Port)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "web-search" {
		t.Errorf("Tools mismatch: %+v", got.Tools)
	}
	if got.A2A.Skills[0].ID != "search" {
		t.Errorf("A2A.Skills[0].ID = %q, want %q", got.A2A.Skills[0].ID, "search")
	}
	if got.Model.Provider != "openai" {
		t.Errorf("Model.Provider = %q, want %q", got.Model.Provider, "openai")
	}
	if got.ToolInterfaceVersion != spec.ToolInterfaceVersion {
		t.Errorf("ToolInterfaceVersion = %q, want %q", got.ToolInterfaceVersion, spec.ToolInterfaceVersion)
	}
	if got.EgressProfile != spec.EgressProfile {
		t.Errorf("EgressProfile = %q, want %q", got.EgressProfile, spec.EgressProfile)
	}
}

func TestAgentSpec_BackwardsCompat(t *testing.T) {
	// Old JSON without new fields should unmarshal without error
	oldJSON := `{"forge_version":"1.0","agent_id":"old-agent","version":"1.0.0","name":"Old Agent"}`
	var spec AgentSpec
	if err := json.Unmarshal([]byte(oldJSON), &spec); err != nil {
		t.Fatalf("unmarshal old JSON: %v", err)
	}
	if spec.AgentID != "old-agent" {
		t.Errorf("AgentID = %q, want %q", spec.AgentID, "old-agent")
	}
	if spec.ToolInterfaceVersion != "" {
		t.Errorf("ToolInterfaceVersion should be empty, got %q", spec.ToolInterfaceVersion)
	}
	if spec.EgressProfile != "" {
		t.Errorf("EgressProfile should be empty, got %q", spec.EgressProfile)
	}
}

func TestToolSpec_Category(t *testing.T) {
	tool := ToolSpec{
		Name:     "web_search",
		Category: "builtin",
	}
	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ToolSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Category != "builtin" {
		t.Errorf("Category = %q, want %q", got.Category, "builtin")
	}

	// Category omitted when empty
	tool2 := ToolSpec{Name: "test"}
	data2, _ := json.Marshal(tool2)
	var raw map[string]any
	json.Unmarshal(data2, &raw)
	if _, ok := raw["category"]; ok {
		t.Error("expected category to be omitted when empty")
	}
}

func TestAgentSpec_OmitEmpty(t *testing.T) {
	spec := AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "minimal",
		Version:      "0.1.0",
		Name:         "Minimal",
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	for _, key := range []string{"description", "runtime", "tools", "policy_scaffold", "identity", "a2a", "model", "tool_interface_version", "skills_spec_version", "forge_skills_ext_version", "egress_profile", "egress_mode"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected key %q to be omitted from zero-value AgentSpec", key)
		}
	}
}

func TestToolSpec_InputSchemaRawMessage(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	tool := ToolSpec{
		Name:        "search",
		Description: "Search tool",
		InputSchema: schema,
		Permissions: []string{"network"},
		ForgeMeta: &ForgeToolMeta{
			AllowedTables:    []string{"users"},
			AllowedEndpoints: []string{"/api/v1"},
		},
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ToolSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(got.InputSchema) != string(schema) {
		t.Errorf("InputSchema = %s, want %s", got.InputSchema, schema)
	}
	if got.ForgeMeta.AllowedTables[0] != "users" {
		t.Errorf("ForgeMeta.AllowedTables[0] = %q, want %q", got.ForgeMeta.AllowedTables[0], "users")
	}
}

func TestRuntimeConfig_JSONRoundTrip(t *testing.T) {
	rc := RuntimeConfig{
		Image:          "python:3.11-slim",
		Entrypoint:     []string{"python", "main.py"},
		Port:           8080,
		Env:            map[string]string{"KEY": "value"},
		DepsFile:       "requirements.txt",
		DepsInstallCmd: "pip install -r requirements.txt",
		HealthCheck:    "/healthz",
		User:           "app",
	}

	data, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got RuntimeConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Image != rc.Image {
		t.Errorf("Image = %q, want %q", got.Image, rc.Image)
	}
	if got.DepsFile != rc.DepsFile {
		t.Errorf("DepsFile = %q, want %q", got.DepsFile, rc.DepsFile)
	}
	if got.User != rc.User {
		t.Errorf("User = %q, want %q", got.User, rc.User)
	}
}

func TestPolicyScaffold_JSONRoundTrip(t *testing.T) {
	ps := PolicyScaffold{
		Guardrails: []Guardrail{
			{
				Type:   "content_filter",
				Config: map[string]any{"blocked_categories": []any{"violence", "profanity"}},
			},
			{
				Type:   "no_pii",
				Config: map[string]any{"fields": []any{"ssn", "email"}},
			},
		},
	}

	data, err := json.Marshal(ps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got PolicyScaffold
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Guardrails) != 2 {
		t.Fatalf("Guardrails count = %d, want 2", len(got.Guardrails))
	}
	if got.Guardrails[0].Type != "content_filter" {
		t.Errorf("Guardrails[0].Type = %q, want %q", got.Guardrails[0].Type, "content_filter")
	}
	if got.Guardrails[1].Type != "no_pii" {
		t.Errorf("Guardrails[1].Type = %q, want %q", got.Guardrails[1].Type, "no_pii")
	}
}

func TestModelConfig_Parameters(t *testing.T) {
	mc := ModelConfig{
		Provider: "openai",
		Name:     "gpt-4",
		Version:  "latest",
		Parameters: map[string]any{
			"temperature": 0.7,
			"max_tokens":  4096.0,
			"top_p":       0.9,
		},
	}

	data, err := json.Marshal(mc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ModelConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Provider != mc.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, mc.Provider)
	}
	if len(got.Parameters) != 3 {
		t.Errorf("Parameters count = %d, want 3", len(got.Parameters))
	}
	if temp, ok := got.Parameters["temperature"].(float64); !ok || temp != 0.7 {
		t.Errorf("Parameters[temperature] = %v, want 0.7", got.Parameters["temperature"])
	}
}

func TestA2AConfig_Skills(t *testing.T) {
	cfg := A2AConfig{
		Endpoint: "/a2a",
		Skills: []A2ASkill{
			{ID: "search", Name: "Web Search", Description: "Search the web", Tags: []string{"search", "web"}},
			{ID: "summarize", Name: "Summarize", Description: "Summarize text", Tags: []string{"nlp"}},
		},
		Capabilities: &A2ACapabilities{
			Streaming:              true,
			PushNotifications:      false,
			StateTransitionHistory: true,
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got A2AConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Skills) != 2 {
		t.Fatalf("Skills count = %d, want 2", len(got.Skills))
	}
	if got.Skills[0].ID != "search" {
		t.Errorf("Skills[0].ID = %q, want %q", got.Skills[0].ID, "search")
	}
	if got.Skills[1].Tags[0] != "nlp" {
		t.Errorf("Skills[1].Tags[0] = %q, want %q", got.Skills[1].Tags[0], "nlp")
	}
	if !got.Capabilities.Streaming {
		t.Error("Capabilities.Streaming = false, want true")
	}
	if got.Capabilities.PushNotifications {
		t.Error("Capabilities.PushNotifications = true, want false")
	}
}
