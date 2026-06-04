package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
)

// Regression test for issue #85: forge build must populate
// spec.A2A.Skills from SKILL.md frontmatter so the post-build
// AgentCard (and any consumer of agent.json) advertises the agent's
// skill surface.

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDiscoverBuildTimeSkills_PicksUpFlatAndSubdirFormats(t *testing.T) {
	workDir := t.TempDir()

	writeFile(t, filepath.Join(workDir, "skills", "weather.md"), `---
name: weather
description: Look up the current weather
category: info
tags: [web, json]
---
# Weather skill body
`)
	writeFile(t, filepath.Join(workDir, "skills", "github", "SKILL.md"), `---
name: github
description: Open issues on GitHub
---
# GitHub skill body
`)

	got := discoverBuildTimeSkills(workDir, "")
	if len(got) != 2 {
		t.Fatalf("expected 2 skills, got %d (%+v)", len(got), got)
	}

	// Deterministically ordered by ID after populateA2ASkillsFromSKILLmd
	// sorts; discoverBuildTimeSkills itself preserves discovery order
	// minus dedup, so the test only checks set membership.
	byID := map[string]agentspec.A2ASkill{}
	for _, s := range got {
		byID[s.ID] = s
	}
	if _, ok := byID["weather"]; !ok {
		t.Errorf("flat-format skill weather.md not discovered")
	}
	if _, ok := byID["github"]; !ok {
		t.Errorf("subdir-format skill github/SKILL.md not discovered")
	}
	// Frontmatter fields propagate.
	w := byID["weather"]
	if w.Description != "Look up the current weather" {
		t.Errorf("description = %q, want frontmatter value", w.Description)
	}
	if len(w.Tags) == 0 || w.Tags[0] != "info" {
		t.Errorf("tags = %v, want category-first then frontmatter tags", w.Tags)
	}
}

func TestDiscoverBuildTimeSkills_SkipsFilesWithoutName(t *testing.T) {
	workDir := t.TempDir()
	writeFile(t, filepath.Join(workDir, "skills", "noname.md"), `---
description: Skill without a name field
---
body
`)
	got := discoverBuildTimeSkills(workDir, "")
	if len(got) != 0 {
		t.Errorf("expected 0 skills (no name → skipped), got %+v", got)
	}
}

func TestDiscoverBuildTimeSkills_DedupesAcrossDiscoveryPaths(t *testing.T) {
	workDir := t.TempDir()
	body := `---
name: dup
description: First copy
---
`
	writeFile(t, filepath.Join(workDir, "skills", "dup.md"), body)
	writeFile(t, filepath.Join(workDir, "skills", "dup", "SKILL.md"), `---
name: dup
description: Second copy (should be skipped)
---
`)
	got := discoverBuildTimeSkills(workDir, "")
	if len(got) != 1 {
		t.Fatalf("expected 1 skill after dedup, got %d", len(got))
	}
	if got[0].Description != "First copy" {
		t.Errorf("first-wins dedup expected, got %q", got[0].Description)
	}
}

func TestPopulateA2ASkillsFromSKILLmd_SortsByID(t *testing.T) {
	workDir := t.TempDir()
	writeFile(t, filepath.Join(workDir, "skills", "zoo.md"), `---
name: zoo
---
`)
	writeFile(t, filepath.Join(workDir, "skills", "alpha.md"), `---
name: alpha
---
`)
	writeFile(t, filepath.Join(workDir, "skills", "mango.md"), `---
name: mango
---
`)

	spec := &agentspec.AgentSpec{AgentID: "test"}
	populateA2ASkillsFromSKILLmd(spec, workDir, "")

	if spec.A2A == nil {
		t.Fatalf("A2A block should be created when skills are discovered")
	}
	ids := []string{}
	for _, s := range spec.A2A.Skills {
		ids = append(ids, s.ID)
	}
	want := []string{"alpha", "mango", "zoo"}
	for i, w := range want {
		if i >= len(ids) || ids[i] != w {
			t.Errorf("Skills order = %v, want %v (sorted by ID for stable agent.json bytes)", ids, want)
			break
		}
	}
}

func TestPopulateA2ASkillsFromSKILLmd_NoOpWhenNoSkillsFound(t *testing.T) {
	workDir := t.TempDir()
	spec := &agentspec.AgentSpec{AgentID: "test"}
	populateA2ASkillsFromSKILLmd(spec, workDir, "")
	if spec.A2A != nil {
		t.Errorf("A2A should remain nil when no SKILL.md files exist, got %+v", spec.A2A)
	}
}

func TestPopulateA2ASkillsFromSKILLmd_HonorsCustomSkillsPath(t *testing.T) {
	workDir := t.TempDir()
	writeFile(t, filepath.Join(workDir, "AGENT.md"), `---
name: main
description: Custom-named main skill
---
`)

	spec := &agentspec.AgentSpec{AgentID: "test"}
	populateA2ASkillsFromSKILLmd(spec, workDir, "AGENT.md")

	if spec.A2A == nil || len(spec.A2A.Skills) != 1 {
		t.Fatalf("expected 1 skill from custom path, got %+v", spec.A2A)
	}
	if spec.A2A.Skills[0].Name != "main" {
		t.Errorf("Name = %q, want main", spec.A2A.Skills[0].Name)
	}
}
