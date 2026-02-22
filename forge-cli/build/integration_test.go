//go:build integration

package build_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-cli/build"
)

// findProjectRoot walks up from the current directory to find go.mod.
func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

func TestIntegration_BuildWithSkillsAndEgress(t *testing.T) {
	root := findProjectRoot(t)
	outDir := t.TempDir()
	workDir := root

	// Create a skills.md in a temp work dir
	skillsDir := t.TempDir()
	skillsContent := []byte("## Tool: web_search\nSearch the web for information.\n\n**Input:** query: string\n**Output:** results: []string\n")
	if err := os.WriteFile(filepath.Join(skillsDir, "skills.md"), skillsContent, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := &types.ForgeConfig{
		AgentID:    "test-agent",
		Version:    "1.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Skills:     types.SkillsRef{Path: filepath.Join(skillsDir, "skills.md")},
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.example.com"},
		},
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   workDir,
		OutputDir: outDir,
	})
	bc.Config = cfg
	bc.Spec = &agentspec.AgentSpec{
		ForgeVersion: "1.0.0",
		AgentID:      "test-agent",
		Version:      "1.0.0",
		Name:         "test-agent",
		Tools: []agentspec.ToolSpec{
			{Name: "web_search"},
		},
	}

	// Run skills + egress + manifest stages
	p := pipeline.New(
		&build.SkillsStage{},
		&build.EgressStage{},
		&build.ManifestStage{},
	)

	if err := p.Run(context.Background(), bc); err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}

	// Verify skills artifacts
	skillsPath := filepath.Join(outDir, "compiled", "skills", "skills.json")
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		t.Error("expected skills.json not found")
	} else {
		data, err := os.ReadFile(skillsPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var skills map[string]any
		if err := json.Unmarshal(data, &skills); err != nil {
			t.Fatalf("unmarshal skills.json: %v", err)
		}
		if skills["count"].(float64) != 1 {
			t.Errorf("skills count = %v, want 1", skills["count"])
		}
	}

	// Verify egress allowlist
	egressPath := filepath.Join(outDir, "compiled", "egress_allowlist.json")
	if _, err := os.Stat(egressPath); os.IsNotExist(err) {
		t.Error("expected egress_allowlist.json not found")
	} else {
		data, err := os.ReadFile(egressPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var egress map[string]any
		if err := json.Unmarshal(data, &egress); err != nil {
			t.Fatalf("unmarshal egress_allowlist.json: %v", err)
		}
		if egress["profile"] != "standard" {
			t.Errorf("egress profile = %v, want standard", egress["profile"])
		}
		if egress["mode"] != "allowlist" {
			t.Errorf("egress mode = %v, want allowlist", egress["mode"])
		}
	}

	// Verify spec was updated
	if bc.Spec.SkillsSpecVersion != "agentskills-v1" {
		t.Errorf("SkillsSpecVersion = %q, want agentskills-v1", bc.Spec.SkillsSpecVersion)
	}
	if bc.Spec.EgressProfile != "standard" {
		t.Errorf("EgressProfile = %q, want standard", bc.Spec.EgressProfile)
	}

	// Verify build manifest
	manifestPath := filepath.Join(outDir, "build-manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Error("expected build-manifest.json not found")
	}
}

func TestIntegration_BuildExportRoundTrip(t *testing.T) {
	outDir := t.TempDir()

	cfg := &types.ForgeConfig{
		AgentID:    "roundtrip-agent",
		Version:    "2.0.0",
		Framework:  "custom",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile: "strict",
			Mode:    "deny-all",
		},
	}

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   t.TempDir(),
		OutputDir: outDir,
	})
	bc.Config = cfg
	bc.Spec = &agentspec.AgentSpec{
		ForgeVersion: "1.0.0",
		AgentID:      "roundtrip-agent",
		Version:      "2.0.0",
		Name:         "roundtrip-agent",
		Tools: []agentspec.ToolSpec{
			{Name: "web_search"},
			{Name: "github_api"},
		},
	}

	p := pipeline.New(
		&build.EgressStage{},
		&build.ManifestStage{},
	)

	if err := p.Run(context.Background(), bc); err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}

	// Read and validate build manifest
	manifestPath := filepath.Join(outDir, "build-manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	// Verify all expected fields
	if manifest["agent_id"] != "roundtrip-agent" {
		t.Errorf("agent_id = %v, want roundtrip-agent", manifest["agent_id"])
	}
	if manifest["version"] != "2.0.0" {
		t.Errorf("version = %v, want 2.0.0", manifest["version"])
	}
	if manifest["egress_profile"] != "strict" {
		t.Errorf("egress_profile = %v, want strict", manifest["egress_profile"])
	}
	if manifest["egress_mode"] != "deny-all" {
		t.Errorf("egress_mode = %v, want deny-all", manifest["egress_mode"])
	}
}
