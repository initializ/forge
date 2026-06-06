package security

import (
	"fmt"
	"os"
	"path/filepath"
)

// PolicyLayer is one of the three sources the runtime reads
// PlatformPolicy from. Each layer's policy is loaded independently;
// at enforcement time the runner walks all loaded layers and unions
// their denies (for *_egress_domains / _tools / _channels /
// forbidden_models) or takes the most restrictive max value (for
// bound caps).
//
// Layers, in order of broadening scope:
//
//   - LayerSystem: /etc/forge/policy.yaml (or FORGE_SYSTEM_POLICY).
//     Set by a sysadmin pushing corporate-laptop policy. Most users
//     can't write to this path; the runtime simply reads it (root
//     not required to read a world-readable policy file).
//
//   - LayerUser: ~/.forge/policy.yaml. Set by the developer via
//     `forge channel disable …` or the GUI's chip toggle. Applies to
//     every agent this user runs on this machine.
//
//   - LayerWorkspace: file at FORGE_PLATFORM_POLICY env var.
//     Set by the workspace operator (initializ Command, custom
//     controller, GitOps tooling) at deploy time. Applies to the
//     specific deployed agent. Unchanged from FWS-5.
//
// See issue #90 / FWS-6.
type PolicyLayer struct {
	// Source is the layer identifier ("system" / "user" / "workspace").
	// Audit events use this verbatim as the fields.layer value.
	Source string
	// Path is the on-disk location of the policy file. Audit events
	// use this as fields.source so a downstream consumer can fetch the
	// document directly.
	Path string
	// Policy is the parsed policy. Zero policy means the layer's file
	// was absent or empty — the loader includes only non-zero layers
	// in the returned slice, so callers don't need to check IsZero.
	Policy PlatformPolicy
}

const (
	LayerSystem    = "system"
	LayerUser      = "user"
	LayerWorkspace = "workspace"

	// DefaultSystemPolicyPath is /etc/forge/policy.yaml. Override via
	// FORGE_SYSTEM_POLICY (test isolation; non-root install paths).
	DefaultSystemPolicyPath = "/etc/forge/policy.yaml"
)

// SystemPolicyPath returns the on-disk system policy path with env
// override taken into account. Exposed so the CLI's --system flag
// writes to the same path the runtime reads from.
func SystemPolicyPath() string {
	if p := os.Getenv("FORGE_SYSTEM_POLICY"); p != "" {
		return p
	}
	return DefaultSystemPolicyPath
}

// UserPolicyPath returns ~/.forge/policy.yaml. Empty when the user
// has no home directory (rare; typically a chroot or test sandbox);
// callers treat empty as "no user layer."
func UserPolicyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".forge", "policy.yaml")
}

// WorkspacePolicyPath returns the path from FORGE_PLATFORM_POLICY
// (the FWS-5 env var, unchanged). Empty when not set — runtime treats
// empty as "no workspace layer."
func WorkspacePolicyPath() string {
	return os.Getenv("FORGE_PLATFORM_POLICY")
}

