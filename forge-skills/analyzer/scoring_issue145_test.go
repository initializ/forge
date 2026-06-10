package analyzer

import (
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

// TestTrustedDomains_GitHubContentEndpointsAndChatGPT pins the
// trustedDomains additions made for issue #145. The bundled
// code-review skill declares raw.githubusercontent.com,
// patch-diff.githubusercontent.com, and chatgpt.com in its SKILL.md
// frontmatter — pre-fix each scored as "unknown" (+10) and tipped the
// skill past the DefaultPolicy MaxRiskScore (75) without operator
// recourse. Post-fix each scores as "trusted" (+2).
func TestTrustedDomains_GitHubContentEndpointsAndChatGPT(t *testing.T) {
	cases := []string{
		"raw.githubusercontent.com",
		"patch-diff.githubusercontent.com",
		"gist.githubusercontent.com",
		"objects.githubusercontent.com",
		"chatgpt.com",
	}
	for _, d := range cases {
		t.Run(d, func(t *testing.T) {
			sd := &contract.SkillDescriptor{Name: "issue-145", EgressDomains: []string{d}}
			a := AnalyzeSkillDescriptor(sd, false, SecurityPolicy{})
			if a.Score.Value != 2 {
				t.Fatalf("%s: expected trusted-domain score 2, got %d", d, a.Score.Value)
			}
			if len(a.Factors) != 1 || !contains(a.Factors[0].Description, "trusted domain") {
				t.Fatalf("%s: expected single trusted-domain factor, got %+v", d, a.Factors)
			}
		})
	}
}

// TestBundledCodeReviewSkill_PassesDefaultPolicy is the integration
// shape from issue #145: the bundled code-review skill (6 egress
// domains, 9 env vars, 3 standard binaries) must pass DefaultPolicy
// out of the box. Pre-fix it scored 100 and failed the
// MaxRiskScore=75 check, blocking `forge build` on a fresh agent.
func TestBundledCodeReviewSkill_PassesDefaultPolicy(t *testing.T) {
	sd := &contract.SkillDescriptor{
		Name: "code_review_diff",
		// Exact list from forge-skills/local/embedded/code-review/SKILL.md.
		EgressDomains: []string{
			"api.anthropic.com",
			"api.openai.com",
			"chatgpt.com",
			"api.github.com",
			"patch-diff.githubusercontent.com",
			"raw.githubusercontent.com",
		},
		RequiredBins: []string{"curl", "jq", "git"},
		RequiredEnv:  nil,
		OneOfEnv: []string{
			"ANTHROPIC_API_KEY",
			"OPENAI_API_KEY",
		},
		OptionalEnv: []string{
			"REVIEW_PROVIDER",
			"REVIEW_MODEL",
			"REVIEW_MAX_DIFF_BYTES",
			"GH_TOKEN",
			"FORGE_REVIEW_STANDARDS_DIR",
			"OPENAI_BASE_URL",
			"OPENAI_USE_RESPONSES_API",
		},
	}
	policy := DefaultPolicy()
	violations := CheckPolicy(sd, true /*hasScript*/, policy)
	for _, v := range violations {
		t.Logf("violation: rule=%s severity=%s msg=%s", v.Rule, v.Severity, v.Message)
	}
	hasMaxRisk := false
	for _, v := range violations {
		if v.Rule == "max_risk_score" {
			hasMaxRisk = true
		}
	}
	if hasMaxRisk {
		// Re-score for the diagnostic so the failure prints the breakdown.
		a := AnalyzeSkillDescriptor(sd, true, policy)
		t.Fatalf("bundled code-review skill must pass DefaultPolicy MaxRiskScore=%d, scored %d. Factors: %+v",
			policy.MaxRiskScore, a.Score.Value, a.Factors)
	}
}
