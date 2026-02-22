package build

import (
	"context"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	coreskills "github.com/initializ/forge/forge-core/skills"
)

func TestRequirementsStage_NoSkills(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
	bc.Spec = &agentspec.AgentSpec{}
	// SkillRequirements is nil

	stage := &RequirementsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a no-op
	if bc.Spec.Requirements != nil {
		t.Error("expected nil Requirements when no skills")
	}
}

func TestRequirementsStage_PopulatesSpec(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
	bc.Spec = &agentspec.AgentSpec{}
	bc.SkillRequirements = &coreskills.AggregatedRequirements{
		Bins:        []string{"curl", "jq"},
		EnvRequired: []string{"API_KEY"},
		EnvOptional: []string{"DEBUG"},
	}

	stage := &RequirementsStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if bc.Spec.Requirements == nil {
		t.Fatal("expected non-nil Requirements")
	}
	if len(bc.Spec.Requirements.Bins) != 2 {
		t.Errorf("Bins = %v, want 2 items", bc.Spec.Requirements.Bins)
	}
	if len(bc.Spec.Requirements.EnvRequired) != 1 {
		t.Errorf("EnvRequired = %v, want 1 item", bc.Spec.Requirements.EnvRequired)
	}
	if len(bc.Spec.Requirements.EnvOptional) != 1 {
		t.Errorf("EnvOptional = %v, want 1 item", bc.Spec.Requirements.EnvOptional)
	}

	// Should have auto-derived cli_execute tool
	found := false
	for _, tool := range bc.Spec.Tools {
		if tool.Name == "cli_execute" {
			found = true
			if tool.ForgeMeta == nil {
				t.Error("expected ForgeMeta on cli_execute tool")
			} else {
				if len(tool.ForgeMeta.AllowedBinaries) != 2 {
					t.Errorf("AllowedBinaries = %v, want 2 items", tool.ForgeMeta.AllowedBinaries)
				}
			}
			break
		}
	}
	if !found {
		t.Error("expected cli_execute tool to be auto-derived")
	}
}
