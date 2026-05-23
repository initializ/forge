package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// --- renderAuthBlock ---

func TestRenderAuthBlock_EmptyAndNone(t *testing.T) {
	if got := renderAuthBlock("", "", nil); got != "" {
		t.Errorf("mode='' → %q, want empty", got)
	}
	if got := renderAuthBlock("none", "", nil); got != "" {
		t.Errorf("mode='none' → %q, want empty", got)
	}
}

func TestRenderAuthBlock_Custom(t *testing.T) {
	got := renderAuthBlock("custom", "", nil)
	if !strings.HasPrefix(got, "# Auth provider chain") {
		t.Errorf("custom mode should start with comment header, got: %s", got)
	}
	if !strings.Contains(got, "oidc") {
		t.Errorf("custom stub should mention oidc, got: %s", got)
	}
}

func TestRenderAuthBlock_OIDC(t *testing.T) {
	got := renderAuthBlock("oidc", "corporate-sso", map[string]any{
		"issuer":   "https://login.example.com",
		"audience": "api://forge",
	})

	wantLines := []string{
		"auth:",
		"  required: true",
		"  providers:",
		"    - type: oidc",
		"      name: corporate-sso",
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

func TestRenderAuthBlock_NameSameAsModeSuppressed(t *testing.T) {
	// When the name equals the mode (the default the wizard supplies),
	// don't emit a redundant `name:` line.
	got := renderAuthBlock("oidc", "oidc", map[string]any{
		"issuer":   "https://x",
		"audience": "y",
	})
	if strings.Contains(got, "name: oidc") {
		t.Errorf("expected name: line suppressed when same as type, got:\n%s", got)
	}
}

func TestRenderAuthBlock_NestedClaimMap(t *testing.T) {
	got := renderAuthBlock("oidc", "", map[string]any{
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
	got := renderAuthBlock("http_verifier", "", map[string]any{
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

func TestRenderAuthBlock_DeterministicOrdering(t *testing.T) {
	// Settings keys should always emit in alphabetical order so diffs
	// of generated files are stable.
	got1 := renderAuthBlock("oidc", "", map[string]any{
		"audience": "a",
		"issuer":   "i",
	})
	got2 := renderAuthBlock("oidc", "", map[string]any{
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
