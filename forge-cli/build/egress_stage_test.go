package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/types"
)

func TestEgressStage_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{AgentID: "test", Version: "1.0.0", Entrypoint: "python main.py"}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Should be no-op
	if bc.EgressResolved != nil {
		t.Error("EgressResolved should be nil when no egress config")
	}
}

func TestEgressStage_DenyAll(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile: "strict",
			Mode:    "deny-all",
		},
	}
	bc.Spec = &agentspec.AgentSpec{AgentID: "test"}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if bc.EgressResolved == nil {
		t.Fatal("EgressResolved should not be nil")
	}
	if bc.Spec.EgressProfile != "strict" {
		t.Errorf("EgressProfile = %q, want %q", bc.Spec.EgressProfile, "strict")
	}

	// Check allowlist file exists
	allowlistPath := filepath.Join(tmpDir, "compiled", "egress_allowlist.json")
	if _, err := os.Stat(allowlistPath); os.IsNotExist(err) {
		t.Error("egress_allowlist.json not created")
	}
}

func TestEgressStage_Allowlist(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:        "standard",
			Mode:           "allowlist",
			AllowedDomains: []string{"api.example.com"},
		},
	}
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test",
		Tools:   []agentspec.ToolSpec{{Name: "web_search"}},
	}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if bc.Spec.EgressMode != "allowlist" {
		t.Errorf("EgressMode = %q, want %q", bc.Spec.EgressMode, "allowlist")
	}
}

func TestEgressStage_AllowlistWithCapabilities(t *testing.T) {
	tmpDir := t.TempDir()
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: tmpDir, WorkDir: tmpDir})
	bc.Config = &types.ForgeConfig{
		AgentID:    "test",
		Version:    "1.0.0",
		Entrypoint: "python main.py",
		Egress: types.EgressRef{
			Profile:      "standard",
			Mode:         "allowlist",
			Capabilities: []string{"slack"},
		},
	}
	bc.Spec = &agentspec.AgentSpec{
		AgentID: "test",
	}

	stage := &EgressStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if bc.EgressResolved == nil {
		t.Fatal("EgressResolved should not be nil")
	}

	// Type-assert to *security.EgressConfig (stored as any to avoid import cycle)
	resolved, ok := bc.EgressResolved.(*security.EgressConfig)
	if !ok {
		t.Fatalf("EgressResolved has unexpected type %T", bc.EgressResolved)
	}

	// Check that slack domains are in the resolved AllDomains
	wantDomains := map[string]bool{
		"slack.com":       true,
		"hooks.slack.com": true,
		"api.slack.com":   true,
	}
	for d := range wantDomains {
		found := false
		for _, got := range resolved.AllDomains {
			if got == d {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in AllDomains, got %v", d, resolved.AllDomains)
		}
	}
}
