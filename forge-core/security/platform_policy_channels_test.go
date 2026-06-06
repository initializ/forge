package security

import (
	"testing"
)

// Regression tests for issue #90 / FWS-6 — three-layer channel
// filtering. EffectiveChannels now takes []PolicyLayer; deny lives in
// system / user / workspace policy files (not in forge.yaml). First-
// match-wins gives attribution to the most-restrictive layer.

func TestPlatformPolicy_ChannelDenied_BasicMatch(t *testing.T) {
	p := PlatformPolicy{DeniedChannels: []string{"slack", "msteams"}}
	if !p.ChannelDenied("slack") {
		t.Errorf("slack should be denied")
	}
	if p.ChannelDenied("telegram") {
		t.Errorf("telegram should NOT be denied")
	}
	if p.ChannelDenied("Slack") {
		t.Errorf("channel match must be case-sensitive")
	}
}

func TestEffectiveChannels_NoLayers_PassThrough(t *testing.T) {
	// Backward-compat: no policy files at all → declared list passes
	// through unchanged, zero skips. Matches pre-FWS-6 behavior.
	declared := []string{"slack", "telegram"}
	effective, skipped := EffectiveChannels(declared, nil)
	if len(effective) != 2 || effective[0] != "slack" || effective[1] != "telegram" {
		t.Errorf("no layers must pass declared through, got %+v", effective)
	}
	if len(skipped) != 0 {
		t.Errorf("no layers must produce no skips, got %+v", skipped)
	}
}

func TestEffectiveChannels_UserLayerOnly(t *testing.T) {
	// Most common case: the developer disabled a channel via the TUI.
	// No system or workspace layer present.
	layers := []PolicyLayer{
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedChannels: []string{"telegram"}}},
	}
	effective, skipped := EffectiveChannels([]string{"slack", "telegram"}, layers)
	if len(effective) != 1 || effective[0] != "slack" {
		t.Errorf("slack should remain, got %+v", effective)
	}
	if len(skipped) != 1 || skipped[0].Channel != "telegram" {
		t.Fatalf("telegram should be skipped, got %+v", skipped)
	}
	if skipped[0].Layer != LayerUser {
		t.Errorf("attribution should be user, got %q", skipped[0].Layer)
	}
	if skipped[0].LayerPath != "/home/dev/.forge/policy.yaml" {
		t.Errorf("LayerPath = %q, want user policy path", skipped[0].LayerPath)
	}
}

func TestEffectiveChannels_SystemBeatsUserBeatsWorkspace(t *testing.T) {
	// When multiple layers deny the same channel, attribution goes to
	// the FIRST loaded layer (system → user → workspace order). The
	// system layer is most-restrictive and the most visible in the
	// operator's audit pipeline.
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{DeniedChannels: []string{"telegram"}}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedChannels: []string{"telegram"}}},
		{Source: LayerWorkspace, Path: "/etc/forge/workspace.yaml",
			Policy: PlatformPolicy{DeniedChannels: []string{"telegram"}}},
	}
	_, skipped := EffectiveChannels([]string{"telegram"}, layers)
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skip, got %+v", skipped)
	}
	if skipped[0].Layer != LayerSystem {
		t.Errorf("system layer should win attribution, got %q", skipped[0].Layer)
	}
}

func TestEffectiveChannels_DifferentLayersDifferentDenies(t *testing.T) {
	// System denies one channel, user denies another. Both get
	// skipped, each attributed to its own layer.
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{DeniedChannels: []string{"slack"}}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedChannels: []string{"telegram"}}},
	}
	_, skipped := EffectiveChannels([]string{"slack", "telegram", "msteams"}, layers)
	if len(skipped) != 2 {
		t.Fatalf("expected 2 skips, got %+v", skipped)
	}
	got := map[string]string{}
	for _, s := range skipped {
		got[s.Channel] = s.Layer
	}
	if got["slack"] != LayerSystem {
		t.Errorf("slack attribution: got %q want %q", got["slack"], LayerSystem)
	}
	if got["telegram"] != LayerUser {
		t.Errorf("telegram attribution: got %q want %q", got["telegram"], LayerUser)
	}
}

func TestEffectiveChannels_DeclaredOrderPreserved(t *testing.T) {
	layers := []PolicyLayer{
		{Source: LayerUser, Path: "/x", Policy: PlatformPolicy{DeniedChannels: []string{"slack"}}},
	}
	declared := []string{"telegram", "slack", "msteams"}
	effective, _ := EffectiveChannels(declared, layers)
	if len(effective) != 2 || effective[0] != "telegram" || effective[1] != "msteams" {
		t.Errorf("declared order not preserved, got %+v", effective)
	}
}
