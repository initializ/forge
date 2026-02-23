package analyzer

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
)

// Well-known trusted domains that receive lower risk scores.
var trustedDomains = map[string]bool{
	"api.github.com":    true,
	"github.com":        true,
	"api.openai.com":    true,
	"api.anthropic.com": true,
	"api.tavily.com":    true,
	"api.slack.com":     true,
	"hooks.slack.com":   true,
	"api.telegram.org":  true,
	"googleapis.com":    true,
	"api.together.ai":   true,
	"api.cohere.com":    true,
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

// AnalyzeSkillDescriptor scores a SkillDescriptor for security risk.
func AnalyzeSkillDescriptor(sd *contract.SkillDescriptor, hasScript bool) SkillRiskAssessment {
	var factors []RiskFactor

	factors = append(factors, scoreEgress(sd.EgressDomains, nil)...)
	factors = append(factors, scoreBinaries(sd.RequiredBins)...)
	factors = append(factors, scoreEnv(sd.RequiredEnv, sd.OneOfEnv, sd.OptionalEnv)...)
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

// AnalyzeSkillEntry scores a SkillEntry for security risk.
func AnalyzeSkillEntry(entry *contract.SkillEntry, hasScript bool) SkillRiskAssessment {
	var factors []RiskFactor
	var egressDomains []string
	var bins []string
	var reqEnv, oneOfEnv, optEnv []string

	if entry.ForgeReqs != nil {
		bins = entry.ForgeReqs.Bins
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

	factors = append(factors, scoreEgress(egressDomains, nil)...)
	factors = append(factors, scoreBinaries(bins)...)
	factors = append(factors, scoreEnv(reqEnv, oneOfEnv, optEnv)...)
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

func scoreEgress(domains []string, extraTrusted []string) []RiskFactor {
	var factors []RiskFactor

	trusted := make(map[string]bool)
	for k, v := range trustedDomains {
		trusted[k] = v
	}
	for _, d := range extraTrusted {
		trusted[d] = true
	}

	for _, domain := range domains {
		if trusted[domain] {
			factors = append(factors, RiskFactor{
				Category:    "egress",
				Description: fmt.Sprintf("trusted domain: %s", domain),
				Points:      2,
			})
		} else {
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

func scoreBinaries(bins []string) []RiskFactor {
	var factors []RiskFactor
	for _, bin := range bins {
		if highRiskBinaries[bin] {
			factors = append(factors, RiskFactor{
				Category:    "binary",
				Description: fmt.Sprintf("high-risk binary: %s", bin),
				Points:      15,
			})
		} else {
			factors = append(factors, RiskFactor{
				Category:    "binary",
				Description: fmt.Sprintf("standard binary: %s", bin),
				Points:      3,
			})
		}
	}
	return factors
}

func scoreEnv(reqEnv, oneOfEnv, optEnv []string) []RiskFactor {
	var factors []RiskFactor
	allEnv := make([]string, 0, len(reqEnv)+len(oneOfEnv)+len(optEnv))
	allEnv = append(allEnv, reqEnv...)
	allEnv = append(allEnv, oneOfEnv...)
	allEnv = append(allEnv, optEnv...)

	for _, env := range allEnv {
		if isSensitiveEnv(env) {
			factors = append(factors, RiskFactor{
				Category:    "env",
				Description: fmt.Sprintf("sensitive variable: %s", env),
				Points:      10,
			})
		} else {
			factors = append(factors, RiskFactor{
				Category:    "env",
				Description: fmt.Sprintf("API key: %s", env),
				Points:      5,
			})
		}
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
