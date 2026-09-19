package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

func TestSkillsStage_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{AgentID: "test", Version: "1.0.0", Entrypoint: "python main.py"}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &SkillsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if bc.SkillsCount != 0 {
		t.Errorf("SkillsCount = %d, want 0", bc.SkillsCount)
	}
}

func TestSkillsStage_WithSkills(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a SKILL.md
	skillsContent := `## Tool: web_search
Search the web for information.
**Input:** query: string
**Output:** results: []string

## Tool: summarize
Summarize text content.
`
	skillsPath := filepath.Join(tmpDir, "SKILL.md")
	if err := os.WriteFile(skillsPath, []byte(skillsContent), 0644); err != nil {
		t.Fatalf("writing skills.md: %v", err)
	}

	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("creating output dir: %v", err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{AgentID: "test", Version: "1.0.0", Entrypoint: "python main.py"}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &SkillsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if bc.SkillsCount != 2 {
		t.Errorf("SkillsCount = %d, want 2", bc.SkillsCount)
	}
	if bc.Spec.SkillsSpecVersion != "agentskills-v1" {
		t.Errorf("SkillsSpecVersion = %q, want %q", bc.Spec.SkillsSpecVersion, "agentskills-v1")
	}

	// Issue #147: the stage no longer writes compiled/skills/skills.json or
	// compiled/prompt.txt — the runtime never opened them and they bloated
	// the container image. Assert they are NOT present so a future change
	// reintroducing the dead writers is caught.
	if _, err := os.Stat(filepath.Join(outDir, "compiled", "skills", "skills.json")); err == nil {
		t.Error("compiled/skills/skills.json should not be generated (issue #147)")
	}
	if _, err := os.Stat(filepath.Join(outDir, "compiled", "prompt.txt")); err == nil {
		t.Error("compiled/prompt.txt should not be generated (issue #147)")
	}
}

// #481: skills.path pointing INSIDE the skills/ tree (skills/<name>/SKILL.md)
// must not double-count — the subdir scan already discovers it, so the root
// parse must be skipped.
func TestSkillsStage_PathInsideSkillsTreeNotDoubled(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "weather")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "## Tool: get_weather\nFetch weather.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID: "test", Version: "1.0.0", Entrypoint: "python main.py",
		Skills: types.SkillsRef{Path: "skills/weather/SKILL.md"}, // points inside skills/
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	if err := (&SkillsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// One skill (one tool) — NOT 2. Before the fix this was doubled.
	if bc.SkillsCount != 1 {
		t.Errorf("SkillsCount = %d, want 1 (skills.path inside skills/ must not double-count)", bc.SkillsCount)
	}
}

func TestSkillsStage_CustomPath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create skills at custom path
	skillsContent := `## Tool: custom_skill
A custom skill.
`
	customDir := filepath.Join(tmpDir, "custom")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("creating custom dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "my-skills.md"), []byte(skillsContent), 0644); err != nil {
		t.Fatalf("writing skills file: %v", err)
	}

	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("creating output dir: %v", err)
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Skills:     types.SkillsRef{Path: "custom/my-skills.md"},
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &SkillsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if bc.SkillsCount != 1 {
		t.Errorf("SkillsCount = %d, want 1", bc.SkillsCount)
	}
}
