package validate

import (
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
)

func TestSimulateImport_FullSpec(t *testing.T) {
	spec := validAgentSpec()
	result := SimulateImport(spec)

	if result.Definition == nil {
		t.Fatal("expected non-nil definition")
	}

	d := result.Definition
	if d.Slug != "test-agent" {
		t.Errorf("Slug = %q, want %q", d.Slug, "test-agent")
	}
	if d.DisplayName != "Test Agent" {
		t.Errorf("DisplayName = %q, want %q", d.DisplayName, "Test Agent")
	}
	if d.ContainerImage != "python:3.11-slim" {
		t.Errorf("ContainerImage = %q, want %q", d.ContainerImage, "python:3.11-slim")
	}
	if d.Port != 8080 {
		t.Errorf("Port = %d, want %d", d.Port, 8080)
	}
	if d.ModelProvider != "openai" {
		t.Errorf("ModelProvider = %q, want %q", d.ModelProvider, "openai")
	}
	if d.ModelName != "gpt-4" {
		t.Errorf("ModelName = %q, want %q", d.ModelName, "gpt-4")
	}
	if d.Capabilities == nil {
		t.Fatal("expected non-nil capabilities")
	}
	if !d.Capabilities.Streaming {
		t.Error("expected Streaming = true")
	}
	// With tool_interface_version set, we expect one version info warning
	versionWarnings := 0
	for _, w := range result.ImportWarnings {
		if contains(w, "tool_interface_version") {
			versionWarnings++
		}
	}
	if versionWarnings != 1 {
		t.Errorf("expected 1 tool_interface_version warning, got %d; all warnings: %v", versionWarnings, result.ImportWarnings)
	}
}

func TestSimulateImport_MinimalSpec(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "minimal",
		Version:      "0.1.0",
		Name:         "Minimal Agent",
	}
	result := SimulateImport(spec)

	d := result.Definition
	if d.Slug != "minimal" {
		t.Errorf("Slug = %q, want %q", d.Slug, "minimal")
	}
	if d.DisplayName != "Minimal Agent" {
		t.Errorf("DisplayName = %q, want %q", d.DisplayName, "Minimal Agent")
	}
	if d.ContainerImage != "" {
		t.Errorf("ContainerImage should be empty, got %q", d.ContainerImage)
	}

	// Should have warnings for missing runtime, model, a2a
	if len(result.ImportWarnings) < 3 {
		t.Errorf("expected at least 3 warnings for minimal spec, got %d: %v", len(result.ImportWarnings), result.ImportWarnings)
	}
}

func TestSimulateImport_ToolMapping(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "1.0",
		AgentID:      "tool-agent",
		Version:      "0.1.0",
		Name:         "Tool Agent",
		Runtime:      &agentspec.RuntimeConfig{Image: "python:3.11"},
		Model:        &agentspec.ModelConfig{Provider: "openai", Name: "gpt-4"},
		A2A: &agentspec.A2AConfig{
			Capabilities: &agentspec.A2ACapabilities{},
		},
		Tools: []agentspec.ToolSpec{
			{
				Name:        "search",
				Description: "Search tool",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			{
				Name:        "calc",
				Description: "Calculator",
			},
		},
	}
	result := SimulateImport(spec)

	if len(result.Definition.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Definition.Tools))
	}

	if result.Definition.Tools[0].Name != "search" {
		t.Errorf("tool[0].Name = %q, want %q", result.Definition.Tools[0].Name, "search")
	}
	if !result.Definition.Tools[0].HasSchema {
		t.Error("tool[0] should have HasSchema=true")
	}

	if result.Definition.Tools[1].Name != "calc" {
		t.Errorf("tool[1].Name = %q, want %q", result.Definition.Tools[1].Name, "calc")
	}
	if result.Definition.Tools[1].HasSchema {
		t.Error("tool[1] should have HasSchema=false")
	}
}

func TestSimulateImport_GuardrailMapping(t *testing.T) {
	spec := validAgentSpec()
	spec.PolicyScaffold = &agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{
			{Type: "content_filter"},
			{Type: "no_pii"},
			{Type: "custom_experimental"},
		},
	}
	result := SimulateImport(spec)

	if len(result.Definition.Guardrails) != 3 {
		t.Fatalf("expected 3 guardrails, got %d", len(result.Definition.Guardrails))
	}

	// Should have 1 warning for the unknown guardrail
	unknownCount := 0
	for _, w := range result.ImportWarnings {
		if contains(w, "custom_experimental") {
			unknownCount++
		}
	}
	if unknownCount != 1 {
		t.Errorf("expected 1 warning about custom_experimental, got %d", unknownCount)
	}
}

func TestSimulateImport_EnvVars(t *testing.T) {
	spec := validAgentSpec()
	spec.Runtime.Env = map[string]string{
		"API_KEY": "test",
		"DEBUG":   "true",
	}
	result := SimulateImport(spec)

	if len(result.Definition.EnvVars) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(result.Definition.EnvVars))
	}
	if result.Definition.EnvVars["API_KEY"] != "test" {
		t.Errorf("EnvVars[API_KEY] = %q, want %q", result.Definition.EnvVars["API_KEY"], "test")
	}
}

