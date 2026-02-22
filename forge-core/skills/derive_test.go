package skills

import (
	"testing"
)

func TestDerive_Basic(t *testing.T) {
	reqs := &AggregatedRequirements{
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
	// API_KEY, ANTHROPIC_KEY, DEBUG, OPENAI_KEY
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

func TestMerge_ExplicitOverrides(t *testing.T) {
	explicit := &DerivedCLIConfig{
		AllowedBinaries: []string{"python"},
		EnvPassthrough:  []string{"CUSTOM_VAR"},
	}
	derived := &DerivedCLIConfig{
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
	explicit := &DerivedCLIConfig{} // empty slices (nil)
	derived := &DerivedCLIConfig{
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
