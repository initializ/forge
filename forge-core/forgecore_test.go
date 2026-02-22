package forgecore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/skills"
	"github.com/initializ/forge/forge-core/types"
)

// ─── Mock LLM Client ─────────────────────────────────────────────────

type mockLLMClient struct {
	response *llm.ChatResponse
	err      error
	calls    int
}

func (m *mockLLMClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockLLMClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	ch := make(chan llm.StreamDelta, 1)
	close(ch)
	return ch, nil
}

func (m *mockLLMClient) ModelID() string { return "mock-model" }

// ─── Mock Tool Executor ──────────────────────────────────────────────

type mockToolExecutor struct {
	tools map[string]string // name -> fixed response
}

func newMockToolExecutor(tools map[string]string) *mockToolExecutor {
	return &mockToolExecutor{tools: tools}
}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	if resp, ok := m.tools[name]; ok {
		return resp, nil
	}
	return "", nil
}

func (m *mockToolExecutor) ToolDefinitions() []llm.ToolDefinition {
	var defs []llm.ToolDefinition
	for name := range m.tools {
		defs = append(defs, llm.ToolDefinition{
			Type: "function",
			Function: llm.FunctionSchema{
				Name:        name,
				Description: "mock tool " + name,
			},
		})
	}
	return defs
}

// ─── Sequential Mock Client ─────────────────────────────────────────

type sequentialMockClient struct {
	responses []*llm.ChatResponse
	callIdx   int
}

func (m *sequentialMockClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.callIdx >= len(m.responses) {
		return &llm.ChatResponse{
			Message:      llm.ChatMessage{Role: llm.RoleAssistant, Content: "fallback"},
			FinishReason: "stop",
		}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

func (m *sequentialMockClient) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	ch := make(chan llm.StreamDelta, 1)
	close(ch)
	return ch, nil
}

func (m *sequentialMockClient) ModelID() string { return "sequential-mock" }

// ─── Compile Tests ───────────────────────────────────────────────────

func TestCompile_BasicConfig(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4",
		},
		Tools: []types.ToolRef{
			{Name: "http_request", Type: "builtin"},
		},
		Egress: types.EgressRef{
			Profile:        "strict",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.openai.com"},
		},
	}

	result, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if result.Spec == nil {
		t.Fatal("Compile() returned nil Spec")
	}
	if result.Spec.AgentID != "test-agent" {
		t.Errorf("AgentID = %q, want test-agent", result.Spec.AgentID)
	}
	if result.Spec.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", result.Spec.Version)
	}
	if result.Spec.ForgeVersion != "1.0" {
		t.Errorf("ForgeVersion = %q, want 1.0", result.Spec.ForgeVersion)
	}
	if result.Spec.Model == nil {
		t.Fatal("Compile() returned nil Model in spec")
	}
	if result.Spec.Model.Provider != "openai" {
		t.Errorf("Model.Provider = %q, want openai", result.Spec.Model.Provider)
	}
	if result.Spec.Model.Name != "gpt-4" {
		t.Errorf("Model.Name = %q, want gpt-4", result.Spec.Model.Name)
	}
	if len(result.Spec.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Spec.Tools))
	}
	if result.Spec.Tools[0].Name != "http_request" {
		t.Errorf("Tool[0].Name = %q, want http_request", result.Spec.Tools[0].Name)
	}
	if result.EgressConfig == nil {
		t.Fatal("Compile() returned nil EgressConfig")
	}
	if len(result.Allowlist) == 0 {
		t.Error("Compile() returned empty Allowlist")
	}
	if result.Spec.EgressProfile != "strict" {
		t.Errorf("EgressProfile = %q, want strict", result.Spec.EgressProfile)
	}
	if result.Spec.EgressMode != "allowlist" {
		t.Errorf("EgressMode = %q, want allowlist", result.Spec.EgressMode)
	}
}

