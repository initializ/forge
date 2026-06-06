package security

import (
	"fmt"
	"strings"

	"github.com/initializ/forge/forge-core/types"
)

// PolicyViolation describes a single conflict between forge.yaml's
// declaration and a policy layer. Multiple violations may be reported
// from one enforcement pass — the runner emits all of them to audit,
// then aborts with a single combined error so the developer sees
// every problem in one go instead of fixing them one at a time.
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

	// Layer is the policy source that enforced this rule
	// ("system" / "user" / "workspace"). System-layer violations
	// signal a sysadmin-set bound; user-layer signals the local
	// developer's own policy; workspace signals the deploy-time
	// operator policy. See issue #90 / FWS-6.
	Layer string

	// LayerPath is the on-disk location of the enforcing policy file.
	// Surfaced in audit events so consumers can fetch the document
	// directly and in the formatted error so developers know which
	// file to look at.
	LayerPath string
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
// See issue #89 / FWS-5, issue #90 / FWS-6.
func EnforcePolicy(cfg *types.ForgeConfig, layers []PolicyLayer) []PolicyViolation {
	if len(layers) == 0 {
		return nil
	}
	var violations []PolicyViolation

	// Egress: each declared domain that any layer denies is a
	// violation; first layer to deny takes attribution credit.
	for _, d := range cfg.Egress.AllowedDomains {
		if src := FirstLayerDenyingEgress(layers, d); src != nil {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationDeniedEgress,
				OffendingValue: d,
				ForgeYAMLField: "egress.allowed_domains",
				Layer:          src.Source,
				LayerPath:      src.Path,
			})
		}
	}

	// Tools: declared tools (cfg.Tools[].Name) or builtin_tools[]
	// matched against the union of layer deny lists.
	for _, t := range cfg.Tools {
		if src := FirstLayerDenyingTool(layers, t.Name); src != nil {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationDeniedTool,
				OffendingValue: t.Name,
				ForgeYAMLField: "tools",
				Layer:          src.Source,
				LayerPath:      src.Path,
			})
		}
	}
	for _, t := range cfg.BuiltinTools {
		if src := FirstLayerDenyingTool(layers, t); src != nil {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationDeniedTool,
				OffendingValue: t,
				ForgeYAMLField: "builtin_tools",
				Layer:          src.Source,
				LayerPath:      src.Path,
			})
		}
	}

	// Model: primary + every fallback. Each match attributed to the
	// most-restrictive layer (first deny wins).
	if cfg.Model.Provider != "" {
		if src := FirstLayerForbiddingModel(layers, cfg.Model.Provider, cfg.Model.Name); src != nil {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationForbiddenModel,
				OffendingValue: cfg.Model.Provider + "/" + cfg.Model.Name,
				ForgeYAMLField: "model",
				Layer:          src.Source,
				LayerPath:      src.Path,
			})
		}
	}
	for i, fb := range cfg.Model.Fallbacks {
		if src := FirstLayerForbiddingModel(layers, fb.Provider, fb.Name); src != nil {
			violations = append(violations, PolicyViolation{
				Kind:           ViolationForbiddenModel,
				OffendingValue: fb.Provider + "/" + fb.Name,
				ForgeYAMLField: fmt.Sprintf("model.fallbacks[%d]", i),
				Layer:          src.Source,
				LayerPath:      src.Path,
			})
		}
	}

	// Size bounds use the MOST RESTRICTIVE non-zero value across all
	// layers ("most restrictive wins"); the layer whose bound was
	// effective takes attribution.
	if max, src := MostRestrictiveEgressMax(layers); max > 0 && len(cfg.Egress.AllowedDomains) > max {
		violations = append(violations, PolicyViolation{
			Kind:           ViolationEgressBoundExceeded,
			OffendingValue: fmt.Sprintf("%d (max %d)", len(cfg.Egress.AllowedDomains), max),
			ForgeYAMLField: "egress.allowed_domains",
			Layer:          src.Source,
			LayerPath:      src.Path,
		})
	}
	effective := EffectiveToolCount(cfg, layers)
	if max, src := MostRestrictiveToolMax(layers); max > 0 && effective > max {
		violations = append(violations, PolicyViolation{
			Kind:           ViolationToolBoundExceeded,
			OffendingValue: fmt.Sprintf("%d (max %d)", effective, max),
			ForgeYAMLField: "tools+builtin_tools",
			Layer:          src.Source,
			LayerPath:      src.Path,
		})
	}

	return violations
}

