package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for issue #89 / FWS-5 — platform policy schema +
// loader + validation. These lock the contract the runner depends on
// when intersecting forge.yaml's declaration against the platform's
// workspace-level ceiling.

func TestLoadPlatformPolicy_EmptyPathReturnsZeroPolicy(t *testing.T) {
	// FORGE_PLATFORM_POLICY unset is the common case for `forge run`
	// and self-managed deployments. Must NOT error — backward compat
	// with pre-FWS-5 behavior.
	p, err := LoadPlatformPolicy("")
	if err != nil {
		t.Fatalf("empty path should be no-op, got error %v", err)
	}
	if !p.IsZero() {
		t.Errorf("empty path should return zero policy, got %+v", p)
	}
}

func TestLoadPlatformPolicy_MissingFileReturnsZeroPolicy(t *testing.T) {
	// `optional: true` ConfigMap mount produces an empty mount when
	// the operator hasn't created the ConfigMap. Must be silent — the
	// generated Deployment manifest is policy-ready by default, and a
	// missing file must not block agent startup.
	p, err := LoadPlatformPolicy("/no/such/path/policy.yaml")
	if err != nil {
		t.Fatalf("missing file should be no-op, got error %v", err)
	}
	if !p.IsZero() {
		t.Errorf("missing file should return zero policy, got %+v", p)
	}
}

func TestLoadPlatformPolicy_MalformedYAMLReturnsError(t *testing.T) {
	// A malformed policy is an operator mistake that MUST fail loudly.
	// Silently treating a broken YAML as "no policy" would be the
	// opposite of safe.
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte("denied_egress_domains: [not-closed-list"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlatformPolicy(path); err == nil {
		t.Errorf("malformed YAML should return error")
	}
}

func TestLoadPlatformPolicy_UnknownFieldRejected(t *testing.T) {
	// Strict decoding catches operator typos like "deinied_tools" that
	// would silently no-op under permissive YAML and become a security
	// regression. Lock this in.
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte("deinied_tools: [cli_execute]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPlatformPolicy(path)
	if err == nil {
		t.Fatalf("typo'd field name should be rejected by strict decoder")
	}
	if !strings.Contains(err.Error(), "deinied_tools") {
		t.Errorf("error should point at the offending field, got %v", err)
	}
}

func TestParsePlatformPolicy_EmptyDocumentIsZeroPolicy(t *testing.T) {
	// Operators sometimes mount a policy ConfigMap with an empty file
	// as a placeholder. Empty YAML decodes to a zero policy — same
	// effect as no file at all.
	p, err := ParsePlatformPolicy([]byte(""))
	if err != nil {
		t.Fatalf("empty document should parse without error, got %v", err)
	}
	if !p.IsZero() {
		t.Errorf("empty document should yield zero policy, got %+v", p)
	}
}

func TestParsePlatformPolicy_FullSchemaRoundTrip(t *testing.T) {
	// Lock in the YAML field names operators will write — these are
	// the documented policy schema. A field rename without doc update
	// breaks every existing policy ConfigMap silently.
	doc := []byte(`
denied_egress_domains:
  - api.slack.com
  - hooks.slack.com
denied_tools:
  - cli_execute
  - http_request
forbidden_models:
  - provider: anthropic
    name: claude-opus-4
  - provider: openai
    name: gpt-4-32k
max_egress_allowlist_size: 50
max_tool_count: 100
denied_channels:
  - telegram
`)
	p, err := ParsePlatformPolicy(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.EgressDomainDenied("api.slack.com") {
		t.Errorf("api.slack.com should be denied")
	}
	if !p.EgressDomainDenied("API.SLACK.COM") {
		t.Errorf("egress deny match should be case-insensitive")
	}
	if p.EgressDomainDenied("api.notion.com") {
		t.Errorf("api.notion.com should NOT be denied (not in list)")
	}
	if !p.ToolDenied("cli_execute") {
		t.Errorf("cli_execute should be denied")
	}
	if p.ToolDenied("CLI_Execute") {
		t.Errorf("tool deny match must be case-sensitive (registry names are)")
	}
	if !p.ModelForbidden("anthropic", "claude-opus-4") {
		t.Errorf("claude-opus-4 should be forbidden")
	}
	if p.ModelForbidden("anthropic", "claude-sonnet-4-6") {
		t.Errorf("sonnet should be allowed (not in forbidden list)")
	}
	if p.MaxEgressAllowlistSize != 50 || p.MaxToolCount != 100 {
		t.Errorf("size bounds not parsed correctly: %+v", p)
	}
	if len(p.DeniedChannels) != 1 || p.DeniedChannels[0] != "telegram" {
		t.Errorf("FWS-6 denied_channels slot not parsed: %+v", p.DeniedChannels)
	}
}

func TestValidate_ForbiddenModelRequiresBothFields(t *testing.T) {
	// Loose "provider: anthropic" without a name would match any
	// anthropic model — a footgun. The schema requires both fields.
	cases := []struct {
		name    string
		policy  PlatformPolicy
		wantErr bool
	}{
		{"valid", PlatformPolicy{ForbiddenModels: []ModelMatcher{{Provider: "anthropic", Name: "claude-opus-4"}}}, false},
		{"missing provider", PlatformPolicy{ForbiddenModels: []ModelMatcher{{Name: "claude-opus-4"}}}, true},
		{"missing name", PlatformPolicy{ForbiddenModels: []ModelMatcher{{Provider: "anthropic"}}}, true},
		{"both missing", PlatformPolicy{ForbiddenModels: []ModelMatcher{{}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_NegativeSizeBoundsRejected(t *testing.T) {
	cases := []PlatformPolicy{
		{MaxEgressAllowlistSize: -1},
		{MaxToolCount: -1},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: negative bound should be rejected", i)
		}
	}
}

func TestIsZero_NonTrivialPolicyNotZero(t *testing.T) {
	cases := []PlatformPolicy{
		{DeniedEgressDomains: []string{"x"}},
		{DeniedTools: []string{"x"}},
		{ForbiddenModels: []ModelMatcher{{Provider: "x", Name: "y"}}},
		{MaxEgressAllowlistSize: 1},
		{MaxToolCount: 1},
		{DeniedChannels: []string{"x"}},
	}
	for i, c := range cases {
		if c.IsZero() {
			t.Errorf("case %d: should NOT be zero, got %+v", i, c)
		}
	}
	if !(PlatformPolicy{}).IsZero() {
		t.Errorf("zero-value should be zero")
	}
}

func TestModelMatcher_String(t *testing.T) {
	// Used in error messages and audit fields. Lock the format so
	// downstream parsers / log queries don't break on a refactor.
	m := ModelMatcher{Provider: "anthropic", Name: "claude-opus-4"}
	if got := m.String(); got != "anthropic/claude-opus-4" {
		t.Errorf("String() = %q, want %q", got, "anthropic/claude-opus-4")
	}
}
