package validate

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
)

func validAgentSpec() *agentspec.AgentSpec {
	return &agentspec.AgentSpec{
		ForgeVersion:         "1.0",
		AgentID:              "test-agent",
		Version:              "0.1.0",
		Name:                 "Test Agent",
		ToolInterfaceVersion: "1.0",
		Runtime: &agentspec.RuntimeConfig{
			Image: "python:3.11-slim",
			Port:  8080,
		},
		Tools: []agentspec.ToolSpec{
			{
				Name:        "web-search",
				Description: "Search the web",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		},
		PolicyScaffold: &agentspec.PolicyScaffold{
			Guardrails: []agentspec.Guardrail{
				{Type: "content_filter"},
			},
		},
		A2A: &agentspec.A2AConfig{
			Capabilities: &agentspec.A2ACapabilities{
				Streaming: true,
			},
		},
		Model: &agentspec.ModelConfig{
			Provider: "openai",
			Name:     "gpt-4",
		},
	}
}

func TestValidateCommandCompat_ValidSpec(t *testing.T) {
	r := ValidateCommandCompat(validAgentSpec())
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("expected no warnings, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_InvalidAgentID(t *testing.T) {
	spec := validAgentSpec()
	spec.AgentID = "INVALID_ID!"
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for bad agent_id")
	}
	found := false
	for _, e := range r.Errors {
		if contains(e, "agent_id") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected agent_id error, got: %v", r.Errors)
	}
}

func TestValidateCommandCompat_EmptyAgentID(t *testing.T) {
	spec := validAgentSpec()
	spec.AgentID = ""
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for empty agent_id")
	}
}

func TestValidateCommandCompat_UnsupportedForgeVersion(t *testing.T) {
	spec := validAgentSpec()
	spec.ForgeVersion = "2.0"
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for unsupported forge_version")
	}
	found := false
	for _, e := range r.Errors {
		if contains(e, "forge_version") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected forge_version error, got: %v", r.Errors)
	}
}

func TestValidateCommandCompat_ForgeVersion11(t *testing.T) {
	spec := validAgentSpec()
	spec.ForgeVersion = "1.1"
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("forge_version 1.1 should be valid, got errors: %v", r.Errors)
	}
}

func TestValidateCommandCompat_MissingRuntime(t *testing.T) {
	spec := validAgentSpec()
	spec.Runtime = nil
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for nil runtime")
	}
}

func TestValidateCommandCompat_EmptyRuntimeImage(t *testing.T) {
	spec := validAgentSpec()
	spec.Runtime.Image = ""
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for empty runtime.image")
	}
}

func TestValidateCommandCompat_UnknownGuardrails(t *testing.T) {
	spec := validAgentSpec()
	spec.PolicyScaffold = &agentspec.PolicyScaffold{
		Guardrails: []agentspec.Guardrail{
			{Type: "content_filter"},
			{Type: "custom_unknown_type"},
		},
	}
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("unknown guardrails should produce warnings not errors, got errors: %v", r.Errors)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected warnings for unknown guardrail type")
	}
}