// EffectiveEgressAllowlist returns forge.yaml's allowed_domains with
// any entry denied by ANY layer removed. The unioned deny list is
// what reaches the EgressEnforcer.
//
// Returns the input unchanged when no layers are loaded.
func EffectiveEgressAllowlist(cfg *types.ForgeConfig, layers []PolicyLayer) []string {
	if len(layers) == 0 {
		return cfg.Egress.AllowedDomains
	}
	out := make([]string, 0, len(cfg.Egress.AllowedDomains))
	for _, d := range cfg.Egress.AllowedDomains {
		if FirstLayerDenyingEgress(layers, d) == nil {
			out = append(out, d)
		}
	}
	return out
}

// EffectiveDeniedTools returns the union of forge.yaml's declared
// deny list (from the derived CLI config) and every layer's tool deny.
// Used by the runner to strip tools from the registry at startup.
// Dedupes; preserves forge.yaml ordering first, then appends each
// layer's denies in load order.
func EffectiveDeniedTools(forgeDenied []string, layers []PolicyLayer) []string {
	if len(layers) == 0 {
		return forgeDenied
	}
	totalCap := len(forgeDenied)
	for _, l := range layers {
		totalCap += len(l.Policy.DeniedTools)
	}
	seen := make(map[string]struct{}, totalCap)
	out := make([]string, 0, totalCap)
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, t := range forgeDenied {
		add(t)
	}
	for _, l := range layers {
		for _, t := range l.Policy.DeniedTools {
			add(t)
		}
	}
	return out
}

// EffectiveToolCount returns how many tools the agent would register
// after every layer's deny strip. Used by the bound check above and
// the runner's startup log.
func EffectiveToolCount(cfg *types.ForgeConfig, layers []PolicyLayer) int {
	count := 0
	for _, t := range cfg.Tools {
		if FirstLayerDenyingTool(layers, t.Name) == nil {
			count++
		}
	}
	for _, t := range cfg.BuiltinTools {
		if FirstLayerDenyingTool(layers, t) == nil {
			count++
		}
	}
	return count
}

// ChannelSkip records a channel that was skipped at startup and which
// layer's deny list named it. The runner emits one
// channel_denied_by_policy audit event per skip with the layer name
// attached. The security package stays free of the runtime
// dependency (audit lives in forge-core/runtime); the caller walks
// the slice and emits.
//
// See issue #90 / FWS-6.
type ChannelSkip struct {
	Channel   string
	Layer     string // "system" / "user" / "workspace"
	LayerPath string // on-disk path for fields.source
}

// EffectiveChannels returns the channel list that should actually be
// started (after policy filtering) along with one ChannelSkip per
// filtered entry. The caller iterates the effective list to start
// adapters and the skip list to emit audit events.
//
// Attribution: when a channel is denied by multiple layers, the
// system layer wins (first match in layer load order: system → user
// → workspace). System-layer denies are the most visible in the
// audit pipeline.
//
// See issue #90 / FWS-6.
func EffectiveChannels(declared []string, layers []PolicyLayer) (effective []string, skipped []ChannelSkip) {
	effective = make([]string, 0, len(declared))
	for _, c := range declared {
		if src := FirstLayerDenyingChannel(layers, c); src != nil {
			skipped = append(skipped, ChannelSkip{Channel: c, Layer: src.Source, LayerPath: src.Path})
			continue
		}
		effective = append(effective, c)
	}
	return effective, skipped
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
		// Include the deciding layer + path so the developer knows
		// which policy file owns the rule (and who to ask for an
		// exception — sysadmin for system, themselves for user,
		// platform operator for workspace).
		if v.Layer != "" {
			fmt.Fprintf(&b, "  - %s: %s at forge.yaml field %s (enforced by %s policy: %s)\n",
				v.Kind, v.OffendingValue, v.ForgeYAMLField, v.Layer, v.LayerPath)
		} else {
			fmt.Fprintf(&b, "  - %s: %s at forge.yaml field %s\n",
				v.Kind, v.OffendingValue, v.ForgeYAMLField)
		}
	}
	b.WriteString("\nThis configuration is forbidden by one or more policy layers " +
		"(system / user / workspace). Either update forge.yaml to comply, edit " +
		"the user policy at ~/.forge/policy.yaml, or contact the policy owner. " +
		"See docs/security/platform-policy.md.")
	return b.String()
}