func TestCompile_WithSkills(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "skill-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile: "strict",
			Mode:    "deny-all",
		},
	}

	entries := []skills.SkillEntry{
		{
			Name:        "summarize",
			Description: "Summarize text content",
			InputSpec:   "text: string",
			OutputSpec:  "summary: string",
		},
		{
			Name:        "translate",
			Description: "Translate text to another language",
			InputSpec:   "text: string, target_lang: string",
			OutputSpec:  "translated: string",
		},
	}

	result, err := Compile(CompileRequest{
		Config:       cfg,
		SkillEntries: entries,
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if result.CompiledSkills == nil {
		t.Fatal("Compile() returned nil CompiledSkills")
	}
	if len(result.CompiledSkills.Skills) != 2 {
		t.Errorf("got %d compiled skills, want 2", len(result.CompiledSkills.Skills))
	}
	if result.CompiledSkills.Count != 2 {
		t.Errorf("CompiledSkills.Count = %d, want 2", result.CompiledSkills.Count)
	}
	if result.CompiledSkills.Version != "agentskills-v1" {
		t.Errorf("CompiledSkills.Version = %q, want agentskills-v1", result.CompiledSkills.Version)
	}

	// Verify skill names
	foundSummarize, foundTranslate := false, false
	for _, s := range result.CompiledSkills.Skills {
		switch s.Name {
		case "summarize":
			foundSummarize = true
		case "translate":
			foundTranslate = true
		}
	}
	if !foundSummarize {
		t.Error("missing compiled skill 'summarize'")
	}
	if !foundTranslate {
		t.Error("missing compiled skill 'translate'")
	}
}

func TestCompile_NoEgress(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "no-egress",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
	}

	result, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if result.Spec == nil {
		t.Fatal("Compile() returned nil Spec")
	}
	// Default egress should be resolved (strict / deny-all)
	if result.EgressConfig == nil {
		t.Fatal("Compile() returned nil EgressConfig even with defaults")
	}
	if result.EgressConfig.Profile != "strict" {
		t.Errorf("default EgressConfig.Profile = %q, want strict", result.EgressConfig.Profile)
	}
	if result.EgressConfig.Mode != "deny-all" {
		t.Errorf("default EgressConfig.Mode = %q, want deny-all", result.EgressConfig.Mode)
	}
	if result.Spec.EgressProfile != "strict" {
		t.Errorf("default Spec.EgressProfile = %q, want strict", result.Spec.EgressProfile)
	}
	if result.Spec.EgressMode != "deny-all" {
		t.Errorf("default Spec.EgressMode = %q, want deny-all", result.Spec.EgressMode)
	}
}

func TestCompile_NoSkills(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "no-skills",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
	}

	result, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if result.CompiledSkills != nil {
		t.Error("expected nil CompiledSkills when no skill entries provided")
	}
}

func TestCompile_RuntimeInferred(t *testing.T) {
	// Python entrypoint -> python:3.12-slim image
	cfg := &types.ForgeConfig{
		AgentID:    "runtime-test",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
	}

	result, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if result.Spec.Runtime == nil {
		t.Fatal("expected non-nil Runtime")
	}
	if result.Spec.Runtime.Image != "python:3.12-slim" {
		t.Errorf("Runtime.Image = %q, want python:3.12-slim", result.Spec.Runtime.Image)
	}
	if result.Spec.Runtime.Port != 8080 {
		t.Errorf("Runtime.Port = %d, want 8080", result.Spec.Runtime.Port)
	}
}

func TestCompile_NodeEntrypoint(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "node-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "node index.js",
	}

	result, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if result.Spec.Runtime.Image != "node:20-slim" {
		t.Errorf("Runtime.Image = %q, want node:20-slim", result.Spec.Runtime.Image)
	}
}

func TestCompile_InvalidEgressProfile(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "bad-egress",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile: "invalid-profile",
		},
	}

	_, err := Compile(CompileRequest{Config: cfg})
	if err == nil {
		t.Fatal("expected error for invalid egress profile")
	}
}

// ─── Validate Tests ──────────────────────────────────────────────────

func TestValidateConfig_Valid(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "valid-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
	}

	result := ValidateConfig(cfg)
	if !result.IsValid() {
		t.Errorf("ValidateConfig() not valid: errors=%v", result.Errors)
	}
}

func TestValidateConfig_MissingFields(t *testing.T) {
	cfg := &types.ForgeConfig{}
	result := ValidateConfig(cfg)
	if result.IsValid() {
		t.Error("ValidateConfig() should fail for empty config")
	}
	// Should have errors for agent_id, version, entrypoint
	if len(result.Errors) < 3 {
		t.Errorf("expected at least 3 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestValidateConfig_InvalidAgentID(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "Invalid_Agent_ID!",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
	}

	result := ValidateConfig(cfg)
	if result.IsValid() {
		t.Error("ValidateConfig() should fail for invalid agent_id format")
	}
}

func TestValidateConfig_InvalidSemver(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "not-a-version",
		Framework:  "custom",
		Entrypoint: "python main.py",
	}

	result := ValidateConfig(cfg)
	if result.IsValid() {
		t.Error("ValidateConfig() should fail for invalid semver")
	}
}

