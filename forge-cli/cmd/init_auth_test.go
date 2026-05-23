package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// --- renderAuthBlock ---

func TestRenderAuthBlock_EmptyAndNone(t *testing.T) {
	if got := renderAuthBlock("", nil); got != "" {
		t.Errorf("mode='' → %q, want empty", got)
	}
	if got := renderAuthBlock("none", nil); got != "" {
		t.Errorf("mode='none' → %q, want empty", got)
	}
}

func TestRenderAuthBlock_Custom(t *testing.T) {
	got := renderAuthBlock("custom", nil)
	if !strings.HasPrefix(got, "# Auth provider chain") {
		t.Errorf("custom mode should start with comment header, got: %s", got)
	}
	if !strings.Contains(got, "oidc") {
		t.Errorf("custom stub should mention oidc, got: %s", got)
	}
}

func TestRenderAuthBlock_OIDC(t *testing.T) {
	got := renderAuthBlock("oidc", map[string]any{
		"issuer":   "https://login.example.com",
		"audience": "api://forge",
	})

	wantLines := []string{
		"auth:",
		"  required: true",
		"  providers:",
		"    - type: oidc",
		"      settings:",
		"        audience: api://forge",
		"        issuer: https://login.example.com",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("missing line %q in:\n%s", line, got)
		}
	}
}

func TestRenderAuthBlock_NoNameEverEmitted(t *testing.T) {
	// Review #11d: the wizard doesn't capture an explicit provider
	// name, so the rendered YAML must never include a `name:` line.
	// If a future wizard step adds name capture, this test changes
	// at the same time as the function signature.
	got := renderAuthBlock("oidc", map[string]any{
		"issuer":   "https://x",
		"audience": "y",
	})
	if strings.Contains(got, "name:") {
		t.Errorf("expected no name: line in output, got:\n%s", got)
	}
}

func TestRenderAuthBlock_NestedClaimMap(t *testing.T) {
	got := renderAuthBlock("oidc", map[string]any{
		"issuer":    "https://login.example.com",
		"audience":  "api://forge",
		"claim_map": map[string]any{"groups": "roles"},
	})

	if !strings.Contains(got, "claim_map:") {
		t.Errorf("missing claim_map:\n%s", got)
	}
	if !strings.Contains(got, "          groups: roles") {
		t.Errorf("missing nested groups: roles (check indentation):\n%s", got)
	}
}

func TestRenderAuthBlock_HTTPVerifier(t *testing.T) {
	got := renderAuthBlock("http_verifier", map[string]any{
		"url":         "https://verify.example.com",
		"default_org": "acme",
	})
	if !strings.Contains(got, "- type: http_verifier") {
		t.Errorf("missing http_verifier type:\n%s", got)
	}
	if !strings.Contains(got, "default_org: acme") {
		t.Errorf("missing default_org:\n%s", got)
	}
}

func TestRenderAuthBlock_QuotesUnsafeValues(t *testing.T) {
	// Review #12.8: writeYAMLMap used to emit "key: value" raw. Values
	// containing ": " or starting with YAML-significant chars could
	// break the parser. The hardening adds quoting; this test pins it.
	tests := []struct {
		name      string
		value     string
		wantQuote bool
	}{
		{"plain url", "https://login.example.com", false},
		{"audience uri", "api://forge", false},
		{"plain identifier", "my-org", false},
		{"contains ': ' (colon-space — splits as map)", "title: subtitle", true},
		{"contains ' #' (comment marker)", "value #with comment", true},
		{"trailing colon", "foo:", true},
		{"leading ! (tag)", "!secret", true},
		{"leading * (alias)", "*ref", true},
		{"leading - (sequence)", "-name", true},
		{"leading [ (flow seq)", "[item]", true},
		{"leading { (flow map)", "{key: val}", true},
		{"leading # (comment)", "#commented", true},
		{"empty", "", true},
		{"YAML 1.1 boolean 'true'", "true", true},
		{"YAML 1.1 boolean 'YES'", "YES", true},
		{"YAML null literal", "null", true},
		{"YAML tilde null", "~", true},
		{"contains newline", "line1\nline2", true},
		{"contains tab", "with\ttab", true},
		{"leading whitespace", " starts with space", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderAuthBlock("oidc", map[string]any{
				"issuer":   "https://x", // anchor that's never quoted
				"audience": "y",
				"probe":    tt.value,
			})
			line := findSettingsLine(got, "probe")
			if line == "" {
				t.Fatalf("no probe: line in output:\n%s", got)
			}
			isQuoted := strings.Contains(line, `"`)
			if isQuoted != tt.wantQuote {
				t.Errorf("value=%q quoted=%v, want=%v\nrendered: %q",
					tt.value, isQuoted, tt.wantQuote, line)
			}
		})
	}
}

