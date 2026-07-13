package requirements

import (
	"reflect"
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestDerive_Basic(t *testing.T) {
	reqs := &contract.AggregatedRequirements{
		Bins:        []string{"curl", "jq"},
		EnvRequired: []string{"API_KEY"},
		EnvOneOf:    [][]string{{"OPENAI_KEY", "ANTHROPIC_KEY"}},
		EnvOptional: []string{"DEBUG"},
	}

	cfg := DeriveCLIConfig(reqs)

	if len(cfg.AllowedBinaries) != 2 {
		t.Errorf("AllowedBinaries = %v, want 2 items", cfg.AllowedBinaries)
	}
	if cfg.AllowedBinaries[0] != "curl" || cfg.AllowedBinaries[1] != "jq" {
		t.Errorf("AllowedBinaries = %v, want [curl jq]", cfg.AllowedBinaries)
	}

	// EnvPassthrough should be union of all env vars, sorted
	// ANTHROPIC_KEY, API_KEY, DEBUG, OPENAI_KEY
	if len(cfg.EnvPassthrough) != 4 {
		t.Fatalf("EnvPassthrough = %v, want 4 items", cfg.EnvPassthrough)
	}
	expected := []string{"ANTHROPIC_KEY", "API_KEY", "DEBUG", "OPENAI_KEY"}
	for i, v := range expected {
		if cfg.EnvPassthrough[i] != v {
			t.Errorf("EnvPassthrough[%d] = %q, want %q", i, cfg.EnvPassthrough[i], v)
		}
	}
}

func TestDerive_FiltersShellInterpreters(t *testing.T) {
	reqs := &contract.AggregatedRequirements{
		Bins: []string{"bash", "curl", "gh", "jq", "sh", "zsh"},
	}

	cfg := DeriveCLIConfig(reqs)

	// bash, sh, zsh should be filtered out
	expected := []string{"curl", "gh", "jq"}
	if len(cfg.AllowedBinaries) != len(expected) {
		t.Fatalf("AllowedBinaries = %v, want %v", cfg.AllowedBinaries, expected)
	}
	for i, v := range expected {
		if cfg.AllowedBinaries[i] != v {
			t.Errorf("AllowedBinaries[%d] = %q, want %q", i, cfg.AllowedBinaries[i], v)
		}
	}
}

func TestDerive_WorkflowPhasesPassthrough(t *testing.T) {
	reqs := &contract.AggregatedRequirements{
		WorkflowPhases: []string{"edit", "finalize"},
	}

	cfg := DeriveCLIConfig(reqs)

	if len(cfg.WorkflowPhases) != 2 {
		t.Fatalf("WorkflowPhases = %v, want 2 items", cfg.WorkflowPhases)
	}
	if cfg.WorkflowPhases[0] != "edit" || cfg.WorkflowPhases[1] != "finalize" {
		t.Errorf("WorkflowPhases = %v, want [edit finalize]", cfg.WorkflowPhases)
	}
}

func TestMerge_ExplicitOverrides(t *testing.T) {
	explicit := &contract.DerivedCLIConfig{
		AllowedBinaries: []string{"python"},
		EnvPassthrough:  []string{"CUSTOM_VAR"},
	}
	derived := &contract.DerivedCLIConfig{
		AllowedBinaries: []string{"curl", "jq"},
		EnvPassthrough:  []string{"API_KEY"},
	}

	merged := MergeCLIConfig(explicit, derived)
	if len(merged.AllowedBinaries) != 1 || merged.AllowedBinaries[0] != "python" {
		t.Errorf("AllowedBinaries = %v, want [python]", merged.AllowedBinaries)
	}
	if len(merged.EnvPassthrough) != 1 || merged.EnvPassthrough[0] != "CUSTOM_VAR" {
		t.Errorf("EnvPassthrough = %v, want [CUSTOM_VAR]", merged.EnvPassthrough)
	}
}

func TestMerge_NilAllowsDerived(t *testing.T) {
	explicit := &contract.DerivedCLIConfig{} // empty slices (nil)
	derived := &contract.DerivedCLIConfig{
		AllowedBinaries: []string{"curl", "jq"},
		EnvPassthrough:  []string{"API_KEY"},
	}

	merged := MergeCLIConfig(explicit, derived)
	if len(merged.AllowedBinaries) != 2 {
		t.Errorf("AllowedBinaries = %v, want [curl jq]", merged.AllowedBinaries)
	}
	if len(merged.EnvPassthrough) != 1 || merged.EnvPassthrough[0] != "API_KEY" {
		t.Errorf("EnvPassthrough = %v, want [API_KEY]", merged.EnvPassthrough)
	}
}

func TestDeriveBrowserConfig_NilWithoutCapability(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "github",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []contract.BinRequirement{{Name: "curl"}},
			},
		},
	}
	reqs := AggregateRequirements(entries)
	if cfg := DeriveBrowserConfig(reqs, entries); cfg != nil {
		t.Errorf("DeriveBrowserConfig = %+v, want nil without browser capability", cfg)
	}
	if cfg := DeriveBrowserConfig(nil, nil); cfg != nil {
		t.Errorf("DeriveBrowserConfig(nil, nil) = %+v, want nil", cfg)
	}
}

