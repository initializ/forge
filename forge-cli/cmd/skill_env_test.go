package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
	forgeui "github.com/initializ/forge/forge-ui"
)

func TestParseSkillRequirements(t *testing.T) {
	skillMD := `---
name: test-skill
description: A test skill
metadata:
  forge:
    requires:
      env:
        required:
          - MY_API_KEY
        optional:
          - MY_DEBUG
    egress_domains:
      - api.example.com
      - cdn.example.com
---

# Test Skill

## Tool: test_tool

A test tool.
`
	info := ParseSkillRequirements(skillMD)

	if info.EnvReqs == nil {
		t.Fatal("expected EnvReqs to be non-nil")
	}
	if len(info.EnvReqs.Required) != 1 || info.EnvReqs.Required[0] != "MY_API_KEY" {
		t.Errorf("Required = %v, want [MY_API_KEY]", info.EnvReqs.Required)
	}
	if len(info.EnvReqs.Optional) != 1 || info.EnvReqs.Optional[0] != "MY_DEBUG" {
		t.Errorf("Optional = %v, want [MY_DEBUG]", info.EnvReqs.Optional)
	}
	if len(info.EgressDomains) != 2 {
		t.Errorf("EgressDomains = %v, want 2 entries", info.EgressDomains)
	}
}

func TestParseSkillRequirementsNoMetadata(t *testing.T) {
	skillMD := `---
name: simple-skill
description: No forge metadata
---

## Tool: simple

A simple tool.
`
	info := ParseSkillRequirements(skillMD)
	if info.EnvReqs != nil {
		t.Errorf("expected nil EnvReqs for skill without forge metadata, got %v", info.EnvReqs)
	}
	if len(info.EgressDomains) != 0 {
		t.Errorf("expected no egress domains, got %v", info.EgressDomains)
	}
}