func TestRenderAuthBlock_QuotedValueIsParseable(t *testing.T) {
	// Round-trip check: the quoted output must parse back via yaml.v3
	// as the original string. Catches escaping bugs.
	weird := `weird: value # with comment ! and "quote"`
	got := renderAuthBlock("oidc", map[string]any{
		"issuer":   "https://x",
		"audience": "y",
		"probe":    weird,
	})

	// Find the probe: line, extract the value, parse via yaml.v3.
	line := findSettingsLine(got, "probe")
	if line == "" {
		t.Fatalf("no probe line:\n%s", got)
	}
	// "        probe: \"…\""  →  yaml.Unmarshal of "value: <quoted>"
	idx := strings.Index(line, "probe:")
	docFragment := line[idx:]
	var parsed map[string]string
	if err := yaml.Unmarshal([]byte(docFragment), &parsed); err != nil {
		t.Fatalf("rendered line %q is not valid YAML: %v", line, err)
	}
	if parsed["probe"] != weird {
		t.Errorf("round-trip mismatch:\n  got  %q\n  want %q", parsed["probe"], weird)
	}
}

// findSettingsLine returns the first line in `block` whose key is `key`.
// Used by the quoting tests to inspect a single rendered key/value pair.
func findSettingsLine(block, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, prefix) {
			return line
		}
	}
	return ""
}

func TestRenderAuthBlock_DeterministicOrdering(t *testing.T) {
	// Settings keys should always emit in alphabetical order so diffs
	// of generated files are stable.
	got1 := renderAuthBlock("oidc", map[string]any{
		"audience": "a",
		"issuer":   "i",
	})
	got2 := renderAuthBlock("oidc", map[string]any{
		"issuer":   "i",
		"audience": "a",
	})
	if got1 != got2 {
		t.Errorf("output is not deterministic across map ordering")
	}
	// audience must come before issuer (alphabetical).
	audIdx := strings.Index(got1, "audience:")
	issIdx := strings.Index(got1, "issuer:")
	if audIdx == -1 || issIdx == -1 || audIdx > issIdx {
		t.Errorf("audience should appear before issuer; got:\n%s", got1)
	}
}

// --- buildAuthFromFlags ---

func mockInitCmdWithFlags() *cobra.Command {
	c := &cobra.Command{Use: "init"}
	c.Flags().String("auth", "", "")
	c.Flags().String("auth-issuer", "", "")
	c.Flags().String("auth-audience", "", "")
	c.Flags().String("auth-url", "", "")
	c.Flags().String("auth-default-org", "", "")
	c.Flags().String("auth-groups-claim", "", "")
	// Phase 2 flags:
	c.Flags().String("auth-aws-region", "", "")
	c.Flags().String("auth-aws-audience", "", "")
	c.Flags().StringSlice("auth-aws-allowed-principal", nil, "")
	c.Flags().String("auth-aws-cache-ttl", "", "")
	c.Flags().String("auth-gcp-iap-audience", "", "")
	c.Flags().String("auth-azure-tenant", "", "")
	c.Flags().String("auth-azure-audience", "", "")
	c.Flags().Bool("auth-azure-multi-tenant", false, "")
	c.Flags().String("auth-azure-groups-mode", "", "")
	return c
}

