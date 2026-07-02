package graders

import (
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/types"
)

// EnforceAndRecord runs the REAL platform-policy engine (security.EnforcePolicy)
// against cfg + layers, and if it finds violations emits the same
// policy_violation_at_build_time audit event the runner emits before aborting.
// It returns the violations so a test can assert on Kind and layer attribution.
func EnforceAndRecord(rec *Recorder, cfg *types.ForgeConfig, layers []security.PolicyLayer) []security.PolicyViolation {
	violations := security.EnforcePolicy(cfg, layers)
	for _, v := range violations {
		rec.Logger.EmitPolicyViolationAtBuildTime(map[string]any{
			"kind":             string(v.Kind),
			"offending_value":  v.OffendingValue,
			"forge_yaml_field": v.ForgeYAMLField,
			"layer":            v.Layer,
			"layer_path":       v.LayerPath,
		})
	}
	return violations
}

// PolicyViolationRecorded reports whether a policy_violation_at_build_time
// event was recorded attributing the given violation kind to the given layer.
// This is the authoritative signal for policy-containment tests.
func PolicyViolationRecorded(rec *Recorder, kind, layer string) bool {
	for _, e := range rec.Events() {
		if e["event"] != coreruntime.AuditPolicyViolationAtBuildTime {
			continue
		}
		f, ok := e["fields"].(map[string]any)
		if !ok {
			continue
		}
		if f["kind"] == kind && f["layer"] == layer {
			return true
		}
	}
	return false
}
