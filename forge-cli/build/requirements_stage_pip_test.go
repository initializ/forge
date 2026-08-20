package build

import (
	"context"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
)

// TestRequirementsStage_InjectsPythonForSkillRequirements verifies #405 D1: a
// skill shipping a requirements.txt forces python3 + pip into the bin manifest
// so the interpreter is provisioned even when the SKILL.md didn't declare them.
func TestRequirementsStage_InjectsPythonForSkillRequirements(t *testing.T) {
	bc := &pipeline.BuildContext{
		Spec:                 &agentspec.AgentSpec{},
		SkillRequirements:    &contract.AggregatedRequirements{},
		SkillPipRequirements: []string{"skills/pdf-tools/requirements.txt"},
	}
	if err := (&RequirementsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("RequirementsStage: %v", err)
	}
	names := manifestNames(bc)
	for _, want := range []string{"python3", "pip"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s not injected into BinManifest for a pip-requiring skill; got %v", want, names)
		}
	}
}

// TestRequirementsStage_NoPythonWithoutRequirements ensures python3/pip are NOT
// force-injected when no skill ships a requirements.txt.
func TestRequirementsStage_NoPythonWithoutRequirements(t *testing.T) {
	bc := &pipeline.BuildContext{
		Spec: &agentspec.AgentSpec{},
		SkillRequirements: &contract.AggregatedRequirements{
			Bins:            []string{"curl"},
			BinRequirements: []contract.BinRequirement{{Name: "curl"}},
		},
	}
	if err := (&RequirementsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("RequirementsStage: %v", err)
	}
	for _, n := range manifestNames(bc) {
		if n == "python3" || n == "pip" {
			t.Errorf("python injected without a requirements.txt: %v", manifestNames(bc))
		}
	}
}

// TestRequirementsStage_PythonNotDuplicated ensures a skill that already
// declares python3/pip doesn't get duplicate manifest entries.
func TestRequirementsStage_PythonNotDuplicated(t *testing.T) {
	bc := &pipeline.BuildContext{
		Spec: &agentspec.AgentSpec{},
		SkillRequirements: &contract.AggregatedRequirements{
			Bins:            []string{"python3", "pip"},
			BinRequirements: []contract.BinRequirement{{Name: "python3"}, {Name: "pip"}},
		},
		SkillPipRequirements: []string{"skills/x/requirements.txt"},
	}
	if err := (&RequirementsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("RequirementsStage: %v", err)
	}
	counts := map[string]int{}
	for _, n := range manifestNames(bc) {
		counts[n]++
	}
	if counts["python3"] != 1 || counts["pip"] != 1 {
		t.Errorf("python3=%d pip=%d, want 1 each: %v", counts["python3"], counts["pip"], manifestNames(bc))
	}
}