func TestBuildAuthFromFlags_None(t *testing.T) {
	c := mockInitCmdWithFlags()
	settings, hosts, err := buildAuthFromFlags(c, "none")
	if err != nil {
		t.Fatal(err)
	}
	if settings != nil || hosts != nil {
		t.Errorf("none should return nils, got %v / %v", settings, hosts)
	}
}

func TestBuildAuthFromFlags_OIDC_HappyPath(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-issuer", "https://login.example.com/")
	_ = c.Flags().Set("auth-audience", "api://forge")

	settings, hosts, err := buildAuthFromFlags(c, "oidc")
	if err != nil {
		t.Fatal(err)
	}
	if settings["issuer"] != "https://login.example.com" {
		t.Errorf("trailing slash should be stripped from issuer; got %v", settings["issuer"])
	}
	if settings["audience"] != "api://forge" {
		t.Errorf("audience wrong: %v", settings["audience"])
	}
	if _, ok := settings["claim_map"]; ok {
		t.Errorf("claim_map should not be set without --auth-groups-claim")
	}
	if !reflect.DeepEqual(hosts, []string{"login.example.com"}) {
		t.Errorf("hosts = %v, want [login.example.com]", hosts)
	}
}

func TestBuildAuthFromFlags_OIDC_CustomGroupsClaim(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-issuer", "https://x")
	_ = c.Flags().Set("auth-audience", "y")
	_ = c.Flags().Set("auth-groups-claim", "roles")

	settings, _, err := buildAuthFromFlags(c, "oidc")
	if err != nil {
		t.Fatal(err)
	}
	cm, ok := settings["claim_map"].(map[string]any)
	if !ok {
		t.Fatalf("expected claim_map, got %v", settings["claim_map"])
	}
	if cm["groups"] != "roles" {
		t.Errorf("claim_map.groups = %v, want roles", cm["groups"])
	}
}

func TestBuildAuthFromFlags_OIDC_DefaultGroupsClaimSuppressed(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-issuer", "https://x")
	_ = c.Flags().Set("auth-audience", "y")
	_ = c.Flags().Set("auth-groups-claim", "groups") // same as default

	settings, _, err := buildAuthFromFlags(c, "oidc")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := settings["claim_map"]; ok {
		t.Errorf("default groups claim should be suppressed")
	}
}

func TestBuildAuthFromFlags_OIDC_MissingFields(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-issuer", "https://x")
	// missing audience
	_, _, err := buildAuthFromFlags(c, "oidc")
	if err == nil {
		t.Error("expected error for missing audience")
	}
}

