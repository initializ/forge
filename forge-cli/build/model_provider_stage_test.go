package build

import (
	"context"
	"slices"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
)

func TestModelProviderStage_AddsProviderKeyAsOptional(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
	bc.Config = &types.ForgeConfig{Model: types.ModelRef{Provider: "openai"}}
	bc.Spec = &agentspec.AgentSpec{
		Requirements: &agentspec.AgentRequirements{EnvRequired: []string{"SKILL_API_KEY"}},
	}

	if err := (&ModelProviderStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !slices.Contains(bc.Spec.Requirements.EnvOptional, "OPENAI_API_KEY") {
		t.Errorf("EnvOptional = %v, want it to contain OPENAI_API_KEY", bc.Spec.Requirements.EnvOptional)
	}
	if slices.Contains(bc.Spec.Requirements.EnvRequired, "OPENAI_API_KEY") {
		t.Errorf("OPENAI_API_KEY should be optional, not required")
	}
	// Existing skill env vars are untouched.
	if !slices.Contains(bc.Spec.Requirements.EnvRequired, "SKILL_API_KEY") {
		t.Errorf("existing EnvRequired lost: %v", bc.Spec.Requirements.EnvRequired)
	}
}

func TestModelProviderStage_PopulatesRequirementsWhenNil(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
	bc.Config = &types.ForgeConfig{Model: types.ModelRef{Provider: "anthropic"}}
	bc.Spec = &agentspec.AgentSpec{} // Requirements nil

	if err := (&ModelProviderStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if bc.Spec.Requirements == nil || !slices.Contains(bc.Spec.Requirements.EnvOptional, "ANTHROPIC_API_KEY") {
		t.Errorf("expected ANTHROPIC_API_KEY in EnvOptional, got %+v", bc.Spec.Requirements)
	}
}

func TestModelProviderStage_SkipsWhenAlreadyDeclared(t *testing.T) {
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
	bc.Config = &types.ForgeConfig{Model: types.ModelRef{Provider: "openai"}}
	bc.Spec = &agentspec.AgentSpec{
		Requirements: &agentspec.AgentRequirements{EnvRequired: []string{"OPENAI_API_KEY"}},
	}

	if err := (&ModelProviderStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if slices.Contains(bc.Spec.Requirements.EnvOptional, "OPENAI_API_KEY") {
		t.Errorf("should not re-add OPENAI_API_KEY to optional when already required")
	}
}

func TestModelProviderStage_KeylessOrUnknownProviderAddsNothing(t *testing.T) {
	for _, prov := range []string{"ollama", "custom", "does-not-exist", ""} {
		bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})
		bc.Config = &types.ForgeConfig{Model: types.ModelRef{Provider: prov}}
		bc.Spec = &agentspec.AgentSpec{}

		if err := (&ModelProviderStage{}).Execute(context.Background(), bc); err != nil {
			t.Fatalf("Execute(%q): %v", prov, err)
		}
		if bc.Spec.Requirements != nil && len(bc.Spec.Requirements.EnvOptional) > 0 {
			t.Errorf("provider %q should add no env var, got %v", prov, bc.Spec.Requirements.EnvOptional)
		}
	}
}
