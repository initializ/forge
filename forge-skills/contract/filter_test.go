package contract

import (
	"testing"
)

func TestFilterSkills_EmptyFilter(t *testing.T) {
	skills := []SkillDescriptor{
		{Name: "a", Category: "sre", Tags: []string{"kubernetes"}},
		{Name: "b", Category: "dev", Tags: []string{"ci"}},
	}
	result := FilterSkills(skills, SkillFilter{})
	if len(result) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(result))
	}
}

func TestFilterSkills_ByCategory(t *testing.T) {
	skills := []SkillDescriptor{
		{Name: "a", Category: "sre"},
		{Name: "b", Category: "dev"},
		{Name: "c", Category: "sre"},
	}
	result := FilterSkills(skills, SkillFilter{Category: "sre"})
	if len(result) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(result))
	}
	if result[0].Name != "a" || result[1].Name != "c" {
		t.Errorf("unexpected skills: %v", result)
	}
}

func TestFilterSkills_ByCategoryInsensitive(t *testing.T) {
	skills := []SkillDescriptor{
		{Name: "a", Category: "SRE"},
		{Name: "b", Category: "dev"},
	}
	result := FilterSkills(skills, SkillFilter{Category: "sre"})
	if len(result) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(result))
	}
	if result[0].Name != "a" {
		t.Errorf("expected skill 'a', got %q", result[0].Name)
	}
}

func TestFilterSkills_ByTags(t *testing.T) {
	skills := []SkillDescriptor{
		{Name: "a", Tags: []string{"kubernetes", "triage"}},
		{Name: "b", Tags: []string{"kubernetes"}},
		{Name: "c", Tags: []string{"ci", "triage"}},
	}
	result := FilterSkills(skills, SkillFilter{Tags: []string{"kubernetes", "triage"}})
	if len(result) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(result))
	}
	if result[0].Name != "a" {
		t.Errorf("expected skill 'a', got %q", result[0].Name)
	}
}

func TestFilterSkills_CombinedCategoryAndTags(t *testing.T) {
	skills := []SkillDescriptor{
		{Name: "a", Category: "sre", Tags: []string{"kubernetes", "triage"}},
		{Name: "b", Category: "sre", Tags: []string{"ci"}},
		{Name: "c", Category: "dev", Tags: []string{"kubernetes", "triage"}},
	}
	result := FilterSkills(skills, SkillFilter{Category: "sre", Tags: []string{"kubernetes"}})
	if len(result) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(result))
	}
	if result[0].Name != "a" {
		t.Errorf("expected skill 'a', got %q", result[0].Name)
	}
}

func TestFilterSkills_NoMatch(t *testing.T) {
	skills := []SkillDescriptor{
		{Name: "a", Category: "sre", Tags: []string{"kubernetes"}},
	}
	result := FilterSkills(skills, SkillFilter{Category: "nonexistent"})
	if len(result) != 0 {
		t.Fatalf("expected 0 skills, got %d", len(result))
	}
}
