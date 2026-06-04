package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/types"
)

// Regression test for issue #85: at runtime (both forge dev with no
// build artifact AND forge run post-build), the Runner must walk
// SKILL.md frontmatter from the agent's workdir and append discovered
// skills to the published Agent Card.

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnrichAgentCardWithSkills_AddsParsedSKILLmd(t *testing.T) {
	workDir := t.TempDir()

	writeSkillFile(t, filepath.Join(workDir, "skills", "weather.md"), `---
name: weather
description: Look up the current weather
category: info-retrieval
tags: [web]
---
# Weather

## Tool: get_weather
Fetches the weather.
`)

	r := &Runner{cfg: RunnerConfig{WorkDir: workDir, Config: &types.ForgeConfig{AgentID: "test"}}}
	card := &a2a.AgentCard{Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0"}

	r.enrichAgentCardWithSkills(card)

	if len(card.Skills) != 1 {
		t.Fatalf("expected 1 skill on card, got %d (%+v)", len(card.Skills), card.Skills)
	}
	s := card.Skills[0]
	if s.ID != "weather" || s.Name != "weather" {
		t.Errorf("ID/Name = %q/%q, want weather/weather", s.ID, s.Name)
	}
	if s.Description != "Look up the current weather" {
		t.Errorf("Description = %q, want frontmatter value", s.Description)
	}
	if len(s.Tags) == 0 || s.Tags[0] != "info-retrieval" {
		t.Errorf("Tags = %v, want category-first then frontmatter tags", s.Tags)
	}
}

func TestEnrichAgentCardWithSkills_PreservesExistingSkills(t *testing.T) {
	workDir := t.TempDir()
	writeSkillFile(t, filepath.Join(workDir, "skills", "extra.md"), `---
name: extra
description: A runtime-discovered skill
---
body
`)

	r := &Runner{cfg: RunnerConfig{WorkDir: workDir, Config: &types.ForgeConfig{AgentID: "test"}}}
	// Pre-existing skill on card (from AgentSpec.A2A.Skills typically).
	// The enrichment must NOT clobber it.
	card := &a2a.AgentCard{
		Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0",
		Skills: []a2a.Skill{
			{ID: "extra", Name: "Custom Display Name", Description: "from spec", Tags: []string{"spec"}},
			{ID: "tool", Name: "tool", Tags: []string{"tool"}},
		},
	}

	r.enrichAgentCardWithSkills(card)

	// Should still have exactly the two original entries (extra was
	// already present, dedup keeps the original).
	if len(card.Skills) != 2 {
		t.Fatalf("expected 2 skills (no new appends due to dedup), got %d", len(card.Skills))
	}
	for _, s := range card.Skills {
		if s.ID == "extra" && s.Description != "from spec" {
			t.Errorf("existing skill clobbered: %+v", s)
		}
	}
}

func TestEnrichAgentCardWithSkills_NoOpWhenNoSKILLmd(t *testing.T) {
	workDir := t.TempDir() // empty
	r := &Runner{cfg: RunnerConfig{WorkDir: workDir, Config: &types.ForgeConfig{AgentID: "test"}}}
	card := &a2a.AgentCard{Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0"}

	r.enrichAgentCardWithSkills(card)

	if len(card.Skills) != 0 {
		t.Errorf("expected empty skill list, got %+v", card.Skills)
	}
}

func TestEnrichAgentCardWithSkills_SkipsSKILLmdWithoutName(t *testing.T) {
	workDir := t.TempDir()
	writeSkillFile(t, filepath.Join(workDir, "skills", "noname.md"), `---
description: No name field, no A2A identity
---
body
`)

	r := &Runner{cfg: RunnerConfig{WorkDir: workDir, Config: &types.ForgeConfig{AgentID: "test"}}}
	card := &a2a.AgentCard{Name: "test", URL: "http://x", Version: "0.1.0", ProtocolVersion: "0.3.0"}

	r.enrichAgentCardWithSkills(card)
	if len(card.Skills) != 0 {
		t.Errorf("nameless SKILL.md should not produce a card skill, got %+v", card.Skills)
	}
}
