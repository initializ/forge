package build

import (
	"context"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/packaging"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
)

func runRequirementsStage(t *testing.T, reqs *contract.AggregatedRequirements, entries []contract.SkillEntry) *pipeline.BuildContext {
	t.Helper()
	bc := &pipeline.BuildContext{
		Spec:              &agentspec.AgentSpec{},
		SkillRequirements: reqs,
		SkillEntries:      entries,
	}
	if err := (&RequirementsStage{}).Execute(context.Background(), bc); err != nil {
		t.Fatalf("RequirementsStage: %v", err)
	}
	return bc
}

func manifestNames(bc *pipeline.BuildContext) []string {
	m, ok := bc.BinManifest.(*packaging.BinManifest)
	if !ok || m == nil {
		return nil
	}
	var names []string
	for _, r := range m.Requirements {
		names = append(names, r.Name)
	}
	return names
}

func TestRequirementsStage_InjectsChromiumForBrowserCapability(t *testing.T) {
	reqs := &contract.AggregatedRequirements{
		Capabilities: []string{"browser"},
	}
	entries := []contract.SkillEntry{
		{Name: "web-browse", ForgeReqs: &contract.SkillRequirements{Capabilities: []string{"browser"}}},
	}
	bc := runRequirementsStage(t, reqs, entries)

	names := manifestNames(bc)
	found := false
	for _, n := range names {
		if n == "chromium" {
			found = true
		}
	}
	if !found {
		t.Fatalf("chromium not injected into BinManifest for browser capability; got %v", names)
	}
	m := bc.BinManifest.(*packaging.BinManifest)
	if m.SkillOrigin["chromium"] != "capability:browser" {
		t.Errorf("chromium origin = %q, want capability:browser", m.SkillOrigin["chromium"])
	}
}

func TestRequirementsStage_NoChromiumWithoutCapability(t *testing.T) {
	reqs := &contract.AggregatedRequirements{
		Bins:            []string{"curl"},
		BinRequirements: []contract.BinRequirement{{Name: "curl"}},
	}
	entries := []contract.SkillEntry{
		{Name: "github", ForgeReqs: &contract.SkillRequirements{Bins: []contract.BinRequirement{{Name: "curl"}}}},
	}
	bc := runRequirementsStage(t, reqs, entries)
	for _, n := range manifestNames(bc) {
		if n == "chromium" {
			t.Errorf("chromium injected for a non-browser agent: %v", manifestNames(bc))
		}
	}
}

func TestRequirementsStage_ChromiumNotDuplicated(t *testing.T) {
	// A skill that explicitly declares chromium as a bin AND the capability
	// must not get two chromium entries.
	reqs := &contract.AggregatedRequirements{
		Bins:            []string{"chromium"},
		BinRequirements: []contract.BinRequirement{{Name: "chromium"}},
		Capabilities:    []string{"browser"},
	}
	entries := []contract.SkillEntry{
		{Name: "web-browse", ForgeReqs: &contract.SkillRequirements{
			Bins:         []contract.BinRequirement{{Name: "chromium"}},
			Capabilities: []string{"browser"},
		}},
	}
	bc := runRequirementsStage(t, reqs, entries)
	count := 0
	for _, n := range manifestNames(bc) {
		if n == "chromium" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("chromium appears %d times, want 1: %v", count, manifestNames(bc))
	}
}
