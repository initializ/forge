package analyzer

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
)

// DefaultPolicy returns a SecurityPolicy with sensible defaults.
func DefaultPolicy() SecurityPolicy {
	return SecurityPolicy{
		MaxEgressDomains: 0, // unlimited
		BinaryDenylist:   []string{"nc", "ncat", "netcat", "nmap", "ssh", "scp"},
		DeniedEnvPatterns: []string{
			"AWS_SECRET_ACCESS_KEY",
			"AWS_SESSION_TOKEN",
		},
		ScriptPolicy:   "warn",
		MaxRiskScore:   75,
		TrustedDomains: nil,
	}
}

// CheckPolicy evaluates a SkillDescriptor against a SecurityPolicy.
func CheckPolicy(sd *contract.SkillDescriptor, hasScript bool, policy SecurityPolicy) []PolicyViolation {
	var violations []PolicyViolation

	// Rule 1: MaxEgressDomains
	if policy.MaxEgressDomains > 0 && len(sd.EgressDomains) > policy.MaxEgressDomains {
		violations = append(violations, PolicyViolation{
			Rule:     "max_egress_domains",
			Severity: "error",
			Message:  fmt.Sprintf("skill has %d egress domains (max: %d)", len(sd.EgressDomains), policy.MaxEgressDomains),
		})
	}

	// Rule 2: BinaryDenylist
	denySet := make(map[string]bool, len(policy.BinaryDenylist))
	for _, b := range policy.BinaryDenylist {
		denySet[b] = true
	}
	for _, bin := range sd.RequiredBins {
		if denySet[bin] {
			violations = append(violations, PolicyViolation{
				Rule:     "binary_denylist",
				Severity: "error",
				Message:  fmt.Sprintf("binary %q is denied by policy", bin),
			})
		}
	}

	// Rule 3: DeniedEnvPatterns
	allEnv := make([]string, 0, len(sd.RequiredEnv)+len(sd.OneOfEnv)+len(sd.OptionalEnv))
	allEnv = append(allEnv, sd.RequiredEnv...)
	allEnv = append(allEnv, sd.OneOfEnv...)
	allEnv = append(allEnv, sd.OptionalEnv...)
	for _, env := range allEnv {
		for _, pattern := range policy.DeniedEnvPatterns {
			if strings.Contains(strings.ToUpper(env), strings.ToUpper(pattern)) {
				violations = append(violations, PolicyViolation{
					Rule:     "denied_env_pattern",
					Severity: "error",
					Message:  fmt.Sprintf("env var %q matches denied pattern %q", env, pattern),
				})
			}
		}
	}

	// Rule 4: ScriptPolicy
	if hasScript {
		switch policy.ScriptPolicy {
		case "deny":
			violations = append(violations, PolicyViolation{
				Rule:     "script_policy",
				Severity: "error",
				Message:  "skill has an executable script (denied by policy)",
			})
		case "warn":
			violations = append(violations, PolicyViolation{
				Rule:     "script_policy",
				Severity: "warning",
				Message:  "skill has an executable script",
			})
		}
		// "allow" - no violation
	}

	// Rule 5: MaxRiskScore
	if policy.MaxRiskScore > 0 {
		assessment := AnalyzeSkillDescriptor(sd, hasScript)
		if assessment.Score.Value > policy.MaxRiskScore {
			violations = append(violations, PolicyViolation{
				Rule:     "max_risk_score",
				Severity: "error",
				Message:  fmt.Sprintf("risk score %d exceeds maximum %d", assessment.Score.Value, policy.MaxRiskScore),
			})
		}
	}

	return violations
}

// CheckPolicyFromEntry evaluates a SkillEntry against a SecurityPolicy.
// It builds a temporary SkillDescriptor from the entry's metadata.
func CheckPolicyFromEntry(entry *contract.SkillEntry, hasScript bool, policy SecurityPolicy) []PolicyViolation {
	sd := entryToDescriptor(entry)
	return CheckPolicy(sd, hasScript, policy)
}

// entryToDescriptor converts a SkillEntry to a SkillDescriptor for policy checking.
func entryToDescriptor(entry *contract.SkillEntry) *contract.SkillDescriptor {
	sd := &contract.SkillDescriptor{
		Name: entry.Name,
	}
	if entry.ForgeReqs != nil {
		sd.RequiredBins = entry.ForgeReqs.Bins
		if entry.ForgeReqs.Env != nil {
			sd.RequiredEnv = entry.ForgeReqs.Env.Required
			sd.OneOfEnv = entry.ForgeReqs.Env.OneOf
			sd.OptionalEnv = entry.ForgeReqs.Env.Optional
		}
	}
	if entry.Metadata != nil && entry.Metadata.Metadata != nil {
		if forgeMap, ok := entry.Metadata.Metadata["forge"]; ok {
			if raw, ok := forgeMap["egress_domains"]; ok {
				if arr, ok := raw.([]any); ok {
					for _, v := range arr {
						if s, ok := v.(string); ok {
							sd.EgressDomains = append(sd.EgressDomains, s)
						}
					}
				}
			}
		}
	}
	return sd
}
