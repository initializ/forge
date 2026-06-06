package security

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// Regression tests for the policy enforcement engine. Issue #89 / FWS-5
// introduced the single-layer EnforcePolicy(cfg, PlatformPolicy);
// issue #90 / FWS-6 broadened it to EnforcePolicy(cfg, []PolicyLayer)
// so a system / user / workspace stack can compose. The pure-function
// shape (config + layers → []violation) lets us exhaustively cover
// every conflict category without spinning a runner.

// wrap is a tiny helper that turns a single PlatformPolicy into a
// one-element []PolicyLayer attributed to the system layer. The pre-
// FWS-6 single-policy cases convert cleanly via this wrapper.
func wrap(p PlatformPolicy) []PolicyLayer {
	return []PolicyLayer{{Source: LayerSystem, Path: "/etc/forge/policy.yaml", Policy: p}}
}

func TestEnforcePolicy_NoLayers_NoViolations(t *testing.T) {
	// Backward-compat: no policy files at all → no constraints. The
	// runner used to call this with PlatformPolicy{}; the layered API
	// expresses the same thing as a nil slice.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"api.openai.com", "api.slack.com"}},
		Tools:  []types.ToolRef{{Name: "cli_execute"}},
	}
	if v := EnforcePolicy(cfg, nil); len(v) != 0 {
		t.Errorf("no layers must report no violations, got %+v", v)
	}
}

func TestEnforcePolicy_DeniedEgressDomain_OneViolationPerDomain(t *testing.T) {
	// The Troy concern from the design meeting: a forge.yaml that
	// declares a domain on the platform deny list must be rejected
	// at startup — runtime safety net even if the PR linter missed it.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{
			"api.openai.com", "api.slack.com", "hooks.slack.com",
		}},
	}
	layers := wrap(PlatformPolicy{
		DeniedEgressDomains: []string{"api.slack.com", "hooks.slack.com"},
	})
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations (one per offending domain), got %d: %+v", len(violations), violations)
	}
	for _, v := range violations {
		if v.Kind != ViolationDeniedEgress {
			t.Errorf("wrong kind: %+v", v)
		}
		if v.ForgeYAMLField != "egress.allowed_domains" {
			t.Errorf("wrong field path: %+v", v)
		}
		if v.Layer != LayerSystem {
			t.Errorf("layer attribution: got %q, want %q", v.Layer, LayerSystem)
		}
		if v.LayerPath != "/etc/forge/policy.yaml" {
			t.Errorf("layer path: got %q", v.LayerPath)
		}
	}
}

func TestEnforcePolicy_DeniedTool_BothListsScanned(t *testing.T) {
	// Both tools[].name and builtin_tools[] (string list) must be
	// checked — otherwise a developer could bypass the deny list by
	// listing the tool under the wrong section.
	cfg := &types.ForgeConfig{
		Tools:        []types.ToolRef{{Name: "http_request"}, {Name: "allowed_tool"}},
		BuiltinTools: []string{"cli_execute", "datetime_now"},
	}
	layers := wrap(PlatformPolicy{
		DeniedTools: []string{"http_request", "cli_execute"},
	})
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations, got %+v", violations)
	}
	gotFields := map[string]bool{}
	for _, v := range violations {
		gotFields[v.ForgeYAMLField] = true
	}
	if !gotFields["tools"] || !gotFields["builtin_tools"] {
		t.Errorf("both tools and builtin_tools must be reported separately, got %+v", violations)
	}
}

func TestEnforcePolicy_ForbiddenModel_PrimaryAndFallbacks(t *testing.T) {
	// Fallbacks count as model declarations — a forbidden model in
	// the fallback list is just as much a violation as the primary,
	// otherwise developers could route around the deny list by
	// putting the forbidden model in fallback.
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "openai", Name: "gpt-4o",
			Fallbacks: []types.ModelFallback{
				{Provider: "anthropic", Name: "claude-opus-4"}, // forbidden
				{Provider: "anthropic", Name: "claude-sonnet-4-6"},
			},
		},
	}
	layers := wrap(PlatformPolicy{
		ForbiddenModels: []ModelMatcher{
			{Provider: "anthropic", Name: "claude-opus-4"},
		},
	})
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %+v", violations)
	}
	if violations[0].OffendingValue != "anthropic/claude-opus-4" {
		t.Errorf("wrong offending value: %q", violations[0].OffendingValue)
	}
	if violations[0].ForgeYAMLField != "model.fallbacks[0]" {
		t.Errorf("fallback field path missing index: %q", violations[0].ForgeYAMLField)
	}
}

func TestEnforcePolicy_EgressBoundExceeded(t *testing.T) {
	// Defense against allowlist bloat — a developer pasting 200
	// third-party domains. Bound check applies to the declared count,
	// not the policy-filtered count (the bound is "how big can your
	// allowlist be," not "how many of your allowed entries survive
	// the deny filter").
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{
			AllowedDomains: []string{"a", "b", "c", "d", "e"},
		},
	}
	layers := wrap(PlatformPolicy{MaxEgressAllowlistSize: 3})
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 1 || violations[0].Kind != ViolationEgressBoundExceeded {
		t.Fatalf("expected egress_bound_exceeded violation, got %+v", violations)
	}
	if !strings.Contains(violations[0].OffendingValue, "5") || !strings.Contains(violations[0].OffendingValue, "3") {
		t.Errorf("offending value should name both actual and max, got %q", violations[0].OffendingValue)
	}
}

