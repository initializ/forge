package compiler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestCompile_Empty(t *testing.T) {
	cs, err := Compile(nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if cs.Count != 0 {
		t.Errorf("Count = %d, want 0", cs.Count)
	}
	if len(cs.Skills) != 0 {
		t.Errorf("Skills length = %d, want 0", len(cs.Skills))
	}
	if cs.Version != "agentskills-v1" {
		t.Errorf("Version = %q, want %q", cs.Version, "agentskills-v1")
	}
}

func TestCompile_MultipleSkills(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "web_search", Description: "Search the web", InputSpec: "query: string", OutputSpec: "results: []string"},
		{Name: "translate", Description: "Translate text", InputSpec: "text: string"},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if cs.Count != 2 {
		t.Errorf("Count = %d, want 2", cs.Count)
	}
	if cs.Skills[0].Name != "web_search" {
		t.Errorf("Skills[0].Name = %q, want %q", cs.Skills[0].Name, "web_search")
	}
	if cs.Skills[1].InputSpec != "text: string" {
		t.Errorf("Skills[1].InputSpec = %q, want %q", cs.Skills[1].InputSpec, "text: string")
	}

	// Check JSON serialization
	data, err := json.Marshal(cs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["count"].(float64) != 2 {
		t.Errorf("JSON count = %v, want 2", raw["count"])
	}

	// Check prompt is non-empty
	if cs.Prompt == "" {
		t.Error("Prompt should not be empty")
	}
}

func TestCompile_SingleSkill(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "translate", Description: "Translate text between languages", InputSpec: "text: string, target_lang: string", OutputSpec: "translated: string"},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if cs.Count != 1 {
		t.Errorf("Count = %d, want 1", cs.Count)
	}
	if cs.Version != "agentskills-v1" {
		t.Errorf("Version = %q, want %q", cs.Version, "agentskills-v1")
	}
	if cs.Prompt == "" {
		t.Error("Prompt should not be empty for a single skill")
	}
	if cs.Skills[0].Name != "translate" {
		t.Errorf("Skills[0].Name = %q, want %q", cs.Skills[0].Name, "translate")
	}
	if cs.Skills[0].OutputSpec != "translated: string" {
		t.Errorf("Skills[0].OutputSpec = %q", cs.Skills[0].OutputSpec)
	}
}

func TestCompile_PromptContainsNames(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "web_search", Description: "Search the internet"},
		{Name: "translate", Description: "Translate long text"},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !strings.Contains(cs.Prompt, "web_search") {
		t.Error("Prompt should contain skill name 'web_search'")
	}
	if !strings.Contains(cs.Prompt, "translate") {
		t.Error("Prompt should contain skill name 'translate'")
	}
	if !strings.Contains(cs.Prompt, "Search the internet") {
		t.Error("Prompt should contain skill description")
	}
}

func TestCompile_CategoryAndTagsPropagated(t *testing.T) {
	meta := &contract.SkillMetadata{
		Name:     "k8s-triage",
		Category: "sre",
		Tags:     []string{"kubernetes", "incident-response"},
	}
	entries := []contract.SkillEntry{
		{Name: "k8s_triage", Description: "Diagnose workloads", Metadata: meta},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if cs.Skills[0].Category != "sre" {
		t.Errorf("Category = %q, want sre", cs.Skills[0].Category)
	}
	if len(cs.Skills[0].Tags) != 2 || cs.Skills[0].Tags[0] != "kubernetes" {
		t.Errorf("Tags = %v, want [kubernetes incident-response]", cs.Skills[0].Tags)
	}
}

func TestCompile_NilMetadataNoCategory(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "simple", Description: "No metadata"},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if cs.Skills[0].Category != "" {
		t.Errorf("Category should be empty, got %q", cs.Skills[0].Category)
	}
	if cs.Skills[0].Tags != nil {
		t.Errorf("Tags should be nil, got %v", cs.Skills[0].Tags)
	}
}

func TestCompile_OutputFormat(t *testing.T) {
	t.Run("prompt contains output format when set", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "k8s_triage", Description: "Diagnose workloads", OutputFormat: "Use markdown tables for status summaries."},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if !strings.Contains(cs.Prompt, "Output format: Use markdown tables for status summaries.") {
			t.Errorf("Prompt should contain output format line, got:\n%s", cs.Prompt)
		}
	})

	t.Run("compiled skill has OutputFormat populated", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "k8s_triage", Description: "Diagnose workloads", OutputFormat: "Use code blocks."},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if cs.Skills[0].OutputFormat != "Use code blocks." {
			t.Errorf("OutputFormat = %q, want 'Use code blocks.'", cs.Skills[0].OutputFormat)
		}
	})

	t.Run("omitted from prompt when empty", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "simple", Description: "No format hint"},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if strings.Contains(cs.Prompt, "Output format:") {
			t.Errorf("Prompt should not contain 'Output format:' when empty, got:\n%s", cs.Prompt)
		}
	})

	t.Run("omitted from JSON when empty", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "simple", Description: "No format hint"},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		data, err := json.Marshal(cs.Skills[0])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(data), "output_format") {
			t.Errorf("JSON should omit output_format when empty, got: %s", string(data))
		}
	})
}

func TestCompile_Body(t *testing.T) {
	t.Run("prompt contains body when set", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "k8s_triage", Description: "Diagnose workloads", Body: "## Detection Heuristics\n\n- Check pod status"},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if !strings.Contains(cs.Prompt, "## Detection Heuristics") {
			t.Errorf("Prompt should contain body text, got:\n%s", cs.Prompt)
		}
		if !strings.Contains(cs.Prompt, "Check pod status") {
			t.Errorf("Prompt should contain body details, got:\n%s", cs.Prompt)
		}
	})

	t.Run("compiled skill has Body populated", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "k8s_triage", Description: "Diagnose workloads", Body: "Full instructions here"},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if cs.Skills[0].Body != "Full instructions here" {
			t.Errorf("Body = %q, want 'Full instructions here'", cs.Skills[0].Body)
		}
	})

	t.Run("omitted from JSON when empty", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "simple", Description: "No extra content"},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		data, err := json.Marshal(cs.Skills[0])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(data), `"body"`) {
			t.Errorf("JSON should omit body when empty, got: %s", string(data))
		}
	})

	t.Run("body not in prompt when empty", func(t *testing.T) {
		entries := []contract.SkillEntry{
			{Name: "simple", Description: "No body"},
		}

		cs, err := Compile(entries)
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		// Verify no body content leaked into prompt
		if strings.Contains(cs.Prompt, "Detection") {
			t.Errorf("Prompt should not contain body content when body is empty")
		}
	})
}

func TestCompile_EmptyDescription(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "no_desc_skill", Description: ""},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if cs.Count != 1 {
		t.Errorf("Count = %d, want 1", cs.Count)
	}
	if cs.Skills[0].Description != "" {
		t.Errorf("Description should be empty, got %q", cs.Skills[0].Description)
	}
}
