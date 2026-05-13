package analyzer

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadPolicyFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	yaml := `
script_policy: allow
max_risk_score: 90
trusted_domains:
  - internal.example.com
  - corp.example.com
acknowledged_bins:
  - python
  - python3
acknowledged_env:
  - DB_PASSWORD
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing policy file: %v", err)
	}

	p, err := LoadPolicyFromFile(path)
	if err != nil {
		t.Fatalf("LoadPolicyFromFile: %v", err)
	}

	if p.ScriptPolicy != "allow" {
		t.Errorf("ScriptPolicy = %q, want %q", p.ScriptPolicy, "allow")
	}
	if p.MaxRiskScore != 90 {
		t.Errorf("MaxRiskScore = %d, want 90", p.MaxRiskScore)
	}
	if !slices.Equal(p.TrustedDomains, []string{"internal.example.com", "corp.example.com"}) {
		t.Errorf("TrustedDomains = %v", p.TrustedDomains)
	}
	if !slices.Equal(p.AcknowledgedBins, []string{"python", "python3"}) {
		t.Errorf("AcknowledgedBins = %v", p.AcknowledgedBins)
	}
	if !slices.Equal(p.AcknowledgedEnv, []string{"DB_PASSWORD"}) {
		t.Errorf("AcknowledgedEnv = %v", p.AcknowledgedEnv)
	}
}

// TestLoadPolicyFromFile_PolicyAffectsScoring is an end-to-end check that a
// policy loaded from YAML actually changes the assessment — the regression
// the existing wiring gap created and #49 closes.
func TestLoadPolicyFromFile_PolicyAffectsScoring(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	yaml := "trusted_domains: [internal.example.com]\nacknowledged_bins: [python]\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("writing policy file: %v", err)
	}

	policy, err := LoadPolicyFromFile(path)
	if err != nil {
		t.Fatalf("LoadPolicyFromFile: %v", err)
	}

	factors := scoreEgress([]string{"internal.example.com"}, policy)
	if len(factors) != 1 || factors[0].Points != 2 {
		t.Errorf("scoreEgress with loaded TrustedDomains: factors=%v", factors)
	}

	factors = scoreBinaries([]string{"python"}, policy)
	if len(factors) != 1 || factors[0].Points != 3 {
		t.Errorf("scoreBinaries with loaded AcknowledgedBins: factors=%v", factors)
	}
}

func TestLoadPolicyFromFile_MissingFile(t *testing.T) {
	_, err := LoadPolicyFromFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadPolicyFromFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("this: is: not: valid: yaml: ["), 0o644); err != nil {
		t.Fatalf("writing bad file: %v", err)
	}
	_, err := LoadPolicyFromFile(path)
	if err == nil {
		t.Fatal("expected parse error for invalid YAML")
	}
}
