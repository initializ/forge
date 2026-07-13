package security

import "testing"

import "github.com/initializ/forge/forge-core/agentspec"

func cmdLayer(source, path string, filters ...agentspec.CommandFilter) PolicyLayer {
	return PolicyLayer{Source: source, Path: path, Policy: PlatformPolicy{DeniedCommandPatterns: filters}}
}

// TestEffectiveDeniedCommandPatterns_UnionDedupeAttribution covers the #238
// resolution: patterns union across layers in load order, dedupe by pattern
// string, and the FIRST declaring layer owns the attribution.
func TestEffectiveDeniedCommandPatterns_UnionDedupeAttribution(t *testing.T) {
	layers := []PolicyLayer{
		cmdLayer("system", "/etc/forge/policy.yaml",
			agentspec.CommandFilter{Pattern: `rm\s+-rf`, Message: "no recursive delete"},
			agentspec.CommandFilter{Pattern: `kubectl\s+delete`}),
		cmdLayer("workspace", "/ws/policy.yaml",
			agentspec.CommandFilter{Pattern: `kubectl\s+delete`, Message: "dup — should be ignored"}, // dup by pattern
			agentspec.CommandFilter{Pattern: `git\s+push\s+--force`}),
	}

	got := EffectiveDeniedCommandPatterns(layers)
	if len(got) != 3 {
		t.Fatalf("expected 3 unioned patterns, got %d: %+v", len(got), got)
	}
	// First-layer attribution: the deduped kubectl pattern keeps the system layer.
	for _, p := range got {
		if p.Pattern == `kubectl\s+delete` {
			if p.LayerSource != "system" {
				t.Errorf("dedup should keep the first (system) layer's attribution, got %q", p.LayerSource)
			}
		}
		if p.Pattern == `git\s+push\s+--force` && p.LayerSource != "workspace" {
			t.Errorf("git pattern should be attributed to workspace, got %q", p.LayerSource)
		}
	}
}

func TestEffectiveDeniedCommandPatterns_EmptyLayers(t *testing.T) {
	if got := EffectiveDeniedCommandPatterns(nil); got != nil {
		t.Errorf("nil layers should yield nil, got %+v", got)
	}
}

// TestPlatformPolicy_IsZero_CommandPatterns confirms a policy carrying only
// denied_command_patterns is non-zero, so the runtime doesn't skip loading it.
func TestPlatformPolicy_IsZero_CommandPatterns(t *testing.T) {
	p := PlatformPolicy{DeniedCommandPatterns: []agentspec.CommandFilter{{Pattern: `rm\s+-rf`}}}
	if p.IsZero() {
		t.Error("a policy with denied_command_patterns must not be IsZero")
	}
}

// TestParsePlatformPolicy_DeniedCommandPatterns pins that the YAML round-trips
// under strict decoding — CommandFilter carries only json tags, so this
// verifies yaml.v3's lowercase-field-name mapping (pattern/message) holds and
// the strict decoder doesn't reject the block.
func TestParsePlatformPolicy_DeniedCommandPatterns(t *testing.T) {
	yaml := `
denied_command_patterns:
  - pattern: 'kubectl\s+delete'
    message: "destructive kubectl blocked by org policy"
  - pattern: 'git\s+push\s+--force'
`
	p, err := ParsePlatformPolicy([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.DeniedCommandPatterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(p.DeniedCommandPatterns))
	}
	if p.DeniedCommandPatterns[0].Pattern != `kubectl\s+delete` ||
		p.DeniedCommandPatterns[0].Message != "destructive kubectl blocked by org policy" {
		t.Errorf("first pattern decoded wrong: %+v", p.DeniedCommandPatterns[0])
	}
}
