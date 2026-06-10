package analyzer

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
)

// Well-known trusted domains that receive lower risk scores.
//
// Entries are operator-evident "everyone uses these" endpoints for
// LLM providers, channels, and source-control surfaces bundled skills
// target. Adding to this map is a project-level acknowledgement that
// the domain is owned by a vetted vendor. Per-agent acknowledgements
// belong in SecurityPolicy.TrustedDomains so operators can extend
// trust without patching the analyzer.
var trustedDomains = map[string]bool{
	// GitHub-owned surfaces.
	"api.github.com":                   true,
	"github.com":                       true,
	"raw.githubusercontent.com":        true, // raw-file endpoint
	"patch-diff.githubusercontent.com": true, // PR-diff endpoint
	"gist.githubusercontent.com":       true, // gist endpoint
	"objects.githubusercontent.com":    true, // release-asset CDN
	// LLM providers (canonical endpoints; OpenAI-compatible gateways
	// belong in SecurityPolicy.TrustedDomains per-agent).
	"api.openai.com":    true,
	"chatgpt.com":       true, // OpenAI product-domain redirect target
	"api.anthropic.com": true,
	"api.together.ai":   true,
	"api.cohere.com":    true,
	"api.tavily.com":    true,
	// Channels.
	"api.slack.com":    true,
	"hooks.slack.com":  true,
	"api.telegram.org": true,
	// Cloud APIs.
	"googleapis.com": true,
}

// High-risk binaries that may allow arbitrary code execution.
var highRiskBinaries = map[string]bool{
	"bash":    true,
	"sh":      true,
	"python":  true,
	"python3": true,
	"node":    true,
	"ssh":     true,
	"nc":      true,
	"ncat":    true,
	"netcat":  true,
	"perl":    true,
	"ruby":    true,
}

// Patterns that indicate sensitive environment variables.
var sensitiveEnvPatterns = []string{
	"SECRET",
	"PASSWORD",
	"PRIVATE_KEY",
	"SESSION_TOKEN",
	"CREDENTIALS",
}

// AnalyzeSkillDescriptor scores a SkillDescriptor for security risk under the
// given policy. A zero-value SecurityPolicy{} preserves the historical default
// scoring (no overrides applied).
func AnalyzeSkillDescriptor(sd *contract.SkillDescriptor, hasScript bool, policy SecurityPolicy) SkillRiskAssessment {
	var factors []RiskFactor

	factors = append(factors, scoreEgress(sd.EgressDomains, policy)...)
	factors = append(factors, scoreBinaries(sd.RequiredBins, policy)...)
	factors = append(factors, scoreEnv(sd.RequiredEnv, sd.OneOfEnv, sd.OptionalEnv, policy)...)
	if hasScript {
		factors = append(factors, scoreScript()...)
	}

	total := sumPoints(factors)
	return SkillRiskAssessment{
		SkillName:       sd.Name,
		Score:           RiskScore{Value: total, Level: classifyScore(total)},
		Factors:         factors,
		Recommendations: generateRecommendations(factors, hasScript),
	}
}

// AnalyzeSkillEntry scores a SkillEntry for security risk under the given
// policy. A zero-value SecurityPolicy{} preserves the historical default
// scoring (no overrides applied).
func AnalyzeSkillEntry(entry *contract.SkillEntry, hasScript bool, policy SecurityPolicy) SkillRiskAssessment {
	var factors []RiskFactor
	var egressDomains []string
	var bins []string
	var reqEnv, oneOfEnv, optEnv []string

	if entry.ForgeReqs != nil {
		for _, b := range entry.ForgeReqs.Bins {
			bins = append(bins, b.Name)
		}
		if entry.ForgeReqs.Env != nil {
			reqEnv = entry.ForgeReqs.Env.Required
			oneOfEnv = entry.ForgeReqs.Env.OneOf
			optEnv = entry.ForgeReqs.Env.Optional
		}
	}
	if entry.Metadata != nil && entry.Metadata.Metadata != nil {
		if forgeMap, ok := entry.Metadata.Metadata["forge"]; ok {
			if raw, ok := forgeMap["egress_domains"]; ok {
				if arr, ok := raw.([]any); ok {
					for _, v := range arr {
						if s, ok := v.(string); ok {
							egressDomains = append(egressDomains, s)
						}
					}
				}
			}
		}
	}

	factors = append(factors, scoreEgress(egressDomains, policy)...)
	factors = append(factors, scoreBinaries(bins, policy)...)
	factors = append(factors, scoreEnv(reqEnv, oneOfEnv, optEnv, policy)...)
	if hasScript {
		factors = append(factors, scoreScript()...)
	}

	total := sumPoints(factors)
	return SkillRiskAssessment{
		SkillName:       entry.Name,
		Score:           RiskScore{Value: total, Level: classifyScore(total)},
		Factors:         factors,
		Recommendations: generateRecommendations(factors, hasScript),
	}
}

func scoreEgress(domains []string, policy SecurityPolicy) []RiskFactor {
	var factors []RiskFactor

	// Union builtin trusted domains with policy.TrustedDomains. We track the
	// policy-trusted set separately so the RiskFactor description can record
	// that the trust came from a policy override.
	policyTrusted := make(map[string]bool, len(policy.TrustedDomains))
	for _, d := range policy.TrustedDomains {
		policyTrusted[d] = true
	}

	for _, domain := range domains {
		switch {
		case trustedDomains[domain]:
			factors = append(factors, RiskFactor{
				Category:    "egress",
				Description: fmt.Sprintf("trusted domain: %s", domain),
				Points:      2,
			})
		case policyTrusted[domain]:
			factors = append(factors, RiskFactor{
				Category:    "egress",
				Description: fmt.Sprintf("trusted domain (via policy): %s", domain),
				Points:      2,
			})
		default:
			factors = append(factors, RiskFactor{
				Category:    "egress",
				Description: fmt.Sprintf("unknown domain: %s", domain),
				Points:      10,
			})
		}
	}

	if len(domains) > 5 {
		factors = append(factors, RiskFactor{
			Category:    "egress",
			Description: fmt.Sprintf(">5 total domains (%d)", len(domains)),
			Points:      15,
		})
	}

	return factors
}