func TestBuildAuthFromFlags_HTTPVerifier_HappyPath(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-url", "https://verify.example.com/verify")
	_ = c.Flags().Set("auth-default-org", "acme")

	settings, hosts, err := buildAuthFromFlags(c, "http_verifier")
	if err != nil {
		t.Fatal(err)
	}
	if settings["url"] != "https://verify.example.com/verify" {
		t.Errorf("url wrong: %v", settings["url"])
	}
	if settings["default_org"] != "acme" {
		t.Errorf("default_org wrong: %v", settings["default_org"])
	}
	if !reflect.DeepEqual(hosts, []string{"verify.example.com"}) {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestBuildAuthFromFlags_HTTPVerifier_MissingURL(t *testing.T) {
	c := mockInitCmdWithFlags()
	_, _, err := buildAuthFromFlags(c, "http_verifier")
	if err == nil {
		t.Error("expected error for missing --auth-url")
	}
}

func TestBuildAuthFromFlags_UnknownMode(t *testing.T) {
	c := mockInitCmdWithFlags()
	_, _, err := buildAuthFromFlags(c, "ldap")
	if err == nil {
		t.Error("expected error for unknown mode")
	}
}

// --- authEgressHostsFromSettings (PR6) ---

func TestAuthEgressHostsFromSettings_OIDC(t *testing.T) {
	got := authEgressHostsFromSettings("oidc", map[string]any{
		"issuer":   "https://login.example.com/realms/x",
		"audience": "api://forge",
	})
	if !reflect.DeepEqual(got, []string{"login.example.com"}) {
		t.Errorf("got %v, want [login.example.com]", got)
	}
}

func TestAuthEgressHostsFromSettings_OIDCWithJWKSURL(t *testing.T) {
	got := authEgressHostsFromSettings("oidc", map[string]any{
		"issuer":   "https://login.example.com",
		"audience": "api://forge",
		"jwks_url": "https://keys.example.com/jwks",
	})
	want := []string{"login.example.com", "keys.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAuthEgressHostsFromSettings_HTTPVerifier(t *testing.T) {
	got := authEgressHostsFromSettings("http_verifier", map[string]any{
		"url": "https://verify.example.com/verify",
	})
	if !reflect.DeepEqual(got, []string{"verify.example.com"}) {
		t.Errorf("got %v, want [verify.example.com]", got)
	}
}

func TestAuthEgressHostsFromSettings_NoNetworkModes(t *testing.T) {
	for _, mode := range []string{"none", "custom", "static_token", "unknown"} {
		if got := authEgressHostsFromSettings(mode, map[string]any{"x": "y"}); got != nil {
			t.Errorf("mode %q returned %v, want nil", mode, got)
		}
	}
}

func TestAuthEgressHostsFromSettings_NilSettings(t *testing.T) {
	if got := authEgressHostsFromSettings("oidc", nil); got != nil {
		t.Errorf("got %v, want nil for nil settings", got)
	}
}

func TestAuthEgressHostsFromSettings_MalformedURL(t *testing.T) {
	if got := authEgressHostsFromSettings("oidc", map[string]any{
		"issuer": "::not a url",
	}); got != nil {
		t.Errorf("got %v, want nil for malformed URL", got)
	}
}

// --- mergeEgressDomains ---

func TestMergeEgressDomains_Dedupe(t *testing.T) {
	got := mergeEgressDomains(
		[]string{"a", "b", "c"},
		[]string{"b", "d"},
	)
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeEgressDomains_EmptyInputs(t *testing.T) {
	if got := mergeEgressDomains(nil, nil); len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	if got := mergeEgressDomains([]string{"a"}, nil); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("got %v", got)
	}
	if got := mergeEgressDomains(nil, []string{"b"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("got %v", got)
	}
}

func TestMergeEgressDomains_SortedOutput(t *testing.T) {
	// Review #11c — review of #11 batch: the function returns sorted
	// output so the generated forge.yaml stays stable across runs even
	// when AuthEgressHosts arrive in non-deterministic order (e.g.,
	// derived from map iteration).
	got := mergeEgressDomains(
		[]string{"api.openai.com", "api.anthropic.com"},
		[]string{"login.example.com", "graph.microsoft.com"},
	)
	want := []string{"api.anthropic.com", "api.openai.com", "graph.microsoft.com", "login.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output not sorted:\n  got  %v\n  want %v", got, want)
	}
}

// --- Phase 2 renderer + flag tests ---

func TestRenderAuthBlock_AWSSigv4_Minimal(t *testing.T) {
	got := renderAuthBlock("aws_sigv4", map[string]any{"region": "us-east-1"})
	if !strings.Contains(got, "type: aws_sigv4") {
		t.Errorf("missing type line:\n%s", got)
	}
	if !strings.Contains(got, "region: us-east-1") {
		t.Errorf("missing region:\n%s", got)
	}
	if strings.Contains(got, "audience:") {
		t.Errorf("audience should not be emitted when unset:\n%s", got)
	}
}

func TestRenderAuthBlock_AWSSigv4_ARNsList(t *testing.T) {
	wantARNs := []string{
		"arn:aws:sts::123:assumed-role/ci-deploy/*",
		"arn:aws:sts::123:assumed-role/forge-runner/*",
	}
	got := renderAuthBlock("aws_sigv4", map[string]any{
		"region":             "us-east-1",
		"allowed_principals": wantARNs,
	})
	if !strings.Contains(got, "allowed_principals:") {
		t.Errorf("missing allowed_principals header:\n%s", got)
	}
	// ARN contains ":" but not ": " (colon-space), so YAML doesn't need
	// to quote it — verify round-trip parse instead of insisting on quotes.
	parsed := parseAuthBlockYAML(t, got)
	provs, _ := parsed["auth"].(map[string]any)["providers"].([]any)
	settings := provs[0].(map[string]any)["settings"].(map[string]any)
	gotARNs := toStringSlice(settings["allowed_principals"])
	if !reflect.DeepEqual(gotARNs, wantARNs) {
		t.Errorf("ARN round-trip mismatch:\n  got  %v\n  want %v", gotARNs, wantARNs)
	}
}

func TestRenderAuthBlock_GCPIAP(t *testing.T) {
	wantAud := "/projects/12345/global/backendServices/67890"
	got := renderAuthBlock("gcp_iap", map[string]any{
		"audience": wantAud,
	})
	if !strings.Contains(got, "type: gcp_iap") {
		t.Errorf("missing type:\n%s", got)
	}
	parsed := parseAuthBlockYAML(t, got)
	provs, _ := parsed["auth"].(map[string]any)["providers"].([]any)
	gotAud := provs[0].(map[string]any)["settings"].(map[string]any)["audience"]
	if gotAud != wantAud {
		t.Errorf("audience round-trip mismatch:\n  got  %v\n  want %v", gotAud, wantAud)
	}
}

func parseAuthBlockYAML(t *testing.T, block string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(block), &out); err != nil {
		t.Fatalf("renderAuthBlock output is not valid YAML: %v\n%s", err, block)
	}
	return out
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i], _ = x.(string)
	}
	return out
}

