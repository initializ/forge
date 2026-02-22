package export

import (
	"encoding/json"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
)

func TestValidateForExport_DevTool(t *testing.T) {
	spec := &agentspec.AgentSpec{
		Tools: []agentspec.ToolSpec{
			{Name: "local_shell", Category: "dev"},
		},
	}

	v := ValidateForExport(spec, false)
	if len(v.Errors) == 0 {
		t.Error("expected error for dev tool in non-dev mode")
	}

	v = ValidateForExport(spec, true)
	if len(v.Errors) > 0 {
		t.Error("expected no errors for dev tool in dev mode")
	}
}

func TestValidateForExport_Warnings(t *testing.T) {
	spec := &agentspec.AgentSpec{
		EgressMode: "dev-open",
		Tools: []agentspec.ToolSpec{
			{Name: "http_request"},
		},
	}

	v := ValidateForExport(spec, true)
	if len(v.Warnings) == 0 {
		t.Error("expected warnings for dev-open egress and empty tool_interface_version")
	}
}

func TestBuildEnvelope_Basic(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID:      "test-agent",
		Version:      "1.0",
		ForgeVersion: "1.0",
		Name:         "Test Agent",
		Tools: []agentspec.ToolSpec{
			{Name: "http_request", Category: "builtin"},
			{Name: "my_tool", Category: "custom"},
		},
		EgressProfile: "strict",
		EgressMode:    "allowlist",
	}

	envelope, err := BuildEnvelope(spec, []string{"api.openai.com"}, "0.1.0")
	if err != nil {
		t.Fatalf("BuildEnvelope() error: %v", err)
	}

	// Check meta
	meta, ok := envelope["_forge_export_meta"].(map[string]any)
	if !ok {
		t.Fatal("missing _forge_export_meta")
	}
	if meta["forge_cli_version"] != "0.1.0" {
		t.Errorf("forge_cli_version = %v, want 0.1.0", meta["forge_cli_version"])
	}

	cats, ok := meta["tool_categories"].(map[string]int)
	if !ok {
		t.Fatal("missing tool_categories")
	}
	if cats["builtin"] != 1 || cats["custom"] != 1 {
		t.Errorf("tool_categories = %v", cats)
	}

	// Check security block
	sec, ok := envelope["security"].(map[string]any)
	if !ok {
		t.Fatal("missing security block")
	}
	egress := sec["egress"].(map[string]any)
	if egress["profile"] != "strict" {
		t.Errorf("egress.profile = %v, want strict", egress["profile"])
	}
	domains, ok := egress["allowed_domains"].([]string)
	if !ok || len(domains) != 1 || domains[0] != "api.openai.com" {
		t.Errorf("egress.allowed_domains = %v", egress["allowed_domains"])
	}

	// Check network_policy block
	np, ok := envelope["network_policy"].(map[string]any)
	if !ok {
		t.Fatal("missing network_policy block")
	}
	if np["default_egress"] != "deny" {
		t.Errorf("network_policy.default_egress = %v", np["default_egress"])
	}
}

func TestBuildEnvelope_NoEgress(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID:      "test-agent",
		Version:      "1.0",
		ForgeVersion: "1.0",
		Name:         "Test Agent",
	}

	envelope, err := BuildEnvelope(spec, nil, "0.1.0")
	if err != nil {
		t.Fatalf("BuildEnvelope() error: %v", err)
	}

	if _, ok := envelope["security"]; ok {
		t.Error("should not have security block when no egress config")
	}
	if _, ok := envelope["network_policy"]; ok {
		t.Error("should not have network_policy block when no egress config")
	}
}

func TestBuildEnvelope_WithSkills(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID:      "test-agent",
		Version:      "1.0",
		ForgeVersion: "1.0",
		Name:         "Test Agent",
		A2A: &agentspec.A2AConfig{
			Skills: []agentspec.A2ASkill{
				{ID: "skill-1", Name: "Skill 1"},
				{ID: "skill-2", Name: "Skill 2"},
			},
		},
	}

	envelope, err := BuildEnvelope(spec, nil, "0.1.0")
	if err != nil {
		t.Fatalf("BuildEnvelope() error: %v", err)
	}

	meta := envelope["_forge_export_meta"].(map[string]any)
	count, _ := meta["skills_count"].(int)
	if count != 2 {
		t.Errorf("skills_count = %v, want 2", meta["skills_count"])
	}
}

func TestBuildEnvelope_Roundtrip(t *testing.T) {
	spec := &agentspec.AgentSpec{
		AgentID:       "roundtrip-agent",
		Version:       "2.0",
		ForgeVersion:  "1.1",
		Name:          "Roundtrip",
		EgressProfile: "standard",
		EgressMode:    "allowlist",
	}

	envelope, err := BuildEnvelope(spec, []string{"example.com"}, "0.2.0")
	if err != nil {
		t.Fatalf("BuildEnvelope() error: %v", err)
	}

	// Should be marshalable to JSON
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	if len(data) == 0 {
		t.Error("exported JSON is empty")
	}
}

func TestValidateProdConfig_DevOpen(t *testing.T) {
	v := ValidateProdConfig("dev-open", nil)
	if len(v.Errors) == 0 {
		t.Error("expected error for dev-open egress mode")
	}
}

func TestValidateProdConfig_DevTool(t *testing.T) {
	v := ValidateProdConfig("allowlist", []string{"http_request", "local_shell"})
	if len(v.Errors) == 0 {
		t.Error("expected error for dev tool local_shell")
	}
}

func TestValidateProdConfig_Clean(t *testing.T) {
	v := ValidateProdConfig("allowlist", []string{"http_request", "web_search"})
	if len(v.Errors) > 0 {
		t.Errorf("expected no errors, got: %v", v.Errors)
	}
}
