package local

import (
	"strings"
	"testing"
)

func TestEmbeddedRegistry_DiscoverAll(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(skills) != 3 {
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		t.Fatalf("expected 3 skills, got %d: %v", len(skills), names)
	}

	// Verify all expected skills are present
	expectedSkills := map[string]struct {
		displayName string
		hasEnv      bool
		hasBins     bool
		hasEgress   bool
	}{
		"github":        {displayName: "Github", hasEnv: true, hasBins: true, hasEgress: true},
		"weather":       {displayName: "Weather", hasEnv: false, hasBins: true, hasEgress: true},
		"tavily-search": {displayName: "Tavily Search", hasEnv: true, hasBins: true, hasEgress: true},
	}

	for _, s := range skills {
		exp, ok := expectedSkills[s.Name]
		if !ok {
			t.Errorf("unexpected skill %q", s.Name)
			continue
		}
		if s.DisplayName != exp.displayName {
			t.Errorf("skill %q: DisplayName = %q, want %q", s.Name, s.DisplayName, exp.displayName)
		}
		if s.Description == "" {
			t.Errorf("skill %q: empty Description", s.Name)
		}
		if exp.hasEnv && len(s.RequiredEnv) == 0 {
			t.Errorf("skill %q: expected RequiredEnv", s.Name)
		}
		if exp.hasBins && len(s.RequiredBins) == 0 {
			t.Errorf("skill %q: expected RequiredBins", s.Name)
		}
		if exp.hasEgress && len(s.EgressDomains) == 0 {
			t.Errorf("skill %q: expected EgressDomains", s.Name)
		}
	}
}

func TestEmbeddedRegistry_GitHubDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("github")
	if s == nil {
		t.Fatal("Get(\"github\") returned nil")
	}
	if s.Description != "Create issues, PRs, and query repositories" {
		t.Errorf("Description = %q", s.Description)
	}
	if len(s.RequiredEnv) != 1 || s.RequiredEnv[0] != "GH_TOKEN" {
		t.Errorf("RequiredEnv = %v", s.RequiredEnv)
	}
	if len(s.RequiredBins) != 1 || s.RequiredBins[0] != "gh" {
		t.Errorf("RequiredBins = %v", s.RequiredBins)
	}

	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.github.com" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Errorf("EgressDomains = %v, want api.github.com", s.EgressDomains)
	}
}

func TestEmbeddedRegistry_TavilySearchDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("tavily-search")
	if s == nil {
		t.Fatal("Get(\"tavily-search\") returned nil")
	}
	if len(s.RequiredEnv) != 1 || s.RequiredEnv[0] != "TAVILY_API_KEY" {
		t.Errorf("RequiredEnv = %v", s.RequiredEnv)
	}
	if len(s.RequiredBins) < 2 {
		t.Errorf("RequiredBins = %v, want at least [curl, jq]", s.RequiredBins)
	}

	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.tavily.com" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Errorf("EgressDomains = %v, want api.tavily.com", s.EgressDomains)
	}

	// Check script
	if !reg.HasScript("tavily-search") {
		t.Error("tavily-search should have a script")
	}
	script, err := reg.LoadScript("tavily-search")
	if err != nil {
		t.Fatalf("LoadScript error: %v", err)
	}
	if !strings.Contains(string(script), "TAVILY_API_KEY") {
		t.Error("script should reference TAVILY_API_KEY")
	}
}

func TestEmbeddedRegistry_LoadContent(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	skills, _ := reg.List()
	for _, s := range skills {
		content, err := reg.LoadContent(s.Name)
		if err != nil {
			t.Errorf("LoadContent(%q) error: %v", s.Name, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("LoadContent(%q) returned empty content", s.Name)
		}
		if !strings.Contains(string(content), "## Tool:") {
			t.Errorf("LoadContent(%q) missing '## Tool:' heading", s.Name)
		}
	}
}

func TestEmbeddedRegistry_NonexistentSkill(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	if reg.Get("nonexistent") != nil {
		t.Error("Get(\"nonexistent\") should return nil")
	}
	if reg.HasScript("nonexistent") {
		t.Error("HasScript(\"nonexistent\") should return false")
	}
}
