package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/packaging"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

// TestSkillsStage_InstructionalBrowserSkillFlowsToManifest is the regression
// test for the build-path instructional-skill bug: a capability-only browser
// SKILL.md (no "## Tool:" entries) must still contribute its browser
// capability, so the requirements stage injects chromium. Before the fix the
// scanner dropped it and browser images shipped without Chromium.
func TestSkillsStage_InstructionalBrowserSkillFlowsToManifest(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "web-browse")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Note: no "## Tool:" heading — instructional skill.
	skill := `---
name: web-browse
description: Browse the web
metadata:
  forge:
    requires:
      capabilities:
        - browser
    egress_domains:
      - example.com
---
Teaches the agent to drive the browser tools.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}

	bc := &pipeline.BuildContext{
		Opts:   pipeline.PipelineOptions{WorkDir: dir},
		Config: &types.ForgeConfig{},
		Spec:   &agentspec.AgentSpec{},
	}

	if err := (&SkillsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("SkillsStage: %v", err)
	}

	reqs, ok := bc.SkillRequirements.(*contract.AggregatedRequirements)
	if !ok || reqs == nil {
		t.Fatal("SkillRequirements not stored for instructional browser skill")
	}
	if len(reqs.Capabilities) == 0 || reqs.Capabilities[0] != "browser" {
		t.Fatalf("Capabilities = %v, want [browser]", reqs.Capabilities)
	}

	if err := (&RequirementsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("RequirementsStage: %v", err)
	}
	m, ok := bc.BinManifest.(*packaging.BinManifest)
	if !ok || m == nil {
		t.Fatal("BinManifest not built; chromium would be missing from the image")
	}
	found := false
	for _, r := range m.Requirements {
		if r.Name == "chromium" {
			found = true
		}
	}
	if !found {
		t.Errorf("chromium not injected for instructional browser skill; manifest = %+v", m.Requirements)
	}
}