func TestValidateConfig_UnknownFramework(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "1.0.0",
		Framework:  "unknown-framework",
		Entrypoint: "python main.py",
	}

	result := ValidateConfig(cfg)
	// Unknown framework is a warning, not an error
	if !result.IsValid() {
		t.Errorf("expected valid (warnings only) for unknown framework, got errors: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning for unknown framework")
	}
}

func TestValidateConfig_InvalidEgressProfile(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile: "bad-profile",
		},
	}

	result := ValidateConfig(cfg)
	if result.IsValid() {
		t.Error("ValidateConfig() should fail for invalid egress profile")
	}
}

func TestValidateConfig_DevOpenWarning(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Mode: "dev-open",
		},
	}

	result := ValidateConfig(cfg)
	if len(result.Warnings) == 0 {
		t.Error("expected warning for dev-open mode")
	}
}

func TestValidateAgentSpec_ValidJSON(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "Test Agent",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
			Port:  8080,
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	errs, err := ValidateAgentSpec(data)
	if err != nil {
		t.Fatalf("ValidateAgentSpec() error: %v", err)
	}
	if len(errs) > 0 {
		t.Errorf("ValidateAgentSpec() errors: %v", errs)
	}
}

func TestValidateAgentSpec_InvalidJSON(t *testing.T) {
	_, err := ValidateAgentSpec([]byte(`{invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestValidateCommandCompat_Valid(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "Test Agent",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
		},
	}

	result := ValidateCommandCompat(spec)
	if !result.IsValid() {
		t.Errorf("ValidateCommandCompat() not valid: errors=%v", result.Errors)
	}
}

func TestValidateCommandCompat_MissingRequired(t *testing.T) {
	spec := &agentspec.AgentSpec{} // all fields empty

	result := ValidateCommandCompat(spec)
	if result.IsValid() {
		t.Error("expected validation failure for empty spec")
	}
	// Should have errors for agent_id, name, version, forge_version, runtime
	if len(result.Errors) < 4 {
		t.Errorf("expected at least 4 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestValidateCommandCompat_UnsupportedVersion(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "99.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "Test Agent",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
		},
	}

	result := ValidateCommandCompat(spec)
	if result.IsValid() {
		t.Error("expected validation failure for unsupported forge version")
	}
}

func TestValidateCommandCompat_MissingRuntimeImage(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "Test Agent",
		Runtime: &agentspec.RuntimeConfig{
			// Image intentionally empty
			Port: 8080,
		},
	}

	result := ValidateCommandCompat(spec)
	if result.IsValid() {
		t.Error("expected validation failure for missing runtime.image")
	}
}

func TestValidateCommandCompat_WarningsForOptional(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "Test Agent",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
		},
		// No A2A, no Model, no tool_interface_version
	}

	result := ValidateCommandCompat(spec)
	if !result.IsValid() {
		t.Errorf("expected valid with warnings, got errors: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warnings for missing optional fields")
	}
}

// ─── SimulateImport Tests ────────────────────────────────────────────

func TestSimulateImport(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "sim-agent",
		Version:      "1.0.0",
		Name:         "Sim Agent",
		Description:  "A test agent for simulation",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
			Port:  8080,
		},
		Model: &agentspec.ModelConfig{
			Provider: "openai",
			Name:     "gpt-4",
		},
		Tools: []agentspec.ToolSpec{
			{Name: "http_request", Category: "builtin"},
		},
	}

	result := SimulateImport(spec)
	if result.Definition == nil {
		t.Fatal("SimulateImport() returned nil Definition")
	}
	if result.Definition.Slug != "sim-agent" {
		t.Errorf("Slug = %q, want sim-agent", result.Definition.Slug)
	}
	if result.Definition.DisplayName != "Sim Agent" {
		t.Errorf("DisplayName = %q, want Sim Agent", result.Definition.DisplayName)
	}
	if result.Definition.Description != "A test agent for simulation" {
		t.Errorf("Description = %q, want 'A test agent for simulation'", result.Definition.Description)
	}
	if result.Definition.ContainerImage != "python:3.12-slim" {
		t.Errorf("ContainerImage = %q, want python:3.12-slim", result.Definition.ContainerImage)
	}
	if result.Definition.Port != 8080 {
		t.Errorf("Port = %d, want 8080", result.Definition.Port)
	}
	if result.Definition.ModelProvider != "openai" {
		t.Errorf("ModelProvider = %q, want openai", result.Definition.ModelProvider)
	}
	if result.Definition.ModelName != "gpt-4" {
		t.Errorf("ModelName = %q, want gpt-4", result.Definition.ModelName)
	}
	if len(result.Definition.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Definition.Tools))
	}
	if result.Definition.Tools[0].Name != "http_request" {
		t.Errorf("Tool[0].Name = %q, want http_request", result.Definition.Tools[0].Name)
	}
	if result.Definition.Tools[0].Category != "builtin" {
		t.Errorf("Tool[0].Category = %q, want builtin", result.Definition.Tools[0].Category)
	}
}

func TestSimulateImport_NoRuntime(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "no-runtime",
		Version:      "1.0.0",
		Name:         "No Runtime",
	}

	result := SimulateImport(spec)
	if result.Definition.ContainerImage != "" {
		t.Errorf("expected empty ContainerImage, got %q", result.Definition.ContainerImage)
	}
	// Should have warning about no runtime
	found := false
	for _, w := range result.ImportWarnings {
		if w == "no runtime config; container image will not be set" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected import warning about missing runtime")
	}
}

func TestSimulateImport_NoModel(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "no-model",
		Version:      "1.0.0",
		Name:         "No Model",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
		},
	}

	result := SimulateImport(spec)
	if result.Definition.ModelProvider != "" {
		t.Errorf("expected empty ModelProvider, got %q", result.Definition.ModelProvider)
	}
	found := false
	for _, w := range result.ImportWarnings {
		if w == "no model config; model will not be configured" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected import warning about missing model")
	}
}

func TestSimulateImport_WithA2ASkills(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "skill-sim",
		Version:      "1.0.0",
		Name:         "Skill Sim",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.12-slim",
		},
		A2A: &agentspec.A2AConfig{
			Skills: []agentspec.A2ASkill{
				{ID: "summarize", Name: "Summarize"},
				{ID: "translate", Name: "Translate"},
			},
			Capabilities: &agentspec.A2ACapabilities{
				Streaming:         true,
				PushNotifications: false,
			},
		},
	}

	result := SimulateImport(spec)
	if len(result.Definition.Skills) != 2 {
		t.Errorf("got %d skills, want 2", len(result.Definition.Skills))
	}
	if result.Definition.Capabilities == nil {
		t.Fatal("expected non-nil Capabilities")
	}
	if !result.Definition.Capabilities.Streaming {
		t.Error("expected Streaming = true")
	}
}

// ─── Runtime Tests ───────────────────────────────────────────────────

func TestNewRuntime_Basic(t *testing.T) {
	client := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: "Hello! I'm a test agent.",
			},
			FinishReason: "stop",
		},
	}

	tools := newMockToolExecutor(map[string]string{
		"http_request": `{"status": 200, "body": "ok"}`,
	})

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     client,
		Tools:         tools,
		SystemPrompt:  "You are a test agent.",
		MaxIterations: 5,
	})

	if executor == nil {
		t.Fatal("NewRuntime() returned nil")
	}

	// Execute a simple message
	task := &a2a.Task{ID: "test-task-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Hello")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute() returned nil response")
	}
	if resp.Role != a2a.MessageRoleAgent {
		t.Errorf("response Role = %q, want agent", resp.Role)
	}
	if len(resp.Parts) == 0 {
		t.Fatal("Execute() returned empty parts")
	}
	if resp.Parts[0].Text != "Hello! I'm a test agent." {
		t.Errorf("response text = %q, want 'Hello! I'm a test agent.'", resp.Parts[0].Text)
	}

	// Check that LLM was called
	if client.calls != 1 {
		t.Errorf("LLM was called %d times, want 1", client.calls)
	}
}

func TestNewRuntime_WithToolCalling(t *testing.T) {
	toolCallClient := &sequentialMockClient{
		responses: []*llm.ChatResponse{
			{
				Message: llm.ChatMessage{
					Role: llm.RoleAssistant,
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call-1",
							Type: "function",
							Function: llm.FunctionCall{
								Name:      "http_request",
								Arguments: `{"url": "https://example.com"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Content: "I fetched the URL and got: ok",
				},
				FinishReason: "stop",
			},
		},
	}

	tools := newMockToolExecutor(map[string]string{
		"http_request": `{"status": 200, "body": "ok"}`,
	})

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     toolCallClient,
		Tools:         tools,
		SystemPrompt:  "You are a test agent with tools.",
		MaxIterations: 10,
	})

	task := &a2a.Task{ID: "tool-task-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Fetch https://example.com")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute() returned nil")
	}
	if resp.Parts[0].Text != "I fetched the URL and got: ok" {
		t.Errorf("response text = %q, want 'I fetched the URL and got: ok'", resp.Parts[0].Text)
	}

	// Should have made 2 LLM calls
	if toolCallClient.callIdx != 2 {
		t.Errorf("LLM was called %d times, want 2", toolCallClient.callIdx)
	}
}