// LoadAllPolicyLayers reads each of the three layers and returns the
// non-zero ones in source order (system, then user, then workspace).
// Each absent or empty layer is silently omitted; this is the
// backward-compat path for pre-FWS-6 deployments that had only the
// FORGE_PLATFORM_POLICY env (or none of the three).
//
// A malformed policy at ANY layer is an error — operator (or
// sysadmin, or developer) mistake that must fail loudly. Silently
// dropping a broken layer would let a typo bypass intended bounds.
//
// Layer-ordering rule for audit attribution: when an offending value
// (e.g. a denied domain) is on multiple layers' deny lists, the FIRST
// loaded layer that contains it takes credit. The load order is
// system → user → workspace, so a system-layer deny "wins"
// attribution over a user-layer or workspace-layer deny on the same
// value. This makes the most-restrictive layer the most visible in
// the audit pipeline.
func LoadAllPolicyLayers() ([]PolicyLayer, error) {
	var out []PolicyLayer
	if p, err := loadLayerIfPresent(SystemPolicyPath()); err != nil {
		return nil, fmt.Errorf("loading system policy: %w", err)
	} else if !p.IsZero() {
		out = append(out, PolicyLayer{Source: LayerSystem, Path: SystemPolicyPath(), Policy: p})
	}
	if up := UserPolicyPath(); up != "" {
		if p, err := loadLayerIfPresent(up); err != nil {
			return nil, fmt.Errorf("loading user policy: %w", err)
		} else if !p.IsZero() {
			out = append(out, PolicyLayer{Source: LayerUser, Path: up, Policy: p})
		}
	}
	if wp := WorkspacePolicyPath(); wp != "" {
		if p, err := loadLayerIfPresent(wp); err != nil {
			return nil, fmt.Errorf("loading workspace policy: %w", err)
		} else if !p.IsZero() {
			out = append(out, PolicyLayer{Source: LayerWorkspace, Path: wp, Policy: p})
		}
	}
	return out, nil
}

// loadLayerIfPresent delegates to LoadPlatformPolicy. The file-not-
// found path returns zero policy without error (matches the
// optional:true ConfigMap mount semantics from FWS-5); other errors
// (parse, unknown fields, validation) propagate.
func loadLayerIfPresent(path string) (PlatformPolicy, error) {
	return LoadPlatformPolicy(path)
}

// FirstLayerDenyingEgress returns the first layer whose deny list
// contains the given domain (system → user → workspace order).
// Returns nil when no layer denies. Used by the runner to attribute
// a policy_violation_at_build_time audit event to the deciding layer.
func FirstLayerDenyingEgress(layers []PolicyLayer, domain string) *PolicyLayer {
	for i := range layers {
		if layers[i].Policy.EgressDomainDenied(domain) {
			return &layers[i]
		}
	}
	return nil
}

// FirstLayerDenyingTool — see FirstLayerDenyingEgress.
func FirstLayerDenyingTool(layers []PolicyLayer, name string) *PolicyLayer {
	for i := range layers {
		if layers[i].Policy.ToolDenied(name) {
			return &layers[i]
		}
	}
	return nil
}

// FirstLayerForbiddingModel — see FirstLayerDenyingEgress.
func FirstLayerForbiddingModel(layers []PolicyLayer, provider, name string) *PolicyLayer {
	for i := range layers {
		if layers[i].Policy.ModelForbidden(provider, name) {
			return &layers[i]
		}
	}
	return nil
}

// FirstLayerDenyingChannel — see FirstLayerDenyingEgress.
func FirstLayerDenyingChannel(layers []PolicyLayer, name string) *PolicyLayer {
	for i := range layers {
		if layers[i].Policy.ChannelDenied(name) {
			return &layers[i]
		}
	}
	return nil
}

// MostRestrictiveEgressMax walks the layers and returns the smallest
// non-zero MaxEgressAllowlistSize plus the layer it came from. When
// no layer sets a non-zero max, returns 0 and nil — caller treats as
// "no cap." See issue #90 / FWS-6.
func MostRestrictiveEgressMax(layers []PolicyLayer) (int, *PolicyLayer) {
	var max int
	var src *PolicyLayer
	for i := range layers {
		v := layers[i].Policy.MaxEgressAllowlistSize
		if v <= 0 {
			continue
		}
		if max == 0 || v < max {
			max = v
			src = &layers[i]
		}
	}
	return max, src
}

// MostRestrictiveToolMax — see MostRestrictiveEgressMax.
func MostRestrictiveToolMax(layers []PolicyLayer) (int, *PolicyLayer) {
	var max int
	var src *PolicyLayer
	for i := range layers {
		v := layers[i].Policy.MaxToolCount
		if v <= 0 {
			continue
		}
		if max == 0 || v < max {
			max = v
			src = &layers[i]
		}
	}
	return max, src
}