func TestEnforcePolicy_ToolBoundExceeded_AppliesToEffectiveCount(t *testing.T) {
	// The tool count bound applies to the EFFECTIVE count (after
	// policy strip), not the declared count. Otherwise stripping a
	// denied tool would cause a spurious bound violation on a
	// forge.yaml that's actually under the limit.
	cfg := &types.ForgeConfig{
		BuiltinTools: []string{"a", "b", "c", "denied_tool"},
	}
	layers := wrap(PlatformPolicy{
		DeniedTools:  []string{"denied_tool"},
		MaxToolCount: 3,
	})
	// Declared count = 4 (would exceed), effective count = 3 (under
	// limit). NO bound violation expected — but the declaration of
	// "denied_tool" itself IS a tool-denied violation.
	violations := EnforcePolicy(cfg, layers)
	for _, v := range violations {
		if v.Kind == ViolationToolBoundExceeded {
			t.Errorf("effective count is under limit, should not report bound violation: %+v", v)
		}
	}
}

func TestEnforcePolicy_MultipleViolationsAllReported(t *testing.T) {
	// Developer fixes errors in batches — surface every conflict in
	// one pass so they can fix the forge.yaml once and re-run rather
	// than ping-pong through one error at a time.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"api.slack.com", "x", "y", "z"}},
		Tools:  []types.ToolRef{{Name: "http_request"}},
		Model:  types.ModelRef{Provider: "anthropic", Name: "claude-opus-4"},
	}
	layers := wrap(PlatformPolicy{
		DeniedEgressDomains:    []string{"api.slack.com"},
		DeniedTools:            []string{"http_request"},
		ForbiddenModels:        []ModelMatcher{{Provider: "anthropic", Name: "claude-opus-4"}},
		MaxEgressAllowlistSize: 2,
	})
	violations := EnforcePolicy(cfg, layers)
	if len(violations) < 4 {
		t.Errorf("expected at least 4 violations (egress, tool, model, bound), got %d: %+v", len(violations), violations)
	}
}

// --- FWS-6 multi-layer behavior ---------------------------------------

func TestEnforcePolicy_MultiLayer_SystemBeatsUserBeatsWorkspace(t *testing.T) {
	// When the same offending value is on multiple layers' deny
	// lists, attribution goes to the FIRST loaded layer (system →
	// user → workspace). Locks the audit-attribution contract:
	// system-layer denies are the most visible in the audit pipeline
	// so consumers can grep "layer=system" to find sysadmin policy
	// hits without false positives from user / workspace overrides.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"api.slack.com"}},
	}
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}}},
		{Source: LayerWorkspace, Path: "/run/forge/workspace.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}}},
	}
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation (one per offending value across layers), got %+v", violations)
	}
	if violations[0].Layer != LayerSystem {
		t.Errorf("system should win attribution, got %q", violations[0].Layer)
	}
	if violations[0].LayerPath != "/etc/forge/policy.yaml" {
		t.Errorf("layer path: got %q", violations[0].LayerPath)
	}
}

func TestEnforcePolicy_MultiLayer_DifferentLayersDifferentDenies(t *testing.T) {
	// Two layers each denying a distinct value: each violation is
	// attributed to the layer that actually denies that value, NOT
	// to the load order.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"api.slack.com", "api.notion.com"}},
	}
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.notion.com"}}},
	}
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations, got %+v", violations)
	}
	got := map[string]string{}
	for _, v := range violations {
		got[v.OffendingValue] = v.Layer
	}
	if got["api.slack.com"] != LayerSystem {
		t.Errorf("slack should be system-attributed, got %q", got["api.slack.com"])
	}
	if got["api.notion.com"] != LayerUser {
		t.Errorf("notion should be user-attributed, got %q", got["api.notion.com"])
	}
}

func TestEnforcePolicy_MultiLayer_MostRestrictiveBoundWins(t *testing.T) {
	// Bound bounds resolve via min-non-zero across layers. The layer
	// whose bound was effective takes attribution credit so operators
	// know which file to edit if they need an exception.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"a", "b", "c", "d"}},
	}
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{MaxEgressAllowlistSize: 10}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{MaxEgressAllowlistSize: 3}}, // most restrictive
		{Source: LayerWorkspace, Path: "/run/forge/workspace.yaml",
			Policy: PlatformPolicy{MaxEgressAllowlistSize: 5}},
	}
	violations := EnforcePolicy(cfg, layers)
	if len(violations) != 1 || violations[0].Kind != ViolationEgressBoundExceeded {
		t.Fatalf("expected egress_bound_exceeded, got %+v", violations)
	}
	if violations[0].Layer != LayerUser {
		t.Errorf("user (lowest non-zero max) should win attribution, got %q", violations[0].Layer)
	}
	if !strings.Contains(violations[0].OffendingValue, "max 3") {
		t.Errorf("offending value should report the winning max, got %q", violations[0].OffendingValue)
	}
}

