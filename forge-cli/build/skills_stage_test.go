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

	// Create a skills.md
	skillsContent := `## Tool: web_search
Search the web for information.
**Input:** query: string
**Output:** results: []string

## Tool: summarize
Summarize text content.
`
	skillsPath := filepath.Join(tmpDir, "skills.md")
	if err := os.WriteFile(skillsPath, []byte(skillsContent), 0644); err != nil {
		t.Fatalf("writing skills.md: %v", err)
	}

	outDir := filepath.Join(tmpDir, "output")
	os.MkdirAll(outDir, 0755)

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

	// Check artifacts exist
	if _, err := os.Stat(filepath.Join(outDir, "compiled", "skills", "skills.json")); os.IsNotExist(err) {
		t.Error("skills.json not created")
	}
	if _, err := os.Stat(filepath.Join(outDir, "compiled", "prompt.txt")); os.IsNotExist(err) {
		t.Error("prompt.txt not created")
	}
}

func TestSkillsStage_CustomPath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create skills at custom path
	skillsContent := `## Tool: custom_skill
A custom skill.
`
	customDir := filepath.Join(tmpDir, "custom")
	os.MkdirAll(customDir, 0755)
	if err := os.WriteFile(filepath.Join(customDir, "my-skills.md"), []byte(skillsContent), 0644); err != nil {
		t.Fatalf("writing skills file: %v", err)
	}

	outDir := filepath.Join(tmpDir, "output")
	os.MkdirAll(outDir, 0755)

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