func TestValidateCommandCompat_MissingA2A(t *testing.T) {
	spec := validAgentSpec()
	spec.A2A = nil
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("missing a2a should produce warnings not errors, got errors: %v", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if contains(w, "a2a") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a2a warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_MissingA2ACapabilities(t *testing.T) {
	spec := validAgentSpec()
	spec.A2A = &agentspec.A2AConfig{}
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("missing capabilities should produce warnings not errors, got errors: %v", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if contains(w, "capabilities") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected capabilities warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_MissingModel(t *testing.T) {
	spec := validAgentSpec()
	spec.Model = nil
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("missing model should produce warnings not errors, got errors: %v", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if contains(w, "model") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected model warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_EmptyModelProvider(t *testing.T) {
	spec := validAgentSpec()
	spec.Model = &agentspec.ModelConfig{Name: "gpt-4"}
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("empty model.provider should produce warnings not errors, got errors: %v", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if contains(w, "model.provider") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected model.provider warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_InvalidToolSchema(t *testing.T) {
	spec := validAgentSpec()
	spec.Tools = []agentspec.ToolSpec{
		{
			Name:        "bad-tool",
			InputSchema: json.RawMessage(`{not valid json`),
		},
	}
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for bad tool input_schema")
	}
}

func TestValidateCommandCompat_MultipleIssues(t *testing.T) {
	spec := &agentspec.AgentSpec{
		ForgeVersion: "3.0",
		AgentID:      "INVALID!",
		// Name, Version empty
		// Runtime nil
	}
	r := ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected multiple errors")
	}
	// Should have errors for: agent_id pattern, name, version, forge_version, runtime
	if len(r.Errors) < 4 {
		t.Errorf("expected at least 4 errors, got %d: %v", len(r.Errors), r.Errors)
	}
}

// contains checks if substr is in s (case-insensitive-ish, just substring match).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestValidateCommandCompat_ToolInterfaceVersion(t *testing.T) {
	// Valid version
	spec := validAgentSpec()
	spec.ToolInterfaceVersion = "1.0"
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("expected valid for tool_interface_version 1.0, got errors: %v", r.Errors)
	}

	// Unsupported version
	spec.ToolInterfaceVersion = "2.0"
	r = ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for unsupported tool_interface_version")
	}
	found := false
	for _, e := range r.Errors {
		if contains(e, "tool_interface_version") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected tool_interface_version error, got: %v", r.Errors)
	}
}

func TestValidateCommandCompat_SkillsSpecVersion(t *testing.T) {
	// Valid version
	spec := validAgentSpec()
	spec.SkillsSpecVersion = "agentskills-v1"
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("expected valid for skills_spec_version agentskills-v1, got errors: %v", r.Errors)
	}

	// Unrecognized version
	spec.SkillsSpecVersion = "unknown-v2"
	r = ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for unrecognized skills_spec_version")
	}
	found := false
	for _, e := range r.Errors {
		if contains(e, "skills_spec_version") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skills_spec_version error, got: %v", r.Errors)
	}
}

func TestValidateCommandCompat_ToolCategories(t *testing.T) {
	spec := validAgentSpec()
	// Known category — no warning
	spec.Tools = []agentspec.ToolSpec{
		{Name: "t1", Category: "builtin", InputSchema: json.RawMessage(`{}`)},
	}
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	for _, w := range r.Warnings {
		if contains(w, "unknown category") {
			t.Errorf("unexpected unknown category warning: %s", w)
		}
	}

	// Unknown category — warning
	spec.Tools = []agentspec.ToolSpec{
		{Name: "t2", Category: "experimental", InputSchema: json.RawMessage(`{}`)},
	}
	r = ValidateCommandCompat(spec)
	found := false
	for _, w := range r.Warnings {
		if contains(w, "unknown category") && contains(w, "experimental") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown category warning for 'experimental', got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_SkillOriginValidation(t *testing.T) {
	spec := validAgentSpec()
	spec.A2A = &agentspec.A2AConfig{
		Skills: []agentspec.A2ASkill{
			{ID: "pdf-processing", Name: "PDF Processing", Description: "Processes PDFs"},
		},
		Capabilities: &agentspec.A2ACapabilities{Streaming: true},
	}

	// Valid reference
	spec.Tools = []agentspec.ToolSpec{
		{Name: "pdf-tool", SkillOrigin: "pdf-processing", InputSchema: json.RawMessage(`{}`)},
	}
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	for _, w := range r.Warnings {
		if contains(w, "skill_origin") && contains(w, "not found") {
			t.Errorf("unexpected dangling skill_origin warning: %s", w)
		}
	}

	// Dangling reference
	spec.Tools = []agentspec.ToolSpec{
		{Name: "orphan-tool", SkillOrigin: "nonexistent-skill", InputSchema: json.RawMessage(`{}`)},
	}
	r = ValidateCommandCompat(spec)
	found := false
	for _, w := range r.Warnings {
		if contains(w, "skill_origin") && contains(w, "not found") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dangling skill_origin warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_SkillIDPattern(t *testing.T) {
	spec := validAgentSpec()
	spec.A2A = &agentspec.A2AConfig{
		Skills: []agentspec.A2ASkill{
			{ID: "valid-skill", Name: "Valid Skill", Description: "A valid skill"},
		},
		Capabilities: &agentspec.A2ACapabilities{Streaming: true},
	}
	r := ValidateCommandCompat(spec)
	if !r.IsValid() {
		t.Fatalf("expected valid for valid skill ID, got errors: %v", r.Errors)
	}

	// Invalid skill ID
	spec.A2A.Skills = []agentspec.A2ASkill{
		{ID: "INVALID_SKILL!", Name: "Bad Skill", Description: "desc"},
	}
	r = ValidateCommandCompat(spec)
	if r.IsValid() {
		t.Fatal("expected invalid for bad skill ID")
	}
	found := false
	for _, e := range r.Errors {
		if contains(e, "a2a.skills") && contains(e, "must match") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skill ID pattern error, got: %v", r.Errors)
	}
}

func TestValidateCommandCompat_SkillDescriptionWarning(t *testing.T) {
	spec := validAgentSpec()
	spec.A2A = &agentspec.A2AConfig{
		Skills: []agentspec.A2ASkill{
			{ID: "no-desc", Name: "No Desc Skill"},
		},
		Capabilities: &agentspec.A2ACapabilities{Streaming: true},
	}
	r := ValidateCommandCompat(spec)
	found := false
	for _, w := range r.Warnings {
		if contains(w, "missing description") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing description warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_TooManySkills(t *testing.T) {
	spec := validAgentSpec()
	skills := make([]agentspec.A2ASkill, 21)
	for i := range skills {
		skills[i] = agentspec.A2ASkill{
			ID:          fmt.Sprintf("skill-%d", i),
			Name:        fmt.Sprintf("Skill %d", i),
			Description: "A skill",
		}
	}
	spec.A2A = &agentspec.A2AConfig{
		Skills:       skills,
		Capabilities: &agentspec.A2ACapabilities{Streaming: true},
	}
	r := ValidateCommandCompat(spec)
	found := false
	for _, w := range r.Warnings {
		if contains(w, ">20") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected >20 skills warning, got: %v", r.Warnings)
	}
}

func TestValidateCommandCompat_EgressModeWarning(t *testing.T) {
	spec := validAgentSpec()
	spec.EgressMode = "allowlist"
	spec.EgressProfile = ""
	r := ValidateCommandCompat(spec)
	found := false
	for _, w := range r.Warnings {
		if contains(w, "egress_mode") && contains(w, "allowlist") && contains(w, "egress_profile") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected egress_mode/profile warning, got: %v", r.Warnings)
	}
}