func TestRenderAuthBlock_AzureAD_SingleTenant(t *testing.T) {
	got := renderAuthBlock("azure_ad", map[string]any{
		"tenant_id": "00000000-1111-2222-3333-444444444444",
		"audience":  "api://forge",
	})
	if !strings.Contains(got, "tenant_id: 00000000-1111-2222-3333-444444444444") {
		t.Errorf("missing tenant_id:\n%s", got)
	}
	if strings.Contains(got, "allow_multi_tenant") {
		t.Errorf("allow_multi_tenant should be omitted when not set:\n%s", got)
	}
}

func TestRenderAuthBlock_AzureAD_MultiTenant(t *testing.T) {
	got := renderAuthBlock("azure_ad", map[string]any{
		"audience":           "api://forge",
		"allow_multi_tenant": true,
	})
	if !strings.Contains(got, "allow_multi_tenant: true") {
		t.Errorf("missing allow_multi_tenant true:\n%s", got)
	}
	if strings.Contains(got, "tenant_id") {
		t.Errorf("tenant_id should not appear when multi-tenant:\n%s", got)
	}
}

func TestRenderAuthBlock_AzureAD_GroupsModeGraph(t *testing.T) {
	got := renderAuthBlock("azure_ad", map[string]any{
		"tenant_id":   "abc",
		"audience":    "api://forge",
		"groups_mode": "graph",
	})
	if !strings.Contains(got, "groups_mode: graph") {
		t.Errorf("missing groups_mode:\n%s", got)
	}
}

