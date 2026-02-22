package registry

import (
	"strings"
	"testing"
)

func TestLoadIndex(t *testing.T) {
	skills, err := LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("LoadIndex() returned empty list")
	}

	// Verify expected entries exist
	names := make(map[string]bool)
	for _, s := range skills {
		names[s.Name] = true
		if s.DisplayName == "" {
			t.Errorf("skill %q has empty display_name", s.Name)
		}
		if s.Description == "" {
			t.Errorf("skill %q has empty description", s.Name)
		}
		if s.SkillFile == "" {
			t.Errorf("skill %q has empty skill_file", s.Name)
		}
	}

	for _, expected := range []string{"summarize", "github", "weather"} {
		if !names[expected] {
			t.Errorf("expected skill %q not found in index", expected)
		}
	}
}

func TestLoadSkillFile(t *testing.T) {
	skills, err := LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex() error: %v", err)
	}

	for _, s := range skills {
		data, err := LoadSkillFile(s.Name)
		if err != nil {
			t.Errorf("LoadSkillFile(%q) error: %v", s.Name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("LoadSkillFile(%q) returned empty content", s.Name)
		}
		// Verify it's valid markdown with at least one tool heading
		content := string(data)
		if !strings.Contains(content, "## Tool:") {
			t.Errorf("LoadSkillFile(%q) missing '## Tool:' heading", s.Name)
		}
	}
}

func TestGetSkillByName(t *testing.T) {
	s := GetSkillByName("github")
	if s == nil {
		t.Fatal("GetSkillByName(\"github\") returned nil")
	}
	if s.DisplayName != "GitHub" {
		t.Errorf("expected display_name \"GitHub\", got %q", s.DisplayName)
	}

	if GetSkillByName("nonexistent") != nil {
		t.Error("GetSkillByName(\"nonexistent\") should return nil")
	}
}

func TestGitHubSkillRequirements(t *testing.T) {
	s := GetSkillByName("github")
	if s == nil {
		t.Fatal("github skill not found")
	}
	if len(s.RequiredEnv) == 0 {
		t.Error("github skill should have required_env")
	}
	if len(s.RequiredBins) == 0 {
		t.Error("github skill should have required_bins")
	}
	if len(s.EgressDomains) == 0 {
		t.Error("github skill should have egress_domains")
	}
}

func TestWeatherSkillRequiredBins(t *testing.T) {
	s := GetSkillByName("weather")
	if s == nil {
		t.Fatal("weather skill not found")
	}
	if len(s.RequiredBins) == 0 {
		t.Error("weather skill should have required_bins")
	}
	found := false
	for _, b := range s.RequiredBins {
		if b == "curl" {
			found = true
		}
	}
	if !found {
		t.Error("weather skill should require curl binary")
	}
}