func TestDeriveBrowserConfig_SourceSkills(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name:      "web-browse",
			ForgeReqs: &contract.SkillRequirements{Capabilities: []string{"browser"}},
		},
		{
			// Second entry from the same SKILL.md (multi-tool skills share
			// Name via metadata) must not duplicate the source.
			Name:      "web-browse",
			ForgeReqs: &contract.SkillRequirements{Capabilities: []string{"browser"}},
		},
		{
			Name:      "price-watch",
			ForgeReqs: &contract.SkillRequirements{Capabilities: []string{"browser"}},
		},
		{
			Name:      "summarize",
			ForgeReqs: &contract.SkillRequirements{},
		},
	}
	reqs := AggregateRequirements(entries)
	cfg := DeriveBrowserConfig(reqs, entries)
	if cfg == nil {
		t.Fatal("DeriveBrowserConfig = nil, want non-nil with browser capability")
	}
	want := []string{"web-browse", "price-watch"}
	if !reflect.DeepEqual(cfg.SourceSkills, want) {
		t.Errorf("SourceSkills = %v, want %v", cfg.SourceSkills, want)
	}
	if cfg.AllowSensitiveFill {
		t.Error("AllowSensitiveFill = true, want false by default")
	}
}

func browserSkill(name string, optInSensitiveFill bool) contract.SkillEntry {
	forge := map[string]any{}
	if optInSensitiveFill {
		forge["guardrails"] = map[string]any{
			"browser": map[string]any{"allow_sensitive_fill": true},
		}
	}
	return contract.SkillEntry{
		Name:      name,
		Metadata:  &contract.SkillMetadata{Name: name, Metadata: map[string]map[string]any{"forge": forge}},
		ForgeReqs: &contract.SkillRequirements{Capabilities: []string{"browser"}},
	}
}

func TestDeriveBrowserConfig_AllowSensitiveFillOptIn(t *testing.T) {
	entries := []contract.SkillEntry{browserSkill("portal-login", true)}
	reqs := AggregateRequirements(entries)
	cfg := DeriveBrowserConfig(reqs, entries)
	if cfg == nil || !cfg.AllowSensitiveFill {
		t.Errorf("DeriveBrowserConfig = %+v, want AllowSensitiveFill true", cfg)
	}

	// Without the opt-in the flag stays false.
	noOptIn := []contract.SkillEntry{browserSkill("portal-login", false)}
	reqs2 := AggregateRequirements(noOptIn)
	if cfg2 := DeriveBrowserConfig(reqs2, noOptIn); cfg2 == nil || cfg2.AllowSensitiveFill {
		t.Errorf("DeriveBrowserConfig without opt-in = %+v, want AllowSensitiveFill false", cfg2)
	}
}

// TestDeriveBrowserConfig_NoCrossSkillEscalation is the security regression:
// an unrelated skill that opts into sensitive fill but does NOT declare the
// browser capability must not enable password/payment fill for a browser
// granted by a different skill.
func TestDeriveBrowserConfig_NoCrossSkillEscalation(t *testing.T) {
	// A non-browser skill carrying the opt-in.
	sneaky := contract.SkillEntry{
		Name: "sneaky-helper",
		Metadata: &contract.SkillMetadata{Name: "sneaky-helper", Metadata: map[string]map[string]any{
			"forge": {"guardrails": map[string]any{"browser": map[string]any{"allow_sensitive_fill": true}}},
		}},
		ForgeReqs: &contract.SkillRequirements{}, // NO browser capability
	}
	// A browser skill that did NOT opt into sensitive fill.
	browser := browserSkill("web-browse", false)

	entries := []contract.SkillEntry{sneaky, browser}
	reqs := AggregateRequirements(entries)
	cfg := DeriveBrowserConfig(reqs, entries)
	if cfg == nil {
		t.Fatal("browser capability not derived")
	}
	if cfg.AllowSensitiveFill {
		t.Error("sensitive fill enabled via a non-browser skill — cross-skill privilege escalation")
	}
}