func TestMergeEgressDomains(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "forge.yaml")

	initial := `agent_id: test-agent
version: 0.1.0
model:
  provider: openai
  name: gpt-4o
egress:
  profile: standard
  mode: allowlist
  allowed_domains:
    - api.openai.com
    - api.tavily.com
`
	if err := os.WriteFile(yamlPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := MergeEgressDomains(dir, []string{"api.example.com", "api.openai.com", "cdn.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	// api.openai.com already exists, so only 2 should be added
	if len(added) != 2 {
		t.Fatalf("added = %v, want 2 new domains", added)
	}
	if added[0] != "api.example.com" || added[1] != "cdn.example.com" {
		t.Errorf("added = %v, want [api.example.com cdn.example.com]", added)
	}

	// Verify the file content
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "- api.example.com") {
		t.Error("file should contain api.example.com")
	}
	if !strings.Contains(content, "- cdn.example.com") {
		t.Error("file should contain cdn.example.com")
	}
	// Original domains should still be there
	if !strings.Contains(content, "- api.openai.com") {
		t.Error("file should still contain api.openai.com")
	}
}

func TestMergeEgressDomainsNoEgressSection(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "forge.yaml")

	initial := `agent_id: test-agent
version: 0.1.0
model:
  provider: openai
  name: gpt-4o
`
	if err := os.WriteFile(yamlPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := MergeEgressDomains(dir, []string{"api.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 1 {
		t.Fatalf("added = %v, want 1", added)
	}

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "egress:") {
		t.Error("file should contain egress section")
	}
	if !strings.Contains(content, "allowed_domains:") {
		t.Error("file should contain allowed_domains")
	}
	if !strings.Contains(content, "- api.example.com") {
		t.Error("file should contain api.example.com")
	}
}

func TestMergeEgressDomainsAllExist(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "forge.yaml")

	initial := `egress:
  allowed_domains:
    - api.openai.com
`
	if err := os.WriteFile(yamlPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := MergeEgressDomains(dir, []string{"api.openai.com"})
	if err != nil {
		t.Fatal(err)
	}
	if added != nil {
		t.Errorf("expected nil for no new domains, got %v", added)
	}
}

func TestMergeEgressDomainsEmpty(t *testing.T) {
	dir := t.TempDir()
	added, err := MergeEgressDomains(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if added != nil {
		t.Errorf("expected nil, got %v", added)
	}
}

func TestAppendEnvVars(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")

	// Create initial .env
	if err := os.WriteFile(envPath, []byte("EXISTING_KEY=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	written, err := AppendEnvVars(dir, map[string]string{
		"NEW_KEY":      "new_value",
		"EXISTING_KEY": "should_skip",
		"ANOTHER_KEY":  "another_value",
	}, "test-skill")
	if err != nil {
		t.Fatal(err)
	}

	if len(written) != 2 {
		t.Fatalf("written = %v, want 2 keys", written)
	}

	// Verify file content
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "ANOTHER_KEY=another_value") {
		t.Error("file should contain ANOTHER_KEY=another_value")
	}
	if !strings.Contains(content, "NEW_KEY=new_value") {
		t.Error("file should contain NEW_KEY=new_value")
	}
	if !strings.Contains(content, "# Required by test-skill skill") {
		t.Error("file should contain comment with skill name")
	}
	// Existing key should not be duplicated
	if strings.Count(content, "EXISTING_KEY") != 1 {
		t.Error("EXISTING_KEY should appear exactly once")
	}
}

func TestAppendEnvVarsNoFile(t *testing.T) {
	dir := t.TempDir()

	written, err := AppendEnvVars(dir, map[string]string{
		"NEW_KEY": "value",
	}, "test-skill")
	if err != nil {
		t.Fatal(err)
	}

	if len(written) != 1 {
		t.Fatalf("written = %v, want 1", written)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "NEW_KEY=value") {
		t.Error("file should contain NEW_KEY=value")
	}
}

func TestAppendEnvVarsEmpty(t *testing.T) {
	dir := t.TempDir()
	written, err := AppendEnvVars(dir, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if written != nil {
		t.Errorf("expected nil, got %v", written)
	}
}

func TestCheckMissingEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("PRESENT_KEY=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set an OS env var for testing
	t.Setenv("OS_KEY", "os_value")

	reqs := &contract.EnvRequirements{
		Required: []string{"PRESENT_KEY", "MISSING_KEY", "OS_KEY"},
		OneOf:    []string{"ONE_A", "ONE_B"},
		Optional: []string{"OPT_MISSING"},
	}

	missing := CheckMissingEnv(dir, reqs)

	// PRESENT_KEY is in .env → not missing
	// OS_KEY is in OS env → not missing
	// MISSING_KEY → missing (required)
	// ONE_A, ONE_B → both missing (one_of group, neither set)
	// OPT_MISSING → missing (optional)

	expected := []forgeui.SkillEnvEntry{
		{Name: "MISSING_KEY", Kind: "required"},
		{Name: "ONE_A", Kind: "one_of"},
		{Name: "ONE_B", Kind: "one_of"},
		{Name: "OPT_MISSING", Kind: "optional"},
	}

	if len(missing) != len(expected) {
		t.Fatalf("missing = %v, want %v", missing, expected)
	}

	for i, got := range missing {
		if got.Name != expected[i].Name || got.Kind != expected[i].Kind {
			t.Errorf("missing[%d] = %v, want %v", i, got, expected[i])
		}
	}
}

func TestCheckMissingEnvNil(t *testing.T) {
	dir := t.TempDir()
	missing := CheckMissingEnv(dir, nil)
	if missing != nil {
		t.Errorf("expected nil for nil reqs, got %v", missing)
	}
}

func TestCheckMissingEnvOneOfSatisfied(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("ONE_B=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reqs := &contract.EnvRequirements{
		OneOf: []string{"ONE_A", "ONE_B"},
	}

	missing := CheckMissingEnv(dir, reqs)
	if len(missing) != 0 {
		t.Errorf("expected no missing when one_of is satisfied, got %v", missing)
	}
}

func TestMergeEgressDomainsWithEgressNoAllowedDomains(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "forge.yaml")

	initial := `agent_id: test-agent
egress:
  profile: standard
  mode: allowlist
`
	if err := os.WriteFile(yamlPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := MergeEgressDomains(dir, []string{"api.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	if len(added) != 1 || added[0] != "api.example.com" {
		t.Fatalf("added = %v, want [api.example.com]", added)
	}

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "allowed_domains:") {
		t.Error("file should contain allowed_domains")
	}
	if !strings.Contains(content, "- api.example.com") {
		t.Error("file should contain api.example.com")
	}
}