func scoreBinaries(bins []string, policy SecurityPolicy) []RiskFactor {
	acknowledged := make(map[string]bool, len(policy.AcknowledgedBins))
	for _, b := range policy.AcknowledgedBins {
		acknowledged[b] = true
	}

	var factors []RiskFactor
	for _, bin := range bins {
		switch {
		case highRiskBinaries[bin] && acknowledged[bin]:
			factors = append(factors, RiskFactor{
				Category:    "binary",
				Description: fmt.Sprintf("high-risk binary (acknowledged by policy): %s", bin),
				Points:      3,
			})
		case highRiskBinaries[bin]:
			factors = append(factors, RiskFactor{
				Category:    "binary",
				Description: fmt.Sprintf("high-risk binary: %s", bin),
				Points:      15,
			})
		default:
			factors = append(factors, RiskFactor{
				Category:    "binary",
				Description: fmt.Sprintf("standard binary: %s", bin),
				Points:      3,
			})
		}
	}
	return factors
}

// envCategoryCap bounds how much the env-var category can contribute
// to a skill's total risk score. Without a cap, a multi-purpose skill
// that declares many config-knob env vars (e.g. the bundled
// code-review skill's 9 OneOf/Optional vars) racks up 45+ points on
// the env axis alone, which is disproportionate — most of those
// names are operator-tunable knobs, not credentials. The cap kicks in
// at the equivalent of 5 non-sensitive vars or 2.5 sensitive vars and
// prevents the env axis from dominating the aggregate score. Per-item
// risk factors are still emitted (and the audit report shows every
// declared var) — only the points-contribution is capped.
const envCategoryCap = 25

func scoreEnv(reqEnv, oneOfEnv, optEnv []string, policy SecurityPolicy) []RiskFactor {
	acknowledged := make(map[string]bool, len(policy.AcknowledgedEnv))
	for _, e := range policy.AcknowledgedEnv {
		acknowledged[e] = true
	}

	allEnv := make([]string, 0, len(reqEnv)+len(oneOfEnv)+len(optEnv))
	allEnv = append(allEnv, reqEnv...)
	allEnv = append(allEnv, oneOfEnv...)
	allEnv = append(allEnv, optEnv...)

	var factors []RiskFactor
	for _, env := range allEnv {
		switch {
		case isSensitiveEnv(env) && acknowledged[env]:
			factors = append(factors, RiskFactor{
				Category:    "env",
				Description: fmt.Sprintf("sensitive variable (acknowledged by policy): %s", env),
				Points:      5,
			})
		case isSensitiveEnv(env):
			factors = append(factors, RiskFactor{
				Category:    "env",
				Description: fmt.Sprintf("sensitive variable: %s", env),
				Points:      10,
			})
		default:
			factors = append(factors, RiskFactor{
				Category:    "env",
				Description: fmt.Sprintf("API key: %s", env),
				Points:      5,
			})
		}
	}

	// Apply the category cap by attributing the overage to a single
	// negative-points adjustment so the per-item factors stay visible
	// in the audit report.
	total := 0
	for _, f := range factors {
		total += f.Points
	}
	if total > envCategoryCap {
		factors = append(factors, RiskFactor{
			Category:    "env",
			Description: fmt.Sprintf("category cap applied (%d → %d, %d declared)", total, envCategoryCap, len(allEnv)),
			Points:      envCategoryCap - total,
		})
	}

	return factors
}

func scoreScript() []RiskFactor {
	return []RiskFactor{{
		Category:    "script",
		Description: "has executable script",
		Points:      20,
	}}
}

func isSensitiveEnv(name string) bool {
	upper := strings.ToUpper(name)
	for _, pattern := range sensitiveEnvPatterns {
		if strings.Contains(upper, pattern) {
			return true
		}
	}
	return false
}

func sumPoints(factors []RiskFactor) int {
	total := 0
	for _, f := range factors {
		total += f.Points
	}
	if total > 100 {
		total = 100
	}
	return total
}

func classifyScore(score int) RiskLevel {
	switch {
	case score == 0:
		return RiskNone
	case score <= 25:
		return RiskLow
	case score <= 50:
		return RiskMedium
	case score <= 75:
		return RiskHigh
	default:
		return RiskCritical
	}
}

func generateRecommendations(factors []RiskFactor, hasScript bool) []string {
	var recs []string
	hasHighRiskBin := false
	hasSensitiveEnv := false
	hasUnknownDomain := false

	for _, f := range factors {
		switch {
		case f.Category == "binary" && f.Points >= 15:
			hasHighRiskBin = true
		case f.Category == "env" && f.Points >= 10:
			hasSensitiveEnv = true
		case f.Category == "egress" && f.Points >= 10:
			hasUnknownDomain = true
		}
	}

	if hasHighRiskBin {
		recs = append(recs, "Review high-risk binary usage; consider restricting to specific commands")
	}
	if hasSensitiveEnv {
		recs = append(recs, "Ensure sensitive credentials are rotated regularly and scoped minimally")
	}
	if hasUnknownDomain {
		recs = append(recs, "Verify unknown egress domains are expected and add to trusted list if appropriate")
	}
	if hasScript {
		recs = append(recs, "Audit executable script content for security issues before deployment")
	}
	return recs
}
