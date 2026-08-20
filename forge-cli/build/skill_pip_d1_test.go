package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
)

// TestDiscoverSkillPipRequirements checks that requirements.txt files directly
// under skills/<name>/ are found (and only those), as container-relative paths.
func TestDiscoverSkillPipRequirements(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, "skills")
	// two skills with requirements.txt, one without, plus a nested one to ignore
	mustWrite(t, filepath.Join(skills, "alpha", "requirements.txt"), "requests\n")
	mustWrite(t, filepath.Join(skills, "beta", "requirements.txt"), "numpy\n")
	mustWrite(t, filepath.Join(skills, "gamma", "SKILL.md"), "---\nname: gamma\n---\n")
	mustWrite(t, filepath.Join(skills, "beta", "scripts", "requirements.txt"), "ignored\n") // nested, not top-level

	got := discoverSkillPipRequirements(skills)
	want := []string{"skills/alpha/requirements.txt", "skills/beta/requirements.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("discoverSkillPipRequirements = %v, want %v", got, want)
	}

	// Absent skills/ → nil.
	if r := discoverSkillPipRequirements(filepath.Join(root, "nope")); r != nil {
		t.Errorf("expected nil for missing dir, got %v", r)
	}
}

// TestDockerfileStage_RendersSkillPipStep asserts the generated Dockerfile
// includes a pip install step for each discovered skill requirements.txt.
func TestDockerfileStage_RendersSkillPipStep(t *testing.T) {
	outDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: outDir})
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "pip-agent",
		Version: "0.1.0",
		Runtime: &agentspec.RuntimeConfig{
			Image:      "debian:bookworm-slim",
			Entrypoint: []string{"forge", "run"},
		},
	}
	bc.SkillPipRequirements = []string{"skills/pdf-tools/requirements.txt"}

	if err := (&DockerfileStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pip3 install --no-cache-dir -r skills/pdf-tools/requirements.txt") {
		t.Errorf("Dockerfile missing skill pip install step:\n%s", data)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
