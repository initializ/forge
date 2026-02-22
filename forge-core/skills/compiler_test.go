package skills

import (
	"encoding/json"
	"strings"
	"testing"
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
	entries := []SkillEntry{
		{Name: "web_search", Description: "Search the web", InputSpec: "query: string", OutputSpec: "results: []string"},
		{Name: "summarize", Description: "Summarize text", InputSpec: "text: string"},
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
	entries := []SkillEntry{
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
	entries := []SkillEntry{
		{Name: "web_search", Description: "Search the internet"},
		{Name: "summarize", Description: "Summarize long text"},
	}

	cs, err := Compile(entries)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if !strings.Contains(cs.Prompt, "web_search") {
		t.Error("Prompt should contain skill name 'web_search'")
	}
	if !strings.Contains(cs.Prompt, "summarize") {
		t.Error("Prompt should contain skill name 'summarize'")
	}
	if !strings.Contains(cs.Prompt, "Search the internet") {
		t.Error("Prompt should contain skill description")
	}
}

func TestCompile_EmptyDescription(t *testing.T) {
	entries := []SkillEntry{
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