func TestBuildAuthFromFlags_AWSSigv4_HappyPath(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-aws-region", "us-east-1")
	_ = c.Flags().Set("auth-aws-audience", "api://forge")
	_ = c.Flags().Set("auth-aws-allowed-principal", "arn:aws:sts::123:assumed-role/x/*")

	settings, hosts, err := buildAuthFromFlags(c, "aws_sigv4")
	if err != nil {
		t.Fatal(err)
	}
	if settings["region"] != "us-east-1" {
		t.Errorf("region = %v", settings["region"])
	}
	if !reflect.DeepEqual(hosts, []string{"sts.us-east-1.amazonaws.com"}) {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestBuildAuthFromFlags_AWSSigv4_MissingRegion(t *testing.T) {
	c := mockInitCmdWithFlags()
	_, _, err := buildAuthFromFlags(c, "aws_sigv4")
	if err == nil {
		t.Fatal("expected error when region missing")
	}
}

func TestBuildAuthFromFlags_GCPIAP_HappyPath(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-gcp-iap-audience", "/projects/12345/...")
	settings, hosts, err := buildAuthFromFlags(c, "gcp_iap")
	if err != nil {
		t.Fatal(err)
	}
	if settings["audience"] == "" {
		t.Error("audience not captured")
	}
	if !reflect.DeepEqual(hosts, []string{"www.gstatic.com"}) {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestBuildAuthFromFlags_GCPIAP_MissingAudience(t *testing.T) {
	c := mockInitCmdWithFlags()
	_, _, err := buildAuthFromFlags(c, "gcp_iap")
	if err == nil {
		t.Fatal("expected error when audience missing")
	}
}

func TestBuildAuthFromFlags_AzureAD_RequiresTenant(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-azure-audience", "api://forge")
	_, _, err := buildAuthFromFlags(c, "azure_ad")
	if err == nil {
		t.Fatal("expected error when tenant + non-multi-tenant")
	}
}

func TestBuildAuthFromFlags_AzureAD_MultiTenantOK(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-azure-audience", "api://forge")
	_ = c.Flags().Set("auth-azure-multi-tenant", "true")
	settings, hosts, err := buildAuthFromFlags(c, "azure_ad")
	if err != nil {
		t.Fatal(err)
	}
	if settings["allow_multi_tenant"] != true {
		t.Errorf("allow_multi_tenant not set: %v", settings)
	}
	if !reflect.DeepEqual(hosts, []string{"login.microsoftonline.com"}) {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestBuildAuthFromFlags_AzureAD_GraphModeAddsGraphHost(t *testing.T) {
	c := mockInitCmdWithFlags()
	_ = c.Flags().Set("auth-azure-audience", "api://forge")
	_ = c.Flags().Set("auth-azure-tenant", "abc-tid")
	_ = c.Flags().Set("auth-azure-groups-mode", "graph")
	_, hosts, err := buildAuthFromFlags(c, "azure_ad")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"login.microsoftonline.com", "graph.microsoft.com"}
	if !reflect.DeepEqual(hosts, want) {
		t.Errorf("hosts = %v, want %v", hosts, want)
	}
}

func TestAuthEgressHostsFromSettings_Phase2(t *testing.T) {
	cases := []struct {
		mode     string
		settings map[string]any
		want     []string
	}{
		{"aws_sigv4", map[string]any{"region": "us-east-1"}, []string{"sts.us-east-1.amazonaws.com"}},
		{"gcp_iap", map[string]any{"audience": "x"}, []string{"www.gstatic.com"}},
		{"azure_ad", map[string]any{"audience": "x"}, []string{"login.microsoftonline.com"}},
		{"azure_ad", map[string]any{"audience": "x", "groups_mode": "graph"}, []string{"login.microsoftonline.com", "graph.microsoft.com"}},
	}
	for _, tc := range cases {
		got := authEgressHostsFromSettings(tc.mode, tc.settings)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %v: got %v, want %v", tc.mode, tc.settings, got, tc.want)
		}
	}
}
