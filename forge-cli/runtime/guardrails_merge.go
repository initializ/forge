package runtime

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/initializ/guardrails/models"
)

// GuardrailTightening records one place where the platform overlay made an
// agent's guardrails STRICTER. Emitted for audit so an operator can see
// exactly what a layer changed (mirrors the violation attribution in
// forge-core/security/platform_policy_enforce.go).
type GuardrailTightening struct {
	// Field is the dotted path into StructuredGuardrails, e.g.
	// "security.commandInjection.action" or "gateConfig.outputGate".
	Field string
	// Change is a short human-readable before→after, e.g. "warn -> block"
	// or "enabled" or "+2 rules".
	Change string
}

// actionSeverity orders the redaction/enforcement actions from least to
// most restrictive. "Most severe wins" during a merge, so the platform can
// raise warn→mask→block but never lower it. Unknown actions rank 0 so an
// unrecognized platform action never silently downgrades a known agent one.
var actionSeverity = map[string]int{
	"warn":                   1,
	"flag":                   1,
	"mask":                   2,
	"redact":                 2,
	"replace":                2,
	"block":                  3,
	"require_human_approval": 4,
}

// moreSevereAction returns whichever action is stricter. When only one is
// set, that one wins. Never returns a less-severe action than `agent`, which
// is what enforces the never-loosen invariant for actions.
func moreSevereAction(agent, platform string) string {
	if platform == "" {
		return agent
	}
	if agent == "" {
		return platform
	}
	if actionSeverity[platform] > actionSeverity[agent] {
		return platform
	}
	return agent
}

// stricterThreshold returns the more-sensitive (LOWER) of two confidence
// thresholds, treating a non-positive value as "unset". A detector fires
// when confidence >= threshold, so a lower threshold fires more often =
// stricter. The platform can lower a threshold but never raise it.
func stricterThreshold(agent, platform float64) float64 {
	switch {
	case platform <= 0:
		return agent
	case agent <= 0:
		return platform
	case platform < agent:
		return platform
	default:
		return agent
	}
}

// MergeGuardrails returns the agent's guardrails tightened by the platform
// overlay — a one-way ratchet: the platform can force detections/gates ON,
// raise actions, lower thresholds, and union rule/denylist/blocked-skill
// sets, but can NEVER loosen anything the agent declared. An absent platform
// section leaves the agent's setting untouched.
//
// The returned value is a deep copy — neither input is mutated. The second
// return is the list of tightenings the platform applied, for audit.
func MergeGuardrails(agent, platform *models.StructuredGuardrails) (*models.StructuredGuardrails, []GuardrailTightening) {
	result := cloneGuardrails(agent)
	if platform == nil {
		return result, nil
	}
	var tt []GuardrailTightening
	add := func(field, change string) { tt = append(tt, GuardrailTightening{Field: field, Change: change}) }

	if platform.PII != nil {
		result.PII = mergePII(result.PII, platform.PII, add)
	}
	if platform.Moderation != nil {
		result.Moderation = mergeModeration(result.Moderation, platform.Moderation, add)
	}
	if platform.Security != nil {
		result.Security = mergeSecurity(result.Security, platform.Security, add)
	}
	if platform.URLFilter != nil {
		result.URLFilter = mergeURLFilter(result.URLFilter, platform.URLFilter, add)
	}
	if platform.CustomRules != nil {
		result.CustomRules = mergeCustomRules(result.CustomRules, platform.CustomRules, add)
	}
	if len(platform.ApprovalGates) > 0 {
		result.ApprovalGates = mergeApprovalGates(result.ApprovalGates, platform.ApprovalGates, add)
	}
	if platform.NSFWText != nil {
		result.NSFWText = mergeNSFW(result.NSFWText, platform.NSFWText, add)
	}
	if platform.Hallucination != nil {
		result.Hallucination = mergeHallucination(result.Hallucination, platform.Hallucination, add)
	}
	if platform.SkillConstraints != nil {
		result.SkillConstraints = mergeSkillConstraints(result.SkillConstraints, platform.SkillConstraints, add)
	}
	if platform.GateConfig != nil {
		result.GateConfig = mergeGateConfig(result.GateConfig, platform.GateConfig, add)
	}

	return result, tt
}