// --- Effective views ---------------------------------------------------

func TestEffectiveEgressAllowlist_FiltersDeniedEntries(t *testing.T) {
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{
			"api.openai.com", "api.slack.com", "api.notion.com",
		}},
	}
	layers := wrap(PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}})
	got := EffectiveEgressAllowlist(cfg, layers)
	if len(got) != 2 || got[0] != "api.openai.com" || got[1] != "api.notion.com" {
		t.Errorf("filter did not strip denied entry, got %+v", got)
	}
}

func TestEffectiveEgressAllowlist_NoLayersPassesThrough(t *testing.T) {
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"x", "y"}},
	}
	got := EffectiveEgressAllowlist(cfg, nil)
	if len(got) != 2 {
		t.Errorf("no layers must not modify allowlist, got %+v", got)
	}
}

func TestEffectiveEgressAllowlist_UnionAcrossLayers(t *testing.T) {
	// System denies one domain, user denies another → both stripped.
	// Locks the union semantics for deny lists across layers.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{
			"api.openai.com", "api.slack.com", "api.notion.com",
		}},
	}
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedEgressDomains: []string{"api.notion.com"}}},
	}
	got := EffectiveEgressAllowlist(cfg, layers)
	if len(got) != 1 || got[0] != "api.openai.com" {
		t.Errorf("union of layer denies should strip both, got %+v", got)
	}
}

func TestEffectiveDeniedTools_UnionPreservesOrderAndDedupes(t *testing.T) {
	// forge.yaml's deny list comes first (preserves the developer's
	// ordering for debug-printable output), then each layer's denies
	// in load order, duplicates collapsed.
	forgeDenied := []string{"web_search", "http_request"}
	layers := []PolicyLayer{
		{Source: LayerSystem, Path: "/etc/forge/policy.yaml",
			Policy: PlatformPolicy{DeniedTools: []string{"http_request", "cli_execute"}}},
		{Source: LayerUser, Path: "/home/dev/.forge/policy.yaml",
			Policy: PlatformPolicy{DeniedTools: []string{"cli_execute", "datetime_now"}}},
	}
	got := EffectiveDeniedTools(forgeDenied, layers)
	want := []string{"web_search", "http_request", "cli_execute", "datetime_now"}
	if len(got) != len(want) {
		t.Fatalf("union size = %d, want %d: got %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("union[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEffectiveDeniedTools_NoLayersReturnsForgeListUnchanged(t *testing.T) {
	forgeDenied := []string{"web_search"}
	got := EffectiveDeniedTools(forgeDenied, nil)
	if len(got) != 1 || got[0] != "web_search" {
		t.Errorf("no layers must pass-through, got %+v", got)
	}
}

func TestEffectiveToolCount_AppliesPolicyDeny(t *testing.T) {
	cfg := &types.ForgeConfig{
		Tools:        []types.ToolRef{{Name: "a"}, {Name: "denied_tool"}},
		BuiltinTools: []string{"b", "denied_tool"},
	}
	layers := wrap(PlatformPolicy{DeniedTools: []string{"denied_tool"}})
	// denied_tool appears in both lists; both occurrences stripped.
	if got := EffectiveToolCount(cfg, layers); got != 2 {
		t.Errorf("effective count = %d, want 2", got)
	}
}

// --- Error formatting --------------------------------------------------

func TestFormatViolations_EmptyReturnsEmptyString(t *testing.T) {
	if got := FormatViolations(nil); got != "" {
		t.Errorf("nil violations should format to empty string, got %q", got)
	}
}

func TestFormatViolations_DeveloperFriendlyMultiLine(t *testing.T) {
	// Lock the error format — developers will be looking at this in
	// their terminal when startup aborts. Each violation on its own
	// indented line, with kind + offending value + forge.yaml field +
	// the deciding layer's name and path.
	violations := []PolicyViolation{
		{Kind: ViolationDeniedEgress, OffendingValue: "api.slack.com", ForgeYAMLField: "egress.allowed_domains",
			Layer: LayerSystem, LayerPath: "/etc/forge/policy.yaml"},
		{Kind: ViolationForbiddenModel, OffendingValue: "anthropic/claude-opus-4", ForgeYAMLField: "model",
			Layer: LayerUser, LayerPath: "/home/dev/.forge/policy.yaml"},
	}
	out := FormatViolations(violations)
	for _, want := range []string{
		"platform policy violations:",
		"denied_egress: api.slack.com at forge.yaml field egress.allowed_domains",
		"enforced by system policy: /etc/forge/policy.yaml",
		"forbidden_model: anthropic/claude-opus-4 at forge.yaml field model",
		"enforced by user policy: /home/dev/.forge/policy.yaml",
		"docs/security/platform-policy.md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted output missing %q, got:\n%s", want, out)
		}
	}
}
