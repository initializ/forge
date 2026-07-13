package analyzer

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/parser"
)

// DefaultPolicy returns a SecurityPolicy with sensible defaults.
//
// MaxRiskScore=90 is the ceiling for vetted multi-purpose skills.
// The bundled code-review skill (6 egress domains + 9 config-knob env
// vars + a backing script) lands in the 75–85 band under the standard
// scoring rules; the legacy 75 ceiling would block it out of the box.
// Operators who want a stricter posture can lower the ceiling via a
// SecurityPolicy YAML file passed through `forge build --policy` or
// `security.policy_path` in forge.yaml.
func DefaultPolicy() SecurityPolicy {
	return SecurityPolicy{
		MaxEgressDomains: 0, // unlimited
		BinaryDenylist:   []string{"nc", "ncat", "netcat", "nmap", "ssh", "scp"},
		DeniedEnvPatterns: []string{
			"AWS_SECRET_ACCESS_KEY",
			"AWS_SESSION_TOKEN",
		},
		ScriptPolicy:   "warn",
		MaxRiskScore:   90,
		MaxTags:        20,
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

	// Rule 5: MaxTags
	if policy.MaxTags > 0 && len(sd.Tags) > policy.MaxTags {
		violations = append(violations, PolicyViolation{
			Rule:     "excessive-tags",
			Severity: "warning",
			Message:  fmt.Sprintf("skill has %d tags (recommended max: %d)", len(sd.Tags), policy.MaxTags),
		})
	}

	// Rule 6: MaxRiskScore
	if policy.MaxRiskScore > 0 {
		assessment := AnalyzeSkillDescriptor(sd, hasScript, policy)
		if assessment.Score.Value > policy.MaxRiskScore {
			violations = append(violations, PolicyViolation{
				Rule:     "max_risk_score",
				Severity: "error",
				Message:  fmt.Sprintf("risk score %d exceeds maximum %d", assessment.Score.Value, policy.MaxRiskScore),
			})
		}
	}

	// Rule 7: capability/trust-hint consistency. Declaring the browser
	// capability while claiming network: false is a contradiction —
	// browsing requires network by definition — and a sign the trust hints
	// were not written honestly.
	if hasCapability(sd.Capabilities, contract.CapabilityBrowser) &&
		sd.TrustHints != nil && sd.TrustHints.Network != nil && !*sd.TrustHints.Network {
		violations = append(violations, PolicyViolation{
			Rule:     "capability_trust_conflict",
			Severity: "critical",
			Message:  "skill declares the browser capability but trust_hints.network is false; browsing requires network access",
		})
	}

	// Rule 8: a browser skill with no deny_output guardrail can leak
	// whatever it reads (page content flows back verbatim). Warn so authors
	// add a redaction pattern.
	if hasCapability(sd.Capabilities, contract.CapabilityBrowser) && !sd.HasDenyOutput {
		violations = append(violations, PolicyViolation{
			Rule:     "capability_guardrail_gap",
			Severity: "warning",
			Message:  "browser skill declares no guardrails.deny_output; extracted page content is returned unredacted",
		})
	}

	return violations
}

func hasCapability(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
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
	if entry.Metadata != nil {
		sd.Category = entry.Metadata.Category
		sd.Tags = entry.Metadata.Tags
	}
	if entry.ForgeReqs != nil {
		for _, b := range entry.ForgeReqs.Bins {
			sd.RequiredBins = append(sd.RequiredBins, b.Name)
		}
		if entry.ForgeReqs.Env != nil {
			sd.RequiredEnv = entry.ForgeReqs.Env.Required
			sd.OneOfEnv = entry.ForgeReqs.Env.OneOf
			sd.OptionalEnv = entry.ForgeReqs.Env.Optional
		}
		sd.Capabilities = entry.ForgeReqs.Capabilities
	}
	// Read egress_domains, trust_hints, and guardrails from the typed forge
	// metadata (round-tripped through ExtractForgeMeta).
	if fm := parser.ExtractForgeMeta(entry.Metadata); fm != nil {
		sd.EgressDomains = fm.EgressDomains
		sd.TrustHints = fm.TrustHints
		if fm.Requires != nil && len(fm.Requires.Capabilities) > 0 {
			sd.Capabilities = fm.Requires.Capabilities
		}
		sd.HasDenyOutput = fm.Guardrails != nil && len(fm.Guardrails.DenyOutput) > 0
		sd.AllowSensitiveFill = fm.Guardrails != nil && fm.Guardrails.Browser != nil && fm.Guardrails.Browser.AllowSensitiveFill
	}
	return sd
}