func mergePII(agent, platform *models.PIIConfig, add func(string, string)) *models.PIIConfig {
	if agent == nil {
		agent = &models.PIIConfig{Categories: map[string]models.PIICategoryConfig{}}
	}
	if platform.Enabled && !agent.Enabled {
		add("pii.enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	if na := moreSevereAction(agent.Action, platform.Action); na != agent.Action {
		add("pii.action", agent.Action+" -> "+na)
		agent.Action = na
	}
	if agent.Categories == nil {
		agent.Categories = map[string]models.PIICategoryConfig{}
	}
	for name, pc := range platform.Categories {
		ac, ok := agent.Categories[name]
		if !ok {
			add("pii.categories."+name, "added")
			agent.Categories[name] = pc
			continue
		}
		ac.Enabled = ac.Enabled || pc.Enabled
		ac.Action = moreSevereAction(ac.Action, pc.Action)
		agent.Categories[name] = ac
	}
	return agent
}

func mergeModeration(agent, platform *models.ModerationConfig, add func(string, string)) *models.ModerationConfig {
	if agent == nil {
		agent = &models.ModerationConfig{Categories: map[string]models.ModerationCategoryConfig{}}
	}
	if platform.Enabled && !agent.Enabled {
		add("moderation.enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	agent.Action = moreSevereAction(agent.Action, platform.Action)
	if agent.Categories == nil {
		agent.Categories = map[string]models.ModerationCategoryConfig{}
	}
	for name, pc := range platform.Categories {
		ac, ok := agent.Categories[name]
		if !ok {
			agent.Categories[name] = pc
			add("moderation.categories."+name, "added")
			continue
		}
		ac.Enabled = ac.Enabled || pc.Enabled
		ac.Action = moreSevereAction(ac.Action, pc.Action)
		ac.Threshold = stricterThreshold(ac.Threshold, pc.Threshold)
		agent.Categories[name] = ac
	}
	return agent
}

func mergeSecurity(agent, platform *models.SecurityConfig, add func(string, string)) *models.SecurityConfig {
	if agent == nil {
		agent = &models.SecurityConfig{}
	}
	agent.JailbreakDetection = mergeThreshold("security.jailbreakDetection", agent.JailbreakDetection, platform.JailbreakDetection, add)
	agent.PromptInjection = mergeThreshold("security.promptInjection", agent.PromptInjection, platform.PromptInjection, add)
	agent.SQLInjection = mergeThreshold("security.sqlInjection", agent.SQLInjection, platform.SQLInjection, add)
	agent.CommandInjection = mergeThreshold("security.commandInjection", agent.CommandInjection, platform.CommandInjection, add)

	if len(platform.CustomPatterns) > 0 {
		before := len(agent.CustomPatterns)
		seen := map[string]struct{}{}
		for _, p := range agent.CustomPatterns {
			seen[p.Name+"|"+p.Pattern] = struct{}{}
		}
		for _, p := range platform.CustomPatterns {
			if _, dup := seen[p.Name+"|"+p.Pattern]; dup {
				continue
			}
			agent.CustomPatterns = append(agent.CustomPatterns, p)
		}
		if n := len(agent.CustomPatterns) - before; n > 0 {
			add("security.customPatterns", fmt.Sprintf("+%d pattern(s)", n))
		}
	}
	return agent
}

func mergeThreshold(field string, agent, platform *models.ThresholdConfig, add func(string, string)) *models.ThresholdConfig {
	if platform == nil {
		return agent
	}
	if agent == nil {
		add(field, "added")
		cp := *platform
		return &cp
	}
	if platform.Enabled && !agent.Enabled {
		add(field+".enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	if nt := stricterThreshold(agent.ConfidenceThreshold, platform.ConfidenceThreshold); nt != agent.ConfidenceThreshold {
		add(field+".confidenceThreshold", fmt.Sprintf("%g -> %g", agent.ConfidenceThreshold, nt))
		agent.ConfidenceThreshold = nt
	}
	if na := moreSevereAction(agent.Action, platform.Action); na != agent.Action {
		add(field+".action", agent.Action+" -> "+na)
		agent.Action = na
	}
	return agent
}

func mergeURLFilter(agent, platform *models.URLFilterConfig, add func(string, string)) *models.URLFilterConfig {
	if agent == nil {
		agent = &models.URLFilterConfig{}
	}
	if platform.Enabled && !agent.Enabled {
		add("urlFilter.enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	agent.Action = moreSevereAction(agent.Action, platform.Action)

	if n := unionInto(&agent.Denylist, platform.Denylist); n > 0 {
		add("urlFilter.denylist", fmt.Sprintf("+%d domain(s)", n))
	}
	// Allowlist intersection tightens (fewer URLs pass). Only intersect when
	// the platform actually declares an allowlist — an empty platform
	// allowlist means "no opinion", not "deny everything".
	if len(platform.Allowlist) > 0 && len(agent.Allowlist) > 0 {
		before := len(agent.Allowlist)
		agent.Allowlist = intersectStrings(agent.Allowlist, platform.Allowlist)
		if before != len(agent.Allowlist) {
			add("urlFilter.allowlist", fmt.Sprintf("%d -> %d (intersect)", before, len(agent.Allowlist)))
		}
	} else if len(platform.Allowlist) > 0 && len(agent.Allowlist) == 0 {
		agent.Allowlist = append([]string(nil), platform.Allowlist...)
		add("urlFilter.allowlist", fmt.Sprintf("set %d (platform)", len(agent.Allowlist)))
	}
	// When both lists are populated, "both" mode is the only one that
	// enforces the union of constraints.
	if len(agent.Allowlist) > 0 && len(agent.Denylist) > 0 {
		agent.Mode = "both"
	} else if agent.Mode == "" {
		agent.Mode = platform.Mode
	}
	return agent
}

func mergeCustomRules(agent, platform *models.CustomRulesConfig, add func(string, string)) *models.CustomRulesConfig {
	if agent == nil {
		agent = &models.CustomRulesConfig{}
	}
	if n := unionInto(&agent.HardConstraints, platform.HardConstraints); n > 0 {
		add("customRules.hardConstraints", fmt.Sprintf("+%d", n))
	}
	if n := unionInto(&agent.SoftConstraints, platform.SoftConstraints); n > 0 {
		add("customRules.softConstraints", fmt.Sprintf("+%d", n))
	}
	if len(platform.Rules) > 0 {
		seen := map[string]struct{}{}
		for _, r := range agent.Rules {
			seen[r.ID] = struct{}{}
		}
		before := len(agent.Rules)
		for _, r := range platform.Rules {
			if r.ID != "" {
				if _, dup := seen[r.ID]; dup {
					continue
				}
				seen[r.ID] = struct{}{}
			}
			agent.Rules = append(agent.Rules, r)
		}
		if n := len(agent.Rules) - before; n > 0 {
			add("customRules.rules", fmt.Sprintf("+%d rule(s)", n))
		}
	}
	return agent
}

func mergeApprovalGates(agent, platform []models.ApprovalCondition, add func(string, string)) []models.ApprovalCondition {
	seen := map[string]struct{}{}
	for _, c := range agent {
		seen[c.ID] = struct{}{}
	}
	before := len(agent)
	for _, c := range platform {
		if c.ID != "" {
			if _, dup := seen[c.ID]; dup {
				continue
			}
			seen[c.ID] = struct{}{}
		}
		agent = append(agent, c)
	}
	if n := len(agent) - before; n > 0 {
		add("approvalGates", fmt.Sprintf("+%d condition(s)", n))
	}
	return agent
}

func mergeNSFW(agent, platform *models.NSFWTextConfig, add func(string, string)) *models.NSFWTextConfig {
	if agent == nil {
		add("nsfwText", "added")
		cp := *platform
		return &cp
	}
	if platform.Enabled && !agent.Enabled {
		add("nsfwText.enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	agent.ConfidenceThreshold = stricterThreshold(agent.ConfidenceThreshold, platform.ConfidenceThreshold)
	if na := moreSevereAction(agent.Action, platform.Action); na != agent.Action {
		add("nsfwText.action", agent.Action+" -> "+na)
		agent.Action = na
	}
	return agent
}

func mergeHallucination(agent, platform *models.HallucinationConfig, add func(string, string)) *models.HallucinationConfig {
	if agent == nil {
		add("hallucination", "added")
		cp := *platform
		return &cp
	}
	if platform.Enabled && !agent.Enabled {
		add("hallucination.enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	if na := moreSevereAction(agent.Action, platform.Action); na != agent.Action {
		add("hallucination.action", agent.Action+" -> "+na)
		agent.Action = na
	}
	// More required sources = stricter.
	if platform.MinSourceCount > agent.MinSourceCount {
		add("hallucination.minSourceCount", fmt.Sprintf("%d -> %d", agent.MinSourceCount, platform.MinSourceCount))
		agent.MinSourceCount = platform.MinSourceCount
	}
	if agent.Mode == "" {
		agent.Mode = platform.Mode
	}
	return agent
}

func mergeSkillConstraints(agent, platform *models.SkillConstraintsConfig, add func(string, string)) *models.SkillConstraintsConfig {
	if agent == nil {
		agent = &models.SkillConstraintsConfig{}
	}
	if platform.Enabled && !agent.Enabled {
		add("skillConstraints.enabled", "enabled")
	}
	agent.Enabled = agent.Enabled || platform.Enabled
	agent.Action = moreSevereAction(agent.Action, platform.Action)
	if n := unionInto(&agent.BlockedSkills, platform.BlockedSkills); n > 0 {
		add("skillConstraints.blockedSkills", fmt.Sprintf("+%d", n))
	}
	if len(platform.AllowedSkills) > 0 && len(agent.AllowedSkills) > 0 {
		before := len(agent.AllowedSkills)
		agent.AllowedSkills = intersectStrings(agent.AllowedSkills, platform.AllowedSkills)
		if before != len(agent.AllowedSkills) {
			add("skillConstraints.allowedSkills", fmt.Sprintf("%d -> %d (intersect)", before, len(agent.AllowedSkills)))
		}
	} else if len(platform.AllowedSkills) > 0 {
		agent.AllowedSkills = append([]string(nil), platform.AllowedSkills...)
		add("skillConstraints.allowedSkills", fmt.Sprintf("set %d (platform)", len(agent.AllowedSkills)))
	}
	return agent
}

func mergeGateConfig(agent, platform *models.GateConfig, add func(string, string)) *models.GateConfig {
	if agent == nil {
		agent = &models.GateConfig{}
	}
	forceOn := func(name string, a *bool, p bool) {
		if p && !*a {
			add("gateConfig."+name, "enabled")
			*a = true
		}
	}
	forceOn("inputGate", &agent.InputGate, platform.InputGate)
	forceOn("contextGate", &agent.ContextGate, platform.ContextGate)
	forceOn("toolCallGate", &agent.ToolCallGate, platform.ToolCallGate)
	forceOn("outputGate", &agent.OutputGate, platform.OutputGate)
	forceOn("streamGate", &agent.StreamGate, platform.StreamGate)
	return agent
}

// unionInto appends every element of add[] not already present in *dst,
// preserving order, and returns how many were added.
func unionInto(dst *[]string, add []string) int {
	seen := map[string]struct{}{}
	for _, s := range *dst {
		seen[s] = struct{}{}
	}
	n := 0
	for _, s := range add {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		*dst = append(*dst, s)
		n++
	}
	return n
}

// intersectStrings returns the elements present in BOTH a and b, in a's
// order. Used for allowlist intersection (fewer entries = stricter).
func intersectStrings(a, b []string) []string {
	inB := make(map[string]struct{}, len(b))
	for _, s := range b {
		inB[s] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if _, ok := inB[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// cloneGuardrails deep-copies via JSON round-trip so the merge never mutates
// the caller's inputs. Returns a fresh empty config when g is nil.
func cloneGuardrails(g *models.StructuredGuardrails) *models.StructuredGuardrails {
	if g == nil {
		return &models.StructuredGuardrails{}
	}
	data, err := json.Marshal(g)
	if err != nil {
		return &models.StructuredGuardrails{}
	}
	var out models.StructuredGuardrails
	if err := json.Unmarshal(data, &out); err != nil {
		return &models.StructuredGuardrails{}
	}
	return &out
}

// sortTightenings orders tightenings by Field for stable audit output.
func sortTightenings(tt []GuardrailTightening) {
	sort.Slice(tt, func(i, j int) bool { return tt[i].Field < tt[j].Field })
}