func TestNewRuntime_WithHooks(t *testing.T) {
	client := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: "Done",
			},
			FinishReason: "stop",
		},
	}

	hooks := runtime.NewHookRegistry()
	beforeCalled := false
	afterCalled := false

	hooks.Register(runtime.BeforeLLMCall, func(ctx context.Context, hc *runtime.HookContext) error {
		beforeCalled = true
		return nil
	})
	hooks.Register(runtime.AfterLLMCall, func(ctx context.Context, hc *runtime.HookContext) error {
		afterCalled = true
		return nil
	})

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     client,
		Tools:         newMockToolExecutor(nil),
		Hooks:         hooks,
		SystemPrompt:  "Test",
		MaxIterations: 5,
	})

	task := &a2a.Task{ID: "hook-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Test hooks")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !beforeCalled {
		t.Error("BeforeLLMCall hook was not called")
	}
	if !afterCalled {
		t.Error("AfterLLMCall hook was not called")
	}
}

func TestNewRuntime_ToolExecHooks(t *testing.T) {
	toolCallClient := &sequentialMockClient{
		responses: []*llm.ChatResponse{
			{
				Message: llm.ChatMessage{
					Role: llm.RoleAssistant,
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call-hook-1",
							Type: "function",
							Function: llm.FunctionCall{
								Name:      "http_request",
								Arguments: `{}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Content: "Done with tool",
				},
				FinishReason: "stop",
			},
		},
	}

	hooks := runtime.NewHookRegistry()
	beforeToolCalled := false
	afterToolCalled := false
	var capturedToolName string

	hooks.Register(runtime.BeforeToolExec, func(ctx context.Context, hc *runtime.HookContext) error {
		beforeToolCalled = true
		capturedToolName = hc.ToolName
		return nil
	})
	hooks.Register(runtime.AfterToolExec, func(ctx context.Context, hc *runtime.HookContext) error {
		afterToolCalled = true
		return nil
	})

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     toolCallClient,
		Tools:         newMockToolExecutor(map[string]string{"http_request": `{"ok":true}`}),
		Hooks:         hooks,
		SystemPrompt:  "Test",
		MaxIterations: 5,
	})

	task := &a2a.Task{ID: "tool-hook-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Use a tool")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !beforeToolCalled {
		t.Error("BeforeToolExec hook was not called")
	}
	if !afterToolCalled {
		t.Error("AfterToolExec hook was not called")
	}
	if capturedToolName != "http_request" {
		t.Errorf("hook captured tool name = %q, want http_request", capturedToolName)
	}
}

func TestNewRuntime_MaxIterations(t *testing.T) {
	// LLM always returns tool calls, never stops
	client := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call-loop",
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "http_request",
							Arguments: `{}`,
						},
					},
				},
			},
			FinishReason: "tool_calls",
		},
	}

	tools := newMockToolExecutor(map[string]string{
		"http_request": `{"ok": true}`,
	})

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     client,
		Tools:         tools,
		MaxIterations: 3,
	})

	task := &a2a.Task{ID: "loop-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Loop forever")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err == nil {
		t.Fatal("expected error for max iterations exceeded")
	}

	// Should have called LLM exactly maxIterations times
	if client.calls != 3 {
		t.Errorf("LLM was called %d times, want 3", client.calls)
	}
}

func TestNewRuntime_NilTools(t *testing.T) {
	client := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: "Response without tools",
			},
			FinishReason: "stop",
		},
	}

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     client,
		Tools:         nil,
		SystemPrompt:  "No tools",
		MaxIterations: 5,
	})

	task := &a2a.Task{ID: "no-tools-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Hello")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute() returned nil")
	}
	if resp.Parts[0].Text != "Response without tools" {
		t.Errorf("unexpected response text: %q", resp.Parts[0].Text)
	}
}

func TestNewRuntime_LLMError(t *testing.T) {
	client := &mockLLMClient{
		err: context.DeadlineExceeded,
	}

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     client,
		Tools:         newMockToolExecutor(nil),
		MaxIterations: 5,
	})

	task := &a2a.Task{ID: "error-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Will fail")},
	}

	_, err := executor.Execute(context.Background(), task, msg)
	if err == nil {
		t.Fatal("expected error from LLM client")
	}
}

func TestNewRuntime_DefaultMaxIterations(t *testing.T) {
	// If MaxIterations is 0, should default to 10
	client := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: "ok",
			},
			FinishReason: "stop",
		},
	}

	executor := NewRuntime(RuntimeConfig{
		LLMClient: client,
		// MaxIterations: 0 -> defaults to 10
	})

	if executor == nil {
		t.Fatal("NewRuntime() returned nil")
	}

	task := &a2a.Task{ID: "default-iter-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Hello")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute() returned nil")
	}
}

func TestNewRuntime_TaskHistory(t *testing.T) {
	client := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: "I remember our conversation",
			},
			FinishReason: "stop",
		},
	}

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     client,
		Tools:         newMockToolExecutor(nil),
		SystemPrompt:  "You are helpful",
		MaxIterations: 5,
	})

	task := &a2a.Task{
		ID: "history-task",
		History: []a2a.Message{
			{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("Previous message")}},
			{Role: a2a.MessageRoleAgent, Parts: []a2a.Part{a2a.NewTextPart("Previous response")}},
		},
	}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("New message")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Execute() returned nil")
	}
}

// ─── Integration: Compile -> Validate -> Runtime ─────────────────────

func TestIntegration_CompileValidateRuntime(t *testing.T) {
	// 1. Define config in memory (no disk)
	cfg := &types.ForgeConfig{
		AgentID:    "integration-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4",
		},
		Tools: []types.ToolRef{
			{Name: "http_request", Type: "builtin"},
		},
		Egress: types.EgressRef{
			Profile:        "strict",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.openai.com"},
		},
	}

	// 2. Validate config
	valResult := ValidateConfig(cfg)
	if !valResult.IsValid() {
		t.Fatalf("ValidateConfig() failed: %v", valResult.Errors)
	}

	// 3. Compile
	compileResult, err := Compile(CompileRequest{
		Config: cfg,
		SkillEntries: []skills.SkillEntry{
			{Name: "summarize", Description: "Summarize content"},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	// Verify compilation output
	if compileResult.Spec == nil {
		t.Fatal("Compile() returned nil Spec")
	}
	if compileResult.CompiledSkills == nil {
		t.Fatal("Compile() returned nil CompiledSkills")
	}
	if compileResult.CompiledSkills.Count != 1 {
		t.Errorf("CompiledSkills.Count = %d, want 1", compileResult.CompiledSkills.Count)
	}

	// 4. Validate spec for Command compatibility
	spec := compileResult.Spec
	// spec already has ForgeVersion, Runtime, etc from compilation

	compatResult := ValidateCommandCompat(spec)
	if !compatResult.IsValid() {
		t.Fatalf("ValidateCommandCompat() failed: %v", compatResult.Errors)
	}

	// 5. Simulate import
	importSim := SimulateImport(spec)
	if importSim.Definition.Slug != "integration-agent" {
		t.Errorf("import sim slug = %q, want integration-agent", importSim.Definition.Slug)
	}
	if importSim.Definition.ContainerImage != "python:3.12-slim" {
		t.Errorf("import sim image = %q, want python:3.12-slim", importSim.Definition.ContainerImage)
	}
	if importSim.Definition.ModelProvider != "openai" {
		t.Errorf("import sim model provider = %q, want openai", importSim.Definition.ModelProvider)
	}

	// 6. Create runtime with injected mock LLM
	mockClient := &mockLLMClient{
		response: &llm.ChatResponse{
			Message: llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: "Integration test response",
			},
			FinishReason: "stop",
		},
	}

	executor := NewRuntime(RuntimeConfig{
		LLMClient:     mockClient,
		Tools:         newMockToolExecutor(map[string]string{"http_request": `{"ok":true}`}),
		SystemPrompt:  "You are " + cfg.AgentID,
		MaxIterations: 5,
	})

	// 7. Execute agent
	task := &a2a.Task{ID: "integration-1"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Hello from integration test")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil || len(resp.Parts) == 0 {
		t.Fatal("Execute() returned empty response")
	}
	if resp.Parts[0].Text != "Integration test response" {
		t.Errorf("response text = %q, want 'Integration test response'", resp.Parts[0].Text)
	}
	if mockClient.calls != 1 {
		t.Errorf("LLM was called %d times, want 1", mockClient.calls)
	}
}

func TestIntegration_CompileWithToolCallLoop(t *testing.T) {
	// Full integration: compile config, then run through tool-calling loop
	cfg := &types.ForgeConfig{
		AgentID:    "tool-loop-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Tools: []types.ToolRef{
			{Name: "web_search", Type: "builtin"},
			{Name: "http_request", Type: "builtin"},
		},
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.google.com", "example.com"},
		},
	}

	// Validate
	valResult := ValidateConfig(cfg)
	if !valResult.IsValid() {
		t.Fatalf("ValidateConfig() failed: %v", valResult.Errors)
	}

	// Compile
	compileResult, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	if len(compileResult.Spec.Tools) != 2 {
		t.Errorf("got %d tools in spec, want 2", len(compileResult.Spec.Tools))
	}

	// Create runtime with multi-step tool calling
	toolCallClient := &sequentialMockClient{
		responses: []*llm.ChatResponse{
			{
				Message: llm.ChatMessage{
					Role: llm.RoleAssistant,
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call-search",
							Type: "function",
							Function: llm.FunctionCall{
								Name:      "web_search",
								Arguments: `{"query": "test"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: llm.ChatMessage{
					Role: llm.RoleAssistant,
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call-fetch",
							Type: "function",
							Function: llm.FunctionCall{
								Name:      "http_request",
								Arguments: `{"url": "https://example.com/result"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Content: "Found and fetched the result",
				},
				FinishReason: "stop",
			},
		},
	}

	executor := NewRuntime(RuntimeConfig{
		LLMClient: toolCallClient,
		Tools: newMockToolExecutor(map[string]string{
			"web_search":   `{"results": ["https://example.com/result"]}`,
			"http_request": `{"status": 200, "body": "result data"}`,
		}),
		SystemPrompt:  "You are " + cfg.AgentID,
		MaxIterations: 10,
	})

	task := &a2a.Task{ID: "multi-tool-task"}
	msg := &a2a.Message{
		Role:  a2a.MessageRoleUser,
		Parts: []a2a.Part{a2a.NewTextPart("Search and fetch results")},
	}

	resp, err := executor.Execute(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if resp == nil || len(resp.Parts) == 0 {
		t.Fatal("Execute() returned empty response")
	}
	if resp.Parts[0].Text != "Found and fetched the result" {
		t.Errorf("response text = %q", resp.Parts[0].Text)
	}
	if toolCallClient.callIdx != 3 {
		t.Errorf("LLM was called %d times, want 3", toolCallClient.callIdx)
	}
}

func TestIntegration_EgressAllowlistJSON(t *testing.T) {
	cfg := &types.ForgeConfig{
		AgentID:    "egress-test",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:        "strict",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.openai.com", "hooks.slack.com"},
		},
	}

	result, err := Compile(CompileRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	// Verify allowlist JSON can be parsed
	var allowlist map[string]interface{}
	if err := json.Unmarshal(result.Allowlist, &allowlist); err != nil {
		t.Fatalf("failed to parse Allowlist JSON: %v", err)
	}

	if allowlist["profile"] != "strict" {
		t.Errorf("allowlist profile = %v, want strict", allowlist["profile"])
	}
	if allowlist["mode"] != "allowlist" {
		t.Errorf("allowlist mode = %v, want allowlist", allowlist["mode"])
	}

	// Verify allowed_domains is present
	domains, ok := allowlist["allowed_domains"].([]interface{})
	if !ok {
		t.Fatal("allowed_domains is not an array")
	}
	if len(domains) != 2 {
		t.Errorf("got %d allowed_domains, want 2", len(domains))
	}
}
