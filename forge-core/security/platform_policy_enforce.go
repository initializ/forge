package security

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/types"
)

// PolicyViolation describes a single conflict between forge.yaml's
// declaration and the platform policy. Multiple violations may be
// reported from one enforcement pass — the runner emits all of them
// to audit, then aborts with a single combined error so the developer
// sees every problem in one go instead of fixing them one at a time.
type PolicyViolation struct {
	// Kind classifies the violation. The runner audit emitter uses
	// this as the violation_kind field; consumers (cost / compliance
	// dashboards) group by kind.
	Kind PolicyViolationKind

	// OffendingValue is what forge.yaml declared that the policy
	// forbids — a domain, tool name, model identifier, or a numeric
	// count for size-bound violations.
	OffendingValue string

	// ForgeYAMLField is the dotted path into forge.yaml where the
	// offending value lives (e.g. "egress.allowed_domains",
	// "model.name"). Lets the developer's error message point at the
	// exact field to edit.
	ForgeYAMLField string
}

// PolicyViolationKind enumerates the conflict categories. New values
// are additive — audit consumers that don't recognize a kind string
// should pass it through rather than reject the event.
type PolicyViolationKind string

const (
	ViolationDeniedEgress        PolicyViolationKind = "denied_egress"
	ViolationDeniedTool          PolicyViolationKind = "denied_tool"
	ViolationForbiddenModel      PolicyViolationKind = "forbidden_model"
	ViolationEgressBoundExceeded PolicyViolationKind = "egress_bound_exceeded"
	ViolationToolBoundExceeded   PolicyViolationKind = "tool_bound_exceeded"
)

// EnforcePolicy compares a forge.yaml-derived ForgeConfig against the
// loaded platform policy and reports every violation. An empty
// violation slice means the configuration is acceptable — the runner
// should also compute the effective allowlist via
// EffectiveEgressAllowlist and the effective deny list via
// EffectiveDeniedTools, both of which apply the intersection / union
// math separately from violation detection.
//
// The split is intentional: forbidden_model and denied_egress are
// HARD errors that abort startup (the developer's declaration is
// explicitly forbidden), while the bound checks are also hard errors
// but classified separately so the audit pipeline can distinguish
// "policy-forbidden-value" from "policy-over-budget" — they need
// different operator responses.
//
// See issue #89 / FWS-5.
func EnforcePolicy(cfg *types.ForgeConfig, policy PlatformPolicy) []PolicyViolation {
	if policy.IsZero() {
		return nil
	}
	var violations []PolicyViolation

	// Egress: each domain in forge.yaml that's on the policy deny
	// list is a violation. Reported individually so the developer's
	// error message can name every offending entry.
	for _, d := range cfg.Egress.AllowedDomains {
		if policy.EgressDomainDenied(d) {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationDeniedEgress,
				OffendingValue: d,
				ForgeYAMLField: "egress.allowed_domains",
			})
		}
	}

	// Tools: per-tool union (forge.yaml's denies are added to the
	// policy denies; a tool ON the policy deny list that forge.yaml
	// ALSO declares is just stripped, not a violation). Violation
	// only fires when the developer is trying to DECLARE a denied
	// tool — i.e. it's in cfg.Tools AND in policy.DeniedTools.
	for _, t := range cfg.Tools {
		if policy.ToolDenied(t.Name) {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationDeniedTool,
				OffendingValue: t.Name,
				ForgeYAMLField: "tools",
			})
		}
	}
	for _, t := range cfg.BuiltinTools {
		if policy.ToolDenied(t) {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationDeniedTool,
				OffendingValue: t,
				ForgeYAMLField: "builtin_tools",
			})
		}
	}

	// Model: primary + every fallback.
	if cfg.Model.Provider != "" && policy.ModelForbidden(cfg.Model.Provider, cfg.Model.Name) {
		violations = append(violations, PolicyViolation{
			Kind:           ViolationForbiddenModel,
			OffendingValue: cfg.Model.Provider + "/" + cfg.Model.Name,
			ForgeYAMLField: "model",
		})
	}
	for i, fb := range cfg.Model.Fallbacks {
		if policy.ModelForbidden(fb.Provider, fb.Name) {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationForbiddenModel,
				OffendingValue: fb.Provider + "/" + fb.Name,
				ForgeYAMLField: fmt.Sprintf("model.fallbacks[%d]", i),
			})
		}
	}

	// Size bounds: the count must NOT exceed the cap (zero cap = no
	// limit). Reported once with the count itself as the offending
	// value so the developer's error reads "egress allow-list size
	// 73 exceeds policy max 50."
	if policy.MaxEgressAllowlistSize > 0 && len(cfg.Egress.AllowedDomains) > policy.MaxEgressAllowlistSize {
		violations = append(violations, PolicyViolation{
			Kind:           ViolationEgressBoundExceeded,
			OffendingValue: fmt.Sprintf("%d (max %d)", len(cfg.Egress.AllowedDomains), policy.MaxEgressAllowlistSize),
			ForgeYAMLField: "egress.allowed_domains",
		})
	}
	// Tool count is checked AFTER the policy strip: the bound applies
	// to the effective tool count, not the declared one (otherwise
	// stripping a denied tool would cause a spurious bound violation
	// on a forge.yaml that's actually under the limit).
	effective := EffectiveToolCount(cfg, policy)
	if policy.MaxToolCount > 0 && effective > policy.MaxToolCount {
		violations = append(violations, PolicyViolation{
			Kind:           ViolationToolBoundExceeded,
			OffendingValue: fmt.Sprintf("%d (max %d)", effective, policy.MaxToolCount),
			ForgeYAMLField: "tools+builtin_tools",
		})
	}

	return violations
}

