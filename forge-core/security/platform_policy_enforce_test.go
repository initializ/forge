package security

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// Regression tests for issue #89 / FWS-5 — the policy enforcement
// engine. The pure-function shape (config + policy → []violation) lets
// us exhaustively cover every conflict category without spinning a
// runner. The runner-level integration is tested separately.

func TestEnforcePolicy_ZeroPolicy_NoViolations(t *testing.T) {
	// The common pre-FWS-5 case: no platform policy → no constraints.
	// Backward-compat guarantee.
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"api.openai.com", "api.slack.com"}},
		Tools:  []types.ToolRef{{Name: "cli_execute"}},
	}
	if v := EnforcePolicy(cfg, PlatformPolicy{}); len(v) != 0 {
		t.Errorf("zero policy must report no violations, got %+v", v)
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
	policy := PlatformPolicy{
		DeniedEgressDomains: []string{"api.slack.com", "hooks.slack.com"},
	}
	violations := EnforcePolicy(cfg, policy)
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
	policy := PlatformPolicy{
		DeniedTools: []string{"http_request", "cli_execute"},
	}
	violations := EnforcePolicy(cfg, policy)
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
	policy := PlatformPolicy{
		ForbiddenModels: []ModelMatcher{
			{Provider: "anthropic", Name: "claude-opus-4"},
		},
	}
	violations := EnforcePolicy(cfg, policy)
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
	policy := PlatformPolicy{MaxEgressAllowlistSize: 3}
	violations := EnforcePolicy(cfg, policy)
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
	policy := PlatformPolicy{
		DeniedTools:  []string{"denied_tool"},
		MaxToolCount: 3,
	}
	// Declared count = 4 (would exceed), effective count = 3 (under
	// limit). NO bound violation expected — but the declaration of
	// "denied_tool" itself IS a tool-denied violation.
	violations := EnforcePolicy(cfg, policy)
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
	policy := PlatformPolicy{
		DeniedEgressDomains:    []string{"api.slack.com"},
		DeniedTools:            []string{"http_request"},
		ForbiddenModels:        []ModelMatcher{{Provider: "anthropic", Name: "claude-opus-4"}},
		MaxEgressAllowlistSize: 2,
	}
	violations := EnforcePolicy(cfg, policy)
	if len(violations) < 4 {
		t.Errorf("expected at least 4 violations (egress, tool, model, bound), got %d: %+v", len(violations), violations)
	}
}

func TestEffectiveEgressAllowlist_FiltersDeniedEntries(t *testing.T) {
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{
			"api.openai.com", "api.slack.com", "api.notion.com",
		}},
	}
	policy := PlatformPolicy{DeniedEgressDomains: []string{"api.slack.com"}}
	got := EffectiveEgressAllowlist(cfg, policy)
	if len(got) != 2 || got[0] != "api.openai.com" || got[1] != "api.notion.com" {
		t.Errorf("filter did not strip denied entry, got %+v", got)
	}
}

func TestEffectiveEgressAllowlist_ZeroPolicyPassesThrough(t *testing.T) {
	cfg := &types.ForgeConfig{
		Egress: types.EgressRef{AllowedDomains: []string{"x", "y"}},
	}
	got := EffectiveEgressAllowlist(cfg, PlatformPolicy{})
	if len(got) != 2 {
		t.Errorf("zero policy must not modify allowlist, got %+v", got)
	}
}

func TestEffectiveDeniedTools_UnionPreservesOrderAndDedupes(t *testing.T) {
	// forge.yaml's deny list comes first (preserves the developer's
	// ordering for debug-printable output), policy denies are
	// appended, duplicates collapsed.
	forgeDenied := []string{"web_search", "http_request"}
	policy := PlatformPolicy{DeniedTools: []string{"http_request", "cli_execute"}}
	got := EffectiveDeniedTools(forgeDenied, policy)
	want := []string{"web_search", "http_request", "cli_execute"}
	if len(got) != len(want) {
		t.Fatalf("union size = %d, want %d: got %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("union[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEffectiveDeniedTools_ZeroPolicyReturnsForgeListUnchanged(t *testing.T) {
	forgeDenied := []string{"web_search"}
	got := EffectiveDeniedTools(forgeDenied, PlatformPolicy{})
	if len(got) != 1 || got[0] != "web_search" {
		t.Errorf("zero policy must pass-through, got %+v", got)
	}
}

func TestEffectiveToolCount_AppliesPolicyDeny(t *testing.T) {
	cfg := &types.ForgeConfig{
		Tools:        []types.ToolRef{{Name: "a"}, {Name: "denied_tool"}},
		BuiltinTools: []string{"b", "denied_tool"},
	}
	policy := PlatformPolicy{DeniedTools: []string{"denied_tool"}}
	// denied_tool appears in both lists; both occurrences stripped.
	if got := EffectiveToolCount(cfg, policy); got != 2 {
		t.Errorf("effective count = %d, want 2", got)
	}
}

func TestFormatViolations_EmptyReturnsEmptyString(t *testing.T) {
	if got := FormatViolations(nil); got != "" {
		t.Errorf("nil violations should format to empty string, got %q", got)
	}
}

func TestFormatViolations_DeveloperFriendlyMultiLine(t *testing.T) {
	// Lock the error format — developers will be looking at this in
	// their terminal when startup aborts. Each violation on its own
	// indented line, with kind + offending value + forge.yaml field.
	violations := []PolicyViolation{
		{Kind: ViolationDeniedEgress, OffendingValue: "api.slack.com", ForgeYAMLField: "egress.allowed_domains"},
		{Kind: ViolationForbiddenModel, OffendingValue: "anthropic/claude-opus-4", ForgeYAMLField: "model"},
	}
	out := FormatViolations(violations)
	for _, want := range []string{
		"platform policy violations:",
		"denied_egress: api.slack.com at forge.yaml field egress.allowed_domains",
		"forbidden_model: anthropic/claude-opus-4 at forge.yaml field model",
		"docs/security/platform-policy.md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted output missing %q, got:\n%s", want, out)
		}
	}
}