func TestSimulateImport_ToolCategories(t *testing.T) {
	spec := validAgentSpec()
	spec.Tools = []agentspec.ToolSpec{
		{Name: "builtin-tool", Category: "builtin", InputSchema: json.RawMessage(`{}`)},
		{Name: "dev-tool", Category: "dev", InputSchema: json.RawMessage(`{}`)},
	}
	result := SimulateImport(spec)

	if len(result.Definition.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Definition.Tools))
	}
	if result.Definition.Tools[0].Category != "builtin" {
		t.Errorf("tool[0].Category = %q, want %q", result.Definition.Tools[0].Category, "builtin")
	}
	if result.Definition.Tools[1].Category != "dev" {
		t.Errorf("tool[1].Category = %q, want %q", result.Definition.Tools[1].Category, "dev")
	}
	// Should have category mapping warnings
	found := 0
	for _, w := range result.ImportWarnings {
		if contains(w, "category") && contains(w, "mapped") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("expected 2 category mapping warnings, got %d", found)
	}
}

func TestSimulateImport_SkillOrigin(t *testing.T) {
	spec := validAgentSpec()
	spec.Tools = []agentspec.ToolSpec{
		{Name: "pdf-tool", SkillOrigin: "pdf-processing", InputSchema: json.RawMessage(`{}`)},
	}
	result := SimulateImport(spec)

	if len(result.Definition.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Definition.Tools))
	}
	if result.Definition.Tools[0].SkillOrigin != "pdf-processing" {
		t.Errorf("tool[0].SkillOrigin = %q, want %q", result.Definition.Tools[0].SkillOrigin, "pdf-processing")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if contains(w, "skill_origin") && contains(w, "pdf-processing") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skill_origin warning, got: %v", result.ImportWarnings)
	}
}

func TestSimulateImport_Skills(t *testing.T) {
	spec := validAgentSpec()
	spec.A2A = &agentspec.A2AConfig{
		Skills: []agentspec.A2ASkill{
			{ID: "skill-a", Name: "Skill A", Description: "First skill"},
			{ID: "skill-b", Name: "Skill B", Description: "Second skill"},
		},
		Capabilities: &agentspec.A2ACapabilities{Streaming: true},
	}
	result := SimulateImport(spec)

	if len(result.Definition.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(result.Definition.Skills))
	}
	if result.Definition.Skills[0] != "skill-a" {
		t.Errorf("skills[0] = %q, want %q", result.Definition.Skills[0], "skill-a")
	}
	if result.Definition.Skills[1] != "skill-b" {
		t.Errorf("skills[1] = %q, want %q", result.Definition.Skills[1], "skill-b")
	}
	// Should have skills count warning
	found := false
	for _, w := range result.ImportWarnings {
		if contains(w, "a2a.skills contains 2 skills") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skills count warning, got: %v", result.ImportWarnings)
	}
}

func TestSimulateImport_EgressFields(t *testing.T) {
	spec := validAgentSpec()
	spec.EgressProfile = "strict"
	spec.EgressMode = "allowlist"
	result := SimulateImport(spec)

	d := result.Definition
	if d.EgressProfile != "strict" {
		t.Errorf("EgressProfile = %q, want %q", d.EgressProfile, "strict")
	}
	if d.EgressMode != "allowlist" {
		t.Errorf("EgressMode = %q, want %q", d.EgressMode, "allowlist")
	}
	// Should have egress warnings
	profileWarning := false
	modeWarning := false
	for _, w := range result.ImportWarnings {
		if contains(w, "egress.profile") && contains(w, "strict") {
			profileWarning = true
		}
		if contains(w, "network_policy") {
			modeWarning = true
		}
	}
	if !profileWarning {
		t.Errorf("expected egress profile warning, got: %v", result.ImportWarnings)
	}
	if !modeWarning {
		t.Errorf("expected network_policy warning, got: %v", result.ImportWarnings)
	}
}

func TestSimulateImport_VersionFields(t *testing.T) {
	spec := validAgentSpec()
	spec.ToolInterfaceVersion = "1.0"
	spec.SkillsSpecVersion = "agentskills-v1"
	result := SimulateImport(spec)

	d := result.Definition
	if d.ToolInterfaceVersion != "1.0" {
		t.Errorf("ToolInterfaceVersion = %q, want %q", d.ToolInterfaceVersion, "1.0")
	}
	if d.SkillsSpecVersion != "agentskills-v1" {
		t.Errorf("SkillsSpecVersion = %q, want %q", d.SkillsSpecVersion, "agentskills-v1")
	}
	// Should have version field warning
	found := false
	for _, w := range result.ImportWarnings {
		if contains(w, "tool_interface_version") && contains(w, "compatible") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected tool_interface_version compatibility warning, got: %v", result.ImportWarnings)
	}
}