// EffectiveEgressAllowlist returns forge.yaml's allowed_domains with
// any entries on the policy deny list removed. Used by the runner's
// security wiring to construct the actual EgressEnforcer config —
// the developer's forge.yaml declaration is filtered through the
// policy before it reaches the enforcer.
//
// Returns the input unchanged when the policy is zero.
func EffectiveEgressAllowlist(cfg *types.ForgeConfig, policy PlatformPolicy) []string {
	if policy.IsZero() || len(policy.DeniedEgressDomains) == 0 {
		return cfg.Egress.AllowedDomains
	}
	out := make([]string, 0, len(cfg.Egress.AllowedDomains))
	for _, d := range cfg.Egress.AllowedDomains {
		if !policy.EgressDomainDenied(d) {
			out = append(out, d)
		}
	}
	return out
}

// EffectiveDeniedTools returns the union of forge.yaml's declared
// deny list (from the derived CLI config — empty here, the runner
// passes its own existing list) and the platform policy's denies.
// Used by the runner to strip tools from the registry at startup.
//
// Caller passes the existing forge.yaml-derived deny list (typically
// runner.derivedCLIConfig.DeniedTools); this function adds the
// policy denies on top.
func EffectiveDeniedTools(forgeDenied []string, policy PlatformPolicy) []string {
	if policy.IsZero() || len(policy.DeniedTools) == 0 {
		return forgeDenied
	}
	seen := make(map[string]struct{}, len(forgeDenied)+len(policy.DeniedTools))
	out := make([]string, 0, len(forgeDenied)+len(policy.DeniedTools))
	for _, t := range forgeDenied {
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	for _, t := range policy.DeniedTools {
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// EffectiveToolCount returns the count of tools forge.yaml would
// register after the policy strip. Used by both the bound check in
// EnforcePolicy and (eventually) by the runner's logging.
func EffectiveToolCount(cfg *types.ForgeConfig, policy PlatformPolicy) int {
	count := 0
	for _, t := range cfg.Tools {
		if !policy.ToolDenied(t.Name) {
			count++
		}
	}
	for _, t := range cfg.BuiltinTools {
		if !policy.ToolDenied(t) {
			count++
		}
	}
	return count
}

// FormatViolations returns a multi-line, developer-friendly error
// message describing every violation. The runner uses this as the
// returned error from NewRunner when violations are present, so the
// CLI's error path surfaces every problem in one pass — developers
// fix the forge.yaml once and re-run rather than ping-ponging through
// one error at a time.
func FormatViolations(violations []PolicyViolation) string {
	if len(violations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("platform policy violations:\n")
	for _, v := range violations {
		fmt.Fprintf(&b, "  - %s: %s at forge.yaml field %s\n",
			v.Kind, v.OffendingValue, v.ForgeYAMLField)
	}
	b.WriteString("\nThe deployed platform policy forbids this configuration. " +
		"Either update forge.yaml to comply, or request a policy change from " +
		"the platform operator. See docs/security/platform-policy.md.")
	return b.String()
}
