package analyzer

import (
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	if p.ScriptPolicy != "warn" {
		t.Fatalf("expected script_policy 'warn', got %q", p.ScriptPolicy)
	}
	if p.MaxRiskScore != 75 {
		t.Fatalf("expected max_risk_score 75, got %d", p.MaxRiskScore)
	}
	if len(p.BinaryDenylist) == 0 {
		t.Fatal("expected non-empty binary denylist")
	}
}

func TestCheckPolicy_Clean(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:         "safe-tool",
		RequiredBins: []string{"curl", "jq"},
		RequiredEnv:  []string{"API_KEY"},
	}
	violations := CheckPolicy(sd, false, DefaultPolicy())

	for _, v := range violations {
		if v.Severity == "error" {
			t.Fatalf("unexpected error violation: %s", v.Message)
		}
	}
}

func TestCheckPolicy_DeniedBinary(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:         "hacker-tool",
		RequiredBins: []string{"nc"},
	}
	violations := CheckPolicy(sd, false, DefaultPolicy())

	found := false
	for _, v := range violations {
		if v.Rule == "binary_denylist" && v.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected binary_denylist error for nc")
	}
}

func TestCheckPolicy_DeniedEnvPattern(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name:        "aws-tool",
		RequiredEnv: []string{"AWS_SECRET_ACCESS_KEY"},
	}
	violations := CheckPolicy(sd, false, DefaultPolicy())

	found := false
	for _, v := range violations {
		if v.Rule == "denied_env_pattern" && v.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected denied_env_pattern error for AWS_SECRET_ACCESS_KEY")
	}
}

func TestCheckPolicy_ScriptPolicyWarn(t *testing.T) {
	sd := &contract.SkillDescriptor{Name: "scripted"}
	violations := CheckPolicy(sd, true, DefaultPolicy())

	found := false
	for _, v := range violations {
		if v.Rule == "script_policy" && v.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected script_policy warning")
	}
}

func TestCheckPolicy_ScriptPolicyDeny(t *testing.T) {
	policy := DefaultPolicy()
	policy.ScriptPolicy = "deny"

	sd := &contract.SkillDescriptor{Name: "scripted"}
	violations := CheckPolicy(sd, true, policy)

	found := false
	for _, v := range violations {
		if v.Rule == "script_policy" && v.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected script_policy error")
	}
}

func TestCheckPolicy_ScriptPolicyAllow(t *testing.T) {
	policy := DefaultPolicy()
	policy.ScriptPolicy = "allow"

	sd := &contract.SkillDescriptor{Name: "scripted"}
	violations := CheckPolicy(sd, true, policy)

	for _, v := range violations {
		if v.Rule == "script_policy" {
			t.Fatal("should not have script_policy violation with allow")
		}
	}
}

func TestCheckPolicy_MaxEgressDomains(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxEgressDomains = 2

	sd := &contract.SkillDescriptor{
		Name:          "chatty",
		EgressDomains: []string{"a.com", "b.com", "c.com"},
	}
	violations := CheckPolicy(sd, false, policy)

	found := false
	for _, v := range violations {
		if v.Rule == "max_egress_domains" && v.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected max_egress_domains error")
	}
}

func TestCheckPolicy_MaxRiskScore(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRiskScore = 10
	// Clear other rules to isolate this test
	policy.BinaryDenylist = nil
	policy.DeniedEnvPatterns = nil
	policy.ScriptPolicy = "allow"

	sd := &contract.SkillDescriptor{
		Name:          "risky",
		EgressDomains: []string{"unknown.example.com", "evil.example.com"},
		RequiredBins:  []string{"bash"},
	}
	violations := CheckPolicy(sd, false, policy)

	found := false
	for _, v := range violations {
		if v.Rule == "max_risk_score" && v.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected max_risk_score error")
	}
}
