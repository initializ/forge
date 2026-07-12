package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/initializ/guardrails/models"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
)

// LoadPlatformGuardrailsOverlay reads the `guardrails:` overlay from every
// platform-policy layer (system → user → workspace, the same layers the
// capability policy uses) and folds them into a single most-restrictive
// overlay.
//
// The overlay is authored in the YAML policy.yaml using the SAME schema as
// the agent's guardrails.json (guardrails.StructuredGuardrails, camelCase
// field names). forge-core carries it as a raw YAML subtree; here we bridge
// each layer YAML→JSON→typed struct and union them via MergeGuardrails.
//
// Returns (nil, nil, nil) when no layer declares a guardrails overlay — the
// common case, where the agent's guardrails.json stands alone. A malformed
// overlay (or a policy layer that won't parse) is a hard error, matching the
// fail-loud posture of the capability-policy loader.
func LoadPlatformGuardrailsOverlay() (*models.StructuredGuardrails, []string, error) {
	layers, err := security.LoadAllPolicyLayers()
	if err != nil {
		return nil, nil, err
	}

	var combined *models.StructuredGuardrails
	var sources []string
	for i := range layers {
		raw := layers[i].Policy.Guardrails
		if len(raw) == 0 {
			continue
		}
		ov, err := overlayFromRawYAML(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing %s-layer guardrails overlay (%s): %w",
				layers[i].Source, layers[i].Path, err)
		}
		// Fold onto the accumulator. MergeGuardrails is a most-restrictive
		// union; the effective strictness is order-independent, though a few
		// first-writer-wins fallbacks (urlFilter/hallucination Mode when
		// unset, rule-ID dedupe) resolve to whichever layer set them first.
		combined, _ = MergeGuardrails(combined, ov)
		sources = append(sources, layers[i].Source)
	}
	if combined == nil {
		return nil, nil, nil
	}
	return combined, sources, nil
}

// overlayFromRawYAML bridges the raw YAML subtree (already decoded to
// map[string]any by the YAML policy loader) into the typed
// StructuredGuardrails: marshal the map to JSON, then strictly unmarshal
// into the struct. Strict unmarshal (DisallowUnknownFields) preserves the
// "operator typos fail loudly" posture of the capability-policy loader — a
// mistyped `comandInjection:` is rejected rather than silently ignored.
func overlayFromRawYAML(raw map[string]any) (*models.StructuredGuardrails, error) {
	j, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encoding overlay: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	var sg models.StructuredGuardrails
	if err := dec.Decode(&sg); err != nil {
		return nil, fmt.Errorf("unknown or malformed guardrails field: %w", err)
	}
	return &sg, nil
}

// applyPlatformGuardrailsOverlay merges the platform overlay (if any) over
// the agent's guardrails and logs every tightening for visibility. Returns
// the effective guardrails to hand to the engine.
//
// FAIL-CLOSED: a malformed overlay is a hard error, not a warning. A platform
// guardrails overlay is an operator mandate in the same class as the egress /
// tool / model denies enforced by platform_policy_enforce.go — those refuse
// to start on conflict, and a typo'd `guardrails:` block that strict-decoding
// rejects must likewise abort rather than silently drop the intended
// tightening (which would start the agent LESS protected than the operator
// mandated). The caller (BuildGuardrailChecker) propagates the error so the
// runner exits non-zero.
func applyPlatformGuardrailsOverlay(agent *models.StructuredGuardrails, logger coreruntime.Logger) (*models.StructuredGuardrails, error) {
	overlay, sources, err := LoadPlatformGuardrailsOverlay()
	if err != nil {
		logger.Error("guardrails: platform overlay failed to load; refusing to start", map[string]any{
			"error": err.Error(),
		})
		return nil, err
	}
	if overlay == nil {
		return agent, nil
	}

	effective, tightenings := MergeGuardrails(agent, overlay)
	sortTightenings(tightenings)

	if len(tightenings) == 0 {
		logger.Info("guardrails: platform overlay loaded but tightened nothing (agent already at least as strict)", map[string]any{
			"layers": sources,
		})
		return effective, nil
	}

	changes := make([]string, 0, len(tightenings))
	for _, t := range tightenings {
		changes = append(changes, t.Field+": "+t.Change)
	}
	logger.Info("guardrails: platform overlay tightened agent guardrails", map[string]any{
		"layers":  sources,
		"changes": changes,
	})
	return effective, nil
}
