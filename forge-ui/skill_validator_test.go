package forgeui

import (
	"testing"
)

func TestValidateSkillMDValid(t *testing.T) {
	content := `---
name: my-skill
description: A test skill
category: ops
tags:
  - testing
metadata:
  forge:
    requires:
      bins:
        - curl
      env:
        required:
          - MY_API_KEY
    egress_domains:
      - api.example.com
---

# My Skill

## Tool: my_tool

A test tool.

**Input:** query string
**Output:** JSON results
`

	result := validateSkillMD(content, nil, "")
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
}

func TestValidateSkillMDMissingFrontmatter(t *testing.T) {
	content := `# No frontmatter here

## Tool: my_tool

A test tool.
`

	result := validateSkillMD(content, nil, "")
	if result.Valid {
		t.Error("expected invalid for missing frontmatter")
	}

	hasNameErr := false
	for _, e := range result.Errors {
		if e.Field == "name" {
			hasNameErr = true
		}
	}
	if !hasNameErr {
		t.Error("expected name error")
	}
}

func TestValidateSkillMDMissingName(t *testing.T) {
	content := `---
description: A test skill
---

# My Skill
`

	result := validateSkillMD(content, nil, "")
	if result.Valid {
		t.Error("expected invalid for missing name")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "name" {
			found = true
		}
	}
	if !found {
		t.Error("expected name error")
	}
}

func TestValidateSkillMDInvalidNameFormat(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"uppercase", "MySkill", true},
		{"spaces", "my skill", true},
		{"path separator", "my/skill", true},
		{"dots", "my..skill", true},
		{"valid kebab", "my-skill", false},
		{"valid single", "skill", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "---\nname: " + tt.input + "\ndescription: test\n---\n# Test\n"
			result := validateSkillMD(content, nil, "")
			if tt.wantErr && result.Valid {
				t.Errorf("expected invalid for name %q", tt.input)
			}
			if !tt.wantErr && !result.Valid {
				t.Errorf("expected valid for name %q, got errors: %v", tt.input, result.Errors)
			}
		})
	}
}

func TestValidateSkillMDMissingDescription(t *testing.T) {
	content := `---
name: my-skill
---

# My Skill
`

	result := validateSkillMD(content, nil, "")
	if result.Valid {
		t.Error("expected invalid for missing description")
	}

	found := false
	for _, e := range result.Errors {
		if e.Field == "description" {
			found = true
		}
	}
	if !found {
		t.Error("expected description error")
	}
}

func TestValidateSkillMDNoToolsWarning(t *testing.T) {
	content := `---
name: my-skill
description: A test skill
---

# My Skill

Just some text, no tools.
`

	result := validateSkillMD(content, nil, "")
	if !result.Valid {
		t.Errorf("should be valid (missing tools is warning, not error)")
	}

	found := false
	for _, w := range result.Warnings {
		if w.Field == "body" {
			found = true
		}
	}
	if !found {
		t.Error("expected body warning for no tools")
	}
}

func TestDetectUndeclaredEgress(t *testing.T) {
	scripts := map[string]string{
		"fetch.sh": `#!/bin/bash
curl https://api.example.com/data
curl https://other-api.com/stuff`,
	}

	// Only api.example.com is declared
	undeclared := detectUndeclaredEgress(scripts, []string{"api.example.com"})

	if len(undeclared) != 1 {
		t.Fatalf("expected 1 undeclared domain, got %d: %v", len(undeclared), undeclared)
	}
	if undeclared[0] != "other-api.com" {
		t.Errorf("undeclared = %q, want %q", undeclared[0], "other-api.com")
	}
}

func TestDetectUndeclaredEgressAllDeclared(t *testing.T) {
	scripts := map[string]string{
		"fetch.sh": `curl https://api.example.com/data`,
	}

	undeclared := detectUndeclaredEgress(scripts, []string{"api.example.com"})
	if len(undeclared) != 0 {
		t.Errorf("expected 0 undeclared domains, got %v", undeclared)
	}
}

func TestExtractArtifacts(t *testing.T) {
	response := "Here is your skill:\n````skill.md\n---\nname: test\ndescription: test\n---\n# Test\n````\n\nAnd a script:\n````script:fetch.sh\n#!/bin/bash\necho hello\n````\n"

	skillMD, scripts := extractArtifacts(response)

	if skillMD == "" {
		t.Error("expected non-empty skillMD")
	}
	if !contains(skillMD, "name: test") {
		t.Errorf("skillMD should contain 'name: test', got: %q", skillMD)
	}

	if len(scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(scripts))
	}
	if _, ok := scripts["fetch.sh"]; !ok {
		t.Error("expected script 'fetch.sh'")
	}
	if !contains(scripts["fetch.sh"], "echo hello") {
		t.Errorf("script should contain 'echo hello', got: %q", scripts["fetch.sh"])
	}
}

func TestExtractArtifactsNestedBackticks(t *testing.T) {
	// Simulate LLM response where SKILL.md body contains inner triple-backtick JSON blocks
	response := "Here is your skill:\n````skill.md\n---\nname: nested-test\ndescription: Skill with inner code blocks\n---\n# Nested Test\n\n## Tool: my_tool\n\n**Output:**\n\n```json\n{\"summary\": \"result\", \"items\": []}\n```\n\n## Safety Constraints\n\n- Read-only\n````\n"

	skillMD, scripts := extractArtifacts(response)

	if skillMD == "" {
		t.Fatal("expected non-empty skillMD")
	}
	if !contains(skillMD, "name: nested-test") {
		t.Errorf("skillMD missing frontmatter, got: %q", skillMD)
	}
	if !contains(skillMD, "```json") {
		t.Errorf("skillMD should contain inner ```json block, got: %q", skillMD)
	}
	if !contains(skillMD, "Safety Constraints") {
		t.Errorf("skillMD should contain content after inner code block, got: %q", skillMD)
	}
	if len(scripts) != 0 {
		t.Errorf("expected 0 scripts, got %d", len(scripts))
	}
}

func TestExtractArtifactsNoMatch(t *testing.T) {
	response := "Just a regular response with no code fences."
	skillMD, scripts := extractArtifacts(response)

	if skillMD != "" {
		t.Errorf("expected empty skillMD, got %q", skillMD)
	}
	if len(scripts) != 0 {
		t.Errorf("expected 0 scripts, got %d", len(scripts))
	}
}

func TestValidateSkillMDUndeclaredEgressWarning(t *testing.T) {
	content := `---
name: my-skill
description: A test skill
---

# My Skill

## Tool: my_tool

A test tool.
`
	scripts := map[string]string{
		"fetch.sh": `curl https://api.example.com/data`,
	}

	result := validateSkillMD(content, scripts, "")
	if !result.Valid {
		t.Error("should be valid")
	}

	found := false
	for _, w := range result.Warnings {
		if w.Field == "egress_domains" {
			found = true
		}
	}
	if !found {
		t.Error("expected egress_domains warning for undeclared domain in scripts")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
