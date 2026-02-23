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

	for _, expected := range []string{"summarize", "github", "weather", "tavily-search"} {
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

func TestTavilySearchSkillRequirements(t *testing.T) {
	s := GetSkillByName("tavily-search")
	if s == nil {
		t.Fatal("tavily-search skill not found")
	}
	if s.DisplayName != "Tavily Search" {
		t.Errorf("expected display_name \"Tavily Search\", got %q", s.DisplayName)
	}
	if len(s.RequiredEnv) == 0 {
		t.Error("tavily-search skill should have required_env")
	}
	foundKey := false
	for _, env := range s.RequiredEnv {
		if env == "TAVILY_API_KEY" {
			foundKey = true
		}
	}
	if !foundKey {
		t.Error("tavily-search skill should require TAVILY_API_KEY")
	}
	if len(s.RequiredBins) < 2 {
		t.Error("tavily-search skill should require curl and jq")
	}
	if len(s.EgressDomains) == 0 {
		t.Error("tavily-search skill should have egress_domains")
	}
	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.tavily.com" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Error("tavily-search skill should have api.tavily.com egress domain")
	}
}

func TestLoadSkillScript(t *testing.T) {
	// tavily-search should have a script
	if !HasSkillScript("tavily-search") {
		t.Fatal("HasSkillScript(\"tavily-search\") returned false")
	}

	data, err := LoadSkillScript("tavily-search")
	if err != nil {
		t.Fatalf("LoadSkillScript(\"tavily-search\") error: %v", err)
	}
	if len(data) == 0 {
		t.Error("LoadSkillScript(\"tavily-search\") returned empty content")
	}
	if !strings.Contains(string(data), "TAVILY_API_KEY") {
		t.Error("tavily-search script should reference TAVILY_API_KEY")
	}

	// Skills without scripts should return false
	if HasSkillScript("github") {
		t.Error("HasSkillScript(\"github\") should return false")
	}
	if HasSkillScript("nonexistent") {
		t.Error("HasSkillScript(\"nonexistent\") should return false")
	}
}
